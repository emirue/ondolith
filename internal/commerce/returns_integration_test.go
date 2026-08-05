package commerce

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// deliveredOrder makes a paid order and walks it to 배송완료.
func deliveredOrder(t *testing.T, s *Store, pool *pgxpool.Pool, slug string, qty int) (string, int) {
	t.Helper()
	ctx := context.Background()
	orderNo, _, goods := paidOrder(t, s, pool, slug, qty)
	for _, to := range []Status{StatusPreparing, StatusShipping, StatusDelivered} {
		if err := s.TransitionOrder(ctx, orderNo, to, "A-506"); err != nil {
			t.Fatal(err)
		}
	}
	return orderNo, goods
}

// **수거 확인 없이 환불이 나가지 않는다** (D14 「수거 우선」).
//
// 물건을 못 받고 돈만 나가는 것을 상태로 막는다 — 상태머신에 `반품접수 → 환불`
// 화살표가 아예 없고, 그것이 이 규칙의 전부다.
func TestRefundNeedsPickupConfirmation(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, _ := deliveredOrder(t, s, pool, "tee", 2)
	items := itemsOf(t, s, orderNo)

	ret, err := s.OpenReturn(ctx, orderNo, ReturnRequest{
		Kind:  KindReturn,
		Lines: []RefundLine{{OrderItemID: items[0].ID, Quantity: 1}},
	}, "P-511", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	// 수거 전이다. 환불이 거부된다.
	if _, err := s.SettleReturn(ctx, ret.ReturnNo, "A-511", "k1"); !errors.Is(err, ErrPickupRequired) {
		t.Fatalf("수거 전 환불 = %v, want ErrPickupRequired", err)
	}
	// 돈도 수량도 움직이지 않았다.
	_, refunded, err := s.RefundedTotal(ctx, orderNo)
	if err != nil {
		t.Fatal(err)
	}
	if refunded != 0 {
		t.Errorf("수거 전인데 %d 이 선점됐다", refunded)
	}
	if again := itemsOf(t, s, orderNo); again[0].Settled != 0 {
		t.Errorf("수거 전인데 수량 %d 가 소진됐다", again[0].Settled)
	}

	// 수거를 확인하면 나간다.
	if err := s.ConfirmPickup(ctx, ret.ReturnNo, "구매자", "차감", 3000, "A-511"); err != nil {
		t.Fatal(err)
	}
	amount, err := s.SettleReturn(ctx, ret.ReturnNo, "A-511", "k2")
	if err != nil {
		t.Fatalf("수거 확인 뒤 환불이 막혔다: %v", err)
	}
	if amount <= 0 {
		t.Errorf("환불액 %d", amount)
	}
}

// 환불액은 **스냅샷에서 서버가 계산**한다 (FR-617). 반품 배송비는 수거 시점에
// 찍은 스냅샷에서 뺀다 — A-512 를 나중에 바꿔도 과거 건이 달라지지 않는다.
func TestReturnRefundIsComputedFromSnapshots(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, goods := deliveredOrder(t, s, pool, "tee", 2)
	items := itemsOf(t, s, orderNo)

	ret, err := s.OpenReturn(ctx, orderNo, ReturnRequest{
		Kind:  KindReturn,
		Lines: []RefundLine{{OrderItemID: items[0].ID, Quantity: 1}},
	}, "P-511", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	const fee = 3000
	if err := s.ConfirmPickup(ctx, ret.ReturnNo, "구매자", "차감", fee, "A-511"); err != nil {
		t.Fatal(err)
	}

	amount, err := s.SettleReturn(ctx, ret.ReturnNo, "A-511", "k1")
	if err != nil {
		t.Fatal(err)
	}
	// 한 개의 몫(상품 합계의 절반)에서 배송비를 뺀 값이다.
	if want := goods/2 - fee; amount != want {
		t.Errorf("환불액 %d, want %d (한 개 몫 %d − 배송비 %d)", amount, want, goods/2, fee)
	}

	// A-512 를 바꿔도 이미 찍힌 스냅샷은 그대로다.
	if _, err := pool.Exec(ctx,
		`UPDATE returns SET shipping_fee_amount = 99999 WHERE return_no = $1`,
		ret.ReturnNo); err == nil {
		// 값을 바꿔도 이미 정산된 refunds.amount 는 달라지지 않는다.
		var stored int
		if err := pool.QueryRow(ctx,
			`SELECT amount FROM refunds WHERE return_id = (SELECT id FROM returns WHERE return_no = $1)`,
			ret.ReturnNo).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored != amount {
			t.Errorf("정산액이 정책 변경으로 %d → %d 로 바뀌었다", amount, stored)
		}
	}
}

// 판매자 귀책이면 배송비는 0 이다 — 하자 상품의 반품비를 구매자가 물지 않는다.
func TestSellerFaultMeansNoShippingFee(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, goods := deliveredOrder(t, s, pool, "tee", 2)
	items := itemsOf(t, s, orderNo)

	ret, err := s.OpenReturn(ctx, orderNo, ReturnRequest{
		Kind:  KindReturn,
		Lines: []RefundLine{{OrderItemID: items[0].ID, Quantity: 1}},
	}, "P-511", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// 화면이 배송비를 실어 보내도 판매자 귀책이면 0 이 된다.
	if err := s.ConfirmPickup(ctx, ret.ReturnNo, "판매자", "차감", 3000, "A-511"); err != nil {
		t.Fatal(err)
	}
	amount, err := s.SettleReturn(ctx, ret.ReturnNo, "A-511", "k1")
	if err != nil {
		t.Fatal(err)
	}
	if amount != goods/2 {
		t.Errorf("판매자 귀책 환불 %d, want %d (배송비 0)", amount, goods/2)
	}
}

// 같은 품목에 처리 중인 반품·교환이 둘 이상 생기지 않는다. 같은 물건을 두 번
// 환불받는 것을 DB 부분 유니크가 막는다.
func TestOneOpenReturnPerItem(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, _ := deliveredOrder(t, s, pool, "tee", 2)
	items := itemsOf(t, s, orderNo)
	line := []RefundLine{{OrderItemID: items[0].ID, Quantity: 1}}

	if _, err := s.OpenReturn(ctx, orderNo, ReturnRequest{Kind: KindReturn, Lines: line},
		"P-511", time.Now()); err != nil {
		t.Fatal(err)
	}
	// 주문 상태가 반품접수라 두 번째 접수는 상태머신이 먼저 막는다 — 그것도
	// 맞는 거부지만, 부분 유니크가 실제로 무는지는 상태를 되돌려 확인한다.
	if _, err := pool.Exec(ctx,
		`UPDATE orders SET status = '배송완료' WHERE order_no = $1`, orderNo); err != nil {
		t.Fatal(err)
	}
	_, err := s.OpenReturn(ctx, orderNo, ReturnRequest{Kind: KindReturn, Lines: line},
		"P-511", time.Now())
	if !errors.Is(err, ErrReturnInProgress) {
		t.Fatalf("두 번째 접수 = %v, want ErrReturnInProgress", err)
	}
}

// 거부하면 처리 중 표시가 내려가고 그 품목에 다시 걸 수 있다. 교환이면
// 예약한 재고도 반드시 푼다 — 풀지 않으면 재고가 조용히 잠긴다.
func TestRejectingReturnReleasesEverything(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, _ := deliveredOrder(t, s, pool, "tee", 2)
	items := itemsOf(t, s, orderNo)

	// 같은 상품의 두 번째 조합을 만든다.
	var productID string
	if err := pool.QueryRow(ctx,
		`SELECT product_id FROM order_items WHERE id = $1`, items[0].ID).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	var newVariant string
	if err := pool.QueryRow(ctx, `
		INSERT INTO product_variants (product_id,option_values,price_delta,stock)
		VALUES ($1,'{"크기":"M"}',1000,5) RETURNING id`, productID).Scan(&newVariant); err != nil {
		t.Fatal(err)
	}

	ret, err := s.OpenReturn(ctx, orderNo, ReturnRequest{
		Kind: KindExchange, NewVariantID: newVariant,
		Lines: []RefundLine{{OrderItemID: items[0].ID, Quantity: 2}},
	}, "P-512", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// 재고가 예약됐다 (D14 「교환 재고」).
	assertStock(t, pool, newVariant, 3)

	if err := s.RejectReturn(ctx, ret.ReturnNo, "재고 확인 불가", "A-511"); err != nil {
		t.Fatal(err)
	}
	// 예약이 풀렸다. 풀지 않으면 재고가 조용히 잠긴다.
	assertStock(t, pool, newVariant, 5)

	// 처리 중 표시가 내려가 다시 걸 수 있다.
	if _, err := s.OpenReturn(ctx, orderNo, ReturnRequest{
		Kind:  KindReturn,
		Lines: []RefundLine{{OrderItemID: items[0].ID, Quantity: 1}},
	}, "P-511", time.Now()); err != nil {
		t.Errorf("거부 뒤 재접수가 막혔다: %v", err)
	}
}

// 교환 대상은 **같은 상품의 다른 조합**이다 (FR-618). 다른 상품을 허용하면
// 교환이 곧 교환 가장한 재주문이 되고, 차액 계산의 근거가 사라진다.
func TestExchangeMustStayWithinTheSameProduct(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, _ := deliveredOrder(t, s, pool, "tee", 1)
	items := itemsOf(t, s, orderNo)

	_, otherVariant := seedProduct(t, pool, "other", 9000, 0, 5)
	_, err := s.OpenReturn(ctx, orderNo, ReturnRequest{
		Kind: KindExchange, NewVariantID: otherVariant,
		Lines: []RefundLine{{OrderItemID: items[0].ID, Quantity: 1}},
	}, "P-512", time.Now())
	if !errors.Is(err, ErrExchangeVariant) {
		t.Fatalf("다른 상품으로 교환 = %v, want ErrExchangeVariant", err)
	}
	// 재고도 예약되지 않았다.
	assertStock(t, pool, otherVariant, 5)
}

// 종류와 인자가 맞아야 한다. 반품에 새 조합을 실으면 **거부**한다 — 무시하면
// 폼을 잘못 쓴 것이 조용히 넘어가고, 그 폼은 다음에도 잘못 쓰인다.
func TestReturnKindAndArgumentsMustAgree(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, _ := deliveredOrder(t, s, pool, "tee", 1)
	items := itemsOf(t, s, orderNo)
	line := []RefundLine{{OrderItemID: items[0].ID, Quantity: 1}}

	for _, c := range []struct {
		why string
		req ReturnRequest
	}{
		{"반품에 새 조합", ReturnRequest{Kind: KindReturn, Lines: line, NewVariantID: "x"}},
		{"교환에 새 조합 없음", ReturnRequest{Kind: KindExchange, Lines: line}},
		{"모르는 종류", ReturnRequest{Kind: "취소", Lines: line}},
	} {
		if _, err := s.OpenReturn(ctx, orderNo, c.req, "P-511", time.Now()); !errors.Is(err, ErrReturnKind) {
			t.Errorf("%s = %v, want ErrReturnKind", c.why, err)
		}
	}
	if _, err := s.OpenReturn(ctx, orderNo,
		ReturnRequest{Kind: KindReturn}, "P-511", time.Now()); err == nil {
		t.Error("품목 없는 접수가 통과했다")
	}
}

// 구매확정 뒤에는 반품·교환을 받지 않는다 (FR-617, FR-618). 상태머신이 안다.
func TestNoReturnAfterConfirmation(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, _ := deliveredOrder(t, s, pool, "tee", 1)
	items := itemsOf(t, s, orderNo)

	if err := s.ConfirmPurchase(ctx, orderNo, "P-510"); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []ReturnKind{KindReturn, KindExchange} {
		req := ReturnRequest{Kind: kind, Lines: []RefundLine{{OrderItemID: items[0].ID, Quantity: 1}}}
		if kind == KindExchange {
			req.NewVariantID = "00000000-0000-0000-0000-000000000000"
		}
		_, err := s.OpenReturn(ctx, orderNo, req, "P-511", time.Now())
		if !errors.Is(err, ErrTransitionNotAllowed) && !errors.Is(err, ErrActorNotAllowed) {
			t.Errorf("구매확정 뒤 %s = %v", kind, err)
		}
	}
}

// 반품 환불과 부분 환불이 **같은 한도**를 나눠 쓴다. 각각 한도까지 쓰면
// 결제액을 넘는 사고가 난다 (D19 P-511).
func TestReturnAndPartialRefundShareTheSameCeiling(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, goods := deliveredOrder(t, s, pool, "tee", 2)
	items := itemsOf(t, s, orderNo)

	// 먼저 한 개를 부분 환불한다.
	if _, amount, err := s.RequestRefund(ctx, orderNo,
		[]RefundLine{{OrderItemID: items[0].ID, Quantity: 1}}, "구매자", "", "k1"); err != nil {
		t.Fatal(err)
	} else if amount != goods/2 {
		t.Fatalf("부분 환불 %d", amount)
	}

	// 남은 한 개를 반품한다. 배송비는 0 으로 둔다.
	ret, err := s.OpenReturn(ctx, orderNo, ReturnRequest{
		Kind:  KindReturn,
		Lines: []RefundLine{{OrderItemID: items[0].ID, Quantity: 1}},
	}, "P-511", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmPickup(ctx, ret.ReturnNo, "판매자", "차감", 0, "A-511"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SettleReturn(ctx, ret.ReturnNo, "A-511", "k2"); err != nil {
		t.Fatal(err)
	}

	approved, refunded, err := s.RefundedTotal(ctx, orderNo)
	if err != nil {
		t.Fatal(err)
	}
	// 둘을 합쳐도 상품 합계를 넘지 않는다.
	if refunded != goods {
		t.Errorf("누적 %d, want %d", refunded, goods)
	}
	if refunded > approved {
		t.Errorf("누적 %d > 승인 %d", refunded, approved)
	}
	// 더는 못 넣는다 — 수량이 다 소진됐다.
	if _, _, err := s.RequestRefund(ctx, orderNo,
		[]RefundLine{{OrderItemID: items[0].ID, Quantity: 1}}, "구매자", "", "k3"); !errors.Is(err, ErrRefundQuantity) {
		t.Errorf("전량 소진 뒤 = %v", err)
	}
}

// 반품 환불액도 **할인 스냅샷**에서 계산한다 (FR-617).
//
// `단가 × 수량` 으로 하면 할인이 붙은 품목에서 틀린다: 39,000원짜리 3개에
// 1,000원이 할인됐다면 할인후 38,000원이고 한 개 몫은 12,666원인데,
// 단가로 계산하면 13,000원이 나간다 — 전 수량을 반품하면 1,000원이 더 나간다.
func TestReturnRefundUsesTheDiscountSnapshot(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()

	_, variant := seedProduct(t, pool, "tee", 12000, 1000, 10)
	owner := CartOwner{GuestKey: "guest-ret-discount-1"}
	if err := s.AddToCart(ctx, owner, variant, 3); err != nil {
		t.Fatal(err)
	}
	const discount = 1000
	order, err := s.CreateOrder(ctx, owner, "", testForm(), Shipping{}, discount, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmPayment(ctx, okGateway(), "toss",
		order.OrderNo, "pk-rd", order.Total, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, to := range []Status{StatusPreparing, StatusShipping, StatusDelivered} {
		if err := s.TransitionOrder(ctx, order.OrderNo, to, "A-506"); err != nil {
			t.Fatal(err)
		}
	}

	items := itemsOf(t, s, order.OrderNo)
	net := items[0].Net()
	naive := items[0].LineAmount / items[0].Quantity // 단가 × 1
	want, err := RefundableAmount(items[0].LineAmount, items[0].Discount,
		items[0].Quantity, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if want == naive {
		t.Fatalf("표본이 두 계산을 구분하지 못한다 (둘 다 %d)", want)
	}

	ret, err := s.OpenReturn(ctx, order.OrderNo, ReturnRequest{
		Kind:  KindReturn,
		Lines: []RefundLine{{OrderItemID: items[0].ID, Quantity: 1}},
	}, "P-511", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmPickup(ctx, ret.ReturnNo, "판매자", "차감", 0, "A-511"); err != nil {
		t.Fatal(err)
	}
	amount, err := s.SettleReturn(ctx, ret.ReturnNo, "A-511", "k1")
	if err != nil {
		t.Fatal(err)
	}
	if amount != want {
		t.Errorf("반품 환불액 %d, want %d (단가 계산이면 %d)", amount, want, naive)
	}

	// 주문은 이제 `환불` 이고 그것은 최종 상태다 (D14) — 두 번째 반품은 열리지
	// 않는다. 남은 수량은 부분 환불(A-507)로 처리하고, **두 경로가 같은 한도와
	// 같은 스냅샷을 쓴다.**
	if _, err := s.OpenReturn(ctx, order.OrderNo, ReturnRequest{
		Kind:  KindReturn,
		Lines: []RefundLine{{OrderItemID: items[0].ID, Quantity: 1}},
	}, "P-511", time.Now()); !errors.Is(err, ErrTransitionNotAllowed) {
		t.Errorf("환불 상태에서 반품 재접수 = %v, want ErrTransitionNotAllowed", err)
	}

	total := amount
	for i := 0; i < 2; i++ {
		again := itemsOf(t, s, order.OrderNo)
		_, amt, err := s.RequestRefund(ctx, order.OrderNo,
			[]RefundLine{{OrderItemID: again[0].ID, Quantity: 1}}, "관리자", "",
			"k"+itoa(i+2))
		if err != nil {
			t.Fatalf("%d번째 부분 환불: %v", i+2, err)
		}
		total += amt
	}
	// 반품 한 번 + 부분 환불 두 번의 합계가 할인후 금액과 **정확히** 같다.
	if total != net {
		t.Errorf("반품+부분환불 합계 %d, 할인후 금액 %d — %d원 차이", total, net, total-net)
	}
	_, refunded, err := s.RefundedTotal(ctx, order.OrderNo)
	if err != nil {
		t.Fatal(err)
	}
	if refunded != net {
		t.Errorf("선점 누적 %d, want %d", refunded, net)
	}
}
