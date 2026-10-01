package commerce

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/emirue/ondolith/internal/commerce/commerceq"
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
// 질의는 queries/*.sql 에 있고 q 가 그 생성 코드다 (D22 6절). 정렬 컬럼처럼
// 바인딩할 수 없는 것은 허용 목록으로 검사한 뒤 CASE 식으로 한 질의에 담는다.
type Store struct {
	pool *pgxpool.Pool
	q    *commerceq.Queries
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool, q: commerceq.New(pool)} }

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
// — 이스케이프가 아니라 목록이다 (D22 6절). 키는 queries/store.sql ListProducts
// 의 ORDER BY CASE 가 그대로 받고, 각 키의 실제 정렬은 거기 적혀 있다.
var productSortColumns = map[string]bool{
	"": true, "new": true, "price": true, "price_desc": true, "name": true,
}

// ListProducts is P-301's read. 상수 개수 쿼리다 (NFR-105): 목록과 조합 요약이
// 한 문장에 있고, 상품마다 조합을 다시 읽지 않는다.
//
// visibleOnly 는 공개 화면이 true 로 부른다. 필터가 WHERE 에 있는 이유는
// D30 이 정한 것과 같다 — 미노출 상품은 데이터베이스 밖으로 나오지 않아야 하고,
// 없는 술어는 잊힐 수 없다.
func (s *Store) ListProducts(ctx context.Context, opt ProductQuery) ([]Product, error) {
	if !productSortColumns[opt.Sort] {
		return nil, fmt.Errorf("%w: %q", ErrUnknownSort, opt.Sort)
	}
	limit, offset := opt.clamp()
	rows, err := s.q.ListProducts(ctx, commerceq.ListProductsParams{
		VisibleOnly: opt.VisibleOnly, CategoryID: nullable(opt.CategoryID), Sort: opt.Sort,
		Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return nil, err
	}
	var out []Product
	for _, r := range rows {
		out = append(out, productFromList(r))
	}
	return out, nil
}

// productFromList converts a list row. SearchProducts 의 행은 필드가 같아
// 구조체 변환으로 여기 온다.
func productFromList(r commerceq.ListProductsRow) Product {
	return Product{ID: r.ID, Slug: r.Slug, Name: r.Name, Description: r.Description,
		BasePrice: int(r.BasePrice), Visible: r.IsVisible, MinDelta: int(r.MinDelta), InStock: r.InStock}
}

func productFromRow(r commerceq.ProductByIDRow) *Product {
	return &Product{ID: r.ID, Slug: r.Slug, Name: r.Name, Description: r.Description,
		BasePrice: int(r.BasePrice), Visible: r.IsVisible}
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
	r, err := s.q.ProductBySlug(ctx, commerceq.ProductBySlugParams{Slug: slug, VisibleOnly: visibleOnly})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return productFromRow(commerceq.ProductByIDRow(r)), nil
}

// Variants lists a product's combinations. sellableOnly 는 P-304 가 쓴다.
func (s *Store) Variants(ctx context.Context, productID string, sellableOnly bool) ([]Variant, error) {
	rows, err := s.q.Variants(ctx, commerceq.VariantsParams{ProductID: productID, SellableOnly: sellableOnly})
	if err != nil {
		return nil, err
	}
	var out []Variant
	for _, r := range rows {
		v := Variant{ID: r.ID, ProductID: r.ProductID, SKU: r.Sku,
			PriceDelta: int(r.PriceDelta), Stock: int(r.Stock), Visible: r.IsVisible}
		if err := json.Unmarshal(r.OptionValues, &v.OptionValues); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// VariantForPurchase reads one combination WITH the product's visibility.
//
// 두 행을 따로 읽지 않는 이유: 상품 숨김과 조합 숨김을 각각 읽으면 그 사이에
// 하나가 바뀔 수 있고, 무엇보다 호출자가 한쪽만 확인하는 경로가 생긴다.
// Sellable 이 둘 다 요구하므로 한 문장이 둘 다 낸다.
func (s *Store) VariantForPurchase(ctx context.Context, variantID string) (*Variant, Sellable, error) {
	r, err := s.q.VariantForPurchase(ctx, variantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, Sellable{}, ErrNotFound
	}
	if err != nil {
		return nil, Sellable{}, err
	}
	v := Variant{ID: r.ID, ProductID: r.ProductID, SKU: r.Sku,
		PriceDelta: int(r.PriceDelta), Stock: int(r.Stock), Visible: r.IsVisible}
	if err := json.Unmarshal(r.OptionValues, &v.OptionValues); err != nil {
		return nil, Sellable{}, err
	}
	sell := Sellable{ProductVisible: r.ProductVisible, VariantVisible: v.Visible, Stock: v.Stock}
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
	q := s.q.WithTx(tx)
	for _, d := range ordered {
		stock, err := q.LockVariantStock(ctx, d.VariantID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if int(stock)+d.Delta < 0 {
			return fmt.Errorf("%w: 재고 %d, 요청 %d", ErrOutOfStock, stock, -d.Delta)
		}
		if err := q.AddVariantStock(ctx, commerceq.AddVariantStockParams{
			ID: d.VariantID, Delta: int32(d.Delta)}); err != nil {
			return err
		}
	}
	return nil
}

// CreateProduct is A-502's write.
func (s *Store) CreateProduct(ctx context.Context, p Product) (string, error) {
	if err := checkBasePrice(p.BasePrice); err != nil {
		return "", err
	}
	id, err := s.q.CreateProduct(ctx, commerceq.CreateProductParams{
		Slug: p.Slug, Name: p.Name, Description: p.Description,
		BasePrice: int32(p.BasePrice), IsVisible: p.Visible})
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
	return categoryParents(ctx, s.q)
}

// categoryParents is the shared body — Reparent 는 트랜잭션 안에서 같은 것을 읽는다.
func categoryParents(ctx context.Context, q *commerceq.Queries) (map[string]string, error) {
	rows, err := q.CategoryParents(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range rows {
		out[r.ID] = r.ParentID
	}
	return out, nil
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
	q := s.q.WithTx(tx)

	// 상수 키 하나. 카테고리 이동은 드물고, 키를 잘게 쪼개면 A→B 와 B→A 가
	// 서로 다른 키를 잡아 직렬화가 성립하지 않는다.
	if err := q.AdvisoryXactLock(ctx, categoryLockKey); err != nil {
		return err
	}

	parents, err := categoryParents(ctx, q)
	if err != nil {
		return err
	}

	if err := CheckReparent(parents, child, newParent); err != nil {
		return err
	}

	if err := q.SetCategoryParent(ctx, commerceq.SetCategoryParentParams{
		ID: child, ParentID: nullable(newParent)}); err != nil {
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

// ErrUnknownSort is a sort key that is not in the allow-list. 400 이지 500 이
// 아니다 — 사용자가 URL 을 고친 것이고 서버가 고장난 것이 아니다.
var ErrUnknownSort = errors.New("commerce: 알 수 없는 정렬")

// Category is one row of categories.
type Category struct {
	ID        string
	ParentID  string
	Name      string
	Slug      string
	SortOrder int
}

func categoryFromRow(r commerceq.CategoriesRow) Category {
	return Category{ID: r.ID, ParentID: r.ParentID, Name: r.Name, Slug: r.Slug, SortOrder: int(r.SortOrder)}
}

// Categories lists them all, in display order. 수십 행이라 전부 읽는다.
func (s *Store) Categories(ctx context.Context) ([]Category, error) {
	rows, err := s.q.Categories(ctx)
	if err != nil {
		return nil, err
	}
	var out []Category
	for _, r := range rows {
		out = append(out, categoryFromRow(r))
	}
	return out, nil
}

// CategoryBySlug is P-302's read.
func (s *Store) CategoryBySlug(ctx context.Context, slug string) (*Category, error) {
	r, err := s.q.CategoryBySlug(ctx, slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c := categoryFromRow(commerceq.CategoriesRow(r))
	return &c, nil
}

// CreateCategory is A-509's write (D19 A-509 「입력 필드」).
//
// **이것이 없어서 카테고리를 만들 방법이 없었다.** A-509 는 「이동」만 구현돼
// 있었고, 그래서 P-302 `/shop/c/{slug}` 는 어떤 주소로도 열 수 없는 화면이었다.
// 카테고리가 하나도 없으니 P-301 의 카테고리 줄도 영영 그려지지 않았다.
//
// 상위는 여기서 검사하지 않는다 — 새로 만드는 행은 자손이 없으므로 순환이
// 성립하지 않고, 존재 확인은 FK 가 한다.
func (s *Store) CreateCategory(ctx context.Context, c Category) (string, error) {
	id, err := s.q.CreateCategory(ctx, commerceq.CreateCategoryParams{
		ParentID: c.ParentID, Name: c.Name, Slug: c.Slug, SortOrder: int32(c.SortOrder)})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return "", ErrSlugTaken
		case "23503":
			return "", ErrCategoryMissing
		}
	}
	return id, err
}

// DeleteCategory is A-509's delete.
//
// 소속 상품이나 하위 카테고리가 있으면 거부한다 — 그 판정은 DB 의 RESTRICT 가
// 하고 여기서는 그 코드를 옮긴다 (D19 A-509 「거부 조건」). 사전 조회로 세면
// 세는 것과 지우는 것 사이에 다른 요청이 상품을 붙일 수 있다.
func (s *Store) DeleteCategory(ctx context.Context, id string) error {
	n, err := s.q.DeleteCategory(ctx, id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "23503" || pgErr.Code == "23001") {
		return ErrCategoryInUse
	}
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SearchProducts is P-305 (FR-614).
//
// 게시판 검색과 같은 이유로 `simple` config + 접두 질의다 (D30 Phase 2 측정):
// 한국어에 형태소 사전이 없으면 `english` 는 어간 추출로 오히려 덜 맞는다.
// 미노출 상품은 여기서도 나오지 않는다.
func (s *Store) SearchProducts(ctx context.Context, term string, page int) ([]Product, error) {
	q := ProductQuery{VisibleOnly: true, Page: page}
	limit, offset := q.clamp()
	rows, err := s.q.SearchProducts(ctx, commerceq.SearchProductsParams{
		Query: ToPrefixQuery(term), Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return nil, err
	}
	var out []Product
	for _, r := range rows {
		out = append(out, productFromList(commerceq.ListProductsRow(r)))
	}
	return out, nil
}

// ToPrefixQuery turns user text into a tsquery.
//
// 사용자 입력이 tsquery 문법에 닿지 않는다. `&`·`|`·`!`·`(` 를 그대로 넘기면
// 구문 오류가 500 이 되고, 운이 나쁘면 의도하지 않은 질의가 된다.
func ToPrefixQuery(term string) string {
	var words []string
	cur := make([]rune, 0, 32)
	flush := func() {
		if len(cur) > 0 {
			words = append(words, string(cur)+":*")
			cur = cur[:0]
		}
	}
	for _, r := range term {
		switch {
		case r == ' ' || r == '\t' || r == '\n':
			flush()
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r > 127:
			cur = append(cur, r)
		default:
			// 나머지는 버린다. 이스케이프가 아니라 제거다 — 무엇이 안전한지
			// 목록으로 정하는 쪽이 무엇이 위험한지 목록으로 정하는 쪽보다 짧다.
			flush()
		}
	}
	flush()
	if len(words) == 0 {
		return ""
	}
	out := words[0]
	for _, w := range words[1:] {
		out += " & " + w
	}
	return out
}

// OptionGroup is one option name and the values that exist for it.
type OptionGroup struct {
	Name   string
	Values []string
}

// OptionGroups derives the selector from the variants themselves.
//
// product_options 를 따로 읽지 않는다. 그 표는 관리자가 편집하는 정의이고,
// 화면이 물어야 하는 것은 "실제로 존재하는 조합" 이다 — 둘이 어긋나면
// 선택할 수 있는데 담을 수 없는 조합이 나온다.
func OptionGroups(variants []Variant) []OptionGroup {
	order := []string{}
	seen := map[string]map[string]bool{}
	for _, v := range variants {
		for _, k := range sortedKeys(v.OptionValues) {
			if seen[k] == nil {
				seen[k] = map[string]bool{}
				order = append(order, k)
			}
			seen[k][v.OptionValues[k]] = true
		}
	}
	out := make([]OptionGroup, 0, len(order))
	for _, name := range order {
		vals := make([]string, 0, len(seen[name]))
		for v := range seen[name] {
			vals = append(vals, v)
		}
		insertionSort(vals)
		out = append(out, OptionGroup{Name: name, Values: vals})
	}
	return out
}

// MatchVariant finds the combination matching every picked value.
//
// 부분 선택이면 nil 이다. "아직 다 안 골랐다" 와 "그런 조합이 없다" 를 화면이
// 구분해야 하는데, 여기서 아무거나 돌려주면 그 구분이 사라진다.
func MatchVariant(variants []Variant, picked map[string]string) *Variant {
	if len(picked) == 0 {
		return nil
	}
	for i := range variants {
		if len(variants[i].OptionValues) != len(picked) {
			continue
		}
		ok := true
		for k, want := range picked {
			if variants[i].OptionValues[k] != want {
				ok = false
				break
			}
		}
		if ok {
			return &variants[i]
		}
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	insertionSort(out)
	return out
}

func insertionSort(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// NewGuestKey makes a cart key for a visitor who is not logged in.
//
// 세션에 담기는 값이고 추측 가능해서는 안 된다 — carts.guest_key 하나가 곧
// 그 장바구니의 열쇠다.
func NewGuestKey() (string, error) { return newRandomKey() }
