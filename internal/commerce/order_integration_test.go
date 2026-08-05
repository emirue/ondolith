package commerce

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var testShipping = Shipping{FlatFee: 3000, FreeThreshold: 50000}

func testForm() OrderForm {
	return OrderForm{
		ReceiverName: "받는이", ReceiverPhone: "010-0000-0000",
		Postcode: "12345", Address1: "서울시 어딘가",
		OrdererEmail: "a@example.com", OrdererPhone: "010-1111-1111",
	}
}

func mkTerm(t *testing.T, pool *pgxpool.Pool, kind string, required bool) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO terms (kind, version, body, effective_at, is_required)
		VALUES ($1, 'v1', '본문', now(), $2) RETURNING id`, kind, required).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// 한 트랜잭션 안에서 재고 차감 → 금액 계산 → 스냅샷 기록이 일어난다.
func TestCreateOrderSnapshotsAndTakesStock(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	_, variant := seedProduct(t, pool, "tee", 12000, 1000, 5)
	owner := CartOwner{GuestKey: "guest-0123456789abc"}
	if err := s.AddToCart(ctx, owner, variant, 2); err != nil {
		t.Fatal(err)
	}

	order, err := s.CreateOrder(ctx, owner, "", testForm(), testShipping, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	// 금액은 서버가 계산한다: (12000+1000)×2 = 26000, 5만원 미만이라 배송비 3000.
	if order.Goods != 26000 || order.Fee != 3000 || order.Total != 29000 {
		t.Errorf("= (%d, %d, %d), want (26000, 3000, 29000)", order.Goods, order.Fee, order.Total)
	}
	if order.Status != StatusPaymentPending {
		t.Errorf("상태 %s, want %s", order.Status, StatusPaymentPending)
	}
	// 주문번호는 순번이 아니다 (SC-3 3항).
	if len(order.OrderNo) < 6 {
		t.Errorf("주문번호 %q", order.OrderNo)
	}

	// **DB 에 들어간 값**을 확인한다. 반환값만 보면 orders.total_amount 에 상품
	// 합계를 써도 통과하고, FR-607 의 대조는 그 컬럼을 단일 출처로 쓴다.
	var stored int
	var storedStatus string
	if err := pool.QueryRow(ctx,
		`SELECT total_amount, status FROM orders WHERE id = $1`, order.ID).
		Scan(&stored, &storedStatus); err != nil {
		t.Fatal(err)
	}
	if stored != 29000 {
		t.Errorf("orders.total_amount = %d, want 29000 (배송비 포함)", stored)
	}
	if storedStatus != string(StatusPaymentPending) {
		t.Errorf("orders.status = %q", storedStatus)
	}

	var stock int
	if err := pool.QueryRow(ctx, `SELECT stock FROM product_variants WHERE id = $1`, variant).
		Scan(&stock); err != nil {
		t.Fatal(err)
	}
	if stock != 3 {
		t.Errorf("재고 %d, want 3", stock)
	}

	// 스냅샷 — 상품이 바뀌어도 주문서는 그대로다 (FR-612).
	if _, err := pool.Exec(ctx,
		`UPDATE products SET name = '바뀐 이름', base_price = 99000`); err != nil {
		t.Fatal(err)
	}
	var name, label string
	var unit, line int
	if err := pool.QueryRow(ctx, `
		SELECT product_name, option_label, unit_price, line_amount FROM order_items
		WHERE order_id = $1`, order.ID).Scan(&name, &label, &unit, &line); err != nil {
		t.Fatal(err)
	}
	if name != "tee" || unit != 13000 || line != 26000 {
		t.Errorf("스냅샷 = %q / %d / %d", name, unit, line)
	}
	if label != "크기: L" {
		t.Errorf("옵션 표기 = %q, want %q", label, "크기: L")
	}

	// 장바구니는 비었다 — 남겨 두면 뒤로 가기 한 번이 같은 것을 또 주문한다.
	items, err := s.CartItems(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Errorf("주문 뒤 장바구니에 %d건 남았다", len(items))
	}
}

// 어느 단계가 실패해도 재고가 줄지 않는다.
func TestFailedOrderLeavesStockUntouched(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	_, variant := seedProduct(t, pool, "tee", 12000, 0, 5)
	owner := CartOwner{GuestKey: "guest-0123456789abc"}
	if err := s.AddToCart(ctx, owner, variant, 2); err != nil {
		t.Fatal(err)
	}
	mkTerm(t, pool, "이용약관", true)

	// 필수 약관 미동의 → 거부.
	if _, err := s.CreateOrder(ctx, owner, "", testForm(), testShipping, time.Now()); !errors.Is(err, ErrTermsRequired) {
		t.Fatalf("약관 미동의 = %v, want ErrTermsRequired", err)
	}
	assertStock(t, pool, variant, 5)

	// 주문자 연락처 없음 → 거부.
	bad := testForm()
	bad.OrdererPhone = ""
	if _, err := s.CreateOrder(ctx, owner, "", bad, testShipping, time.Now()); !errors.Is(err, ErrOrdererContact) {
		t.Errorf("연락처 없는 주문 = %v, want ErrOrdererContact — 제약 위반이면 화면이 500 을 그린다", err)
	}
	assertStock(t, pool, variant, 5)

	// 담아 둔 사이에 품절 → 거부.
	if _, err := pool.Exec(ctx, `UPDATE product_variants SET stock = 1 WHERE id = $1`, variant); err != nil {
		t.Fatal(err)
	}
	term := mkTerm(t, pool, "구매약관", true)
	form := testForm()
	form.AgreedTerms = []string{term}
	if _, err := s.CreateOrder(ctx, owner, "", form, testShipping, time.Now()); !errors.Is(err, ErrOutOfStock) {
		t.Errorf("재고 부족 = %v, want ErrOutOfStock", err)
	}
	assertStock(t, pool, variant, 1)

	// 주문 행도 남지 않았다 — 롤백이 되돌린다.
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM orders`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("실패한 주문이 %d건 남았다", n)
	}
}

