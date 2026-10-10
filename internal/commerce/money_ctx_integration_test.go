package commerce

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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

// pendingPayment 는 승인 결과를 모른 채 '대기' 로 남은 주문결제다 — 승인도 조회도
// 답하지 않은 경우. A-508 이 닫는 대상이다.
func pendingPayment(t *testing.T, s *Store, pool *pgxpool.Pool, slug string) (orderNo string, total int, paymentID string) {
	t.Helper()
	orderNo, total = seedOrder(t, s, pool, slug, 1)
	gw := &fakeGateway{err: ErrPaymentUnknown, getErr: ErrPaymentUnknown}
	if _, err := s.ConfirmPayment(context.Background(), gw, "toss", orderNo, "pk-"+slug, total, time.Now()); !errors.Is(err, ErrPaymentUnknown) {
		t.Fatalf("준비: %v", err)
	}
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM payments WHERE payment_key = $1`, "pk-"+slug).Scan(&paymentID); err != nil {
		t.Fatal(err)
	}
	return orderNo, total, paymentID
}

// **A-508 이 '대기' 결제를 PG 조회 결과로 닫는다** (D19 A-508 「대사 결과로 상태 변경」).
//
// 앞 판의 A-508 은 「PG 는 승인, 우리는 대기」를 보여 주고 「A-506 에서 옮기라」고
// 했지만, 결제대기 → 결제완료 는 P-408 만 할 수 있어 A-506 에 그 선택지가 없었다.
// 돈은 나갔는데 주문을 결제완료로 만들 길이 앱 안에 없었다.
func TestResolvePendingPaymentFollowsTheLookup(t *testing.T) {
	for _, tc := range []struct {
		name      string
		got       func(orderNo string, total int) *Payment
		getErr    error
		wantErr   error
		wantOrder Status
		wantPay   string
	}{
		{"PG 승인", func(o string, n int) *Payment {
			return &Payment{OrderNo: o, Status: PaymentApproved, Amount: n, Raw: []byte(`{"status":"DONE"}`)}
		}, nil, nil, StatusPaid, "승인"},
		{"PG 실패", func(o string, n int) *Payment {
			return &Payment{OrderNo: o, Status: PaymentFailed, Amount: n, Raw: []byte(`{"status":"ABORTED"}`)}
		}, nil, nil, StatusPaymentPending, "실패"},
		{"PG 도 대기", func(o string, n int) *Payment {
			return &Payment{OrderNo: o, Status: PaymentPending, Amount: n}
		}, nil, ErrNothingToResolve, StatusPaymentPending, "대기"},
		{"조회 실패", nil, ErrPaymentUnknown, ErrPaymentUnknown, StatusPaymentPending, "대기"},
		{"금액이 다르다", func(o string, n int) *Payment {
			return &Payment{OrderNo: o, Status: PaymentApproved, Amount: n + 1}
		}, nil, ErrAmountMismatch, StatusPaymentPending, "대기"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, pool := testStore(t)
			orderNo, total, paymentID := pendingPayment(t, s, pool, "tee-resolve")
			gw := &fakeGateway{getErr: tc.getErr}
			if tc.got != nil {
				gw.getResponse = tc.got(orderNo, total)
			}
			_, err := s.ResolvePendingPayment(context.Background(), gw, paymentID)
			if tc.wantErr == nil && err != nil || tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("= %v, want %v", err, tc.wantErr)
			}
			if got := statusOf(t, s, orderNo); got != tc.wantOrder {
				t.Errorf("주문 상태 %s, want %s", got, tc.wantOrder)
			}
			var st string
			if err := pool.QueryRow(context.Background(),
				`SELECT status FROM payments WHERE id = $1`, paymentID).Scan(&st); err != nil || st != tc.wantPay {
				t.Errorf("결제 행 = %q (%v), want %s", st, err, tc.wantPay)
			}
		})
	}
}

// 조회 응답이 다른 주문의 결제면 반영하지 않는다 — 남의 결제로 내 주문을 닫는다.
// 이미 닫힌 결제는 다시 반영하지 않는다.
func TestResolvePendingPaymentRefusesOtherOrdersAndSettledPayments(t *testing.T) {
	s, pool := testStore(t)
	orderNo, total, paymentID := pendingPayment(t, s, pool, "tee-resolve2")
	other := &fakeGateway{getResponse: &Payment{OrderNo: "OTHER-ORDER", Status: PaymentApproved, Amount: total}}
	if _, err := s.ResolvePendingPayment(context.Background(), other, paymentID); err == nil {
		t.Fatal("다른 주문의 승인으로 반영했다")
	}
	if got := statusOf(t, s, orderNo); got != StatusPaymentPending {
		t.Errorf("주문 상태 %s, want %s", got, StatusPaymentPending)
	}

	ok := &fakeGateway{getResponse: &Payment{OrderNo: orderNo, Status: PaymentApproved, Amount: total}}
	if _, err := s.ResolvePendingPayment(context.Background(), ok, paymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolvePendingPayment(context.Background(), ok, paymentID); !errors.Is(err, ErrNothingToResolve) {
		t.Errorf("이미 반영된 결제 = %v, want ErrNothingToResolve", err)
	}
	if _, err := s.ResolvePendingPayment(context.Background(), ok, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrNotFound) {
		t.Errorf("없는 결제 = %v, want ErrNotFound", err)
	}
}

// 조회하는 사이 웹훅이 결제를 닫았으면 그 위에 덮어쓰지 않는다. 잠근 뒤 다시 읽지
// 않으면, 늦게 온 조회의 「실패」가 방금 승인된 결제를 '실패' 로 바꾼다.
func TestResolvePendingPaymentRechecksAfterTheLookup(t *testing.T) {
	s, pool := testStore(t)
	orderNo, total, paymentID := pendingPayment(t, s, pool, "tee-resolve3")
	gw := &fakeGateway{getResponse: &Payment{OrderNo: orderNo, Status: PaymentFailed, Amount: total}}
	gw.onGet = func() { // 그 사이 P-905 가 승인으로 닫는다
		if _, err := pool.Exec(context.Background(),
			`UPDATE payments SET status = '승인' WHERE id = $1`, paymentID); err != nil {
			t.Error(err)
		}
	}
	if _, err := s.ResolvePendingPayment(context.Background(), gw, paymentID); !errors.Is(err, ErrNothingToResolve) {
		t.Errorf("= %v, want ErrNothingToResolve", err)
	}
	var st string
	if err := pool.QueryRow(context.Background(),
		`SELECT status FROM payments WHERE id = $1`, paymentID).Scan(&st); err != nil || st != "승인" {
		t.Errorf("결제 행 = %q (%v), want 승인 — 늦은 조회가 덮어썼다", st, err)
	}
}

// **교환 차액도 결과를 모르면 '실패' 로 내리지 않는다** (D50, ConfirmPayment 와 같다).
// 앞 판은 시간 초과도 '실패' 로 적어, 부분 유니크(`status <> '실패'`)가 같은 교환의
// 두 번째 차액 결제를 열었다 — 첫 결제가 실제로 승인됐다면 이중 청구다.
func TestUnknownExchangeDiffStaysPending(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, returnNo, amount := exchangeAwaitingDiff(t, s, pool, "tee-unk")

	gw := &fakeGateway{err: ErrPaymentUnknown}
	if err := s.ConfirmExchangeDiff(ctx, gw, "toss", orderNo, returnNo, "", "pk-diff-unk", amount, time.Now()); !errors.Is(err, ErrPaymentUnknown) {
		t.Fatalf("= %v, want ErrPaymentUnknown", err)
	}
	var st string
	if err := pool.QueryRow(ctx, `SELECT status FROM payments WHERE payment_key = 'pk-diff-unk'`).Scan(&st); err != nil || st != "대기" {
		t.Errorf("차액 결제 행 = %q (%v), want 대기", st, err)
	}
	if err := s.ConfirmExchangeDiff(ctx, okGateway(), "toss", orderNo, returnNo, "", "pk-diff-2", amount, time.Now()); !errors.Is(err, ErrAlreadyPaid) {
		t.Errorf("결과 불명 뒤 두 번째 차액 결제 = %v, want ErrAlreadyPaid — 이중 청구 길", err)
	}
}
