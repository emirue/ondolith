package commerce

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emirue/ondolith/internal/commerce/commerceq"
)

// 2026-10 저장 계층 감사가 실측으로 잡은 것들의 회귀 검사. 각 테스트의 주석이
// 고치기 전에 무슨 일이 났는지 적는다.

// FR-618: 차액 결제가 한 번 실패하면 그 행이 유니크 자리를 영구히 차지해, 재결제가
// 「이미 결제된 주문」으로만 끝났다.
func TestFailedExchangeDiffCanBeRetried(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, returnNo, amount := exchangeAwaitingDiff(t, s, pool, "tee-retry")

	declined := &fakeGateway{err: errors.New("카드 한도 초과")}
	if err := s.ConfirmExchangeDiff(ctx, declined, "toss", orderNo, returnNo, "",
		"pk-fail", amount, time.Now()); err == nil {
		t.Fatal("준비: 실패해야 하는 승인이 통과했다")
	}

	// 같은 승인 키가 다시 오면 「이미 결제됨」이 아니라 「키 재사용」이다.
	if err := s.ConfirmExchangeDiff(ctx, okGateway(), "toss", orderNo, returnNo, "",
		"pk-fail", amount, time.Now()); !errors.Is(err, ErrPaymentKeyReused) {
		t.Errorf("같은 승인 키 재시도 = %v, want ErrPaymentKeyReused", err)
	}

	if err := s.ConfirmExchangeDiff(ctx, okGateway(), "toss", orderNo, returnNo, "",
		"pk-retry", amount, time.Now()); err != nil {
		t.Fatalf("실패 뒤 재결제가 막혔다: %v", err)
	}
	if got := statusOf(t, s, orderNo); got != StatusExchangeShipped {
		t.Errorf("재결제 뒤 상태 %s, want %s", got, StatusExchangeShipped)
	}

	// 살아 있는 두 번째는 여전히 DB 가 막는다 (FR-608).
	_, err := pool.Exec(ctx, `
		INSERT INTO payments (order_id, return_id, kind, status, pg, payment_key, approved_amount)
		SELECT r.order_id, r.id, '교환차액', '대기', 'toss', 'pk-second', $2
		FROM returns r WHERE r.return_no = $1`, returnNo, amount)
	if err == nil || !strings.Contains(err.Error(), "payments_exchange_idx") {
		t.Errorf("살아 있는 두 번째 차액 결제 = %v, want payments_exchange_idx 위반", err)
	}
}

