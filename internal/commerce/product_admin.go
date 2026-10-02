package commerce

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/emirue/ondolith/internal/commerce/commerceq"
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
	// ErrSkuLength 는 SKU 가 64자를 넘은 경우다 (D30). 막는 것은 DB 의 CHECK
	// (product_variants_sku_check) 이고, 이 이름은 그것을 옮긴 것이다.
	ErrSkuLength = errors.New("commerce: SKU 가 너무 깁니다")
	// ErrBarcodeTaken 은 다른 조합이 이미 쓰는 바코드다 (FR-627, 409). 같은
	// 바코드가 두 조합을 가리키면 스캔이 어느 재고를 움직일지 정할 수 없다.
	ErrBarcodeTaken = errors.New("commerce: 이미 쓰이는 바코드입니다")
	// ErrBarcodeLength 는 바코드가 64자를 넘는 경우다 (DB CHECK).
	ErrBarcodeLength = errors.New("commerce: 바코드가 너무 깁니다")
)

// checkBasePrice 는 기본가가 저장할 수 있는 범위인지 본다. 저장이 int32 변환을
// 거치므로, 여기를 지나지 않은 값은 조용히 접힌다 (MaxAmount).
func checkBasePrice(price int) error {
	if price < 0 {
		return fmt.Errorf("%w: 기본가 %d", ErrPriceNegative, price)
	}
	if price > MaxAmount {
		return fmt.Errorf("%w: 기본가 %d", ErrAmountTooBig, price)
	}
	return nil
}

// checkVariantInput 은 조합 편집 값의 범위다 — 차액·재고 증감은 음수일 수 있다.
func checkVariantInput(priceDelta, stockDelta int) error {
	if priceDelta < -MaxAmount || priceDelta > MaxAmount {
		return fmt.Errorf("%w: 가격 차액 %d", ErrAmountTooBig, priceDelta)
	}
	if stockDelta < -MaxAmount || stockDelta > MaxAmount {
		return fmt.Errorf("%w: 재고 증감 %d", ErrQuantityRange, stockDelta)
	}
	return nil
}

// ProductByID reads one product for A-502.
func (s *Store) ProductByID(ctx context.Context, id string) (*Product, error) {
	r, err := s.q.ProductByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return productFromRow(r), nil
}

