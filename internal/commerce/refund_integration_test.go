package commerce

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// itemsOf lists an order's item ids and quantities.
func itemsOf(t *testing.T, s *Store, orderNo string) []OrderItem {
	t.Helper()
	o, err := s.OrderByNoUnscoped(context.Background(), orderNo)
	if err != nil {
		t.Fatal(err)
	}
	return o.Items
}

// refundAll asks to refund every remaining unit of every item.
func refundAll(t *testing.T, s *Store, orderNo, key string) (string, int) {
	t.Helper()
	var lines []RefundLine
	for _, it := range itemsOf(t, s, orderNo) {
		if it.RemainingQty() > 0 {
			lines = append(lines, RefundLine{OrderItemID: it.ID, Quantity: it.RemainingQty()})
		}
	}
	id, amount, err := s.RequestRefund(context.Background(), orderNo, lines, "구매자", "", key)
	if err != nil {
		t.Fatalf("전량 환불 요청: %v", err)
	}
	return id, amount
}

// paidOrder makes an order and approves it, returning (orderNo, total, goods).
//
// goods 는 배송비를 뺀 상품 합계다. **품목 단위 환불은 배송비를 포함하지
// 않는다** (D50 「부분 취소 시 배송비」) — 배송이 나갔으면 그 비용은 발생했고,
// 전체 취소일 때만 배송비까지 돌려준다.
func paidOrder(t *testing.T, s *Store, pool *pgxpool.Pool, slug string, qty int) (string, int, int) {
	t.Helper()
	orderNo, total := seedOrder(t, s, pool, slug, qty)
	if _, err := s.ConfirmPayment(context.Background(), okGateway(), "toss",
		orderNo, "pk-"+slug, total, time.Now()); err != nil {
		t.Fatal(err)
	}
	goods := 0
	for _, it := range itemsOf(t, s, orderNo) {
		goods += it.Net()
	}
	return orderNo, total, goods
}

// FR-611: 누적 환불액이 결제액을 넘는 요청이 **DB CHECK 에서** 실패한다.
//
// 금액은 서버가 계산하므로 "결제액보다 큰 금액을 적어 보낸다" 는 시나리오가
// 성립하지 않는다. 대신 같은 품목을 남은 수량 이상 환불하려는 요청이 막힌다.
func TestRefundCannotExceedTheOrderedQuantity(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, _, goods := paidOrder(t, s, pool, "tee", 2)
	items := itemsOf(t, s, orderNo)
	if len(items) != 1 || items[0].Quantity != 2 {
		t.Fatalf("품목 %+v", items)
	}

	// 한 개만 환불한다 — 여러 개 산 것 중 하나만 취소하는 경우다.
	_, amount, err := s.RequestRefund(ctx, orderNo,
		[]RefundLine{{OrderItemID: items[0].ID, Quantity: 1}}, "구매자", "일부", "k1")
	if err != nil {
		t.Fatal(err)
	}
	if amount != goods/2 {
		t.Errorf("한 개 환불액 %d, want %d (배송비 제외)", amount, goods/2)
	}

	// 남은 것은 한 개다. 두 개를 더 달라고 하면 막힌다.
	if _, _, err := s.RequestRefund(ctx, orderNo,
		[]RefundLine{{OrderItemID: items[0].ID, Quantity: 2}}, "구매자", "", "k2"); !errors.Is(err, ErrRefundQuantity) {
		t.Fatalf("수량 초과 = %v, want ErrRefundQuantity", err)
	}
	// 남은 한 개는 된다.
	_, rest, err := s.RequestRefund(ctx, orderNo,
		[]RefundLine{{OrderItemID: items[0].ID, Quantity: 1}}, "구매자", "", "k3")
	if err != nil {
		t.Fatalf("남은 수량 환불이 막혔다: %v", err)
	}
	// 두 번의 합이 결제액과 정확히 같다 — 1원도 남거나 모자라지 않는다.
	// 두 번의 합이 **상품 합계**와 정확히 같다. 배송비는 부분 환불에 들어가지
	// 않는다 (D50) — 전체 취소일 때만 포함된다.
	if amount+rest != goods {
		t.Errorf("부분 환불 합계 %d, want %d", amount+rest, goods)
	}
	// 더는 없다.
	if _, _, err := s.RequestRefund(ctx, orderNo,
		[]RefundLine{{OrderItemID: items[0].ID, Quantity: 1}}, "구매자", "", "k4"); !errors.Is(err, ErrRefundQuantity) {
		t.Errorf("전량 소진 뒤 = %v", err)
	}

	approved, refunded, err := s.RefundedTotal(ctx, orderNo)
	if err != nil {
		t.Fatal(err)
	}
	if refunded != goods {
		t.Errorf("누적 %d, 상품 합계 %d (승인 %d — 차이는 배송비다)", refunded, goods, approved)
	}
}