// FR-619: 필수 → 선택으로 개정된 종류에서 옛 필수본이 계속 요구됐다. 주문서는
// 시행본(선택)만 보여 주므로 모든 주문이 ErrTermsRequired 로 끝났다.
func TestTermRevisedToOptionalIsNoLongerDemanded(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	_, variant := seedProduct(t, pool, "tee", 12000, 0, 5)
	owner := CartOwner{GuestKey: "guest-0123456789abc"}
	if err := s.AddToCart(ctx, owner, variant, 1); err != nil {
		t.Fatal(err)
	}

	mkTerm(t, pool, "전자금융", true) // v1 필수
	if _, err := pool.Exec(ctx, `
		INSERT INTO terms (kind, version, body, effective_at, is_required)
		VALUES ('전자금융', 'v2', '선택으로 바뀐 개정본', now(), false)`); err != nil {
		t.Fatal(err)
	}
	stillRequired := mkTerm(t, pool, "이용약관", true)
	now := time.Now().Add(time.Minute)

	got, err := s.RequiredTerms(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != stillRequired {
		t.Errorf("필수 약관 %+v, want 이용약관 v1 하나 — 선택으로 개정된 종류의 옛 필수본이 요구된다", got)
	}

	form := testForm()
	form.AgreedTerms = []string{stillRequired}
	if _, err := s.CreateOrder(ctx, owner, "", form, testShipping, 0, now); err != nil {
		t.Errorf("주문서가 보여 준 필수 약관에 전부 동의했는데 막혔다: %v", err)
	}
}

// 한 종류에 시행 시각이 같은 버전이 둘이면 시행본이 정해지지 않는다.
func TestTermsWithTheSameEffectiveTimeAreRefused(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	now := time.Now()
	day := now.AddDate(0, 1, 0).Truncate(24 * time.Hour)

	if _, err := s.AddTerms(ctx, Terms{Kind: "service", Version: "1.0", Body: "본문",
		EffectiveAt: day, Required: true}, now); err != nil {
		t.Fatal(err)
	}
	_, err := s.AddTerms(ctx, Terms{Kind: "service", Version: "2.0", Body: "본문",
		EffectiveAt: day, Required: true}, now)
	if !errors.Is(err, ErrTermsEffectiveTaken) {
		t.Errorf("같은 시행일의 두 번째 버전 = %v, want ErrTermsEffectiveTaken", err)
	}
	// 버전이 겹친 것은 여전히 제 이름으로 온다.
	_, err = s.AddTerms(ctx, Terms{Kind: "service", Version: "1.0", Body: "본문",
		EffectiveAt: day.AddDate(0, 0, 1), Required: true}, now)
	if !errors.Is(err, ErrTermsVersionTaken) {
		t.Errorf("같은 버전 = %v, want ErrTermsVersionTaken", err)
	}
}

// 가상계좌를 발급받은 주문(입금대기)에서 /checkout/fail 을 열면 살아 있는 결제가
// '실패' 로 내려갔고, 뒤에 온 입금 웹훅은 대조할 결제를 찾지 못했다.
func TestFailPaymentLeavesADepositPendingOrderAlone(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()

	waiting, total := seedOrder(t, s, pool, "va", 1)
	depositOrder(t, s, pool, waiting, total, "s3cret")
	if err := s.FailPayment(ctx, waiting, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("입금대기 주문 = %v, want ErrNotFound", err)
	}
	if got := paymentStatus(t, s, waiting); got != "대기" {
		t.Errorf("입금대기 주문의 결제 %q, want 대기 — 입금을 기다리는 결제를 내렸다", got)
	}

	// 결제대기 주문의 결과 불명 결제는 내린다 (P-409).
	pending, total := seedOrder(t, s, pool, "card", 1)
	if _, err := s.ConfirmPayment(ctx, &fakeGateway{err: ErrPaymentUnknown}, "toss",
		pending, "pk-card", total, time.Now()); !errors.Is(err, ErrPaymentUnknown) {
		t.Fatal(err)
	}
	if err := s.FailPayment(ctx, pending, ""); err != nil {
		t.Fatal(err)
	}
	if got := paymentStatus(t, s, pending); got != "실패" {
		t.Errorf("결제대기 주문의 결제 %q, want 실패", got)
	}
}

// whileWaitingOn holds a row lock on order_items.id = held, starts fn — which
// must wait for that row — and, while it waits, checks that `probe` can still
// be locked. 그것이 「held 를 probe 보다 먼저 잡는다」의 관측이다: 순서가 거꾸로면
// fn 은 probe 를 쥔 채 held 를 기다리고, 그 모양이 교착의 절반이다.
func whileWaitingOn(t *testing.T, pool *pgxpool.Pool, held, probe string, fn func() error) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT 1 FROM order_items WHERE id = $1 FOR UPDATE`, held); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- fn() }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("잠긴 품목을 기다리지 않고 끝났다: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("5초 안에 잠금 대기에 들어가지 않았다")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if _, err := pool.Exec(ctx, probe); err != nil {
		t.Errorf("품목을 기다리는 동안 뒤 순서의 행을 이미 쥐고 있다 — 잠금 순서가 거꾸로다: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Errorf("잠금이 풀린 뒤 실패했다: %v", err)
	}
}

// 잠금 순서는 order_items(id 오름차순) → payments 다. CancelOrder·RejectRefund 는
// 결제를 먼저 잡았고, 품목을 잡고 결제를 기다리는 RequestRefund 와 교착했다 (실측).
func TestCancelAndRejectLockItemsBeforeThePayment(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	paymentFree := func(orderNo string) string {
		return `SELECT 1 FROM payments WHERE order_id = (SELECT id FROM orders WHERE order_no = '` +
			orderNo + `') FOR UPDATE NOWAIT`
	}

	t.Run("CancelOrder", func(t *testing.T) {
		orderNo, _, _ := paidOrder(t, s, pool, "cancel", 1)
		item := itemsOf(t, s, orderNo)[0].ID
		whileWaitingOn(t, pool, item, paymentFree(orderNo), func() error {
			_, err := s.CancelOrder(ctx, orderNo, "P-506", "cancel-key")
			return err
		})
	})
	t.Run("RejectRefund", func(t *testing.T) {
		orderNo, _, _ := paidOrder(t, s, pool, "reject", 1)
		refundID, _ := refundAll(t, s, orderNo, "reject-key")
		item := itemsOf(t, s, orderNo)[0].ID
		whileWaitingOn(t, pool, item, paymentFree(orderNo),
			func() error { return s.RejectRefund(ctx, refundID, "거부") })
	})
}

