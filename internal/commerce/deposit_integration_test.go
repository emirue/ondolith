package commerce

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 가상계좌: 승인 응답이 200 이어도 상태가 WAITING_FOR_DEPOSIT 이면 **입금 전**이다.
// 그것을 결제완료로 옮기면 입금 없이 물건이 나간다 — 운영자는 A-506 의
// 결제완료를 보고 발송한다. 주문은 입금대기로 가고, 결제완료는 P-905 가
// 조회 API 로 DONE 을 확인한 뒤에만 한다 (D14 5절, D50).
func pendingGateway(secret string) *fakeGateway {
	return &fakeGateway{response: &Payment{
		PaymentKey: "pk-va", Status: PaymentPending, Secret: secret,
		Raw: []byte(`{"paymentKey":"pk-va","status":"WAITING_FOR_DEPOSIT"}`),
	}}
}

func statusOf(t *testing.T, s *Store, orderNo string) Status {
	t.Helper()
	d, err := s.OrderByNoUnscoped(context.Background(), orderNo)
	if err != nil {
		t.Fatal(err)
	}
	return d.Status
}

func TestVirtualAccountConfirmIsNotPaid(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, total := seedOrder(t, s, pool, "va", 1)

	_, err := s.ConfirmPayment(ctx, pendingGateway("s3cret"), "toss", orderNo, "pk-va", total, time.Now())
	if !errors.Is(err, ErrDepositPending) {
		t.Fatalf("입금 전 승인 = %v, want ErrDepositPending", err)
	}
	if got := statusOf(t, s, orderNo); got != StatusDepositPending {
		t.Errorf("주문 상태 %s, want %s — 입금 없이 결제완료가 됐다", got, StatusDepositPending)
	}
	var payStatus, secret string
	var approvedAt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT status, COALESCE(secret,''), approved_at FROM payments WHERE kind = '주문결제'`).
		Scan(&payStatus, &secret, &approvedAt); err != nil {
		t.Fatal(err)
	}
	if payStatus != string(PaymentPending) || secret != "s3cret" || approvedAt != nil {
		t.Errorf("payments = (%s, %q, %v), want (대기, s3cret, nil)", payStatus, secret, approvedAt)
	}
}

func depositOrder(t *testing.T, s *Store, pool interface{}, orderNo string, total int, secret string) {
	t.Helper()
	_, err := s.ConfirmPayment(context.Background(), pendingGateway(secret), "toss", orderNo, "pk-va", total, time.Now())
	if !errors.Is(err, ErrDepositPending) {
		t.Fatalf("가상계좌 발급 = %v", err)
	}
}

func webhook(t *testing.T, s *Store, orderNo, secret string, amount int) string {
	t.Helper()
	id, err := s.RecordWebhook(context.Background(), "toss", &WebhookEvent{
		EventID: "DEPOSIT_CALLBACK:" + orderNo + ":" + time.Now().String(),
		Type:    "DEPOSIT_CALLBACK", OrderNo: orderNo, PaymentKey: "pk-va",
		Secret: secret, Amount: amount, Raw: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// **진실은 웹훅 본문이 아니라 조회 API 다.** 웹훅이 DONE 이라고 해도 PG 가
// 조회에 대기라고 답하면 옮기지 않고, DONE 과 우리 금액을 답해야 옮긴다.
func TestDepositWebhookCompletesOnlyOnGatewayDone(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, total := seedOrder(t, s, pool, "va", 1)
	depositOrder(t, s, pool, orderNo, total, "s3cret")

	gw := pendingGateway("s3cret")
	gw.getResponse = &Payment{PaymentKey: "pk-va", Status: PaymentPending, Amount: total}
	if err := s.ProcessWebhook(ctx, gw, webhook(t, s, orderNo, "s3cret", total), &WebhookEvent{
		OrderNo: orderNo, PaymentKey: "pk-va", Secret: "s3cret", Amount: total}); err != nil {
		t.Fatalf("입금 전 알림 처리 = %v", err)
	}
	if got := statusOf(t, s, orderNo); got != StatusDepositPending {
		t.Fatalf("조회가 대기라고 했는데 주문이 %s 다", got)
	}
	if gw.getCalls != 1 {
		t.Errorf("조회 API %d번, want 1 — 웹훅 본문만 믿었다", gw.getCalls)
	}

	gw.getResponse = &Payment{PaymentKey: "pk-va", Status: PaymentApproved, Amount: total,
		Raw: []byte(`{"status":"DONE"}`)}
	if err := s.ProcessWebhook(ctx, gw, webhook(t, s, orderNo, "s3cret", total), &WebhookEvent{
		OrderNo: orderNo, PaymentKey: "pk-va", Secret: "s3cret", Amount: total}); err != nil {
		t.Fatalf("입금 확인 처리 = %v", err)
	}
	if got := statusOf(t, s, orderNo); got != StatusPaid {
		t.Errorf("입금 확인 뒤 주문 %s, want %s", got, StatusPaid)
	}
	var payStatus string
	var approvedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT status, approved_at FROM payments WHERE kind='주문결제'`).
		Scan(&payStatus, &approvedAt); err != nil {
		t.Fatal(err)
	}
	if payStatus != string(PaymentApproved) || approvedAt == nil {
		t.Errorf("payments = (%s, %v), want (승인, 시각)", payStatus, approvedAt)
	}
}

