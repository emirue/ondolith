package commerce

import (
	"context"
	"errors"
	"testing"
	"time"
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
	if err := s.ExecuteRefund(ctx, gw, id); err != nil {
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
	if err := s.ExecuteRefund(ctx, gw, id); err != nil {
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
	if err := s.ExecuteRefund(ctx, unknown, id); !errors.Is(err, ErrPaymentUnknown) {
		t.Fatalf("결과 불명 = %v", err)
	}
	if got := refundStatus(t, s, id); got != "승인" {
		t.Errorf("결과 불명 뒤 상태 %s, want 승인 (다시 부르면 이중 환불)", got)
	}
	if err := s.ExecuteRefund(ctx, unknown, id); !errors.Is(err, ErrRefundState) {
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
	if err := s.ExecuteRefund(ctx, declined, id2); err == nil {
		t.Fatal("확정 실패가 성공으로 돌아왔다")
	}
	if got := refundStatus(t, s, id2); got != "요청" {
		t.Errorf("확정 실패 뒤 상태 %s, want 요청", got)
	}
	if err := s.ExecuteRefund(ctx, okGateway(), id2); err != nil {
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
	if err := s.ExecuteRefund(ctx, gw, id); err != nil {
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
