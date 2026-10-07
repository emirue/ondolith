package commerce

import (
	"context"
	"errors"
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

// **승인 결과를 모르면 그 자리에서 조회 API 로 확인한다** (D50 「10분 만료와 복구」).
//
// D50 은 「결과 불명 → 재승인 금지, 조회 API 로 확인」이라 적었지만 앞 판은 조회를
// 하지 않고 '대기' 로 남기기만 했다. 그 행은 만료 처리가 건너뛰고(재고도 잡힌 채),
// 결제대기 → 결제완료 는 P-408 만 할 수 있어 앱 안에서 닫을 길이 없었다.
// 한 번 물어 답이 오면 그것이 결과다. 재승인은 여전히 하지 않는다.
func TestUnknownConfirmIsResolvedByOneLookup(t *testing.T) {
	s, pool := testStore(t)
	orderNo, total := seedOrder(t, s, pool, "tee-lookup", 1)
	gw := &fakeGateway{err: ErrPaymentUnknown, getResponse: &Payment{
		PaymentKey: "pk-lookup", OrderNo: orderNo, Status: PaymentApproved, Amount: total,
		Raw: []byte(`{"status":"DONE"}`)}}

	if _, err := s.ConfirmPayment(context.Background(), gw, "toss", orderNo, "pk-lookup", total, time.Now()); err != nil {
		t.Fatalf("조회가 승인을 확인했는데 = %v", err)
	}
	if got := statusOf(t, s, orderNo); got != StatusPaid {
		t.Errorf("주문 상태 %s, want %s", got, StatusPaid)
	}
	if gw.count() != 1 || gw.getCalls != 1 {
		t.Errorf("승인 %d회·조회 %d회, want 1·1 — 재승인하지 않고 한 번 묻는다", gw.count(), gw.getCalls)
	}
}

// 조회가 승인·실패를 답하지 않으면 여전히 모른다. '대기' 로 남기고 A-508 이 닫는다.
// 조회의 '대기' 는 READY 와 가상계좌 발급을 가르지 못하고, 다른 주문의 결제를
// 답으로 받으면 남의 결제로 내 주문을 닫는다.
func TestUnknownConfirmLookupOnlyAcceptsADefiniteAnswerForThisOrder(t *testing.T) {
	for _, tc := range []struct {
		name    string
		got     func(orderNo string, total int) *Payment
		getErr  error
		wantErr error
		wantPay string
	}{
		{"조회도 실패", nil, ErrPaymentUnknown, ErrPaymentUnknown, "대기"},
		{"조회가 대기", func(o string, n int) *Payment {
			return &Payment{OrderNo: o, Status: PaymentPending, Amount: n}
		}, nil, ErrPaymentUnknown, "대기"},
		{"다른 주문의 승인", func(_ string, n int) *Payment {
			return &Payment{OrderNo: "OTHER-ORDER", Status: PaymentApproved, Amount: n}
		}, nil, ErrPaymentUnknown, "대기"},
		{"조회가 확정 실패", func(o string, n int) *Payment {
			return &Payment{OrderNo: o, Status: PaymentFailed, Amount: n, Raw: []byte(`{"status":"ABORTED"}`)}
		}, nil, ErrPaymentDeclined, "실패"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, pool := testStore(t)
			orderNo, total := seedOrder(t, s, pool, "tee-lookup2", 1)
			gw := &fakeGateway{err: ErrPaymentUnknown, getErr: tc.getErr}
			if tc.got != nil {
				gw.getResponse = tc.got(orderNo, total)
			}
			if _, err := s.ConfirmPayment(context.Background(), gw, "toss", orderNo, "pk-lookup2", total, time.Now()); !errors.Is(err, tc.wantErr) {
				t.Fatalf("= %v, want %v", err, tc.wantErr)
			}
			if got := statusOf(t, s, orderNo); got != StatusPaymentPending {
				t.Errorf("주문 상태 %s, want %s", got, StatusPaymentPending)
			}
			var st string
			if err := pool.QueryRow(context.Background(),
				`SELECT status FROM payments WHERE payment_key = 'pk-lookup2'`).Scan(&st); err != nil || st != tc.wantPay {
				t.Errorf("결제 행 = %q (%v), want %s", st, err, tc.wantPay)
			}
		})
	}
}