func assertStock(t *testing.T, pool *pgxpool.Pool, variant string, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(context.Background(),
		`SELECT stock FROM product_variants WHERE id = $1`, variant).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("재고 %d, want %d", got, want)
	}
}

// 동시 주문 2건이 마지막 재고 1개를 노릴 때 하나만 성공한다.
func TestConcurrentOrdersForTheLastUnit(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()

	const rounds = 20
	for r := 0; r < rounds; r++ {
		_, variant := seedProduct(t, pool, orderSlug(r), 1000, 0, 1)
		owners := []CartOwner{
			{GuestKey: guestKey(r, 0)},
			{GuestKey: guestKey(r, 1)},
		}
		for _, o := range owners {
			if err := s.AddToCart(ctx, o, variant, 1); err != nil {
				t.Fatal(err)
			}
		}

		var wg sync.WaitGroup
		errs := make([]error, 2)
		gate := make(chan struct{})
		for i, o := range owners {
			wg.Add(1)
			go func(i int, o CartOwner) {
				defer wg.Done()
				<-gate
				_, errs[i] = s.CreateOrder(ctx, o, "", testForm(), testShipping, time.Now())
			}(i, o)
		}
		close(gate)
		wg.Wait()

		ok := 0
		for _, err := range errs {
			if err == nil {
				ok++
				continue
			}
			if !errors.Is(err, ErrOutOfStock) {
				t.Fatalf("라운드 %d: 진 쪽이 %v — 품절이 아니면 화면이 500 을 그린다", r, err)
			}
		}
		if ok != 1 {
			t.Fatalf("라운드 %d: 동시 주문 %d건 성공, want 1 (%v)", r, ok, errs)
		}
		assertStock(t, pool, variant, 0)
	}
}