// **할인이 붙은 주문의 부분 취소.** 사용자가 지적한 바로 그 경우다 — 여러 개
// 사고 하나만 취소하는데 할인이 걸려 있으면, 단가 × 수량은 틀린 답이다.
func TestPartialRefundWithADiscountSumsExactly(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()

	// 두 상품, 각각 다른 수량. 할인은 나누어떨어지지 않는 값으로 고른다.
	_, v1 := seedProduct(t, pool, "tee", 12000, 1000, 10)
	_, v2 := seedProduct(t, pool, "cap", 7000, 0, 10)
	owner := CartOwner{GuestKey: "guest-discount-01234"}
	if err := s.AddToCart(ctx, owner, v1, 3); err != nil {
		t.Fatal(err)
	}
	if err := s.AddToCart(ctx, owner, v2, 2); err != nil {
		t.Fatal(err)
	}
	const discount = 3331 // 소수가 되도록 일부러 어긋난 값
	order, err := s.CreateOrder(ctx, owner, "", testForm(), Shipping{}, discount, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmPayment(ctx, okGateway(), "toss",
		order.OrderNo, "pk-d", order.Total, time.Now()); err != nil {
		t.Fatal(err)
	}

	// 배분 합계가 할인액과 정확히 같다.
	var sumDiscount int
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(sum(discount_amount), 0) FROM order_items
		WHERE order_id = $1`, order.ID).Scan(&sumDiscount); err != nil {
		t.Fatal(err)
	}
	if sumDiscount != discount {
		t.Fatalf("배분 합계 %d, want %d", sumDiscount, discount)
	}

	// 한 개씩 전부 환불한다. 순서는 섞는다 — 어떤 순서로 나눠도 합계가 같아야
	// 한다는 것이 이 설계의 요점이다.
	total := 0
	round := 0
	for {
		items := itemsOf(t, s, order.OrderNo)
		done := true
		for _, it := range items {
			if it.RemainingQty() == 0 {
				continue
			}
			done = false
			_, amt, err := s.RequestRefund(ctx, order.OrderNo,
				[]RefundLine{{OrderItemID: it.ID, Quantity: 1}}, "구매자", "",
				"k-"+itoa(round))
			if err != nil {
				t.Fatalf("라운드 %d: %v", round, err)
			}
			if amt <= 0 {
				t.Fatalf("라운드 %d: 환불액 %d", round, amt)
			}
			total += amt
			round++
		}
		if done {
			break
		}
	}

	// 한 개씩 나눠 환불한 합계가 결제 금액과 **정확히** 같다.
	if total != order.Total {
		t.Errorf("부분 환불 합계 %d, 결제 금액 %d — %d원 차이", total, order.Total, total-order.Total)
	}
	approved, refunded, err := s.RefundedTotal(ctx, order.OrderNo)
	if err != nil {
		t.Fatal(err)
	}
	if refunded != approved {
		t.Errorf("누적 %d, 승인 %d", refunded, approved)
	}
}

// 새로고침 한 번이 이중 환불이 되지 않는다.
func TestRefundRequestKeyIsIdempotentAtTheStore(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, _, _ := paidOrder(t, s, pool, "tee", 2)

	items := itemsOf(t, s, orderNo)
	line := []RefundLine{{OrderItemID: items[0].ID, Quantity: 1}}
	_, first, err := s.RequestRefund(ctx, orderNo, line, "구매자", "", "same-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RequestRefund(ctx, orderNo, line, "구매자", "", "same-key"); !errors.Is(err, ErrRefundDuplicate) {
		t.Fatalf("= %v, want ErrRefundDuplicate", err)
	}
	// 선점도 한 번만 올랐다 — 중복 요청이 금액을 두 번 잡으면 남은 한도가
	// 조용히 줄어든다.
	_, refunded, err := s.RefundedTotal(ctx, orderNo)
	if err != nil {
		t.Fatal(err)
	}
	if refunded != first {
		t.Errorf("선점 %d, want %d", refunded, first)
	}
	// 소진 수량도 한 번만 올랐다. 두 번 오르면 그 품목이 영영 환불 불가가 된다.
	if again := itemsOf(t, s, orderNo); again[0].Settled != 1 {
		t.Errorf("소진 수량 %d, want 1", again[0].Settled)
	}
}

// **동시 부분환불 2건이 같은 수량을 노릴 때 하나만 성공한다.**
//
// 애플리케이션이 먼저 남은 수량을 읽어 보는 방식은 둘이 같은 값을 읽고 각자
// 통과한다. 품목 행을 잠그고, 넘으면 DB CHECK 가 잡는다.
func TestConcurrentRefundsCannotOvershoot(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()

	const rounds = 20
	for r := 0; r < rounds; r++ {
		orderNo, _, goods := paidOrder(t, s, pool, "race-refund-"+itoa(r), 2)
		items := itemsOf(t, s, orderNo)
		// 각자 2개(전량)를 요청한다. 합치면 4개라 하나는 반드시 진다.
		line := []RefundLine{{OrderItemID: items[0].ID, Quantity: 2}}

		var wg sync.WaitGroup
		errs := make([]error, 2)
		gate := make(chan struct{})
		for i := range errs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-gate
				_, _, errs[i] = s.RequestRefund(ctx, orderNo, line, "구매자", "",
					"k-"+itoa(r)+"-"+itoa(i))
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
			if !errors.Is(err, ErrRefundQuantity) {
				t.Fatalf("라운드 %d: 진 쪽이 %v, want ErrRefundQuantity", r, err)
			}
		}
		if ok != 1 {
			t.Fatalf("라운드 %d: %d건 성공, want 1 (%v)", r, ok, errs)
		}
		approved, refunded, err := s.RefundedTotal(ctx, orderNo)
		if err != nil {
			t.Fatal(err)
		}
		if refunded > approved {
			t.Fatalf("라운드 %d: 누적 %d > 승인 %d", r, refunded, approved)
		}
		if refunded != goods {
			t.Fatalf("라운드 %d: 누적 %d, want %d (배송비 제외)", r, refunded, goods)
		}
		// 소진 수량도 정확히 주문 수량이다 — 두 건이 각각 올렸으면 4가 된다.
		if again := itemsOf(t, s, orderNo); again[0].Settled != 2 {
			t.Fatalf("라운드 %d: 소진 %d, want 2", r, again[0].Settled)
		}
	}
}

// 거부는 선점을 같은 트랜잭션에서 되돌린다. 나누면 거부는 됐는데 선점이 남아
// 그 주문이 남은 금액만큼 환불받지 못한다 — 아무 오류도 나지 않는다.
func TestRejectingARefundReleasesTheReservation(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, _, goods := paidOrder(t, s, pool, "tee", 2)

	id, amount := refundAll(t, s, orderNo, "k1")
	if amount != goods {
		t.Fatalf("전량 환불액 %d, want %d (배송비 제외)", amount, goods)
	}
	// 전량이 소진됐으므로 더는 못 넣는다.
	items := itemsOf(t, s, orderNo)
	if _, _, err := s.RequestRefund(ctx, orderNo,
		[]RefundLine{{OrderItemID: items[0].ID, Quantity: 1}}, "구매자", "", "k2"); !errors.Is(err, ErrRefundQuantity) {
		t.Fatalf("= %v", err)
	}

	if err := s.RejectRefund(ctx, id, "재고 확인 불가"); err != nil {
		t.Fatal(err)
	}
	_, refunded, err := s.RefundedTotal(ctx, orderNo)
	if err != nil {
		t.Fatal(err)
	}
	if refunded != 0 {
		t.Errorf("거부 뒤 선점 %d, want 0", refunded)
	}
	// 소진 수량도 되돌아왔다 — 금액만 되돌리면 그 품목은 영영 환불 불가다.
	if again := itemsOf(t, s, orderNo); again[0].Settled != 0 {
		t.Errorf("거부 뒤 소진 수량 %d, want 0", again[0].Settled)
	}
	// 다시 요청할 수 있다.
	if _, again := refundAll(t, s, orderNo, "k3"); again != goods {
		t.Errorf("거부 뒤 재요청 금액 %d, want %d", again, goods)
	}
	// 같은 건을 두 번 거부하지 못한다 — 두 번 되돌리면 선점이 음수가 된다.
	if err := s.RejectRefund(ctx, id, "또"); !errors.Is(err, ErrNotFound) {
		t.Errorf("이중 거부 = %v, want ErrNotFound", err)
	}
}

// 취소는 재고를 되돌리고 전액 환불을 접수한다.
func TestCancelRestoresStockAndRefundsInFull(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, total, _ := paidOrder(t, s, pool, "tee", 2)

	var variant string
	if err := pool.QueryRow(ctx,
		`SELECT variant_id FROM order_items WHERE order_id =
		 (SELECT id FROM orders WHERE order_no = $1)`, orderNo).Scan(&variant); err != nil {
		t.Fatal(err)
	}
	assertStock(t, pool, variant, 8) // 10 - 2

	// 결제완료 → 취소는 표에 있다.
	if err := s.CancelOrder(ctx, orderNo, "P-506", "cancel-1"); err != nil {
		t.Fatal(err)
	}
	assertStock(t, pool, variant, 10)

	_, refunded, err := s.RefundedTotal(ctx, orderNo)
	if err != nil {
		t.Fatal(err)
	}
	if refunded != total {
		t.Errorf("취소 환불 %d, want %d", refunded, total)
	}
	var status string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM orders WHERE order_no = $1`, orderNo).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(StatusCancelled) {
		t.Errorf("상태 %q", status)
	}
}