// OpenReturn 은 폼이 준 순서대로 품목을 잡았다. 역순으로 온 요청은 같은 주문의
// 환불 요청(id 오름차순)과 서로의 품목을 기다렸다.
func TestOpenReturnLocksItemsInIDOrder(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	_, v1 := seedProduct(t, pool, "tee-a", 12000, 0, 5)
	_, v2 := seedProduct(t, pool, "tee-b", 15000, 0, 5)
	owner := CartOwner{GuestKey: "guest-two-items-0123456"}
	for _, v := range []string{v1, v2} {
		if err := s.AddToCart(ctx, owner, v, 1); err != nil {
			t.Fatal(err)
		}
	}
	order, err := s.CreateOrder(ctx, owner, "", testForm(), testShipping, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmPayment(ctx, okGateway(), "toss", order.OrderNo, "pk-two", order.Total, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, to := range []Status{StatusPreparing, StatusShipping, StatusDelivered} {
		if err := s.TransitionOrder(ctx, order.OrderNo, to, "A-506"); err != nil {
			t.Fatal(err)
		}
	}
	items := itemsOf(t, s, order.OrderNo)
	low, high := items[0].ID, items[1].ID
	if low > high {
		low, high = high, low
	}

	// 작은 id 를 쥐고, 폼은 큰 id 를 먼저 보낸다. 정렬했다면 작은 쪽에서 기다리므로
	// 큰 쪽은 아직 잡히지 않았다.
	whileWaitingOn(t, pool, low,
		`SELECT 1 FROM order_items WHERE id = '`+high+`' FOR UPDATE NOWAIT`,
		func() error {
			_, err := s.OpenReturn(ctx, order.OrderNo, ReturnRequest{Kind: KindReturn,
				Lines: []RefundLine{{OrderItemID: high, Quantity: 1}, {OrderItemID: low, Quantity: 1}},
			}, "P-511", time.Now())
			return err
		})
}

// 재고를 잠근 동안 그 조합을 장바구니에 담는 INSERT 가 줄을 섰다 — FOR UPDATE 는
// 외래키 검사(FOR KEY SHARE)와 충돌한다. 재고 갱신은 키를 바꾸지 않는다.
func TestStockLockDoesNotBlockCartInserts(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	productID, variant := seedProduct(t, pool, "tee", 12000, 0, 5)

	for name, lock := range map[string]func(tx pgx.Tx) error{
		"AdjustStock": func(tx pgx.Tx) error {
			return s.AdjustStock(ctx, tx, []StockDelta{{VariantID: variant, Delta: -1}})
		},
		"EditVariants 의 잠금": func(tx pgx.Tx) error {
			_, err := s.q.WithTx(tx).LockVariantOfProduct(ctx,
				commerceq.LockVariantOfProductParams{ID: variant, ProductID: productID})
			return err
		},
	} {
		holder, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := lock(holder); err != nil {
			t.Fatal(err)
		}
		// 막히면 영원히 기다리지 않고 0.5초 뒤 실패한다.
		cart, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cart.Exec(ctx, `SET LOCAL lock_timeout = '500ms'`); err != nil {
			t.Fatal(err)
		}
		_, err = cart.Exec(ctx, `
			WITH c AS (INSERT INTO carts (guest_key) VALUES (gen_random_uuid()::text) RETURNING id)
			INSERT INTO cart_items (cart_id, variant_id, quantity) SELECT c.id, $1, 1 FROM c`, variant)
		if err != nil {
			t.Errorf("%s 중 장바구니 담기가 막혔다: %v", name, err)
		}
		_ = cart.Rollback(ctx)
		if err := holder.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

// product_variants_sku_check (SKU 64자) 위반이 「재고 부족」으로 보고됐다.
func TestLongSkuIsNotReportedAsOutOfStock(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	productID, v1 := seedProduct(t, pool, "tee-sku", 12000, 0, 5)
	long := strings.Repeat("S", 65)

	err := s.EditVariants(ctx, productID, []VariantEdit{{ID: v1, SKU: long, Version: -1}})
	if !errors.Is(err, ErrSkuLength) {
		t.Errorf("EditVariants(긴 SKU) = %v, want ErrSkuLength", err)
	}
	_, err = s.AddVariant(ctx, productID, map[string]string{"크기": "M"}, 0, long)
	if !errors.Is(err, ErrSkuLength) {
		t.Errorf("AddVariant(긴 SKU) = %v, want ErrSkuLength", err)
	}
	// 재고 CHECK 는 여전히 제 이름으로 온다. 버전 검사를 끄면(-1) 판정은 DB 몫이다.
	err = s.EditVariants(ctx, productID, []VariantEdit{{ID: v1, StockDelta: -6, Version: -1}})
	if !errors.Is(err, ErrOutOfStock) {
		t.Errorf("재고를 음수로 = %v, want ErrOutOfStock", err)
	}
}

// 금액·수량은 integer 컬럼에 int32 변환을 거쳐 들어간다. 범위를 넘는 값은 조용히
// 다른 수로 접혔다 — 4294967297 원이 1 원으로 저장됐다.
func TestOutOfRangeAmountsAreRefusedNotWrapped(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	const wraps = 4294967297 // int32 로 접으면 1

	if _, err := s.CreateProduct(ctx, Product{Slug: "big", Name: "big", BasePrice: wraps}); !errors.Is(err, ErrAmountTooBig) {
		t.Errorf("CreateProduct = %v, want ErrAmountTooBig", err)
	}
	productID, variant := seedProduct(t, pool, "tee", 12000, 0, 5)
	if err := s.UpdateProduct(ctx, Product{ID: productID, Slug: "tee", Name: "tee", BasePrice: wraps}); !errors.Is(err, ErrAmountTooBig) {
		t.Errorf("UpdateProduct = %v, want ErrAmountTooBig", err)
	}
	if err := s.EditVariants(ctx, productID, []VariantEdit{{ID: variant, PriceDelta: wraps, Version: -1}}); !errors.Is(err, ErrAmountTooBig) {
		t.Errorf("EditVariants(차액) = %v, want ErrAmountTooBig", err)
	}
	if err := s.EditVariants(ctx, productID, []VariantEdit{{ID: variant, StockDelta: wraps, Version: -1}}); !errors.Is(err, ErrQuantityRange) {
		t.Errorf("EditVariants(재고 증감) = %v, want ErrQuantityRange", err)
	}
	if _, err := s.AddVariant(ctx, productID, map[string]string{"크기": "M"}, wraps, ""); !errors.Is(err, ErrAmountTooBig) {
		t.Errorf("AddVariant = %v, want ErrAmountTooBig", err)
	}
	if _, err := s.Stocktake(ctx, variant, 5, wraps+4); !errors.Is(err, ErrQuantityRange) {
		t.Errorf("Stocktake(장부) = %v, want ErrQuantityRange", err)
	}

	var price, delta, stock int
	if err := pool.QueryRow(ctx, `
		SELECT p.base_price, v.price_delta, v.stock FROM products p
		JOIN product_variants v ON v.product_id = p.id WHERE v.id = $1`, variant).Scan(&price, &delta, &stock); err != nil {
		t.Fatal(err)
	}
	if price != 12000 || delta != 0 || stock != 5 {
		t.Errorf("거부된 값이 저장됐다: 기본가 %d, 차액 %d, 재고 %d", price, delta, stock)
	}

	// 총액도 같은 컬럼에 들어간다. 품목마다는 들어가도 합이 넘치면 거부한다.
	_, _, _, err := Total([]Line{{BasePrice: 1_500_000_000, Quantity: 1}, {BasePrice: 1_500_000_000, Quantity: 1}}, Shipping{})
	if !errors.Is(err, ErrAmountTooBig) {
		t.Errorf("Total(30억) = %v, want ErrAmountTooBig", err)
	}
}

// OpenOrders 는 미완결 주문만 센다 (FR-710). 종료 상태가 섞여도 세지 않는다.
func TestOpenOrdersCountsOnlyNonTerminal(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	seedOrder(t, s, pool, "pending", 1) // 결제대기
	paidOrder(t, s, pool, "paid", 1)    // 결제완료
	done, _, _ := paidOrder(t, s, pool, "done", 1)
	if _, err := s.CancelOrder(ctx, done, "P-506", "k"); err != nil { // 취소 — 종료
		t.Fatal(err)
	}
	n, err := s.OpenOrders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("미완결 주문 %d건, want 2", n)
	}
}

// 상품 목록은 쪽을 안쪽 질의에서 고른 뒤 조합 요약을 그 행에만 붙이고 바깥에서 다시
// 정렬한다. 정렬 키마다 쪽을 이어 붙인 순서가 「키 → id」 전순서와 같고, 요약 값이
// 제 상품의 것이어야 한다.
func TestProductListPagesInTheSameTotalOrder(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()

	// 이름은 ASCII 소문자다 — DB 콜레이션과 Go 의 비교가 같은 순서를 낸다.
	// 가격은 일부러 겹친다: 동률은 id 가 가른다.
	type row struct {
		id, name string
		price    int
		created  time.Time
	}
	var all []row
	for name, price := range map[string]int{"delta": 5000, "alpha": 9000, "echo": 5000, "bravo": 1000, "charlie": 5000} {
		id, _ := seedProduct(t, pool, name, price, 0, 3)
		all = append(all, row{id: id, name: name, price: price})
	}
	hidden, _ := seedProduct(t, pool, "foxtrot", 100, 0, 3)
	if _, err := pool.Exec(ctx, `UPDATE products SET is_visible = false WHERE id = $1`, hidden); err != nil {
		t.Fatal(err)
	}
	for i := range all {
		if err := pool.QueryRow(ctx, `SELECT created_at FROM products WHERE id = $1`, all[i].id).Scan(&all[i].created); err != nil {
			t.Fatal(err)
		}
	}

	want := func(sort string, rows []row) []string {
		sorted := slices.Clone(rows)
		slices.SortFunc(sorted, func(a, b row) int {
			var c int
			switch sort {
			case "price":
				c = cmp.Compare(a.price, b.price)
			case "price_desc":
				c = cmp.Compare(b.price, a.price)
			case "name":
				c = cmp.Compare(a.name, b.name)
			default: // "", "new"
				c = b.created.Compare(a.created)
			}
			if c == 0 {
				c = cmp.Compare(a.id, b.id) // id 는 언제나 오름차순
			}
			return c
		})
		var ids []string
		for _, r := range sorted {
			ids = append(ids, r.id)
		}
		return ids
	}
	collect := func(q ProductQuery) []string {
		t.Helper()
		var ids []string
		for page := 1; page <= 3; page++ { // 2건씩 세 쪽 = 5건
			q.Page, q.PerPage = page, 2
			list, err := s.ListProducts(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range list {
				ids = append(ids, p.ID)
			}
		}
		return ids
	}
	for _, sort := range []string{"", "new", "price", "price_desc", "name"} {
		if got, w := collect(ProductQuery{VisibleOnly: true, Sort: sort}), want(sort, all); !slices.Equal(got, w) {
			t.Errorf("sort=%q:\n got %v\nwant %v", sort, got, w)
		}
	}

	// 카테고리 필터도 안쪽에 있다 — 쪽을 고르기 전에 걸러야 쪽이 찬다.
	var cat string
	if err := pool.QueryRow(ctx, `INSERT INTO categories (name, slug) VALUES ('c', 'c') RETURNING id`).Scan(&cat); err != nil {
		t.Fatal(err)
	}
	inCat := []row{all[0], all[2], all[4]}
	for _, r := range inCat {
		if _, err := pool.Exec(ctx, `INSERT INTO product_categories (product_id, category_id) VALUES ($1, $2)`, r.id, cat); err != nil {
			t.Fatal(err)
		}
	}
	if got, w := collect(ProductQuery{VisibleOnly: true, CategoryID: cat, Sort: "price"}), want("price", inCat); !slices.Equal(got, w) {
		t.Errorf("카테고리 + price:\n got %v\nwant %v", got, w)
	}

	// 조합 요약은 제 상품의 것이다. 조합 둘(-500·품절, +300·재고) 과 조합 없는 상품.
	two, none := all[0].id, all[1].id
	if _, err := pool.Exec(ctx, `DELETE FROM product_variants WHERE product_id = ANY($1)`, []string{two, none}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO product_variants (product_id, option_values, price_delta, stock) VALUES
		($1, '{"크기":"S"}', -500, 0), ($1, '{"크기":"L"}', 300, 2)`, two); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListProducts(ctx, ProductQuery{VisibleOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	summary := map[string]Product{}
	for _, p := range list {
		summary[p.ID] = p
	}
	if p := summary[two]; p.MinDelta != -500 || !p.InStock {
		t.Errorf("조합 둘인 상품의 요약 (차액 %d, 재고 %v), want (-500, true)", p.MinDelta, p.InStock)
	}
	if p := summary[none]; p.MinDelta != 0 || p.InStock {
		t.Errorf("조합 없는 상품의 요약 (차액 %d, 재고 %v), want (0, false)", p.MinDelta, p.InStock)
	}
	if _, ok := summary[hidden]; ok {
		t.Error("미노출 상품이 공개 목록에 나왔다")
	}
}

// A-603 웹훅 이력은 미처리(수신)가 앞이고 그다음 최신순이다. 00023 의 인덱스는 이
// 정렬식 그대로다 — 식이 어긋나면 인덱스는 남고 순서만 조용히 느려진다.
func TestWebhookHistoryPutsUnhandledFirst(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	for i, status := range []string{"처리완료", "수신", "실패", "수신"} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO webhook_events (pg, event_id, status, payload, created_at)
			VALUES ('toss', $1, $2, '{}', now() - make_interval(mins => $3))`,
			"e"+itoa(i), status, 10-i); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.WebhookHistory(ctx, nil, nil, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range got {
		ids = append(ids, r.EventID)
	}
	// 수신 둘(최신 e3 → e1), 그다음 나머지 중 최신(e2). 상한 3.
	if want := []string{"e3", "e1", "e2"}; !slices.Equal(ids, want) {
		t.Errorf("웹훅 이력 %v, want %v", ids, want)
	}

	var def string
	if err := pool.QueryRow(ctx,
		`SELECT indexdef FROM pg_indexes WHERE indexname = 'webhook_events_history_idx'`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(def, "((status = '수신'::text)) DESC, created_at DESC") {
		t.Errorf("인덱스가 정렬식과 다르다: %s", def)
	}
}

// 상품 검색은 **순위로 쪽을 고른다.** 안쪽 질의가 순위 없이 자르면 가장 잘 맞는
// 상품이 id 순서에 밀려 뒤 쪽으로 간다 — 한 쪽에 다 들어오는 검색에서는 보이지 않는다.
func TestSearchProductsRanksBeforePaging(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	for i := range 21 {
		if _, err := pool.Exec(ctx, `
			INSERT INTO products (slug, name, base_price, is_visible)
			VALUES ($1, '쪽나눔 상품', 1000, true)`, "plain-"+itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	// id 가 가장 뒤인데 순위는 가장 높다.
	if _, err := pool.Exec(ctx, `
		INSERT INTO products (id, slug, name, description, base_price, is_visible)
		VALUES ('ffffffff-ffff-ffff-ffff-ffffffffffff', 'best', '쪽나눔 쪽나눔', '쪽나눔 쪽나눔', 1000, true)`); err != nil {
		t.Fatal(err)
	}
	page1, err := s.SearchProducts(ctx, "쪽나눔", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 20 || page1[0].Slug != "best" {
		t.Errorf("1쪽 %d건, 첫 항목 %q — want 20건, 첫 항목 best", len(page1), page1[0].Slug)
	}
	page2, err := s.SearchProducts(ctx, "쪽나눔", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 2 {
		t.Errorf("2쪽 %d건, want 2건", len(page2))
	}
	for _, p := range page2 {
		if p.Slug == "best" {
			t.Error("순위가 가장 높은 상품이 2쪽에 있다")
		}
	}
}
