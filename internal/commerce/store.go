package commerce

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound  = errors.New("commerce: 찾을 수 없습니다")
	ErrSlugTaken = errors.New("commerce: 이미 사용 중인 주소입니다")
)

// Store is the commerce read/write path. 규칙은 이 파일에 없다 — state.go,
// amount.go, stock.go 가 갖고 있고 여기서 부른다. 섞으면 규칙이 SQL 문자열
// 안으로 흩어져서 테스트할 수 없어진다.
//
// 모든 값은 바인딩으로 간다. 정렬 컬럼처럼 바인딩할 수 없는 것은 허용 목록으로
// 검사한다 (D22 6절).
type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Product is one row of products, plus what the list screen needs.
type Product struct {
	ID          string
	Slug        string
	Name        string
	Description string
	BasePrice   int
	Visible     bool
	// MinDelta 는 판매 가능한 조합 중 가장 싼 것의 차액이다. 목록이 "12,000원~"
	// 을 그리는 데 쓴다 — 조합을 다시 읽지 않기 위해 목록 질의가 함께 낸다.
	MinDelta int
	// InStock 은 판매 가능한 조합이 하나라도 있는지다. 목록에서 품절 배지를
	// 그리는 데 쓰고, 이것이 없으면 화면이 상품마다 조합을 조회한다 (N+1).
	InStock bool
}

// Variant is one option combination.
type Variant struct {
	ID           string
	ProductID    string
	OptionValues map[string]string
	SKU          string
	PriceDelta   int
	Stock        int
	Visible      bool
}

// productSortColumns is the allow-list. 요청에서 온 문자열을 SQL 에 잇지 않는다
// — 이스케이프가 아니라 목록이다 (D22 6절).
var productSortColumns = map[string]string{
	"":           "p.created_at DESC, p.id",
	"new":        "p.created_at DESC, p.id",
	"price":      "p.base_price, p.id",
	"price_desc": "p.base_price DESC, p.id",
	"name":       "p.name, p.id",
}