// 배송 후에는 구매자가 취소할 수 없다. 물건이 이미 갔으므로 A-507 이 승인한다.
func TestBuyerCannotCancelAfterDispatch(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, _, _ := paidOrder(t, s, pool, "tee", 2)

	for _, to := range []Status{StatusPreparing, StatusShipping} {
		if err := s.TransitionOrder(ctx, orderNo, to, "A-506"); err != nil {
			t.Fatal(err)
		}
	}
	err := s.CancelOrder(ctx, orderNo, "P-506", "c1")
	if !errors.Is(err, ErrTransitionNotAllowed) && !errors.Is(err, ErrActorNotAllowed) {
		t.Fatalf("배송 후 취소 = %v", err)
	}
	// 재고도 그대로다 — 실패한 취소가 재고를 되돌리면 없는 물건이 생긴다.
	var variant string
	if err := pool.QueryRow(ctx,
		`SELECT variant_id FROM order_items WHERE order_id =
		 (SELECT id FROM orders WHERE order_no = $1)`, orderNo).Scan(&variant); err != nil {
		t.Fatal(err)
	}
	assertStock(t, pool, variant, 8)
}

// 수량과 요청 키의 형태를 저장소가 먼저 거른다.
func TestRefundRequestRejectsBadInput(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, _, _ := paidOrder(t, s, pool, "tee", 2)
	items := itemsOf(t, s, orderNo)
	good := []RefundLine{{OrderItemID: items[0].ID, Quantity: 1}}

	if _, _, err := s.RequestRefund(ctx, orderNo, nil, "구매자", "", "k1"); err == nil {
		t.Error("품목 없는 환불이 통과했다")
	}
	for _, q := range []int{0, -1} {
		if _, _, err := s.RequestRefund(ctx, orderNo,
			[]RefundLine{{OrderItemID: items[0].ID, Quantity: q}}, "구매자", "", "k2"); !errors.Is(err, ErrQuantityRange) {
			t.Errorf("수량 %d = %v, want ErrQuantityRange", q, err)
		}
	}
	if _, _, err := s.RequestRefund(ctx, orderNo, good, "구매자", "", ""); err == nil {
		t.Error("요청 키 없는 환불이 통과했다")
	}
	// **다른 주문의 품목 ID** 는 열리지 않는다. order_id 술어가 막는다.
	otherNo, _, _ := paidOrder(t, s, pool, "other", 1)
	otherItems := itemsOf(t, s, otherNo)
	if _, _, err := s.RequestRefund(ctx, orderNo,
		[]RefundLine{{OrderItemID: otherItems[0].ID, Quantity: 1}}, "구매자", "", "k3"); !errors.Is(err, ErrNotFound) {
		t.Errorf("남의 품목 = %v, want ErrNotFound", err)
	}

	// 아무것도 선점·소진되지 않았다.
	_, refunded, err := s.RefundedTotal(ctx, orderNo)
	if err != nil {
		t.Fatal(err)
	}
	if refunded != 0 {
		t.Errorf("거부된 요청이 %d 을 선점했다", refunded)
	}
	if again := itemsOf(t, s, orderNo); again[0].Settled != 0 {
		t.Errorf("거부된 요청이 수량 %d 를 소진했다", again[0].Settled)
	}
}