// UpdateProduct is A-502's save.
//
// **재고·SKU·조합은 받지 않는다** (D19 A-502 받지 않는 필드) — 여기서 받으면
// 재고 절대값을 덮어쓰는 경로가 하나 더 생긴다. 그것은 A-503 소관이고,
// A-503 도 절대값을 받지 않는다.
func (s *Store) UpdateProduct(ctx context.Context, p Product) error {
	if err := checkBasePrice(p.BasePrice); err != nil {
		return err
	}
	// 상품과 카테고리 지정이 한 트랜잭션이다 (FR-615). 나누면 없는 카테고리로
	// 거부된 저장이 이름·가격은 이미 바꿔 놓는다.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // 커밋됐으면 무의미하다
	q := s.q.WithTx(tx)

	n, err := q.UpdateProduct(ctx, commerceq.UpdateProductParams{
		ID: p.ID, Slug: p.Slug, Name: p.Name, Description: p.Description,
		BasePrice: int32(p.BasePrice), IsVisible: p.Visible})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrSlugTaken
	}
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := setProductCategories(ctx, q, p.ID, p.CategoryIDs); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeleteProduct removes a product that was never ordered.
//
// 주문된 상품은 FK 가 막는다. **애플리케이션이 먼저 세어 보지 않는다** —
// 세고 나서 지우는 사이에 주문이 들어오면 그 검사는 통과하고 삭제도 통과하는
// 것처럼 보이지만, 실제로 막는 것은 FK 다. 여기서는 FK 의 오류를 읽는다.
func (s *Store) DeleteProduct(ctx context.Context, id string) error {
	n, err := s.q.DeleteProduct(ctx, id)
	var pgErr *pgconn.PgError
	// 23503 은 FK 위반, 23001 은 RESTRICT 위반이다. RESTRICT 는 별도 코드를
	// 쓰므로 23503 만 보면 이 경로가 통째로 500 이 된다.
	if errors.As(err, &pgErr) && (pgErr.Code == "23503" || pgErr.Code == "23001") {
		return ErrProductInUse
	}
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// VariantEdit is one row of A-503's editor.
type VariantEdit struct {
	ID  string
	SKU string
	// Barcode 는 빈 값이면 NULL 로 저장된다 (FR-627).
	Barcode    string
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
// **재고는 조정값으로만 움직인다.** queries/product_admin.sql EditVariant 의
// `SET stock = stock + $3` 이 한 문장이고, 읽고-더하고-쓰는 경로는 코드에 없다
// — 그 경로가 있으면 동시 두 건 중 하나가 다른 하나를 덮어쓴다.
//
// 잠금 순서는 variant id 오름차순이다 (AdjustStock 과 같은 이유): 순서를
// 요청자가 정하면 역순 요청 두 건이 교착한다.
func (s *Store) EditVariants(ctx context.Context, productID string, edits []VariantEdit) error {
	for _, e := range edits {
		if err := checkVariantInput(e.PriceDelta, e.StockDelta); err != nil {
			return err
		}
	}
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
	q := s.q.WithTx(tx)

	for _, e := range ordered {
		stock, err := q.LockVariantOfProduct(ctx, commerceq.LockVariantOfProductParams{ID: e.ID, ProductID: productID})
		if errors.Is(err, pgx.ErrNoRows) {
			// 다른 상품의 조합 ID 도 여기로 온다. product_id 술어가 막는다.
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		// 낙관적 잠금: 화면이 읽은 재고와 지금이 다르면 거부한다. 조정값이라도
		// 화면이 "3 → 5" 라고 보여준 결과가 달라지므로, 그 차이를 삼키지 않는다.
		if e.Version >= 0 && e.Version != int(stock) {
			return fmt.Errorf("%w: 화면 %d, 현재 %d", ErrStockVersion, e.Version, stock)
		}

		// **단일 문장이다.** 위에서 읽은 stock 을 쓰지 않는다 — 쓰면 그것이
		// 곧 읽고-더하고-쓰기이고, FOR UPDATE 를 빼는 순간 판매분이 사라진다.
		// 질의가 자리 인자($3·$4)라 생성 이름이 Stock·Column4 다 — 조정값과 SKU.
		n, err := q.EditVariant(ctx, commerceq.EditVariantParams{
			ID: e.ID, ProductID: productID, Stock: int32(e.StockDelta),
			Column4: e.SKU, PriceDelta: int32(e.PriceDelta), Barcode: e.Barcode})
		var pgErr *pgconn.PgError
		switch {
		case errors.As(err, &pgErr) && pgErr.Code == "23505":
			// 무엇이 겹쳤는지 가른다. 한 오류로 접으면 바코드가 겹쳤는데
			// 「이미 쓰이는 SKU」 라고 답한다.
			if pgErr.ConstraintName == "product_variants_barcode_idx" {
				return ErrBarcodeTaken
			}
			return ErrSkuTaken
		case errors.As(err, &pgErr) && pgErr.Code == "23514":
			// **어느 CHECK 인지 본다.** 전부 「재고 부족」으로 접으면 SKU 가
			// 긴 것도 재고 문제로 보고된다.
			switch pgErr.ConstraintName {
			case "product_variants_stock_check": // 백오더는 없다 (D50)
				return ErrOutOfStock
			case "product_variants_sku_check":
				return ErrSkuLength
			case "product_variants_barcode_check":
				return ErrBarcodeLength
			}
			return err
		case err != nil:
			return err
		case n == 0:
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
	if err := checkVariantInput(priceDelta, 0); err != nil {
		return "", err
	}
	raw, err := json.Marshal(options)
	if err != nil {
		return "", err
	}
	id, err := s.q.AddVariant(ctx, commerceq.AddVariantParams{
		ProductID: productID, OptionValues: raw, PriceDelta: int32(priceDelta), Sku: sku})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		// 같은 조합이거나 같은 SKU 다. 둘을 한 오류로 접으면 운영자는 무엇이
		// 겹쳤는지 모른다.
		if pgErr.ConstraintName == "product_variants_sku_idx" {
			return "", ErrSkuTaken
		}
		return "", ErrOptionDuplicate
	}
	if errors.As(err, &pgErr) && pgErr.ConstraintName == "product_variants_sku_check" {
		return "", ErrSkuLength
	}
	return id, err
}

// Option is one option group of a product — 이름과 값 목록 (색상: 빨강·파랑).
type Option struct {
	Name   string
	Values []string
}

// Options lists a product's option groups in display order.
func (s *Store) Options(ctx context.Context, productID string) ([]Option, error) {
	rows, err := s.q.Options(ctx, productID)
	if err != nil {
		return nil, err
	}
	var out []Option
	for _, r := range rows {
		o := Option{Name: r.Name}
		if err := json.Unmarshal(r.Values, &o.Values); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

// SetOptions replaces a product's option groups and **creates the missing
// combinations** (A-503, D19 「조합은 옵션 값의 곱으로 서버가 만든다」).
//
// 이 함수가 없어서 새 상품은 조합이 0개인 채로 남았고, 조합이 없으면 장바구니에
// 담을 것이 없다 — **아무것도 팔 수 없었다.** 스토어에 AddVariant 는 있었지만
// 어떤 화면도 부르지 않았다.
//
// **이미 있는 조합은 건드리지 않는다.** 재고와 SKU 가 거기 붙어 있으므로,
// 옵션을 다시 저장할 때마다 지웠다 만들면 팔린 이력과 재고가 사라진다. 빠진
// 곱만 채운다 — 옵션 값을 지워서 생긴 고아 조합도 남긴다. 그 조합을 가리키는
// 주문이 있을 수 있고(order_items 는 RESTRICT), 재고를 0 으로 두면 팔리지
// 않으므로 지울 이유가 없다.
func (s *Store) SetOptions(ctx context.Context, productID string, opts []Option) error {
	for i := range opts {
		opts[i].Name = strings.TrimSpace(opts[i].Name)
		if opts[i].Name == "" {
			return ErrOptionDuplicate
		}
		seen := map[string]bool{}
		var vals []string
		for _, v := range opts[i].Values {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			if seen[v] {
				return ErrOptionDuplicate
			}
			seen[v] = true
			vals = append(vals, v)
		}
		if len(vals) == 0 {
			return ErrOptionDuplicate
		}
		opts[i].Values = vals
	}
	if len(opts) == 0 {
		return ErrOptionDuplicate
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // 커밋됐으면 무의미하다
	q := s.q.WithTx(tx)

	if _, err := q.ProductExists(ctx, productID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}

	if err := q.DeleteProductOptions(ctx, productID); err != nil {
		return err
	}
	for i, o := range opts {
		vals, err := json.Marshal(o.Values)
		if err != nil {
			return err
		}
		if err := q.InsertProductOption(ctx, commerceq.InsertProductOptionParams{
			ProductID: productID, Name: o.Name, Values: vals, SortOrder: int32(i)}); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return ErrOptionDuplicate
			}
			return err
		}
	}

	// 곱을 만든다. `ON CONFLICT DO NOTHING` 이 이미 있는 조합을 지켜 준다 —
	// UNIQUE (product_id, option_values) 가 그 열쇠다.
	for _, combo := range optionProduct(opts) {
		raw, err := json.Marshal(combo)
		if err != nil {
			return err
		}
		if err := q.InsertVariantIfMissing(ctx, commerceq.InsertVariantIfMissingParams{
			ProductID: productID, OptionValues: raw}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// optionProduct 는 옵션 값의 곱(데카르트 곱)을 낸다.
//
// 조합 수는 값 개수의 곱이라 금방 커진다. 스키마가 그룹당 값을 50개로 막지만
// 그룹이 여럿이면 그것만으로는 부족하므로 여기서 상한을 둔다 — 옵션 몇 줄로
// 수만 행을 만들어 데이터베이스를 채우는 것을 막는다.
const maxVariants = 500

func optionProduct(opts []Option) []map[string]string {
	out := []map[string]string{{}}
	for _, o := range opts {
		var next []map[string]string
		for _, base := range out {
			for _, v := range o.Values {
				if len(next) >= maxVariants {
					return next
				}
				m := make(map[string]string, len(base)+1)
				for k, vv := range base {
					m[k] = vv
				}
				m[o.Name] = v
				next = append(next, m)
			}
		}
		out = next
	}
	return out
}
