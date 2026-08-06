package commerce

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// setReturnFee writes A-512's 반품 배송비 정책. 수거 확인은 이 값을 읽는다 —
// 폼에서 받지 않기 때문이다 (D19 A-511 받지 않는 필드).
func setReturnFee(t *testing.T, pool *pgxpool.Pool, policy string, amount int) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO settings (key, value) VALUES ($1, $2), ($3, $4)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
		SettingReturnFeePolicy, policy, SettingReturnFeeAmount, strconv.Itoa(amount))
	if err != nil {
		t.Fatal(err)
	}
}

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
	if _, err := s.SettleReturn(ctx, orderNo, ret.ReturnNo, "A-511", "k1"); !errors.Is(err, ErrPickupRequired) {
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
	setReturnFee(t, pool, "차감", 3000)
	if err := s.ConfirmPickup(ctx, orderNo, ret.ReturnNo, "구매자", "A-511"); err != nil {
		t.Fatal(err)
	}
	amount, err := s.SettleReturn(ctx, orderNo, ret.ReturnNo, "A-511", "k2")
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
	setReturnFee(t, pool, "차감", fee)
	if err := s.ConfirmPickup(ctx, orderNo, ret.ReturnNo, "구매자", "A-511"); err != nil {
		t.Fatal(err)
	}

	amount, err := s.SettleReturn(ctx, orderNo, ret.ReturnNo, "A-511", "k1")
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
	// 정책이 배송비를 물려도 판매자 귀책이면 0 이 된다.
	setReturnFee(t, pool, "차감", 3000)
	if err := s.ConfirmPickup(ctx, orderNo, ret.ReturnNo, "판매자", "A-511"); err != nil {
		t.Fatal(err)
	}
	amount, err := s.SettleReturn(ctx, orderNo, ret.ReturnNo, "A-511", "k1")
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

	if err := s.RejectReturn(ctx, orderNo, ret.ReturnNo, "재고 확인 불가", "A-511"); err != nil {
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
	setReturnFee(t, pool, "차감", 0)
	if err := s.ConfirmPickup(ctx, orderNo, ret.ReturnNo, "판매자", "A-511"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SettleReturn(ctx, orderNo, ret.ReturnNo, "A-511", "k2"); err != nil {
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
	setReturnFee(t, pool, "차감", 0)
	if err := s.ConfirmPickup(ctx, order.OrderNo, ret.ReturnNo, "판매자", "A-511"); err != nil {
		t.Fatal(err)
	}
	amount, err := s.SettleReturn(ctx, order.OrderNo, ret.ReturnNo, "A-511", "k1")
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

// **배송비가 환불 몫 이상이면 수거 확인 자체가 거부된다.**
//
// 여기가 막을 수 있는 마지막 지점이다: 수거가 커밋되면 `반품수거 → 환불` 말고
// 나가는 길이 없어서 (D14 5절) 정산이 실패하는 순간 그 반품 건은 애플리케이션
// 안에서 영영 멈춘다 — 거부도 되돌리기도 안 되고 DB 를 직접 고쳐야 한다.
func TestOversizedShippingFeeIsRefusedAtPickup(t *testing.T) {
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
	share := goods / 2 // 한 개 몫

	// 몫보다 큰 배송비 — 거부된다.
	setReturnFee(t, pool, "차감", share+1)
	if err := s.ConfirmPickup(ctx, orderNo, ret.ReturnNo, "구매자", "A-511"); !errors.Is(err, ErrShippingFeeTooLarge) {
		t.Fatalf("몫 초과 배송비 = %v, want ErrShippingFeeTooLarge", err)
	}
	// 몫과 같아도 거부된다 — 0원 환불 행은 만들 수 없다.
	setReturnFee(t, pool, "차감", share)
	if err := s.ConfirmPickup(ctx, orderNo, ret.ReturnNo, "구매자", "A-511"); !errors.Is(err, ErrShippingFeeTooLarge) {
		t.Fatalf("몫과 같은 배송비 = %v, want ErrShippingFeeTooLarge", err)
	}

	// 상태가 그대로라 **아직 되돌릴 수 있다.** 이것이 이 검사의 목적이다.
	got, err := s.ReturnByNo(ctx, ret.ReturnNo)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusReturnOpen {
		t.Fatalf("거부된 수거 확인이 상태를 %s 로 옮겼다", got.Status)
	}
	if err := s.RejectReturn(ctx, orderNo, ret.ReturnNo, "배송비 재산정", "A-511"); err != nil {
		t.Errorf("막다른 길이다 — 거부도 안 된다: %v", err)
	}

	// 몫보다 1원 작으면 통과하고 정산까지 간다 — 경계가 상수로 굳어 있지
	// 않다는 것.
	ret2, err := s.OpenReturn(ctx, orderNo, ReturnRequest{
		Kind:  KindReturn,
		Lines: []RefundLine{{OrderItemID: items[0].ID, Quantity: 1}},
	}, "P-511", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	setReturnFee(t, pool, "차감", share-1)
	if err := s.ConfirmPickup(ctx, orderNo, ret2.ReturnNo, "구매자", "A-511"); err != nil {
		t.Fatalf("몫보다 작은 배송비가 막혔다: %v", err)
	}
	amount, err := s.SettleReturn(ctx, orderNo, ret2.ReturnNo, "A-511", "k1")
	if err != nil {
		t.Fatalf("정산이 막혔다: %v", err)
	}
	if amount != 1 {
		t.Errorf("환불액 %d, want 1 (몫 %d − 배송비 %d)", amount, share, share-1)
	}
}

// 교환은 배송비 상한의 대상이 아니다. 차액은 P-514 가 정산하고, 이 배송비는
// 환불에서 빼는 값이라 뺄 환불 자체가 없다.
func TestExchangePickupIsNotBlockedByTheFeeCheck(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, _ := deliveredOrder(t, s, pool, "tee", 2)
	items := itemsOf(t, s, orderNo)

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
		Lines: []RefundLine{{OrderItemID: items[0].ID, Quantity: 1}},
	}, "P-512", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// 큰 배송비라도 교환 수거는 통과한다.
	setReturnFee(t, pool, "별도청구", 999999)
	if err := s.ConfirmPickup(ctx, orderNo, ret.ReturnNo, "구매자", "A-511"); err != nil {
		t.Errorf("교환 수거 확인이 배송비 검사에 막혔다: %v", err)
	}
}

// **음수 배송비는 환불액을 부풀린다** (`amount -= feeAmount`).
//
// DB CHECK 도 막지만 거기서 나오는 것은 제약 위반이라 화면이 500 을 그린다.
// 무엇보다 이 값은 **재인증이 걸리지 않는** 수거 확인 단계에서 심어진다 —
// 돈이 움직이는 것은 정산뿐이라고 보고 재인증을 뺀 자리다.
func TestNegativeShippingFeeIsRefused(t *testing.T) {
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
	// 음수는 환불액을 부풀린다. 값이 들어오는 지점이 폼에서 설정으로 옮겨
	// 갔으므로 거부하는 오류도 설정값 오류다.
	setReturnFee(t, pool, "차감", -5000)
	if err := s.ConfirmPickup(ctx, orderNo, ret.ReturnNo, "구매자", "A-511"); !errors.Is(err, ErrFeeSetting) {
		t.Fatalf("음수 배송비 = %v, want ErrFeeSetting", err)
	}
	// 허용목록 밖의 부담 방식도 마찬가지다 — 500 이 아니라 설명이 있는 오류다.
	setReturnFee(t, pool, "반반", 1000)
	if err := s.ConfirmPickup(ctx, orderNo, ret.ReturnNo, "구매자", "A-511"); !errors.Is(err, ErrFeeSetting) {
		t.Fatalf("알 수 없는 부담 방식 = %v, want ErrFeeSetting", err)
	}
	setReturnFee(t, pool, "차감", 0)
	// 스냅샷도 상태도 그대로다.
	got, err := s.ReturnByNo(ctx, ret.ReturnNo)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusReturnOpen || got.FeeAmount != 0 {
		t.Fatalf("상태 %s · 배송비 %d — 거부된 값이 남았다", got.Status, got.FeeAmount)
	}

	// 정상 값으로는 통과하고, 환불액이 부풀지 않는다.
	setReturnFee(t, pool, "차감", 0)
	if err := s.ConfirmPickup(ctx, orderNo, ret.ReturnNo, "구매자", "A-511"); err != nil {
		t.Fatal(err)
	}
	amount, err := s.SettleReturn(ctx, orderNo, ret.ReturnNo, "A-511", "k1")
	if err != nil {
		t.Fatal(err)
	}
	if amount != goods/2 {
		t.Errorf("환불액 %d, want %d", amount, goods/2)
	}
}

// **다른 주문의 반품 건은 조작할 수 없다.**
//
// 폼의 return_no 를 그대로 조회 키로 쓰면, 다른 주문 화면에서 보낸 번호로
// 엉뚱한 건을 확정하고 원래 주문으로 리다이렉트된다 — 무슨 일이 있었는지
// 아무도 모른다. 소유 관계가 SQL 술어에 있다.
func TestReturnActionsAreScopedToTheirOrder(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	mineNo, _ := deliveredOrder(t, s, pool, "mine", 2)
	otherNo, _ := deliveredOrder(t, s, pool, "other", 2)

	otherItems := itemsOf(t, s, otherNo)
	victim, err := s.OpenReturn(ctx, otherNo, ReturnRequest{
		Kind:  KindReturn,
		Lines: []RefundLine{{OrderItemID: otherItems[0].ID, Quantity: 1}},
	}, "P-511", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	// 내 주문번호 + 남의 반품번호.
	setReturnFee(t, pool, "차감", 0)
	if err := s.ConfirmPickup(ctx, mineNo, victim.ReturnNo, "구매자", "A-511"); !errors.Is(err, ErrNotFound) {
		t.Errorf("남의 반품 수거 확인 = %v, want ErrNotFound", err)
	}
	if _, err := s.SettleReturn(ctx, mineNo, victim.ReturnNo, "A-511", "k1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("남의 반품 정산 = %v, want ErrNotFound", err)
	}
	if err := s.RejectReturn(ctx, mineNo, victim.ReturnNo, "x", "A-511"); !errors.Is(err, ErrNotFound) {
		t.Errorf("남의 반품 거부 = %v, want ErrNotFound", err)
	}

	// 피해 건은 그대로다.
	got, err := s.ReturnByNo(ctx, victim.ReturnNo)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusReturnOpen {
		t.Errorf("남의 반품 상태가 %s 로 바뀌었다", got.Status)
	}
	// 자기 주문으로는 된다 — 위 단언이 "무엇이든 막힌다" 를 본 것이 아니라는 것.
	setReturnFee(t, pool, "차감", 0)
	if err := s.ConfirmPickup(ctx, otherNo, victim.ReturnNo, "구매자", "A-511"); err != nil {
		t.Errorf("자기 주문의 반품이 막혔다: %v", err)
	}
}

// **교환 차액은 품목마다 자기 조합의 차액으로 센다.**
//
// 첫 품목의 값을 전부에 쓰면 같은 상품의 서로 다른 옵션을 함께 교환할 때
// 차액이 틀리고, 요청 순서를 바꾸는 것만으로 유리한 기준을 고를 수 있다.
func TestExchangePriceDiffUsesEachLinesOwnVariant(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()

	// 같은 상품의 두 조합: 차액 0 과 4000.
	var productID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO products (slug,name,base_price,is_visible)
		 VALUES ('tee','티셔츠',10000,true) RETURNING id`).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	mk := func(label string, delta int) string {
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO product_variants (product_id,option_values,price_delta,stock)
			VALUES ($1,$2,$3,10) RETURNING id`,
			productID, `{"크기":"`+label+`"}`, delta).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	small, large, target := mk("S", 0), mk("L", 4000), mk("XL", 6000)

	owner := CartOwner{GuestKey: "guest-exch-0123456"}
	if err := s.AddToCart(ctx, owner, small, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.AddToCart(ctx, owner, large, 1); err != nil {
		t.Fatal(err)
	}
	order, err := s.CreateOrder(ctx, owner, "", testForm(), Shipping{}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmPayment(ctx, okGateway(), "toss",
		order.OrderNo, "pk-exch", order.Total, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, to := range []Status{StatusPreparing, StatusShipping, StatusDelivered} {
		if err := s.TransitionOrder(ctx, order.OrderNo, to, "A-506"); err != nil {
			t.Fatal(err)
		}
	}

	items := itemsOf(t, s, order.OrderNo)
	if len(items) != 2 {
		t.Fatalf("품목 %d개", len(items))
	}
	// XL(6000) 로 교환: S 는 +6000, L 은 +2000 → 합계 8000.
	// 첫 품목 기준으로만 계산하면 순서에 따라 12000 또는 4000 이 나온다.
	const want = 8000

	for _, order2 := range [][]int{{0, 1}, {1, 0}} {
		// 요청 순서를 바꿔도 결과가 같아야 한다 — 순서로 기준을 고를 수
		// 없다는 것이 이 테스트의 요점이다.
		for _, stmt := range []string{
			`DELETE FROM return_items`,
			`DELETE FROM returns`,
			`UPDATE orders SET status = '배송완료' WHERE order_no = $1`,
		} {
			var args []any
			if strings.Contains(stmt, "$1") {
				args = []any{order.OrderNo}
			}
			if _, err := pool.Exec(ctx, stmt, args...); err != nil {
				t.Fatal(err)
			}
		}
		lines := []RefundLine{
			{OrderItemID: items[order2[0]].ID, Quantity: 1},
			{OrderItemID: items[order2[1]].ID, Quantity: 1},
		}
		ret, err := s.OpenReturn(ctx, order.OrderNo, ReturnRequest{
			Kind: KindExchange, NewVariantID: target, Lines: lines,
		}, "P-512", time.Now())
		if err != nil {
			t.Fatalf("순서 %v: %v", order2, err)
		}
		if ret.PriceDiff != want {
			t.Errorf("순서 %v: 차액 %d, want %d — 첫 품목 기준으로 셌다",
				order2, ret.PriceDiff, want)
		}
		// 예약한 재고를 되돌려 다음 라운드를 깨끗하게 시작한다.
		if err := s.RejectReturn(ctx, order.OrderNo, ret.ReturnNo, "정리", "A-511"); err != nil {
			t.Fatal(err)
		}
	}
}

// **`별도청구` 는 환불액에서 빼지 않는다** (D19 A-511 거부 조건: "`차감`이면
// 환불액에서 이미 빠졌으므로 미정산 상태가 없다. `별도청구`는 시스템 밖에서
// 받으므로 완료 처리를 막지 않는다").
//
// 빼면 구매자가 배송비를 두 번 낸다 — 환불에서 한 번, 별도 청구로 또 한 번.
func TestSeparateBillingFeeIsNotDeductedFromRefund(t *testing.T) {
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
	setReturnFee(t, pool, FeePolicySeparate, 3000)
	if err := s.ConfirmPickup(ctx, orderNo, ret.ReturnNo, "구매자", "A-511"); err != nil {
		t.Fatal(err)
	}
	amount, err := s.SettleReturn(ctx, orderNo, ret.ReturnNo, "A-511", "k1")
	if err != nil {
		t.Fatal(err)
	}
	if amount != goods/2 {
		t.Errorf("별도청구 환불액 %d, want %d — 배송비가 환불에서 빠졌다", amount, goods/2)
	}
	// 스냅샷은 남는다. 청구는 시스템 밖에서 하지만 얼마인지는 기록돼 있어야 한다.
	var fee int
	var policy string
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(shipping_fee_amount,0), COALESCE(shipping_fee_policy,'')
		FROM returns WHERE return_no = $1`, ret.ReturnNo).Scan(&fee, &policy); err != nil {
		t.Fatal(err)
	}
	if fee != 3000 || policy != FeePolicySeparate {
		t.Errorf("스냅샷 %d(%s), want 3000(별도청구)", fee, policy)
	}
}

// **수거와 정산 사이에 부분 환불이 끼어들면 그 반품은 영영 멈춘다.**
//
// 수거된 반품이 소진할 수량을 부분 환불이 먼저 써 버리면 정산이 수량 초과로
// 실패하는데, `반품수거` 에서 나가는 화살표는 `환불` 하나뿐이라 (D14 5절)
// 거부도 되돌리기도 못 한다 — 물건은 받았고 돈은 못 준다. 그래서 처리 중인
// 반품이 걸린 품목은 부분 환불이 건드리지 못한다.
func TestPartialRefundCannotStrandAPickedUpReturn(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, _ := deliveredOrder(t, s, pool, "tee", 2)
	items := itemsOf(t, s, orderNo)

	ret, err := s.OpenReturn(ctx, orderNo, ReturnRequest{
		Kind:  KindReturn,
		Lines: []RefundLine{{OrderItemID: items[0].ID, Quantity: 2}},
	}, "P-511", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	setReturnFee(t, pool, FeePolicyDeduct, 0)
	if err := s.ConfirmPickup(ctx, orderNo, ret.ReturnNo, "구매자", "A-511"); err != nil {
		t.Fatal(err)
	}

	// 수거 뒤 부분 환불을 시도한다. 막혀야 한다.
	_, _, err = s.RequestRefund(ctx, orderNo,
		[]RefundLine{{OrderItemID: items[0].ID, Quantity: 1}}, "관리자", "끼어들기", "k-mid")
	if !errors.Is(err, ErrReturnInProgress) {
		t.Fatalf("수거된 반품 품목의 부분 환불 = %v, want ErrReturnInProgress", err)
	}

	// 정산이 정상적으로 끝난다 — 이것이 막은 이유다.
	if _, err := s.SettleReturn(ctx, orderNo, ret.ReturnNo, "A-511", "k-settle"); err != nil {
		t.Fatalf("정산이 막혔다: %v — 반품이 멈췄다", err)
	}
}

// 거부된 반품은 처리 중이 아니므로 부분 환불이 다시 가능하다. 위 검사가
// "반품이 한 번이라도 있으면 영원히 막는다" 가 아니라는 것.
func TestRejectedReturnDoesNotBlockPartialRefund(t *testing.T) {
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
	if err := s.RejectReturn(ctx, orderNo, ret.ReturnNo, "사유 불충분", "A-511"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RequestRefund(ctx, orderNo,
		[]RefundLine{{OrderItemID: items[0].ID, Quantity: 1}}, "관리자", "", "k1"); err != nil {
		t.Errorf("거부된 반품이 부분 환불을 막았다: %v", err)
	}
}

// **`교환수거` 에서 나가는 길이 실제로 있어야 한다.**
//
// 상태머신에 화살표가 둘 있어도 그것을 일으키는 코드가 없으면 교환은 수거된
// 채 멈춘다 — 예약 재고가 조용히 잠기고, `return_items.is_open` 이 내려가지
// 않아 그 품목은 다시 반품·교환할 수도 없다.
func TestExchangeCanLeavePickedUp(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()

	// 차액이 0 인 교환(같은 값의 다른 조합)과 차액이 양수인 교환을 각각 본다.
	for _, tc := range []struct {
		name     string
		slug     string
		newDelta int
		want     Status
	}{
		{"차액 없음", "tee-same", 1000, StatusExchangeShipped},
		{"차액 있음", "tee-more", 5000, StatusExchangeDiffDue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orderNo, _ := deliveredOrder(t, s, pool, tc.slug, 1)
			items := itemsOf(t, s, orderNo)
			var productID string
			if err := pool.QueryRow(ctx,
				`SELECT product_id FROM order_items WHERE id = $1`, items[0].ID).Scan(&productID); err != nil {
				t.Fatal(err)
			}
			var newVariant string
			if err := pool.QueryRow(ctx, `
				INSERT INTO product_variants (product_id,option_values,price_delta,stock)
				VALUES ($1,'{"크기":"M"}',$2,5) RETURNING id`,
				productID, tc.newDelta).Scan(&newVariant); err != nil {
				t.Fatal(err)
			}

			ret, err := s.OpenReturn(ctx, orderNo, ReturnRequest{
				Kind: KindExchange, NewVariantID: newVariant,
				Lines: []RefundLine{{OrderItemID: items[0].ID, Quantity: 1}},
			}, "P-512", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			setReturnFee(t, pool, FeePolicyDeduct, 0)
			if err := s.ConfirmPickup(ctx, orderNo, ret.ReturnNo, "구매자", "A-511"); err != nil {
				t.Fatal(err)
			}

			got, err := s.CompleteExchange(ctx, orderNo, ret.ReturnNo, "A-511")
			if err != nil {
				t.Fatalf("교환 완료가 막혔다: %v — 교환수거에서 나갈 길이 없다", err)
			}
			if got != tc.want {
				t.Errorf("교환 완료 → %s, want %s", got, tc.want)
			}
			// 주문 상태도 함께 옮겨야 한다. 반품 행만 옮기면 화면은 끝났다고
			// 하는데 주문은 교환수거에 남는다.
			detail, err := s.OrderByNoUnscoped(ctx, orderNo)
			if err != nil {
				t.Fatal(err)
			}
			if detail.Status != tc.want {
				t.Errorf("주문 상태 %s, want %s", detail.Status, tc.want)
			}

			// 발송까지 간 건은 처리 중 표시가 내려간다. 차액 대기는 아직이다.
			var open bool
			if err := pool.QueryRow(ctx, `
				SELECT bool_or(is_open) FROM return_items
				WHERE return_id = (SELECT id FROM returns WHERE return_no = $1)`,
				ret.ReturnNo).Scan(&open); err != nil {
				t.Fatal(err)
			}
			if want := tc.want == StatusExchangeDiffDue; open != want {
				t.Errorf("is_open %v, want %v", open, want)
			}
		})
	}
}

// 반품 건을 교환 완료로 처리하지 않는다 — 종류를 섞으면 환불 없이 끝난다.
func TestCompleteExchangeRefusesAReturn(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, _ := deliveredOrder(t, s, pool, "tee", 1)
	items := itemsOf(t, s, orderNo)

	ret, err := s.OpenReturn(ctx, orderNo, ReturnRequest{
		Kind:  KindReturn,
		Lines: []RefundLine{{OrderItemID: items[0].ID, Quantity: 1}},
	}, "P-511", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	setReturnFee(t, pool, FeePolicyDeduct, 0)
	if err := s.ConfirmPickup(ctx, orderNo, ret.ReturnNo, "구매자", "A-511"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteExchange(ctx, orderNo, ret.ReturnNo, "A-511"); !errors.Is(err, ErrReturnKind) {
		t.Fatalf("반품의 교환 완료 = %v, want ErrReturnKind", err)
	}
}

// exchangeAwaitingDiff 는 차액이 양수인 교환을 `차액결제대기` 까지 몰고 간다.
func exchangeAwaitingDiff(t *testing.T, s *Store, pool *pgxpool.Pool, slug string) (string, string, int) {
	t.Helper()
	ctx := context.Background()
	orderNo, _ := deliveredOrder(t, s, pool, slug, 1)
	items := itemsOf(t, s, orderNo)
	var productID string
	if err := pool.QueryRow(ctx,
		`SELECT product_id FROM order_items WHERE id = $1`, items[0].ID).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	var newVariant string
	if err := pool.QueryRow(ctx, `
		INSERT INTO product_variants (product_id,option_values,price_delta,stock)
		VALUES ($1,'{"크기":"M"}',6000,5) RETURNING id`, productID).Scan(&newVariant); err != nil {
		t.Fatal(err)
	}
	ret, err := s.OpenReturn(ctx, orderNo, ReturnRequest{
		Kind: KindExchange, NewVariantID: newVariant,
		Lines: []RefundLine{{OrderItemID: items[0].ID, Quantity: 1}},
	}, "P-512", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	setReturnFee(t, pool, FeePolicyDeduct, 0)
	if err := s.ConfirmPickup(ctx, orderNo, ret.ReturnNo, "구매자", "A-511"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteExchange(ctx, orderNo, ret.ReturnNo, "A-511"); err != nil {
		t.Fatal(err)
	}
	diff, err := s.ExchangeDiffDue(ctx, orderNo, ret.ReturnNo, "")
	if err != nil {
		t.Fatalf("차액결제대기가 아니다: %v", err)
	}
	return orderNo, ret.ReturnNo, diff.Amount
}

// **차액은 `returns` 에 확정된 값이다** (FR-607). 요청이 다른 금액을 말하면
// 게이트웨이를 부르기 전에 거부한다 — 부른 뒤에 대조하면 돈은 이미 나갔다.
func TestExchangeDiffUsesTheStoredAmountOnly(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, returnNo, amount := exchangeAwaitingDiff(t, s, pool, "tee-diff")
	if amount <= 0 {
		t.Fatalf("차액 %d — 이 검사가 성립하려면 양수여야 한다", amount)
	}

	gw := okGateway()
	err := s.ConfirmExchangeDiff(ctx, gw, "toss", orderNo, returnNo, "", "pk-x", 1, time.Now())
	if !errors.Is(err, ErrAmountMismatch) {
		t.Fatalf("= %v, want ErrAmountMismatch", err)
	}
	if gw.count() != 0 {
		t.Errorf("승인 API 를 %d번 불렀다 — 대조는 호출보다 앞이어야 한다", gw.count())
	}

	// 확정 금액이면 통과하고 교환발송으로 간다.
	if err := s.ConfirmExchangeDiff(ctx, gw, "toss", orderNo, returnNo, "", "pk-ok", amount, time.Now()); err != nil {
		t.Fatalf("확정 금액인데 막혔다: %v", err)
	}
	detail, err := s.OrderByNoUnscoped(ctx, orderNo)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Status != StatusExchangeShipped {
		t.Errorf("결제 뒤 상태 %s, want %s", detail.Status, StatusExchangeShipped)
	}
	var stored int
	if err := pool.QueryRow(ctx, `
		SELECT approved_amount FROM payments WHERE kind = '교환차액'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != amount {
		t.Errorf("승인액 %d, want %d", stored, amount)
	}
}

// **두 번째 승인은 DB 가 막는다** (FR-608).
//
// 애플리케이션 검사를 지워도 두 번째가 실패해야 한다 — 그것을 확인하려고
// 부분 유니크에 직접 부딪혀 본다.
func TestSecondExchangeDiffPaymentIsBlockedByTheDatabase(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, returnNo, amount := exchangeAwaitingDiff(t, s, pool, "tee-dup")

	if err := s.ConfirmExchangeDiff(ctx, okGateway(), "toss", orderNo, returnNo, "",
		"pk-1", amount, time.Now()); err != nil {
		t.Fatal(err)
	}

	// 애플리케이션 경로를 우회해 같은 (order_id, return_id) 를 직접 넣는다.
	var orderID, returnID string
	if err := pool.QueryRow(ctx, `
		SELECT o.id, r.id FROM returns r JOIN orders o ON o.id = r.order_id
		WHERE r.return_no = $1`, returnNo).Scan(&orderID, &returnID); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO payments (order_id, return_id, kind, status, pg, payment_key, approved_amount)
		VALUES ($1, $2, '교환차액', '승인', 'toss', 'pk-2', $3)`, orderID, returnID, amount)
	if err == nil {
		t.Fatal("두 번째 교환차액 결제가 들어갔다 — 부분 유니크가 막지 못한다")
	}
	if !strings.Contains(err.Error(), "payments_exchange_idx") {
		t.Errorf("막은 것이 payments_exchange_idx 가 아니다: %v", err)
	}
}

// 상태가 `차액결제대기` 가 아니면 결제할 것이 없다 (409).
func TestExchangeDiffRefusesWhenNotDue(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, _ := deliveredOrder(t, s, pool, "tee-nodiff", 1)
	items := itemsOf(t, s, orderNo)

	ret, err := s.OpenReturn(ctx, orderNo, ReturnRequest{
		Kind:  KindReturn,
		Lines: []RefundLine{{OrderItemID: items[0].ID, Quantity: 1}},
	}, "P-511", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// 반품이라 교환 차액 자체가 성립하지 않는다 — 404 로 갈린다.
	if _, err := s.ExchangeDiffDue(ctx, orderNo, ret.ReturnNo, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("반품 건의 차액 조회 = %v, want ErrNotFound", err)
	}
}

// 남의 교환 건은 없는 건과 같은 404 다. 갈리면 그 차이가 존재를 알려준다.
func TestExchangeDiffIsScopedToTheOwner(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, returnNo, _ := exchangeAwaitingDiff(t, s, pool, "tee-own")

	if _, err := s.ExchangeDiffDue(ctx, orderNo, returnNo,
		"00000000-0000-4000-8000-000000000000"); !errors.Is(err, ErrNotFound) {
		t.Errorf("남의 사용자 ID = %v, want ErrNotFound", err)
	}
	if _, err := s.ExchangeDiffDue(ctx, orderNo, "RN-없는번호", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("없는 반품번호 = %v, want ErrNotFound", err)
	}
}

// 교환이지만 아직 `차액결제대기` 가 아니면 결제할 것이 없다 (409, not 404).
//
// 404 로 접으면 "그런 교환 건이 없다" 가 되어, 구매자는 자기 교환이 사라진
// 줄 안다. 존재는 하고 지금 결제할 수 없을 뿐이다.
func TestExchangeDiffRefusesBeforeCompletion(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, _ := deliveredOrder(t, s, pool, "tee-early", 1)
	items := itemsOf(t, s, orderNo)
	var productID string
	if err := pool.QueryRow(ctx,
		`SELECT product_id FROM order_items WHERE id = $1`, items[0].ID).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	var newVariant string
	if err := pool.QueryRow(ctx, `
		INSERT INTO product_variants (product_id,option_values,price_delta,stock)
		VALUES ($1,'{"크기":"M"}',6000,5) RETURNING id`, productID).Scan(&newVariant); err != nil {
		t.Fatal(err)
	}
	ret, err := s.OpenReturn(ctx, orderNo, ReturnRequest{
		Kind: KindExchange, NewVariantID: newVariant,
		Lines: []RefundLine{{OrderItemID: items[0].ID, Quantity: 1}},
	}, "P-512", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	// 교환접수 — 아직 수거도 안 했다.
	if _, err := s.ExchangeDiffDue(ctx, orderNo, ret.ReturnNo, ""); !errors.Is(err, ErrNoPriceDiff) {
		t.Errorf("교환접수 상태 = %v, want ErrNoPriceDiff", err)
	}

	// 교환수거 — 차액은 정해졌지만 아직 대기 상태가 아니다.
	setReturnFee(t, pool, FeePolicyDeduct, 0)
	if err := s.ConfirmPickup(ctx, orderNo, ret.ReturnNo, "구매자", "A-511"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExchangeDiffDue(ctx, orderNo, ret.ReturnNo, ""); !errors.Is(err, ErrNoPriceDiff) {
		t.Errorf("교환수거 상태 = %v, want ErrNoPriceDiff", err)
	}

	// 교환 완료를 거치면 열린다 — 위 단언이 "항상 막힌다" 를 본 것이 아니다.
	if _, err := s.CompleteExchange(ctx, orderNo, ret.ReturnNo, "A-511"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExchangeDiffDue(ctx, orderNo, ret.ReturnNo, ""); err != nil {
		t.Errorf("차액결제대기인데 막혔다: %v", err)
	}
}