// requester 는 "누가 요청했나" 이고 정산에서 읽힌다. 구매자가 일으킨 취소가
// 관리자 요청으로 기록되면 그 구분이 사라진다.
func TestCancelRecordsWhoAskedForIt(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()

	buyer, _, _ := paidOrder(t, s, pool, "buyer", 1)
	if err := s.CancelOrder(ctx, buyer, "P-506", "c1"); err != nil {
		t.Fatal(err)
	}
	admin, _, _ := paidOrder(t, s, pool, "admin", 1)
	if err := s.CancelOrder(ctx, admin, "A-507", "c2"); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ orderNo, want string }{
		{buyer, "구매자"},
		{admin, "관리자"},
	} {
		var got string
		if err := pool.QueryRow(ctx, `
			SELECT requester FROM refunds
			WHERE order_id = (SELECT id FROM orders WHERE order_no = $1)`, c.orderNo).
			Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("%s 요청자 %q, want %q", c.orderNo, got, c.want)
		}
	}
}

// **동시 부분환불에서 1원이 사라지지 않는다.**
//
// 두 요청이 각각 수량 1개를 환불하고 품목 금액이 홀수라면, 잠그지 않았을 때
// 둘 다 `floor(net/2)` 를 받는다 — 합이 net 보다 1원 적고, 그 1원은 아무
// 오류도 내지 않고 사라진다. DB CHECK 는 이것을 못 잡는다: 소진 수량 합이
// 주문 수량과 같아서 제약이 만족된다.
//
// 품목 행을 잠그면 두 번째가 소진 1을 읽고 `ceil(net/2)` 를 받아 합이 정확히
// 맞는다. 변이 검증이 이 경우를 드러냈다.
func TestConcurrentPartialRefundsDoNotLoseAWon(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()

	const rounds = 25
	for r := 0; r < rounds; r++ {
		// 단가가 홀수가 되도록 만든다: 12001 + 0 = 12001, 2개면 24002 (짝수)
		// 이므로 할인 1원을 넣어 할인후 금액을 홀수로 만든다.
		_, variant := seedProduct(t, pool, "odd-"+itoa(r), 12001, 0, 10)
		owner := CartOwner{GuestKey: "guest-odd-" + itoa(r) + "-01234"}
		if err := s.AddToCart(ctx, owner, variant, 2); err != nil {
			t.Fatal(err)
		}
		order, err := s.CreateOrder(ctx, owner, "", testForm(), Shipping{}, 1, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ConfirmPayment(ctx, okGateway(), "toss",
			order.OrderNo, "pk-odd-"+itoa(r), order.Total, time.Now()); err != nil {
			t.Fatal(err)
		}
		items := itemsOf(t, s, order.OrderNo)
		net := items[0].Net()
		if net%2 == 0 {
			t.Fatalf("라운드 %d: 할인후 금액 %d 가 짝수라 이 테스트가 아무것도 안 본다", r, net)
		}

		var wg sync.WaitGroup
		amounts := make([]int, 2)
		errs := make([]error, 2)
		gate := make(chan struct{})
		for i := range errs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-gate
				_, amounts[i], errs[i] = s.RequestRefund(ctx, order.OrderNo,
					[]RefundLine{{OrderItemID: items[0].ID, Quantity: 1}},
					"구매자", "", "k-odd-"+itoa(r)+"-"+itoa(i))
			}(i)
		}
		close(gate)
		wg.Wait()

		sum := 0
		for i, err := range errs {
			if err != nil {
				t.Fatalf("라운드 %d: %d번째 요청이 실패 %v — 둘 다 성공해야 한다", r, i, err)
			}
			sum += amounts[i]
		}
		// **한 개씩 두 번 환불한 합계가 할인후 금액과 정확히 같다.**
		if sum != net {
			t.Fatalf("라운드 %d: 합계 %d, 할인후 금액 %d — %d원이 사라졌다",
				r, sum, net, net-sum)
		}
		_, refunded, err := s.RefundedTotal(ctx, order.OrderNo)
		if err != nil {
			t.Fatal(err)
		}
		if refunded != net {
			t.Fatalf("라운드 %d: 선점 누적 %d, want %d", r, refunded, net)
		}
	}
}

