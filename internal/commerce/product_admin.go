package commerce

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrProductInUse 는 주문된 상품을 지우려 한 경우다. `order_items.product_id`
	// 가 ON DELETE RESTRICT 라 물리 삭제 경로가 아예 없다 (D30 3-1) —
	// 판매 중단은 `is_visible = false` 다.
	ErrProductInUse = errors.New("commerce: 주문 내역이 있어 삭제할 수 없습니다")
	// ErrSkuTaken 은 두 조합이 같은 SKU 를 가지려 한 경우다. 같으면 재고가
	// 두 벌이 되어 그 자체가 모순이다 (D30).
	ErrSkuTaken = errors.New("commerce: 이미 쓰이는 SKU 입니다")
	// ErrOptionDuplicate 는 한 그룹 안에서 옵션 값이 겹친 경우다.
	ErrOptionDuplicate = errors.New("commerce: 같은 그룹에 중복된 옵션 값이 있습니다")
	// ErrStockVersion 은 낙관적 잠금 버전이 어긋난 경우다 (409).
	ErrStockVersion = errors.New("commerce: 다른 사람이 먼저 바꿨습니다")
)

// ProductByID reads one product for A-502.
func (s *Store) ProductByID(ctx context.Context, id string) (*Product, error) {
	var p Product
	err := s.pool.QueryRow(ctx, `
		SELECT id, slug, name, description, base_price, is_visible
		FROM products WHERE id = $1`, id).
		Scan(&p.ID, &p.Slug, &p.Name, &p.Description, &p.BasePrice, &p.Visible)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// UpdateProduct is A-502's save.
//
// **재고·SKU·조합은 받지 않는다** (D19 A-502 받지 않는 필드) — 여기서 받으면
// 재고 절대값을 덮어쓰는 경로가 하나 더 생긴다. 그것은 A-503 소관이고,
// A-503 도 절대값을 받지 않는다.
func (s *Store) UpdateProduct(ctx context.Context, p Product) error {
	if p.BasePrice < 0 {
		return fmt.Errorf("%w: 기본가 %d", ErrPriceNegative, p.BasePrice)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE products SET slug = $2, name = $3, description = $4,
		       base_price = $5, is_visible = $6, updated_at = now()
		WHERE id = $1`, p.ID, p.Slug, p.Name, p.Description, p.BasePrice, p.Visible)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrSlugTaken
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteProduct removes a product that was never ordered.
//
// 주문된 상품은 FK 가 막는다. **애플리케이션이 먼저 세어 보지 않는다** —
// 세고 나서 지우는 사이에 주문이 들어오면 그 검사는 통과하고 삭제도 통과하는
// 것처럼 보이지만, 실제로 막는 것은 FK 다. 여기서는 FK 의 오류를 읽는다.
func (s *Store) DeleteProduct(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, id)
	var pgErr *pgconn.PgError
	// 23503 은 FK 위반, 23001 은 RESTRICT 위반이다. RESTRICT 는 별도 코드를
	// 쓰므로 23503 만 보면 이 경로가 통째로 500 이 된다.
	if errors.As(err, &pgErr) && (pgErr.Code == "23503" || pgErr.Code == "23001") {
		return ErrProductInUse
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// VariantEdit is one row of A-503's editor.
type VariantEdit struct {
	ID         string
	SKU        string
	PriceDelta int
	// StockDelta 는 **조정값이다. 절대값이 아니다** (D13, D19 A-503).
	// 주문이 동시에 들어오면 절대값 덮어쓰기는 판매분을 지운다.
	StockDelta int
	// Version 은 낙관적 잠금이다. 화면이 읽은 시점의 재고이고, 그 사이 누가
	// 바꿨으면 409 다 — 조정값이라도 화면이 보여준 결과와 달라지기 때문이다.
	Version int
}

// EditVariants applies A-503's edits in one transaction.
//
// **재고는 조정값으로만 움직인다.** `SET stock = stock + $1` 이 한 문장이고,
// 읽고-더하고-쓰는 경로는 코드에 없다 — 그 경로가 있으면 동시 두 건 중 하나가
// 다른 하나를 덮어쓴다.
//
// 잠금 순서는 variant id 오름차순이다 (AdjustStock 과 같은 이유): 순서를
// 요청자가 정하면 역순 요청 두 건이 교착한다.
func (s *Store) EditVariants(ctx context.Context, productID string, edits []VariantEdit) error {
	ordered := append([]VariantEdit(nil), edits...)
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0 && ordered[j].ID < ordered[j-1].ID; j-- {
			ordered[j], ordered[j-1] = ordered[j-1], ordered[j]
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for _, e := range ordered {
		var stock int
		err := tx.QueryRow(ctx,
			`SELECT stock FROM product_variants WHERE id = $1 AND product_id = $2 FOR UPDATE`,
			e.ID, productID).Scan(&stock)
		if errors.Is(err, pgx.ErrNoRows) {
			// 다른 상품의 조합 ID 도 여기로 온다. product_id 술어가 막는다.
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		// 낙관적 잠금: 화면이 읽은 재고와 지금이 다르면 거부한다. 조정값이라도
		// 화면이 "3 → 5" 라고 보여준 결과가 달라지므로, 그 차이를 삼키지 않는다.
		if e.Version >= 0 && e.Version != stock {
			return fmt.Errorf("%w: 화면 %d, 현재 %d", ErrStockVersion, e.Version, stock)
		}

		// **단일 문장이다.** 위에서 읽은 stock 을 쓰지 않는다 — 쓰면 그것이
		// 곧 읽고-더하고-쓰기이고, FOR UPDATE 를 빼는 순간 판매분이 사라진다.
		tag, err := tx.Exec(ctx, `
			UPDATE product_variants
			SET stock = stock + $3, sku = NULLIF($4, ''), price_delta = $5, updated_at = now()
			WHERE id = $1 AND product_id = $2`,
			e.ID, productID, e.StockDelta, e.SKU, e.PriceDelta)
		var pgErr *pgconn.PgError
		switch {
		case errors.As(err, &pgErr) && pgErr.Code == "23505":
			return ErrSkuTaken
		case errors.As(err, &pgErr) && pgErr.Code == "23514":
			// CHECK (stock >= 0). 백오더는 없다 (D50).
			return ErrOutOfStock
		case err != nil:
			return err
		case tag.RowsAffected() == 0:
			return ErrNotFound
		}
	}
	return tx.Commit(ctx)
}

// AddVariant creates one option combination.
//
// 조합은 옵션 값의 곱으로 만든다. 같은 옵션 조합이 두 번 생기지 않도록
// `UNIQUE (product_id, option_values)` 가 막는다 (D30).
func (s *Store) AddVariant(ctx context.Context, productID string, options map[string]string,
	priceDelta int, sku string) (string, error) {

	if len(options) == 0 {
		return "", ErrOptionDuplicate
	}
	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO product_variants (product_id, option_values, price_delta, stock, sku)
		VALUES ($1, $2, $3, 0, NULLIF($4, '')) RETURNING id`,
		productID, options, priceDelta, sku).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		// 같은 조합이거나 같은 SKU 다. 둘을 한 오류로 접으면 운영자는 무엇이
		// 겹쳤는지 모른다.
		if pgErr.ConstraintName == "product_variants_sku_idx" {
			return "", ErrSkuTaken
		}
		return "", ErrOptionDuplicate
	}
	return id, err
}
