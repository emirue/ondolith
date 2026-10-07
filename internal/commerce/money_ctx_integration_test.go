package commerce

import (
	"context"
	"testing"
	"time"
)

// **결제사 호출 중에 브라우저가 떠나도 결과는 기록된다.**
//
// net/http 는 클라이언트 연결이 끊기면 요청 컨텍스트를 취소한다. 돈 경로가 그
// 컨텍스트를 그대로 쓰면, PG 는 승인·취소를 끝냈는데 그 직후의 DB 기록이
// 「context canceled」로 실패해 「PG 는 승인, 우리는 대기」가 남는다 — 탭을 닫거나
// 휴대폰이 잠들거나 더블클릭이 첫 요청을 취소하는 것만으로. 웹훅 처리는 이미
// 분리된 컨텍스트를 쓰고 있었다 (handler_webhook.go).
func TestConfirmIsRecordedEvenIfTheBrowserLeavesMidCall(t *testing.T) {
	s, pool := testStore(t)
	orderNo, total := seedOrder(t, s, pool, "tee-leave", 1)
	ctx, leave := context.WithCancel(context.Background())
	gw := okGateway()
	gw.onConfirm = leave // PG 왕복 중에 연결이 끊긴다

	if _, err := s.ConfirmPayment(ctx, gw, "toss", orderNo, "pk-leave", total, time.Now()); err != nil {
		t.Fatalf("PG 가 승인했는데 기록이 실패했다: %v", err)
	}
	if got := statusOf(t, s, orderNo); got != StatusPaid {
		t.Errorf("주문 상태 %s, want %s — PG 는 승인, 우리는 대기", got, StatusPaid)
	}
}

func TestRefundIsRecordedEvenIfTheBrowserLeavesMidCall(t *testing.T) {
	s, pool := testStore(t)
	orderNo, _, _ := paidOrder(t, s, pool, "tee-rleave", 2)
	items := itemsOf(t, s, orderNo)
	id, _, err := s.RequestRefund(context.Background(), orderNo,
		[]RefundLine{{OrderItemID: items[0].ID, Quantity: 1}}, "관리자", "불량", "rk-leave")
	if err != nil {
		t.Fatal(err)
	}
	ctx, leave := context.WithCancel(context.Background())
	gw := okGateway()
	gw.onCancel = leave

	if err := s.ExecuteRefund(ctx, gw, id); err != nil {
		t.Fatalf("PG 가 취소했는데 기록이 실패했다: %v", err)
	}
	if got := refundStatus(t, s, id); got != "완료" {
		t.Errorf("환불 상태 %s, want 완료 — 돈은 나갔는데 장부는 진행 중", got)
	}
}

func TestExchangeDiffIsRecordedEvenIfTheBrowserLeavesMidCall(t *testing.T) {
	s, pool := testStore(t)
	orderNo, returnNo, amount := exchangeAwaitingDiff(t, s, pool, "tee-xleave")
	ctx, leave := context.WithCancel(context.Background())
	gw := okGateway()
	gw.onConfirm = leave

	if err := s.ConfirmExchangeDiff(ctx, gw, "toss", orderNo, returnNo, "",
		"pk-xleave", amount, time.Now()); err != nil {
		t.Fatalf("PG 가 승인했는데 기록이 실패했다: %v", err)
	}
	if got := statusOf(t, s, orderNo); got != StatusExchangeShipped {
		t.Errorf("주문 상태 %s, want %s", got, StatusExchangeShipped)
	}
}
