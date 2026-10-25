package commerce

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func refundStatus(t *testing.T, s *Store, refundID string) string {
	t.Helper()
	var st string
	if err := s.pool.QueryRow(context.Background(),
		`SELECT status FROM refunds WHERE id = $1`, refundID).Scan(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

// A-507 의 순서는 DB 한도 선점 → PG 호출 → 확정이다 (D13). 이것이 없던 판에서는
// 어떤 경로도 PG 를 부르지 않았다 — 환불은 장부에만 있었고 돈은 나가지 않았다.
func TestExecuteRefundCallsThePGOnceAndCompletes(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, _, _ := paidOrder(t, s, pool, "tee", 2)
	items := itemsOf(t, s, orderNo)
	id, amount, err := s.RequestRefund(ctx, orderNo,
		[]RefundLine{{OrderItemID: items[0].ID, Quantity: 1}}, "관리자", "불량", "rk-1")
	if err != nil {
		t.Fatal(err)
	}
	if refundStatus(t, s, id) != "요청" {
		t.Fatalf("접수 직후 상태 %s", refundStatus(t, s, id))
	}

	gw := okGateway()
	gw.cancelResponse = &Payment{PaymentKey: "pk-tee", Status: PaymentApproved,
		Raw: []byte(`{"status":"PARTIAL_CANCELED","card":{"number":"1234********5678"}}`)}
	if _, err := s.ExecuteRefund(ctx, gw, id, "A-507"); err != nil {
		t.Fatalf("환불 실행 = %v", err)
	}
	if got := refundStatus(t, s, id); got != "완료" {
		t.Errorf("실행 뒤 상태 %s, want 완료", got)
	}
	if len(gw.cancelReqs) != 1 {
		t.Fatalf("취소 API %d번, want 1", len(gw.cancelReqs))
	}
	req := gw.cancelReqs[0]
	if req.Amount != amount || req.PaymentKey != "pk-tee" || req.IdempotencyKey != "rk-1" {
		t.Errorf("취소 요청 %+v — 금액·키·멱등키가 접수와 다르다 (amount=%d)", req, amount)
	}
	// 재전송은 같은 결과이고 PG 를 다시 부르지 않는다.
	if _, err := s.ExecuteRefund(ctx, gw, id, "A-507"); err != nil {
		t.Errorf("완료된 건 재실행 = %v, want nil", err)
	}
	if len(gw.cancelReqs) != 1 {
		t.Errorf("완료된 건에 취소 API 를 다시 불렀다 (%d번) — 이중 환불", len(gw.cancelReqs))
	}
	var raw string
	if err := pool.QueryRow(ctx, `SELECT pg_response::text FROM refunds WHERE id = $1`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw == "" || !containsAll(raw, "PARTIAL_CANCELED") {
		t.Errorf("응답 원문이 보관되지 않았다: %q", raw)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// **미환불이 이중환불보다 낫다.** 결과 불명은 '승인' 에 두고 다시 부르지 않는다;
// 확정 실패는 '요청' 으로 되돌려 다시 시도할 수 있다.
func TestExecuteRefundKeepsUnknownResultsAndRetriesDeclines(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, _, _ := paidOrder(t, s, pool, "tee", 2)
	items := itemsOf(t, s, orderNo)
	id, _, err := s.RequestRefund(ctx, orderNo,
		[]RefundLine{{OrderItemID: items[0].ID, Quantity: 1}}, "관리자", "불량", "rk-u")
	if err != nil {
		t.Fatal(err)
	}

	unknown := okGateway()
	unknown.cancelErr = ErrPaymentUnknown
	if _, err := s.ExecuteRefund(ctx, unknown, id, "A-507"); !errors.Is(err, ErrPaymentUnknown) {
		t.Fatalf("결과 불명 = %v", err)
	}
	if got := refundStatus(t, s, id); got != "승인" {
		t.Errorf("결과 불명 뒤 상태 %s, want 승인 (다시 부르면 이중 환불)", got)
	}
	if _, err := s.ExecuteRefund(ctx, unknown, id, "A-507"); !errors.Is(err, ErrRefundState) {
		t.Errorf("'승인' 건을 다시 실행 = %v, want ErrRefundState", err)
	}
	if len(unknown.cancelReqs) != 1 {
		t.Errorf("결과 불명 뒤 취소 API 를 %d번 불렀다, want 1", len(unknown.cancelReqs))
	}

	// 다른 건: 확정 실패 → 요청으로 복귀 → 재시도 성공.
	id2, _, err := s.RequestRefund(ctx, orderNo,
		[]RefundLine{{OrderItemID: items[0].ID, Quantity: 1}}, "관리자", "불량", "rk-d")
	if err != nil {
		t.Fatal(err)
	}
	declined := okGateway()
	declined.cancelErr = errors.New("commerce: 결제 요청이 거부되었습니다 (HTTP 400, INVALID)")
	if _, err := s.ExecuteRefund(ctx, declined, id2, "A-507"); err == nil {
		t.Fatal("확정 실패가 성공으로 돌아왔다")
	}
	if got := refundStatus(t, s, id2); got != "요청" {
		t.Errorf("확정 실패 뒤 상태 %s, want 요청", got)
	}
	if _, err := s.ExecuteRefund(ctx, okGateway(), id2, "A-507"); err != nil {
		t.Errorf("재시도 = %v", err)
	}
	if got := refundStatus(t, s, id2); got != "완료" {
		t.Errorf("재시도 뒤 상태 %s, want 완료", got)
	}
}

// 취소는 환불 건을 만들어 돌려주고, 그 건이 PG 로 간다.
func TestCancelReturnsItsRefundForExecution(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, total, _ := paidOrder(t, s, pool, "tee", 1)
	id, err := s.CancelOrder(ctx, orderNo, "P-506", "c-1")
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("결제된 주문의 취소가 환불 건을 돌려주지 않았다")
	}
	gw := okGateway()
	if _, err := s.ExecuteRefund(ctx, gw, id, "P-506"); err != nil {
		t.Fatal(err)
	}
	if len(gw.cancelReqs) != 1 || gw.cancelReqs[0].Amount != total {
		t.Errorf("취소 환불 요청 %+v, want 전액 %d", gw.cancelReqs, total)
	}
}

// D14: 결제대기 → 결제실패 (시스템, 10분 만료). AuthWindow 가 지난 주문은 P-408 이
// 거부하므로 결제될 길이 없는데, 재고는 잡힌 채였다.
func TestExpirePendingOrdersRestoresStock(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	stale, _ := seedOrder(t, s, pool, "old", 2) // 재고 10 → 8
	fresh, _ := seedOrder(t, s, pool, "new", 1) // 재고 10 → 9
	limbo, total := seedOrder(t, s, pool, "limbo", 1)
	// limbo: 승인 결과 불명 → '대기' 결제 행이 살아 있다. 만료하면 안 된다.
	unknown := &fakeGateway{err: ErrPaymentUnknown}
	if _, err := s.ConfirmPayment(ctx, unknown, "toss", limbo, "pk-limbo", total, time.Now()); !errors.Is(err, ErrPaymentUnknown) {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE orders SET created_at = now() - interval '11 minutes' WHERE order_no IN ($1, $2)`,
		stale, limbo); err != nil {
		t.Fatal(err)
	}

	n, err := s.ExpirePendingOrders(ctx, time.Now().Add(-AuthWindow), 100)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("만료 %d건, want 1 (stale 만)", n)
	}
	if got := statusOf(t, s, stale); got != StatusPaymentFailed {
		t.Errorf("stale 상태 %s, want %s", got, StatusPaymentFailed)
	}
	if got := statusOf(t, s, fresh); got != StatusPaymentPending {
		t.Errorf("fresh 상태 %s — 창 안의 주문을 만료시켰다", got)
	}
	if got := statusOf(t, s, limbo); got != StatusPaymentPending {
		t.Errorf("limbo 상태 %s — 결과 불명 결제가 있는 주문을 만료시켰다", got)
	}
	var stock int
	if err := pool.QueryRow(ctx, `
		SELECT v.stock FROM product_variants v JOIN products p ON p.id = v.product_id
		WHERE p.slug = 'old'`).Scan(&stock); err != nil {
		t.Fatal(err)
	}
	if stock != 10 {
		t.Errorf("만료 뒤 재고 %d, want 10 — 잡힌 재고가 돌아오지 않았다", stock)
	}
	// 만료된 주문은 승인할 수 없다.
	if _, err := s.ConfirmPayment(ctx, okGateway(), "toss", stale, "pk-late", 0, time.Now()); err == nil {
		t.Error("만료된 주문이 승인됐다")
	}
}

// **결과 불명('승인') 환불은 같은 멱등키로 다시 부를 수 있다 — 키가 살아 있는
// 동안만** (D50, 토스: 같은 키의 재요청은 15일 동안 첫 응답을 돌려준다).
//
// 앞 판은 '승인' 을 「사람이 조회로 확인한 뒤 손으로 닫는다」고 적었지만 닫는
// 경로가 없었다. 키가 살아 있으면 다시 부르는 것이 곧 조회다 — 첫 호출이
// 처리됐으면 그 결과가, 닿지 않았으면 이번 호출이 처리된다. 키가 죽은 뒤에는
// 두 번째 환불이 될 수 있어 부르지 않는다.
func TestRetryRefundResendsAnUnknownRefundOnlyWhileTheKeyLives(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, _, _ := paidOrder(t, s, pool, "tee", 2)
	items := itemsOf(t, s, orderNo)
	unknownRefund := func(key string) string {
		id, _, err := s.RequestRefund(ctx, orderNo,
			[]RefundLine{{OrderItemID: items[0].ID, Quantity: 1}}, "관리자", "불량", key)
		if err != nil {
			t.Fatal(err)
		}
		gw := okGateway()
		gw.cancelErr = ErrPaymentUnknown
		if _, err := s.ExecuteRefund(ctx, gw, id, "A-507"); !errors.Is(err, ErrPaymentUnknown) {
			t.Fatalf("결과 불명 = %v", err)
		}
		return id
	}

	id := unknownRefund("rk-retry")
	gw := okGateway()
	if _, err := s.RetryRefund(ctx, gw, id, time.Now(), "A-507"); err != nil {
		t.Fatalf("키가 살아 있는 결과 불명 재실행 = %v", err)
	}
	if got := refundStatus(t, s, id); got != "완료" {
		t.Errorf("재실행 뒤 상태 %s, want 완료", got)
	}
	if len(gw.cancelReqs) != 1 || gw.cancelReqs[0].IdempotencyKey != "rk-retry" {
		t.Errorf("재실행 취소 요청 = %+v, want 같은 키(rk-retry)로 한 번", gw.cancelReqs)
	}

	old := unknownRefund("rk-old")
	late := okGateway()
	if _, err := s.RetryRefund(ctx, late, old, time.Now().Add(RefundRetryWindow+time.Hour), "A-507"); !errors.Is(err, ErrRefundRetryExpired) {
		t.Errorf("키가 죽은 뒤 재실행 = %v, want ErrRefundRetryExpired", err)
	}
	if len(late.cancelReqs) != 0 || refundStatus(t, s, old) != "승인" {
		t.Errorf("키가 죽은 뒤 취소 %d번·상태 %s, want 0번·승인", len(late.cancelReqs), refundStatus(t, s, old))
	}
}

// **같은 환불 건을 두 요청이 동시에 실행해도 PG 취소는 한 번이다** (지금 동작). 환불 행
// 잠금이 둘을 줄 세우고, 뒤의 것은 「호출 중(승인)」 을 보고 상태 오류로 돌아선다 — 이중 환불은
// 결국 취소 API 호출 수다.
func TestConcurrentExecuteSendsOnce(t *testing.T) {
	s, pool := testStore(t)
	orderNo, _, _ := paidOrder(t, s, pool, "tee", 2)
	id, _ := refundAll(t, s, orderNo, "rk-race")
	gw := okGateway()
	gw.onCancel = func() { time.Sleep(200 * time.Millisecond) } // 앞 호출이 PG 에 머무는 동안
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.ExecuteRefund(context.Background(), gw, id, "A-507")
		}()
	}
	wg.Wait()
	if len(gw.cancelReqs) != 1 {
		t.Errorf("취소 API %d 번 — 이중 환불", len(gw.cancelReqs))
	}
	ok, busy := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrRefundState):
			busy++
		default:
			t.Errorf("예상 밖 오류: %v", err)
		}
	}
	if ok != 1 || busy != 1 {
		t.Errorf("성공 %d · 상태 오류 %d — 하나씩이어야 한다", ok, busy)
	}
	if got := refundStatus(t, s, id); got != "완료" {
		t.Errorf("환불 상태 %s, want 완료", got)
	}
}

// **배송 전 주문은 품목을 A-507 로 전부 환불해도 열린 채다** (지금 동작). 품목 단위
// 환불은 배송비를 돌려주지 않으므로(D50) 환불액이 결제액에 못 미치고, 배송 전의 남은
// 배송비를 돌려주며 닫는 길은 구매자의 전체 취소(P-506)뿐이다. 배송 뒤는
// TestAllItemsRefundedAfterShippingClosesTheOrder 다. 정책을 정하면 이 시험을 바꾼다.
func TestFullItemRefundWithShippingFeeLeavesTheOrderOpen(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, total, goods := paidOrder(t, s, pool, "tee", 1)
	if total == goods {
		t.Fatalf("배송비가 없는 주문이다 (결제 %d) — 전제가 틀렸다", total)
	}
	id, _ := refundAll(t, s, orderNo, "rk-all-items")
	to, err := s.ExecuteRefund(ctx, okGateway(), id, "A-507")
	if err != nil {
		t.Fatal(err)
	}
	if to != "" {
		t.Errorf("주문이 %s 로 닫혔다", to)
	}
	assertOrderStatus(t, pool, orderNo, StatusPaid)
}

// **배송 뒤 품목이 전부 환불되면 배송비가 남아도 주문은 환불로 끝난다** (FR-611). 배송 뒤의
// 배송비는 돌려주지 않으므로(D50) 금액만 보면 영영 「전액」이 아니다 — 그 주문은 배송완료로
// 남아 구매확정까지 갈 수 있었다. 돈은 그대로다: 환불액은 상품 합계다.
func TestAllItemsRefundedAfterShippingClosesTheOrder(t *testing.T) {
	s, pool := testStore(t)
	orderNo, goods := deliveredOrder(t, s, pool, "tee", 1)
	if approved, _, err := s.RefundedTotal(context.Background(), orderNo); err != nil || approved <= goods {
		t.Fatalf("결제 %d, 상품 %d, %v — 배송비가 붙은 주문이어야 한다", approved, goods, err)
	}
	id, amount := refundAll(t, s, orderNo, "rk-shipped")
	if amount != goods {
		t.Fatalf("환불액 %d, want %d (배송비 제외)", amount, goods)
	}
	to, err := s.ExecuteRefund(context.Background(), okGateway(), id, "A-507")
	if err != nil || to != StatusRefunded {
		t.Fatalf("= %q, %v — want 환불", to, err)
	}
	assertOrderStatus(t, pool, orderNo, StatusRefunded)
}

// **남은 환불(요청·승인)이 있으면 닫지 않는다** (FR-611). 결제액 전부가 선점됐어도 그중
// 한 건은 아직 PG 로 가지 않았다 — 먼저 실행한 건이 주문을 닫던 판은, 남은 건이 거절되어
// 한도가 돌아가도 주문은 이미 끝나 있었다. 마지막 건이 확정될 때 닫힌다.
func TestOpenRefundKeepsTheOrderOpen(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo := freeShippingOrder(t, s, pool, "two")
	item := itemsOf(t, s, orderNo)[0].ID
	first, _, err := s.RequestRefund(ctx, orderNo, []RefundLine{{OrderItemID: item, Quantity: 3}}, "관리자", "", "rk-o1")
	if err != nil {
		t.Fatal(err)
	}
	last, _, err := s.RequestRefund(ctx, orderNo, []RefundLine{{OrderItemID: item, Quantity: 1}}, "관리자", "", "rk-o2")
	if err != nil {
		t.Fatal(err)
	}
	if to, err := s.ExecuteRefund(ctx, okGateway(), first, "A-507"); err != nil || to != "" {
		t.Fatalf("앞 건 = %q, %v — 남은 건이 있어 그대로여야 한다", to, err)
	}
	assertOrderStatus(t, pool, orderNo, StatusPaid)
	if to, err := s.ExecuteRefund(ctx, okGateway(), last, "A-507"); err != nil || to != StatusCancelled {
		t.Fatalf("마지막 건 = %q, %v — want 취소", to, err)
	}
}

// freeShippingOrder is a paid order above the free-shipping line (13,000 × 4), so
// refunding every item is refunding the whole payment.
func freeShippingOrder(t *testing.T, s *Store, pool *pgxpool.Pool, slug string) string {
	t.Helper()
	orderNo, total, goods := paidOrder(t, s, pool, slug, 4)
	if total != goods {
		t.Fatalf("배송비 %d 원이 붙었다 — 전제가 틀렸다", total-goods)
	}
	return orderNo
}

// **PG 가 확정한 환불이 결제액 전부면 주문도 끝난다** (D14: 배송 전 → 취소, 배송 후 → 환불).
// 그 결정은 환불을 실행하는 쪽이 한다 — 화면마다 따로 기억하면 한 화면이 잊는다.
func TestExecuteRefundClosesAFullyRefundedOrder(t *testing.T) {
	s, pool := testStore(t)
	orderNo := freeShippingOrder(t, s, pool, "full")
	id, _ := refundAll(t, s, orderNo, "rk-full")
	to, err := s.ExecuteRefund(context.Background(), okGateway(), id, "A-507")
	if err != nil || to != StatusCancelled {
		t.Fatalf("= %q, %v — want 취소", to, err)
	}
	assertOrderStatus(t, pool, orderNo, StatusCancelled)
}

// 부분 환불은 주문을 그대로 둔다 — 남은 물건은 아직 간다.
func TestExecuteRefundLeavesAPartialOrder(t *testing.T) {
	s, pool := testStore(t)
	orderNo := freeShippingOrder(t, s, pool, "part")
	id, _, err := s.RequestRefund(context.Background(), orderNo,
		[]RefundLine{{OrderItemID: itemsOf(t, s, orderNo)[0].ID, Quantity: 1}}, "관리자", "", "rk-part")
	if err != nil {
		t.Fatal(err)
	}
	if to, err := s.ExecuteRefund(context.Background(), okGateway(), id, "A-507"); err != nil || to != "" {
		t.Fatalf("= %q, %v — want 그대로", to, err)
	}
	assertOrderStatus(t, pool, orderNo, StatusPaid)
}

// 결과를 몰라 다시 보낸 환불(A-507 「실행」)도 전액이면 주문을 끝낸다.
func TestRetryRefundClosesToo(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo := freeShippingOrder(t, s, pool, "retry")
	id, _ := refundAll(t, s, orderNo, "rk-retry")
	unknown := okGateway()
	unknown.cancelErr = ErrPaymentUnknown
	if _, err := s.ExecuteRefund(ctx, unknown, id, "A-507"); !errors.Is(err, ErrPaymentUnknown) {
		t.Fatalf("첫 실행 = %v, want ErrPaymentUnknown", err)
	}
	to, err := s.RetryRefund(ctx, okGateway(), id, time.Now(), "A-507")
	if err != nil || to != StatusCancelled {
		t.Fatalf("다시 실행 = %q, %v — want 취소", to, err)
	}
}

// **돈이 나간 뒤 주문 종료 기록이 실패해도 환불을 되돌리지 않는다** — 환불 건은 완료이고,
// 호출자는 ErrRefundSentNotSettled 를 보고 로그만 남긴다(돈은 이미 움직였다).
func TestSettleFailureAfterPGIsReportedNotUndone(t *testing.T) {
	s, pool := testStore(t)
	orderNo := freeShippingOrder(t, s, pool, "lost")
	id, _ := refundAll(t, s, orderNo, "rk-lost")
	gw := okGateway()
	// PG 왕복 중에 주문을 찾을 수 없게 만든다 — 종료 단계의 잠금이 실패한다.
	gw.onCancel = func() {
		if _, err := pool.Exec(context.Background(),
			`UPDATE orders SET order_no = order_no || '-x' WHERE order_no = $1`, orderNo); err != nil {
			t.Error(err)
		}
	}
	_, err := s.ExecuteRefund(context.Background(), gw, id, "A-507")
	if !errors.Is(err, ErrRefundSentNotSettled) {
		t.Fatalf("= %v, want ErrRefundSentNotSettled", err)
	}
	if got := refundStatus(t, s, id); got != "완료" {
		t.Errorf("환불 상태 %s, want 완료 — 나간 돈의 기록을 되돌렸다", got)
	}
}

// **주문 종료는 품목을 잠근 뒤에 판정한다** (FR-611). 환불 접수·거부는 주문 행을 잡지 않고
// 품목 → 결제로 잡는다. 판정(결제액·남은 환불·소진 수량을 차례로 읽는다)을 잠그지 않고 하면
// 그 사이에 거부가 커밋될 수 있다 — 「전액」을 읽은 뒤 거부로 한도가 풀려도 주문은 취소로
// 닫히고, 품목을 다시 전부 소진해 그 몫은 돌려줄 길이 없어진다. 품목이 잡혀 있으면 판정
// 전에 기다려야 한다. 닫을 곳이 없는 주문이라야 관측된다(닫을 곳이 있으면 어차피 품목을 잡는다).
func TestCloseDecidesUnderTheItemLock(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo := freeShippingOrder(t, s, pool, "decide")
	item := itemsOf(t, s, orderNo)[0].ID
	first, _, err := s.RequestRefund(ctx, orderNo, []RefundLine{{OrderItemID: item, Quantity: 3}}, "관리자", "", "rk-d1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RequestRefund(ctx, orderNo, []RefundLine{{OrderItemID: item, Quantity: 1}}, "관리자", "", "rk-d2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExecuteRefund(ctx, okGateway(), first, "A-507"); err != nil {
		t.Fatal(err)
	}
	whileWaitingOn(t, pool, itemLock(item),
		`SELECT 1 FROM payments WHERE order_id = (SELECT id FROM orders WHERE order_no = '`+orderNo+`') FOR UPDATE NOWAIT`,
		func() error {
			to, err := s.CloseRefundedOrder(ctx, orderNo, "A-507")
			if err == nil && to != "" {
				return fmt.Errorf("남은 환불이 있는데 %s 로 닫았다", to)
			}
			return err
		})
	assertOrderStatus(t, pool, orderNo, StatusPaid)
}

// **배송 뒤라도 품목이 남아 있으면 닫지 않는다** — 남은 물건은 구매자에게 있고 반품·구매확정이
// 아직 그 주문으로 온다.
func TestPartialRefundAfterShippingLeavesTheOrderOpen(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, _ := deliveredOrder(t, s, pool, "tee", 2)
	id, _, err := s.RequestRefund(ctx, orderNo,
		[]RefundLine{{OrderItemID: itemsOf(t, s, orderNo)[0].ID, Quantity: 1}}, "관리자", "", "rk-shipped-part")
	if err != nil {
		t.Fatal(err)
	}
	if to, err := s.ExecuteRefund(ctx, okGateway(), id, "A-507"); err != nil || to != "" {
		t.Fatalf("= %q, %v — want 그대로", to, err)
	}
	assertOrderStatus(t, pool, orderNo, StatusDelivered)
}

// **자동 종료가 실패해 열린 채 남은 주문은 A-507 「주문 종료」가 닫는다** (FR-611). 돈은 이미
// 나갔고 「실행」은 완료된 건을 받지 않으니, 이것이 없으면 그 주문을 닫을 길이 없다.
// 화면은 닫을 곳(RefundCloseTarget)이 있을 때만 단추를 그린다.
func TestCloseRefundedOrderClosesWhatTheAutoCloseMissed(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo := freeShippingOrder(t, s, pool, "reclose")
	id, _ := refundAll(t, s, orderNo, "rk-reclose")
	gw := okGateway()
	gw.onCancel = func() {
		if _, err := pool.Exec(ctx, `UPDATE orders SET order_no = order_no || '-x' WHERE order_no = $1`, orderNo); err != nil {
			t.Error(err)
		}
	}
	if _, err := s.ExecuteRefund(ctx, gw, id, "A-507"); !errors.Is(err, ErrRefundSentNotSettled) {
		t.Fatalf("= %v, want ErrRefundSentNotSettled", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE orders SET order_no = $1 WHERE order_no = $1 || '-x'`, orderNo); err != nil {
		t.Fatal(err)
	}
	o, err := s.OrderByNoUnscoped(ctx, orderNo)
	if err != nil {
		t.Fatal(err)
	}
	if to, err := s.RefundCloseTarget(ctx, o.ID, o.Status, "A-507"); err != nil || to != StatusCancelled {
		t.Fatalf("닫을 곳 = %q, %v — want 취소", to, err)
	}
	if to, err := s.CloseRefundedOrder(ctx, orderNo, "A-507"); err != nil || to != StatusCancelled {
		t.Fatalf("주문 종료 = %q, %v — want 취소", to, err)
	}
	assertOrderStatus(t, pool, orderNo, StatusCancelled)
	if to, err := s.RefundCloseTarget(ctx, o.ID, StatusCancelled, "A-507"); err != nil || to != "" {
		t.Errorf("닫은 뒤 닫을 곳 = %q, %v — want 없음", to, err)
	}
}
