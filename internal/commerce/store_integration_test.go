package commerce

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/emirue/ondolith/internal/migrations"
)

const dsnEnv = "ONDOLITH_TEST_DSN"

func testStore(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s 미설정 — 통합 테스트를 건너뜁니다 (make test-integration)", dsnEnv)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, stmt := range []string{"DROP SCHEMA public CASCADE", "CREATE SCHEMA public"} {
		if _, err := pool.Exec(ctx, stmt); err != nil && !strings.Contains(err.Error(), "does not exist") {
			t.Fatal(err)
		}
	}
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	if err := migrations.Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	return NewStore(pool), pool
}

// seedProduct makes a visible product with one visible variant.
func seedProduct(t *testing.T, pool *pgxpool.Pool, slug string, price, delta, stock int) (productID, variantID string) {
	t.Helper()
	ctx := context.Background()
	if err := pool.QueryRow(ctx,
		`INSERT INTO products (slug,name,base_price,is_visible) VALUES ($1,$1,$2,true) RETURNING id`,
		slug, price).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO product_variants (product_id,option_values,price_delta,stock)
		 VALUES ($1,'{"크기":"L"}',$2,$3) RETURNING id`,
		productID, delta, stock).Scan(&variantID); err != nil {
		t.Fatal(err)
	}
	return productID, variantID
}

func mkUser(t *testing.T, pool *pgxpool.Pool, email string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email,password_hash,display_name) VALUES ($1,'x','n') RETURNING id`,
		email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// 미노출 상품은 공개 목록에도 상세에도 나오지 않는다. 술어가 WHERE 에 있어야
// 데이터베이스 밖으로 나오지 않는다.
func TestHiddenProductsNeverLeaveTheDatabase(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	seedProduct(t, pool, "visible-tee", 12000, 0, 5)
	hidden, _ := seedProduct(t, pool, "hidden-tee", 12000, 0, 5)
	if _, err := pool.Exec(ctx, `UPDATE products SET is_visible = false WHERE id = $1`, hidden); err != nil {
		t.Fatal(err)
	}

	pub, err := s.ListProducts(ctx, ProductQuery{VisibleOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pub {
		if p.Slug == "hidden-tee" {
			t.Error("공개 목록에 미노출 상품이 있다")
		}
	}
	if len(pub) != 1 {
		t.Errorf("공개 목록 %d건, want 1", len(pub))
	}
	// 관리자는 본다 — 필터가 상수로 굳어 있으면 A-501 이 아무것도 못 본다.
	all, err := s.ListProducts(ctx, ProductQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Errorf("관리자 목록 %d건, want 2", len(all))
	}

	if _, err := s.ProductBySlug(ctx, "hidden-tee", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("미노출 상세 = %v, want ErrNotFound — 숨김이 아니라 없음이어야 한다", err)
	}
	if _, err := s.ProductBySlug(ctx, "hidden-tee", false); err != nil {
		t.Errorf("관리자 상세가 막혔다: %v", err)
	}
}

// 정렬은 허용 목록이다. 요청 문자열이 SQL 에 닿지 않는다.
func TestSortIsAnAllowList(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	seedProduct(t, pool, "b-cheap", 1000, 0, 5)
	seedProduct(t, pool, "a-dear", 9000, 0, 5)

	byPrice, err := s.ListProducts(ctx, ProductQuery{Sort: "price"})
	if err != nil {
		t.Fatal(err)
	}
	if byPrice[0].Slug != "b-cheap" {
		t.Errorf("price 정렬 첫 항목 = %s", byPrice[0].Slug)
	}
	byName, err := s.ListProducts(ctx, ProductQuery{Sort: "name"})
	if err != nil {
		t.Fatal(err)
	}
	if byName[0].Slug != "a-dear" {
		t.Errorf("name 정렬 첫 항목 = %s", byName[0].Slug)
	}
	for _, bad := range []string{"id; DROP TABLE products", "p.base_price--", "unknown"} {
		if _, err := s.ListProducts(ctx, ProductQuery{Sort: bad}); err == nil {
			t.Errorf("정렬 %q 가 통과했다", bad)
		}
	}
	// 통과한 뒤에도 표가 남아 있는지 — 위 단언이 오류만 보고 부작용을 안 본다.
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM products`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("상품이 %d건 남았다", n)
	}
}

// 목록은 상수 개수 쿼리다 (NFR-105). 상품이 늘어도 질의 수가 늘지 않는다.
func TestListIsAConstantNumberOfQueries(t *testing.T) {
	_, pool := testStore(t)
	ctx := context.Background()
	for _, slug := range []string{"p1", "p2", "p3", "p4", "p5"} {
		seedProduct(t, pool, slug, 1000, 0, 3)
	}

	// 질의를 실제로 센다. pg_stat_* 는 다른 연결과 트랜잭션까지 섞여 들어와
	// 무엇을 세는지 알 수 없었다 — 추적기는 이 풀이 보낸 것만 센다.
	counter := &queryCounter{}
	counted := poolWithTracer(t, counter)
	cs := NewStore(counted)

	counter.reset()
	list, err := cs.ListProducts(ctx, ProductQuery{VisibleOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 5 {
		t.Fatalf("%d건", len(list))
	}
	if n := counter.count(); n != 1 {
		t.Errorf("상품 5건 목록에 질의 %d건, want 1 — 상품 수에 비례하면 N+1 이다", n)
	}

	// 상품을 늘려도 질의 수가 그대로인지 — 1건일 때 우연히 1인 것과 구분된다.
	for _, slug := range []string{"p6", "p7", "p8"} {
		seedProduct(t, pool, slug, 1000, 0, 3)
	}
	counter.reset()
	if _, err := cs.ListProducts(ctx, ProductQuery{VisibleOnly: true}); err != nil {
		t.Fatal(err)
	}
	if n := counter.count(); n != 1 {
		t.Errorf("상품 8건 목록에 질의 %d건, want 1", n)
	}

	// 목록이 조합 요약을 함께 낸다 — 이것이 없으면 화면이 상품마다 조회한다.
	for _, p := range list {
		if !p.InStock {
			t.Errorf("%s 재고 요약이 비었다", p.Slug)
		}
	}
}

// queryCounter counts the queries this pool sends.
type queryCounter struct {
	mu sync.Mutex
	n  int
}

func (c *queryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn,
	_ pgx.TraceQueryStartData) context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return ctx
}

func (c *queryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *queryCounter) reset() { c.mu.Lock(); c.n = 0; c.mu.Unlock() }
func (c *queryCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func poolWithTracer(t *testing.T, tr pgx.QueryTracer) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv(dsnEnv))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Tracer = tr
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// 담기는 재고를 차감하지 않는다. 차감하면 담아 두고 안 사는 사람이 품절을 만든다.
func TestAddToCartDoesNotTakeStock(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	_, variant := seedProduct(t, pool, "tee", 12000, 1000, 5)

	if err := s.AddToCart(ctx, CartOwner{GuestKey: "guest-key-0123456789"}, variant, 2); err != nil {
		t.Fatal(err)
	}
	var stock int
	if err := pool.QueryRow(ctx, `SELECT stock FROM product_variants WHERE id = $1`, variant).
		Scan(&stock); err != nil {
		t.Fatal(err)
	}
	if stock != 5 {
		t.Errorf("담기 후 재고 %d, want 5", stock)
	}
}

// 같은 조합을 두 번 담으면 수량이 는다. 합친 수량으로 재고를 확인해야 한다 —
// 3개씩 두 번과 6개 한 번은 같은 요청이다.
func TestAddToCartSumsAndChecksTheTotalAgainstStock(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	_, variant := seedProduct(t, pool, "tee", 12000, 0, 5)
	owner := CartOwner{GuestKey: "guest-key-0123456789"}

	if err := s.AddToCart(ctx, owner, variant, 3); err != nil {
		t.Fatal(err)
	}
	if err := s.AddToCart(ctx, owner, variant, 3); !errors.Is(err, ErrOutOfStock) {
		t.Fatalf("합계 6 (재고 5) = %v, want ErrOutOfStock", err)
	}
	if err := s.AddToCart(ctx, owner, variant, 2); err != nil {
		t.Fatalf("합계 5 가 막혔다: %v", err)
	}

	items, err := s.CartItems(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("항목 %d개 — 같은 조합은 한 행이다", len(items))
	}
	if items[0].Quantity != 5 {
		t.Errorf("수량 %d, want 5", items[0].Quantity)
	}
	if items[0].UnitPrice != 12000 {
		t.Errorf("단가 %d, want 12000", items[0].UnitPrice)
	}
}

// 담기는 판매 중이 아닌 조합을 거부한다 (D50: 백오더 없음).
func TestAddToCartRefusesUnsellable(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	product, variant := seedProduct(t, pool, "tee", 12000, 0, 5)
	owner := CartOwner{GuestKey: "guest-key-0123456789"}

	if _, err := pool.Exec(ctx, `UPDATE products SET is_visible = false WHERE id = $1`, product); err != nil {
		t.Fatal(err)
	}
	if err := s.AddToCart(ctx, owner, variant, 1); !errors.Is(err, ErrNotSellable) {
		t.Errorf("숨긴 상품 = %v, want ErrNotSellable", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE products SET is_visible = true WHERE id = $1`, product); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE product_variants SET stock = 0 WHERE id = $1`, variant); err != nil {
		t.Fatal(err)
	}
	if err := s.AddToCart(ctx, owner, variant, 1); !errors.Is(err, ErrOutOfStock) {
		t.Errorf("재고 0 = %v, want ErrOutOfStock", err)
	}
}

// SC-3: 소유권이 WHERE 절에 있다. 항목 ID 만으로는 남의 장바구니를 못 건드린다.
func TestCartItemsAreLockedToTheirOwner(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	_, variant := seedProduct(t, pool, "tee", 12000, 0, 9)

	mine := CartOwner{GuestKey: "mine-0123456789abcd"}
	theirs := CartOwner{GuestKey: "theirs-0123456789ab"}
	if err := s.AddToCart(ctx, theirs, variant, 3); err != nil {
		t.Fatal(err)
	}
	items, err := s.CartItems(ctx, theirs)
	if err != nil || len(items) != 1 {
		t.Fatalf("%v / %d", err, len(items))
	}
	victim := items[0].ID

	if err := s.UpdateCartItem(ctx, mine, victim, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("남의 항목 수정 = %v, want ErrNotFound", err)
	}
	if err := s.UpdateCartItem(ctx, mine, victim, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("남의 항목 삭제 = %v, want ErrNotFound", err)
	}
	// 피해자 쪽은 그대로다 — 위 단언은 오류만 보고 부작용을 안 본다.
	items, _ = s.CartItems(ctx, theirs)
	if len(items) != 1 || items[0].Quantity != 3 {
		t.Errorf("남의 장바구니가 바뀌었다: %+v", items)
	}
	// 주인은 할 수 있다.
	if err := s.UpdateCartItem(ctx, theirs, victim, 1); err != nil {
		t.Errorf("주인의 수정이 막혔다: %v", err)
	}
	if err := s.UpdateCartItem(ctx, theirs, victim, 0); err != nil {
		t.Errorf("주인의 삭제가 막혔다: %v", err)
	}
	if items, _ = s.CartItems(ctx, theirs); len(items) != 0 {
		t.Errorf("삭제 뒤에도 %d건 남았다", len(items))
	}
}

// 주인은 정확히 하나다 (D30 carts_owner_is_one). 구조체가 그것을 막지 못하면
// DB 가 막지만, 잡히는 곳은 화면에서 500 이다.
func TestCartOwnerMustBeExactlyOne(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	_, variant := seedProduct(t, pool, "tee", 12000, 0, 5)
	user := mkUser(t, pool, "a@example.com")

	for _, bad := range []CartOwner{{}, {UserID: user, GuestKey: "g-0123456789abcdef"}} {
		if err := s.AddToCart(ctx, bad, variant, 1); !errors.Is(err, ErrCartOwner) {
			t.Errorf("%+v = %v, want ErrCartOwner", bad, err)
		}
		if _, err := s.CartItems(ctx, bad); !errors.Is(err, ErrCartOwner) {
			t.Errorf("%+v 읽기 = %v", bad, err)
		}
	}
}

// D50 「비회원 장바구니 병합」: 더하되 재고 상한에서 자르고, 비회원 쪽은 비운다.
func TestMergeCartsAddsAndClearsTheGuestSide(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	_, shared := seedProduct(t, pool, "shared", 1000, 0, 4)
	_, guestOnly := seedProduct(t, pool, "guest-only", 2000, 0, 9)
	user := mkUser(t, pool, "m@example.com")

	guest := CartOwner{GuestKey: "guest-0123456789abc"}
	member := CartOwner{UserID: user}
	if err := s.AddToCart(ctx, member, shared, 3); err != nil {
		t.Fatal(err)
	}
	if err := s.AddToCart(ctx, guest, shared, 3); err != nil {
		t.Fatal(err)
	}
	if err := s.AddToCart(ctx, guest, guestOnly, 2); err != nil {
		t.Fatal(err)
	}

	if err := s.MergeCarts(ctx, guest.GuestKey, user); err != nil {
		t.Fatal(err)
	}

	items, err := s.CartItems(ctx, member)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, it := range items {
		got[it.VariantID] = it.Quantity
	}
	// 3 + 3 = 6 이지만 재고가 4 다.
	if got[shared] != 4 {
		t.Errorf("겹치는 조합 수량 = %d, want 4 (재고 상한)", got[shared])
	}
	// 비회원 쪽에만 있던 것은 살아남는다 — 버리면 방금 담은 것이 사라진다.
	if got[guestOnly] != 2 {
		t.Errorf("비회원 전용 조합 = %d, want 2", got[guestOnly])
	}

	// 비회원 장바구니는 비었다. 남겨 두면 로그아웃한 같은 브라우저가 다시 본다.
	rest, err := s.CartItems(ctx, guest)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 0 {
		t.Errorf("비회원 장바구니에 %d건 남았다", len(rest))
	}
}

// 동시 차감 두 건이 마지막 재고 하나를 노리면 하나만 성공하고, **실패한 쪽은
// "품절" 이라고 말한다.**
//
// 성공 건수만 세면 잠금 없이도 통과한다 — DB 의 `CHECK (stock >= 0)` 이 둘째를
// 잡기 때문이다. 하지만 그때 나오는 것은 제약 위반이지 품절이 아니고, 화면은
// 그것을 500 으로 그린다. 잠금이 있어야 "재고가 없습니다" 를 말할 수 있다.
//
// 라운드를 반복하는 이유: 고루틴 둘이 실제로 겹치지 않으면 잠금 없이도 순서대로
// 실행돼 통과한다. 한 번이라도 겹치는 라운드에서 차이가 드러난다.
func TestConcurrentStockAdjustmentsSerialiseAndSayOutOfStock(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()

	const rounds = 30
	for r := 0; r < rounds; r++ {
		_, variant := seedProduct(t, pool, fmt.Sprintf("last-%d", r), 1000, 0, 1)

		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		for i := range errs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				tx, err := pool.Begin(ctx)
				if err != nil {
					errs[i] = err
					return
				}
				defer tx.Rollback(ctx)
				if err := s.AdjustStock(ctx, tx, []StockDelta{{VariantID: variant, Delta: -1}}); err != nil {
					errs[i] = err
					return
				}
				errs[i] = tx.Commit(ctx)
			}(i)
		}
		close(start)
		wg.Wait()

		ok := 0
		for _, err := range errs {
			if err == nil {
				ok++
				continue
			}
			if !errors.Is(err, ErrOutOfStock) {
				t.Fatalf("라운드 %d: 실패한 쪽이 %v — 품절이 아니라 제약 위반이면 화면이 500 을 그린다", r, err)
			}
		}
		if ok != 1 {
			t.Fatalf("라운드 %d: 동시 차감 %d건 성공, want 1 (%v)", r, ok, errs)
		}
		var stock int
		if err := pool.QueryRow(ctx, `SELECT stock FROM product_variants WHERE id = $1`, variant).
			Scan(&stock); err != nil {
			t.Fatal(err)
		}
		if stock != 0 {
			t.Fatalf("라운드 %d: 재고 %d, want 0", r, stock)
		}
	}
}

// A-509. 순환 판정은 순수 함수(category.go)가 하고, 저장소는 그것을 직렬화한다.
func TestReparentRefusesCyclesAndSerialises(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	mk := func(slug string) string {
		var id string
		if err := pool.QueryRow(ctx,
			`INSERT INTO categories (name,slug) VALUES ($1,$1) RETURNING id`, slug).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	a, b := mk("a"), mk("b")

	if err := s.Reparent(ctx, b, a); err != nil {
		t.Fatal(err)
	}
	// b 는 a 의 자식이다. a 를 b 아래로 넣으면 고리가 된다 — DB 는 못 잡는다.
	if err := s.Reparent(ctx, a, b); !errors.Is(err, ErrCategoryCycle) {
		t.Fatalf("순환 = %v, want ErrCategoryCycle", err)
	}
	// 실패한 이동이 부작용을 남기지 않는다.
	parents, err := s.CategoryParents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if parents[a] != "" || parents[b] != a {
		t.Errorf("계층이 바뀌었다: %v", parents)
	}

	// 직렬화가 실제로 걸리는지는 결정적으로 확인한다. 고루틴 둘을 띄우는
	// 방식은 겹치지 않으면 잠금 없이도 통과한다 — 무엇을 확인했는지 알 수 없다.
	//
	// 다른 트랜잭션이 같은 키를 쥐고 있으면 Reparent 는 기다려야 한다. 잠금이
	// 없으면 기다리지 않고 통과한다.
	c, d := mk("c"), mk("d")

	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, categoryLockKey); err != nil {
		t.Fatal(err)
	}

	blocked, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	err = s.Reparent(blocked, d, c)
	if err == nil {
		_ = holder.Rollback(ctx)
		t.Fatal("다른 트랜잭션이 잠금을 쥐고 있는데 이동이 통과했다 — 직렬화가 없다")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		_ = holder.Rollback(ctx)
		t.Fatalf("막힌 이동 = %v, want DeadlineExceeded", err)
	}

	// 잠금이 풀리면 같은 이동이 성공한다 — 위 실패가 잠금 때문이지 다른
	// 이유가 아니라는 것.
	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Reparent(ctx, d, c); err != nil {
		t.Errorf("잠금 해제 뒤 이동이 막혔다: %v", err)
	}
}