// 취소된 주문에는 부분 환불을 더 넣을 수 없다. 금액 한도만으로는 막히지
// 않는다 — 취소가 전액을 선점하지만 수량은 남아 있기 때문이다.
func TestCancelledOrderRefusesFurtherRefunds(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, _, _ := paidOrder(t, s, pool, "tee", 2)
	items := itemsOf(t, s, orderNo)

	if err := s.CancelOrder(ctx, orderNo, "P-506", "c1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RequestRefund(ctx, orderNo,
		[]RefundLine{{OrderItemID: items[0].ID, Quantity: 1}}, "구매자", "", "k1"); !errors.Is(err, ErrRefundQuantity) {
		t.Errorf("취소 뒤 부분 환불 = %v, want ErrRefundQuantity", err)
	}
	// 소진 수량이 주문 수량과 같다.
	if again := itemsOf(t, s, orderNo); again[0].RemainingQty() != 0 {
		t.Errorf("취소 뒤 남은 수량 %d, want 0", again[0].RemainingQty())
	}
}

// **부분 환불 뒤 전체 취소는 남은 몫만 돌려준다** (FR-611).
//
// 예전에는 `refunded_amount` 에 승인금액을 대입해서, 앞선 부분 환불의 선점이
// 지워졌다. 지워진 값은 `CHECK (환불누적액 <= 승인금액)` 도 볼 수 없어서 두
// 환불의 합이 결제액을 넘었다.
func TestCancelAfterPartialRefundOnlyReturnsWhatIsLeft(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, _, goods := paidOrder(t, s, pool, "tee", 2)
	items := itemsOf(t, s, orderNo)

	var approved int
	if err := pool.QueryRow(ctx, `
		SELECT approved_amount FROM payments p JOIN orders o ON o.id = p.order_id
		WHERE o.order_no = $1 AND p.kind = '주문결제'`, orderNo).Scan(&approved); err != nil {
		t.Fatal(err)
	}

	// 품목 전부를 부분 환불한다 (배송비는 남는다 — D50 「부분 취소 시 배송비」).
	if _, _, err := s.RequestRefund(ctx, orderNo, []RefundLine{
		{OrderItemID: items[0].ID, Quantity: 2},
	}, "관리자", "품목 환불", "k-part"); err != nil {
		t.Fatal(err)
	}

	// 배송준비까지 간 뒤 구매자가 전체 취소한다 (P-506).
	if err := s.TransitionOrder(ctx, orderNo, StatusPreparing, "A-506"); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelOrder(ctx, orderNo, "P-506", "k-cancel"); err != nil {
		t.Fatal(err)
	}

	var reserved, sum int
	if err := pool.QueryRow(ctx, `
		SELECT p.refunded_amount, COALESCE((SELECT sum(amount) FROM refunds WHERE payment_id = p.id), 0)
		FROM payments p JOIN orders o ON o.id = p.order_id
		WHERE o.order_no = $1 AND p.kind = '주문결제'`, orderNo).Scan(&reserved, &sum); err != nil {
		t.Fatal(err)
	}
	if sum != approved {
		t.Errorf("환불 합계 %d, want %d (승인금액) — 같은 돈을 두 번 돌려줬다", sum, approved)
	}
	if reserved != approved {
		t.Errorf("선점액 %d, want %d", reserved, approved)
	}
	if goods >= approved {
		t.Fatalf("전제가 틀렸다: 상품합 %d, 승인 %d — 배송비가 없으면 이 회귀를 못 본다", goods, approved)
	}
}