// ListProducts is P-301's read. 상수 개수 쿼리다 (NFR-105): 목록과 조합 요약이
// 한 문장에 있고, 상품마다 조합을 다시 읽지 않는다.
//
// visibleOnly 는 공개 화면이 true 로 부른다. 필터가 WHERE 에 있는 이유는
// D30 이 정한 것과 같다 — 미노출 상품은 데이터베이스 밖으로 나오지 않아야 하고,
// 없는 술어는 잊힐 수 없다.
func (s *Store) ListProducts(ctx context.Context, opt ProductQuery) ([]Product, error) {
	order, ok := productSortColumns[opt.Sort]
	if !ok {
		return nil, fmt.Errorf("commerce: 알 수 없는 정렬: %q", opt.Sort)
	}
	limit, offset := opt.clamp()

	q := `
		SELECT p.id, p.slug, p.name, p.description, p.base_price, p.is_visible,
		       COALESCE(v.min_delta, 0), COALESCE(v.in_stock, false)
		FROM products p
		LEFT JOIN LATERAL (
			SELECT min(price_delta) AS min_delta, bool_or(stock > 0) AS in_stock
			FROM product_variants
			WHERE product_id = p.id AND is_visible
		) v ON true
		WHERE ($1::boolean IS NOT TRUE OR p.is_visible)
		  AND ($2::uuid IS NULL OR EXISTS (
		        SELECT 1 FROM product_categories pc
		        WHERE pc.product_id = p.id AND pc.category_id = $2))
		ORDER BY ` + order + `
		LIMIT $3 OFFSET $4`

	var category any
	if opt.CategoryID != "" {
		category = opt.CategoryID
	}
	rows, err := s.pool.Query(ctx, q, opt.VisibleOnly, category, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Product
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.Slug, &p.Name, &p.Description,
			&p.BasePrice, &p.Visible, &p.MinDelta, &p.InStock); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ProductQuery is what the list screen may ask for. 클라이언트가 정렬 컬럼을
// 문자열로 보내지만, 그 문자열은 위 허용 목록의 키일 뿐 SQL 에 닿지 않는다.
type ProductQuery struct {
	VisibleOnly bool
	CategoryID  string
	Sort        string
	Page        int
	PerPage     int
}

func (q ProductQuery) clamp() (limit, offset int) {
	per := q.PerPage
	if per < 1 {
		per = 20
	}
	if per > 100 {
		per = 100
	}
	page := q.Page
	if page < 1 {
		page = 1
	}
	return per, (page - 1) * per
}

// ProductBySlug is P-303's read. visibleOnly 가 true 면 미노출 상품은 404 다 —
// 숨김이 아니라 없음이어야 URL 을 아는 사람도 존재를 확인하지 못한다.
func (s *Store) ProductBySlug(ctx context.Context, slug string, visibleOnly bool) (*Product, error) {
	const q = `
		SELECT id, slug, name, description, base_price, is_visible
		FROM products WHERE slug = $1 AND ($2::boolean IS NOT TRUE OR is_visible)`
	var p Product
	err := s.pool.QueryRow(ctx, q, slug, visibleOnly).Scan(&p.ID, &p.Slug, &p.Name,
		&p.Description, &p.BasePrice, &p.Visible)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &p, err
}

// Variants lists a product's combinations. sellableOnly 는 P-304 가 쓴다.
func (s *Store) Variants(ctx context.Context, productID string, sellableOnly bool) ([]Variant, error) {
	const q = `
		SELECT id, product_id, option_values, COALESCE(sku, ''), price_delta, stock, is_visible
		FROM product_variants
		WHERE product_id = $1 AND ($2::boolean IS NOT TRUE OR (is_visible AND stock > 0))
		ORDER BY price_delta, id`
	rows, err := s.pool.Query(ctx, q, productID, sellableOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Variant
	for rows.Next() {
		var v Variant
		var raw []byte
		if err := rows.Scan(&v.ID, &v.ProductID, &raw, &v.SKU,
			&v.PriceDelta, &v.Stock, &v.Visible); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &v.OptionValues); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// VariantForPurchase reads one combination WITH the product's visibility.
//
// 두 행을 따로 읽지 않는 이유: 상품 숨김과 조합 숨김을 각각 읽으면 그 사이에
// 하나가 바뀔 수 있고, 무엇보다 호출자가 한쪽만 확인하는 경로가 생긴다.
// Sellable 이 둘 다 요구하므로 한 문장이 둘 다 낸다.
func (s *Store) VariantForPurchase(ctx context.Context, variantID string) (*Variant, Sellable, error) {
	const q = `
		SELECT v.id, v.product_id, v.option_values, COALESCE(v.sku, ''), v.price_delta,
		       v.stock, v.is_visible, p.is_visible, p.base_price
		FROM product_variants v JOIN products p ON p.id = v.product_id
		WHERE v.id = $1`
	var v Variant
	var sell Sellable
	var raw []byte
	var basePrice int
	err := s.pool.QueryRow(ctx, q, variantID).Scan(&v.ID, &v.ProductID, &raw, &v.SKU,
		&v.PriceDelta, &v.Stock, &v.Visible, &sell.ProductVisible, &basePrice)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, Sellable{}, ErrNotFound
	}
	if err != nil {
		return nil, Sellable{}, err
	}
	if err := json.Unmarshal(raw, &v.OptionValues); err != nil {
		return nil, Sellable{}, err
	}
	sell.VariantVisible = v.Visible
	sell.Stock = v.Stock
	return &v, sell, nil
}

// AdjustStock applies deltas inside a transaction, locking each row first.
//
// `SELECT ... FOR UPDATE` 로 잠그고 나서 더한다. 잠그지 않으면 두 요청이 같은
// 잔량을 읽고 둘 다 통과하는데, DB 의 `CHECK (stock >= 0)` 이 그중 하나를
// 잡더라도 그것은 오류이지 "품절" 이 아니다 — 사용자에게 이유를 말할 수 없다.
//
// 잠금 순서는 variant_id 오름차순이다. 순서가 없으면 두 주문이 서로의 행을
// 기다리는 교착이 생긴다.
func (s *Store) AdjustStock(ctx context.Context, tx pgx.Tx, deltas []StockDelta) error {
	ordered := append([]StockDelta(nil), deltas...)
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0 && ordered[j].VariantID < ordered[j-1].VariantID; j-- {
			ordered[j], ordered[j-1] = ordered[j-1], ordered[j]
		}
	}
	for _, d := range ordered {
		var stock int
		err := tx.QueryRow(ctx,
			`SELECT stock FROM product_variants WHERE id = $1 FOR UPDATE`, d.VariantID).Scan(&stock)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if stock+d.Delta < 0 {
			return fmt.Errorf("%w: 재고 %d, 요청 %d", ErrOutOfStock, stock, -d.Delta)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE product_variants SET stock = stock + $2, updated_at = now() WHERE id = $1`,
			d.VariantID, d.Delta); err != nil {
			return err
		}
	}
	return nil
}

// CreateProduct is A-502's write.
func (s *Store) CreateProduct(ctx context.Context, p Product) (string, error) {
	const q = `
		INSERT INTO products (slug, name, description, base_price, is_visible)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`
	var id string
	err := s.pool.QueryRow(ctx, q, p.Slug, p.Name, p.Description, p.BasePrice, p.Visible).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return "", ErrSlugTaken
	}
	return id, err
}

// CategoryParents reads the whole hierarchy for CheckReparent.
//
// 전부 읽는다. 카테고리는 수십 행이고, 그렇게 해야 순환 판정이 순수 함수로
// 남는다 (category.go). 재귀 CTE 로 옮기면 그 판정이 SQL 안으로 들어가 테스트가
// DB 를 요구하게 된다.
func (s *Store) CategoryParents(ctx context.Context) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, COALESCE(parent_id::text, '') FROM categories`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, parent string
		if err := rows.Scan(&id, &parent); err != nil {
			return nil, err
		}
		out[id] = parent
	}
	return out, rows.Err()
}

// Reparent moves a category, serialised against other movers.
//
// pg_advisory_xact_lock 이 필요한 이유: CheckReparent 는 읽은 시점의 계층으로
// 판정하는데, 두 요청이 각각 합법인 이동을 동시에 하면 합쳐서 고리가 된다
// (A→B 와 B→A). 같은 키로 직렬화하면 두 번째가 첫 번째의 결과를 보고 거부된다.
//
// 트랜잭션 잠금이라 커밋·롤백에서 자동으로 풀린다 — 명시적 해제 코드가 없다는
// 것이 곧 해제를 잊을 수 없다는 뜻이다.
func (s *Store) Reparent(ctx context.Context, child, newParent string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// 상수 키 하나. 카테고리 이동은 드물고, 키를 잘게 쪼개면 A→B 와 B→A 가
	// 서로 다른 키를 잡아 직렬화가 성립하지 않는다.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, categoryLockKey); err != nil {
		return err
	}

	rows, err := tx.Query(ctx, `SELECT id, COALESCE(parent_id::text, '') FROM categories`)
	if err != nil {
		return err
	}
	parents := map[string]string{}
	for rows.Next() {
		var id, parent string
		if err := rows.Scan(&id, &parent); err != nil {
			rows.Close()
			return err
		}
		parents[id] = parent
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	if err := CheckReparent(parents, child, newParent); err != nil {
		return err
	}

	var parent any
	if newParent != "" {
		parent = newParent
	}
	if _, err := tx.Exec(ctx,
		`UPDATE categories SET parent_id = $2, updated_at = now() WHERE id = $1`,
		child, parent); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// categoryLockKey is arbitrary but fixed. 다른 advisory lock 을 쓰게 되면 여기
// 목록을 늘려서 충돌을 눈에 보이게 한다.
const categoryLockKey = 0x0C47_0001

// unmarshalOptions decodes the option_values JSONB.
func unmarshalOptions(raw []byte, into *map[string]string) error {
	return json.Unmarshal(raw, into)
}