// **secret 이 다르면 우리 결제에 대한 알림이 아니다.** 비어 오는 것도 다르다.
func TestWebhookSecretMismatchIsRefused(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, total := seedOrder(t, s, pool, "va", 1)
	depositOrder(t, s, pool, orderNo, total, "s3cret")
	gw := pendingGateway("s3cret")
	gw.getResponse = &Payment{PaymentKey: "pk-va", Status: PaymentApproved, Amount: total}

	for _, wrong := range []string{"s3cret-x", ""} {
		// 기록 단계에서 먼저 걸린다 — 맞지 않는 알림은 행을 남기지 않는다.
		if _, err := s.RecordWebhook(ctx, "toss", &WebhookEvent{
			EventID: "DEPOSIT_CALLBACK:" + orderNo + ":" + wrong, OrderNo: orderNo,
			PaymentKey: "pk-va", Secret: wrong, Raw: []byte(`{}`)}); !errors.Is(err, ErrWebhookUnmatched) {
			t.Errorf("secret %q 기록 = %v, want ErrWebhookUnmatched", wrong, err)
		}
		// 처리 단계의 대조도 그대로다 — 기록된 행에 다른 secret 의 알림을 붙여 본다.
		err := s.ProcessWebhook(ctx, gw, webhook(t, s, orderNo, "s3cret", total), &WebhookEvent{
			OrderNo: orderNo, PaymentKey: "pk-va", Secret: wrong, Amount: total})
		if err == nil {
			t.Errorf("secret %q 이 통과했다", wrong)
		}
	}
	if got := statusOf(t, s, orderNo); got != StatusDepositPending {
		t.Errorf("잘못된 secret 으로 주문이 %s 가 됐다", got)
	}
	if gw.getCalls != 0 {
		t.Errorf("secret 이 다른데 조회 API 를 %d번 불렀다", gw.getCalls)
	}
}

// 200 에 실려 온 취소·만료는 확정된 실패다. payments 만 '실패' 로 내리고 주문은
// 결제대기에 둔다 — 부분 유니크의 `status <> '실패'` 가 재결제 경로를 연다.
func TestDeclinedConfirmLeavesRetryOpen(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, total := seedOrder(t, s, pool, "card", 1)

	declined := &fakeGateway{response: &Payment{PaymentKey: "pk-1", Status: PaymentFailed,
		Raw: []byte(`{"status":"ABORTED"}`)}}
	_, err := s.ConfirmPayment(ctx, declined, "toss", orderNo, "pk-1", total, time.Now())
	if !errors.Is(err, ErrPaymentDeclined) {
		t.Fatalf("취소 응답 = %v, want ErrPaymentDeclined", err)
	}
	if got := statusOf(t, s, orderNo); got != StatusPaymentPending {
		t.Fatalf("취소됐는데 주문이 %s 다", got)
	}
	if _, err := s.ConfirmPayment(ctx, okGateway(), "toss", orderNo, "pk-2", total, time.Now()); err != nil {
		t.Fatalf("재결제가 막혔다: %v", err)
	}
	if got := statusOf(t, s, orderNo); got != StatusPaid {
		t.Errorf("재결제 뒤 %s, want %s", got, StatusPaid)
	}
}