// twoItemPaidOrder 는 서로 다른 품목 둘이 든 결제완료 주문이다. 잠금 순서를
// 보려면 잠글 행이 둘 이상이어야 한다.
func twoItemPaidOrder(t *testing.T, s *Store, pool *pgxpool.Pool, tag string) string {
	t.Helper()
	ctx := context.Background()
	_, v1 := seedProduct(t, pool, tag+"a", 12000, 1000, 10)
	_, v2 := seedProduct(t, pool, tag+"b", 9000, 0, 10)
	owner := CartOwner{GuestKey: "guest-" + tag + "-0123456789"}
	for _, v := range []string{v1, v2} {
		if err := s.AddToCart(ctx, owner, v, 2); err != nil {
			t.Fatal(err)
		}
	}
	order, err := s.CreateOrder(ctx, owner, "", testForm(), testShipping, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmPayment(ctx, okGateway(), "toss", order.OrderNo,
		"pk-"+tag, order.Total, time.Now()); err != nil {
		t.Fatal(err)
	}
	return order.OrderNo
}

// **잠금 순서를 요청자가 정하게 두지 않는다.**
//
// 품목을 폼이 준 순서대로 잠그면 `item_id=A&item_id=B` 와 그 역순 두 요청이
// 서로의 행을 기다린다. 롤백되므로 돈은 안전하지만 운영자에게 가는 것은 원인
// 없는 500 이고, 그 순서를 정하는 것은 보내는 쪽이다.
func TestConcurrentRefundsDoNotDeadlockOnLineOrder(t *testing.T) {
	s, pool := testStore(t)
	const rounds = 12
	for i := range rounds {
		orderNo := twoItemPaidOrder(t, s, pool, fmt.Sprintf("dl%d", i))
		items := itemsOf(t, s, orderNo)
		if len(items) != 2 {
			t.Fatalf("품목 %d개 — 두 개여야 순서가 의미를 갖는다", len(items))
		}
		forward := []RefundLine{{OrderItemID: items[0].ID, Quantity: 1},
			{OrderItemID: items[1].ID, Quantity: 1}}
		reverse := []RefundLine{{OrderItemID: items[1].ID, Quantity: 1},
			{OrderItemID: items[0].ID, Quantity: 1}}

		var wg sync.WaitGroup
		errs := make([]error, 2)
		for j, lines := range [][]RefundLine{forward, reverse} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _, errs[j] = s.RequestRefund(context.Background(), orderNo, lines,
					"관리자", "", fmt.Sprintf("k%d-%d", i, j))
			}()
		}
		wg.Wait()

		for j, err := range errs {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
				t.Fatalf("%d회차 %d번 요청이 교착했다: %v", i, j, err)
			}
			if err != nil {
				t.Fatalf("%d회차 %d번 요청: %v", i, j, err)
			}
		}
	}
}