func orderSlug(r int) string { return "race-" + itoa(r) }
func guestKey(r, i int) string {
	return "guest-" + itoa(r) + "-" + itoa(i) + "-0123456789"
}
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// FR-619: 필수 약관 동의가 기록된다. 본문은 복사하지 않고 참조만 남긴다.
func TestOrderRecordsTermAgreements(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	_, variant := seedProduct(t, pool, "tee", 12000, 0, 5)
	owner := CartOwner{GuestKey: "guest-0123456789abc"}
	if err := s.AddToCart(ctx, owner, variant, 1); err != nil {
		t.Fatal(err)
	}
	required := mkTerm(t, pool, "이용약관", true)
	optional := mkTerm(t, pool, "마케팅수신", false)

	form := testForm()
	form.AgreedTerms = []string{required, optional}
	order, err := s.CreateOrder(ctx, owner, "", form, testShipping, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM order_agreements WHERE order_id = $1`, order.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("동의 이력 %d건, want 2", n)
	}
	// 동의 이력이 가리키는 약관은 지워지지 않는다 (RESTRICT).
	if _, err := pool.Exec(ctx, `DELETE FROM terms WHERE id = $1`, required); err == nil {
		t.Error("동의 이력이 가리키는 약관이 지워졌다")
	}
}

// 선택 약관만 동의하고 필수를 빼면 거부한다. 종류마다 최신 시행본 하나만
// 요구한다 — 여러 버전을 다 요구하면 개정할 때마다 과거 버전에도 동의해야 한다.
func TestOnlyTheLatestRequiredTermPerKindIsDemanded(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	_, variant := seedProduct(t, pool, "tee", 12000, 0, 5)
	owner := CartOwner{GuestKey: "guest-0123456789abc"}
	if err := s.AddToCart(ctx, owner, variant, 1); err != nil {
		t.Fatal(err)
	}

	old := mkTerm(t, pool, "이용약관", true)
	var newer string
	if err := pool.QueryRow(ctx, `
		INSERT INTO terms (kind, version, body, effective_at, is_required)
		VALUES ('이용약관', 'v2', '본문', now(), true) RETURNING id`).Scan(&newer); err != nil {
		t.Fatal(err)
	}

	form := testForm()
	form.AgreedTerms = []string{old}
	if _, err := s.CreateOrder(ctx, owner, "", form, testShipping, time.Now()); !errors.Is(err, ErrTermsRequired) {
		t.Errorf("옛 버전만 동의 = %v, want ErrTermsRequired", err)
	}
	form.AgreedTerms = []string{newer}
	if _, err := s.CreateOrder(ctx, owner, "", form, testShipping, time.Now()); err != nil {
		t.Errorf("최신 버전 동의가 막혔다: %v", err)
	}
}

// 빈 장바구니로는 주문이 생기지 않는다.
func TestEmptyCartCannotOrder(t *testing.T) {
	s, _ := testStore(t)
	owner := CartOwner{GuestKey: "guest-0123456789abc"}
	if _, err := s.CreateOrder(context.Background(), owner, "", testForm(), testShipping, time.Now()); !errors.Is(err, ErrCartEmpty) {
		t.Errorf("= %v, want ErrCartEmpty", err)
	}
}

// 옵션 표기는 키 순서가 고정돼야 한다. map 순회는 Go 가 매번 섞으므로,
// 정렬하지 않으면 같은 조합의 주문 두 건이 다른 표기를 갖는다.
func TestOptionLabelIsStable(t *testing.T) {
	opts := map[string]string{"사이즈": "L", "색상": "검정", "소재": "면"}
	first := OptionLabel(opts)
	for i := 0; i < 50; i++ {
		if got := OptionLabel(opts); got != first {
			t.Fatalf("표기가 흔들린다: %q vs %q", first, got)
		}
	}
	if first != "사이즈: L / 색상: 검정 / 소재: 면" {
		t.Errorf("= %q", first)
	}
	if OptionLabel(nil) != "" {
		t.Errorf("빈 옵션 = %q", OptionLabel(nil))
	}
}

// 선택 약관은 동의하지 않아도 주문이 된다. is_required 를 안 보면 마케팅 수신
// 동의가 결제를 막는다.
func TestOptionalTermsDoNotBlockTheOrder(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	_, variant := seedProduct(t, pool, "tee", 12000, 0, 5)
	owner := CartOwner{GuestKey: "guest-0123456789abc"}
	if err := s.AddToCart(ctx, owner, variant, 1); err != nil {
		t.Fatal(err)
	}
	required := mkTerm(t, pool, "이용약관", true)
	mkTerm(t, pool, "마케팅수신", false)

	form := testForm()
	form.AgreedTerms = []string{required}
	if _, err := s.CreateOrder(ctx, owner, "", form, testShipping, time.Now()); err != nil {
		t.Errorf("선택 약관 미동의로 주문이 막혔다: %v", err)
	}
}

// 같은 장바구니로 동시에 두 번 주문하면 하나만 성립한다.
//
// 주문 버튼 두 번 누르기가 이것이다. 장바구니를 잠그지 않으면 두 요청이 같은
// 항목을 읽고 각각 주문을 만든다 — 재고가 넉넉하면 DB 도 막지 않는다.
func TestDoubleSubmitCreatesOneOrder(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()

	const rounds = 20
	for r := 0; r < rounds; r++ {
		// 재고를 넉넉히 둔다 — 재고가 막는 것과 장바구니 잠금이 막는 것을
		// 구분하기 위해서다.
		_, variant := seedProduct(t, pool, "dbl-"+itoa(r), 1000, 0, 50)
		owner := CartOwner{GuestKey: guestKey(r, 9)}
		if err := s.AddToCart(ctx, owner, variant, 1); err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		errs := make([]error, 2)
		gate := make(chan struct{})
		for i := range errs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-gate
				_, errs[i] = s.CreateOrder(ctx, owner, "", testForm(), testShipping, time.Now())
			}(i)
		}
		close(gate)
		wg.Wait()

		ok := 0
		for _, err := range errs {
			if err == nil {
				ok++
				continue
			}
			if !errors.Is(err, ErrCartEmpty) {
				t.Fatalf("라운드 %d: 두 번째 제출 = %v, want ErrCartEmpty", r, err)
			}
		}
		if ok != 1 {
			t.Fatalf("라운드 %d: 주문 %d건 성립, want 1 (%v)", r, ok, errs)
		}
		// 재고도 한 번만 줄었다.
		assertStock(t, pool, variant, 49)
	}
}

// 담아 둔 뒤에 상품이 숨겨지면 주문할 수 없다.
//
// 담기 시점의 검사만으로는 부족하다 — 장바구니는 며칠 남아 있고, 그 사이
// A-503 이 상품을 내릴 수 있다. 재고 검사(AdjustStock)는 이것을 잡지 못한다:
// 숨긴 상품에도 재고는 남아 있다.
func TestHiddenAfterAddingCannotBeOrdered(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	product, variant := seedProduct(t, pool, "tee", 12000, 0, 5)
	owner := CartOwner{GuestKey: "guest-0123456789abc"}
	if err := s.AddToCart(ctx, owner, variant, 1); err != nil {
		t.Fatal(err)
	}

	for _, hide := range []string{
		`UPDATE products SET is_visible = false WHERE id = $1`,
		`UPDATE product_variants SET is_visible = false WHERE product_id = $1`,
	} {
		if _, err := pool.Exec(ctx, hide, product); err != nil {
			t.Fatal(err)
		}
		_, err := s.CreateOrder(ctx, owner, "", testForm(), testShipping, time.Now())
		if !errors.Is(err, ErrNotSellable) {
			t.Errorf("%s → %v, want ErrNotSellable", hide, err)
		}
		assertStock(t, pool, variant, 5)
		if _, err := pool.Exec(ctx,
			`UPDATE products SET is_visible = true; UPDATE product_variants SET is_visible = true`); err != nil {
			t.Fatal(err)
		}
	}
	// 다시 보이면 주문된다 — 위 실패가 숨김 때문이라는 것.
	if _, err := s.CreateOrder(ctx, owner, "", testForm(), testShipping, time.Now()); err != nil {
		t.Errorf("복구 뒤 주문이 막혔다: %v", err)
	}
}