// 차액 결제도 같다: 승인이 아니면 교환품을 보내지 않는다.
func TestExchangeDiffRequiresApproval(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, returnNo, amount := exchangeAwaitingDiff(t, s, pool, "tee-va")

	err := s.ConfirmExchangeDiff(ctx, pendingGateway(""), "toss", orderNo, returnNo, "", "pk-va", amount, time.Now())
	if !errors.Is(err, ErrDepositPending) {
		t.Fatalf("입금 전 차액 승인 = %v, want ErrDepositPending", err)
	}
	if got := statusOf(t, s, orderNo); got == StatusExchangeShipped {
		t.Errorf("차액을 받지 않았는데 교환발송이다")
	}
	var retStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM returns WHERE return_no = $1`, returnNo).Scan(&retStatus); err != nil {
		t.Fatal(err)
	}
	if retStatus != string(StatusExchangeDiffDue) {
		t.Errorf("반품 상태 %s, want %s", retStatus, StatusExchangeDiffDue)
	}
}

// 응답 금액도 대조한다 (ConfirmPayment 와 같은 검사).
func TestExchangeDiffResponseAmountIsVerified(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, returnNo, amount := exchangeAwaitingDiff(t, s, pool, "tee-amt")

	gw := okGateway()
	gw.response.Amount = amount + 1
	err := s.ConfirmExchangeDiff(ctx, gw, "toss", orderNo, returnNo, "", "pk-amt", amount, time.Now())
	if !errors.Is(err, ErrAmountMismatch) {
		t.Fatalf("응답 금액이 다른데 = %v, want ErrAmountMismatch", err)
	}
	if got := statusOf(t, s, orderNo); got == StatusExchangeShipped {
		t.Errorf("금액이 다른데 교환발송이다")
	}
}

// 교환 대상은 노출 중인 조합만이다 (D19 P-512: 미노출 조합은 404).
func TestExchangeRefusesHiddenVariant(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, _ := deliveredOrder(t, s, pool, "tee-hid", 1)
	items := itemsOf(t, s, orderNo)
	var productID string
	if err := pool.QueryRow(ctx,
		`SELECT product_id FROM order_items WHERE id = $1`, items[0].ID).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	var hidden string
	if err := pool.QueryRow(ctx, `
		INSERT INTO product_variants (product_id,option_values,price_delta,stock,is_visible)
		VALUES ($1,'{"크기":"XL"}',-3000,5,false) RETURNING id`, productID).Scan(&hidden); err != nil {
		t.Fatal(err)
	}
	_, err := s.OpenReturn(ctx, orderNo, ReturnRequest{
		Kind: KindExchange, NewVariantID: hidden,
		Lines: []RefundLine{{OrderItemID: items[0].ID, Quantity: 1}},
	}, "P-512", time.Now())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("숨긴 조합으로 교환 = %v, want ErrNotFound", err)
	}
	assertStock(t, pool, hidden, 5)
}

// **우리 결제와 맞는 알림만 기록된다** (P-905 「출처 검증 실패」). 이 주소는 공개이고
// 토스는 결제 상태 알림에 서명하지 않으므로, 형태만 보고 기록하면 아무나 주문번호와
// 결제키를 지어내 webhook_events 를 요청마다 64KiB 씩 채울 수 있었다.
func TestOnlyOurPaymentsAreRecorded(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, total := seedOrder(t, s, pool, "va", 1)
	depositOrder(t, s, pool, orderNo, total, "s3cret")
	unpaid, _ := seedOrder(t, s, pool, "card", 1)
	// 카드 결제는 secret 이 없다 — 결제키 하나가 알림을 그 결제에 묶는다.
	card, cardTotal := seedOrder(t, s, pool, "card2", 1)
	if _, err := s.ConfirmPayment(ctx, okGateway(), "toss", card, "pk-1", cardTotal, time.Now()); err != nil {
		t.Fatal(err)
	}

	ev := func(order, key, secret string) *WebhookEvent {
		return &WebhookEvent{EventID: "E:" + order + ":" + key + ":" + secret, OrderNo: order,
			PaymentKey: key, Secret: secret, Raw: []byte(`{}`)}
	}
	for _, tc := range []struct {
		name string
		ev   *WebhookEvent
		ok   bool
	}{
		{"없는 주문", ev("20260101-NOPE", "pk-va", "s3cret"), false},
		{"결제가 없는 주문", ev(unpaid, "pk-va", "s3cret"), false},
		{"다른 결제키", ev(orderNo, "pk-other", "s3cret"), false},
		{"다른 secret", ev(orderNo, "pk-va", "s3cret-x"), false},
		{"결제키도 secret 도 없음", ev(orderNo, "", ""), false},
		{"secret 만 빠짐 (가상계좌)", ev(orderNo, "pk-va", ""), false},
		{"카드: 결제키도 secret 도 없음", ev(card, "", ""), false},
		{"카드: 우리에게 없는 secret", ev(card, "pk-1", "남의"), false},
		{"결제 상태 알림", ev(orderNo, "pk-va", "s3cret"), true},
		{"입금 알림 (결제키 없음)", ev(orderNo, "", "s3cret"), true},
		{"카드 결제 상태 알림 (secret 없음)", ev(card, "pk-1", ""), true},
	} {
		_, err := s.RecordWebhook(ctx, "toss", tc.ev)
		if tc.ok && err != nil || !tc.ok && !errors.Is(err, ErrWebhookUnmatched) {
			t.Errorf("%s: err = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM webhook_events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("webhook_events %d행, want 3 — 맞지 않는 알림이 기록됐다", n)
	}
}
