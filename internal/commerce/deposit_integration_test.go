package commerce

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
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

	gw.getResponse = &Payment{PaymentKey: "pk-va", OrderNo: orderNo, Status: PaymentApproved, Amount: total,
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

// **조회 응답이 다른 주문의 것이면 옮기지 않는다.** 승인·결과 불명 분기는 조회한
// orderId 를 대조했는데 웹훅 분기만 빠져 있었다 (2026-10-21 점검). 지금 저장된 키가
// 그 주문에 묶여 있어 악용 길은 없지만, 진실의 출처를 대조 없이 믿지 않는다.
func TestDepositWebhookChecksTheLookedUpOrder(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, total := seedOrder(t, s, pool, "va", 1)
	depositOrder(t, s, pool, orderNo, total, "s3cret")

	gw := pendingGateway("s3cret")
	gw.getResponse = &Payment{PaymentKey: "pk-va", OrderNo: "SOMEONE-ELSE", Status: PaymentApproved,
		Amount: total, Raw: []byte(`{"status":"DONE"}`)}
	if err := s.ProcessWebhook(ctx, gw, webhook(t, s, orderNo, "s3cret", total), &WebhookEvent{
		OrderNo: orderNo, PaymentKey: "pk-va", Secret: "s3cret", Amount: total}); err == nil {
		t.Error("다른 주문의 조회 응답으로 처리가 성공했다")
	}
	if got := statusOf(t, s, orderNo); got != StatusDepositPending {
		t.Errorf("다른 주문의 DONE 으로 주문이 %s 가 됐다", got)
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

// vaGateway 는 입금 기한이 due 인 가상계좌를 발급한다. 기한은 토스 문서의 모양
// (`yyyy-MM-dd'T'HH:mm:ss`, 오프셋 없음 — 한국 시각)으로 싣는다.
func vaGateway(secret string, due time.Time) *fakeGateway {
	gw := pendingGateway(secret)
	gw.response.Raw = []byte(`{"paymentKey":"pk-va","status":"WAITING_FOR_DEPOSIT","virtualAccount":{"dueDate":"` +
		due.In(time.FixedZone("KST", 9*3600)).Format("2006-01-02T15:04:05") + `"}}`)
	return gw
}

func stockOfOrder(t *testing.T, pool *pgxpool.Pool, orderNo string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT v.stock FROM product_variants v
		JOIN order_items oi ON oi.variant_id = v.id JOIN orders o ON o.id = oi.order_id
		WHERE o.order_no = $1`, orderNo).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// **입금 기한이 지난 가상계좌 주문은 결제실패가 되고 재고를 놓는다** (D14: 입금대기 →
// 결제실패, 시스템 「입금 기한 초과」). 표에만 있고 실행하는 코드가 없어, 입금하지 않는
// 가상계좌 주문이 재고를 영원히 잡았다 — 돈 없이 재고를 잠그는 길이었다 (2026-10-21 재점검).
func TestDepositOrderExpiresAfterItsDueDate(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, total := seedOrder(t, s, pool, "va-exp", 1)
	held := stockOfOrder(t, pool, orderNo)
	due := time.Now().Add(time.Hour).Truncate(time.Second)
	if _, err := s.ConfirmPayment(ctx, vaGateway("s3cret", due), "toss", orderNo, "pk-va", total, time.Now()); !errors.Is(err, ErrDepositPending) {
		t.Fatalf("가상계좌 발급 = %v", err)
	}
	gw := &fakeGateway{getResponse: &Payment{PaymentKey: "pk-va", OrderNo: orderNo, Status: PaymentPending, Amount: total}}

	// 기한 전에는 PG 에 묻지도 않는다.
	if n, err := s.ExpireDepositOrders(ctx, gw, due.Add(-time.Minute), 10); err != nil || n != 0 {
		t.Fatalf("기한 전 만료 = %d, %v", n, err)
	}
	if gw.getCalls != 0 {
		t.Errorf("기한 전에 PG 를 %d번 불렀다", gw.getCalls)
	}
	if got := statusOf(t, s, orderNo); got != StatusDepositPending {
		t.Fatalf("기한 전 주문 %s", got)
	}

	// 기한 + 유예가 지나고 PG 도 입금 전이라고 하면 놓는다.
	if n, err := s.ExpireDepositOrders(ctx, gw, due.Add(DepositGrace+time.Minute), 10); err != nil || n != 1 {
		t.Fatalf("기한 뒤 만료 = %d, %v", n, err)
	}
	if got := statusOf(t, s, orderNo); got != StatusPaymentFailed {
		t.Errorf("기한 뒤 주문 %s, want %s", got, StatusPaymentFailed)
	}
	if got := stockOfOrder(t, pool, orderNo); got != held+1 {
		t.Errorf("재고 %d, want %d — 놓지 않았다", got, held+1)
	}
	var pay string
	if err := pool.QueryRow(ctx, `SELECT status FROM payments WHERE kind = '주문결제'`).Scan(&pay); err != nil {
		t.Fatal(err)
	}
	if pay != string(PaymentFailed) {
		t.Errorf("결제 %s, want %s", pay, PaymentFailed)
	}
}

// 기한이 지났어도 PG 가 입금됐다(DONE)고 하면 놓지 않는다 — 결제완료는 웹훅·A-508 이 옮긴다.
func TestDepositOrderPaidAtTheGatewayIsNotExpired(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, total := seedOrder(t, s, pool, "va-paid", 1)
	due := time.Now().Add(-time.Hour).Truncate(time.Second)
	if _, err := s.ConfirmPayment(ctx, vaGateway("s3cret", due), "toss", orderNo, "pk-va", total, time.Now()); !errors.Is(err, ErrDepositPending) {
		t.Fatalf("가상계좌 발급 = %v", err)
	}
	gw := &fakeGateway{getResponse: &Payment{PaymentKey: "pk-va", OrderNo: orderNo, Status: PaymentApproved, Amount: total}}
	if n, err := s.ExpireDepositOrders(ctx, gw, time.Now(), 10); err != nil || n != 0 {
		t.Fatalf("입금된 주문 만료 = %d, %v", n, err)
	}
	if got := statusOf(t, s, orderNo); got != StatusDepositPending {
		t.Errorf("입금된 주문이 %s 가 됐다", got)
	}
}

// A-508 이 입금대기 주문의 결제를 PG 의 실패로 닫으면 주문과 재고도 놓는다. 결제 행만
// '실패' 로 내리면 그 주문은 어느 만료에도 걸리지 않고 재고를 잡은 채 남았다.
func TestResolvingAFailedDepositReleasesTheOrder(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, total := seedOrder(t, s, pool, "va-fail", 1)
	held := stockOfOrder(t, pool, orderNo)
	depositOrder(t, s, pool, orderNo, total, "s3cret")
	var paymentID string
	if err := pool.QueryRow(ctx, `SELECT id FROM payments WHERE kind = '주문결제'`).Scan(&paymentID); err != nil {
		t.Fatal(err)
	}
	gw := &fakeGateway{getResponse: &Payment{PaymentKey: "pk-va", OrderNo: orderNo, Status: PaymentFailed,
		Raw: []byte(`{"status":"EXPIRED"}`)}}
	if _, err := s.ResolvePendingPayment(ctx, gw, paymentID); err != nil {
		t.Fatalf("대사 반영 = %v", err)
	}
	if got := statusOf(t, s, orderNo); got != StatusPaymentFailed {
		t.Errorf("주문 %s, want %s", got, StatusPaymentFailed)
	}
	if got := stockOfOrder(t, pool, orderNo); got != held+1 {
		t.Errorf("재고 %d, want %d", got, held+1)
	}
}

// **A-508 반영은 주문 → 결제 순서로 잠근다** (FR-609). 입금 만료(expireDeposit)와
// 입금 웹훅(P-905)이 그 순서인데, 반영이 결제를 먼저 쥐고 주문을 기다리면 둘이 서로를
// 기다린다 — PostgreSQL 이 한쪽을 교착으로 끊고, 반영이면 운영자는 「결제사 조회에
// 실패했습니다」(502)를 본다. 주문이 잠긴 동안 결제 행이 비어 있어야 한다.
func TestResolveLocksTheOrderBeforeThePayment(t *testing.T) {
	for _, tc := range []struct {
		name string
		pg   PaymentStatus
		want Status
	}{
		{"승인", PaymentApproved, StatusPaid},
		{"실패", PaymentFailed, StatusPaymentFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, pool := testStore(t)
			ctx := context.Background()
			orderNo, total := seedOrder(t, s, pool, "va-lock", 1)
			depositOrder(t, s, pool, orderNo, total, "s3cret")
			var paymentID string
			if err := pool.QueryRow(ctx, `SELECT id FROM payments WHERE kind = '주문결제'`).Scan(&paymentID); err != nil {
				t.Fatal(err)
			}
			gw := &fakeGateway{getResponse: &Payment{PaymentKey: "pk-va", OrderNo: orderNo, Status: tc.pg,
				Amount: total, Raw: []byte(`{}`)}}
			whileWaitingOn(t, pool,
				`SELECT 1 FROM orders WHERE order_no = '`+orderNo+`' FOR UPDATE`,
				`SELECT 1 FROM payments WHERE id = '`+paymentID+`' FOR UPDATE NOWAIT`,
				func() error {
					_, err := s.ResolvePendingPayment(ctx, gw, paymentID)
					return err
				})
			if got := statusOf(t, s, orderNo); got != tc.want {
				t.Errorf("주문 %s, want %s", got, tc.want)
			}
		})
	}
}

// keyedGateway 는 결제키마다 다르게 답한다 — fail 의 키는 조회가 실패한다.
type keyedGateway struct {
	*fakeGateway
	fail string
	resp map[string]*Payment
}

func (g keyedGateway) Get(_ context.Context, key string) (*Payment, error) {
	if key == g.fail {
		return nil, errors.New("조회 실패")
	}
	return g.resp[key], nil
}

// **닫지 못한 앞줄이 뒤의 만료를 막지 않는다.** 오래된 순 몇 건만 보는데, 조회가 늘 실패하는
// 주문(키를 바꾼 뒤의 옛 결제키 등)이 앞에 쌓이면 그 뒤 가상계좌는 영영 만료되지 않았다
// (2026-10-21 3차 점검). 확인하고 닫지 못한 것은 뒤로 간다.
func TestDepositExpiryIsNotBlockedByAnUnanswerableHead(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	due := time.Now().Add(-time.Hour).Truncate(time.Second)
	head, totalHead := seedOrder(t, s, pool, "va-head", 1)
	next, totalNext := seedOrder(t, s, pool, "va-next", 1)
	for _, o := range []struct {
		no, key string
		total   int
	}{{head, "pk-head", totalHead}, {next, "pk-next", totalNext}} {
		gw := vaGateway("s3cret", due)
		gw.response.PaymentKey = o.key
		if _, err := s.ConfirmPayment(ctx, gw, "toss", o.no, o.key, o.total, time.Now()); !errors.Is(err, ErrDepositPending) {
			t.Fatalf("가상계좌 발급 %s = %v", o.no, err)
		}
	}
	gw := keyedGateway{fakeGateway: &fakeGateway{}, fail: "pk-head",
		resp: map[string]*Payment{"pk-next": {PaymentKey: "pk-next", OrderNo: next, Status: PaymentPending}}}
	// 한 번에 한 건씩. 첫 회는 앞줄(조회 실패)에서 멈춘다.
	if _, err := s.ExpireDepositOrders(ctx, gw, time.Now(), 1); err == nil {
		t.Fatal("조회 실패가 오류로 올라오지 않았다")
	}
	// 둘째 회는 그 뒤의 것을 닫는다.
	if n, err := s.ExpireDepositOrders(ctx, gw, time.Now(), 1); err != nil || n != 1 {
		t.Fatalf("둘째 회 = %d, %v — 앞줄이 막고 있다", n, err)
	}
	if got := statusOf(t, s, next); got != StatusPaymentFailed {
		t.Errorf("뒤의 주문 %s, want %s", got, StatusPaymentFailed)
	}
	if got := statusOf(t, s, head); got != StatusDepositPending {
		t.Errorf("조회가 실패한 주문이 %s 가 됐다 — 근거 없이 닫았다", got)
	}
}

// **결제가 이미 닫혔는데 입금대기에 남은 주문도 놓는다.** 이 개정 전의 A-508 은 입금대기 주문의
// 결제를 '실패' 로만 내렸다 — 그 주문은 어느 만료도 집지 않아 재고를 잡은 채 남아 있다.
func TestStrandedDepositOrderIsReleased(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, total := seedOrder(t, s, pool, "va-stranded", 1)
	held := stockOfOrder(t, pool, orderNo)
	depositOrder(t, s, pool, orderNo, total, "s3cret")
	if _, err := pool.Exec(ctx, `UPDATE payments SET status = '실패' WHERE kind = '주문결제'`); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ExpireDepositOrders(ctx, &fakeGateway{}, time.Now(), 10); err != nil || n != 1 {
		t.Fatalf("고아 입금대기 정리 = %d, %v", n, err)
	}
	if got := statusOf(t, s, orderNo); got != StatusPaymentFailed {
		t.Errorf("주문 %s, want %s", got, StatusPaymentFailed)
	}
	if got := stockOfOrder(t, pool, orderNo); got != held+1 {
		t.Errorf("재고 %d, want %d", got, held+1)
	}
}

// 기한이 오프셋과 함께 와도(RFC 3339) 읽는다 — 문서의 모양과 실제 응답이 다를 수 있다.
func TestDepositDueDateWithAnOffsetIsRead(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, total := seedOrder(t, s, pool, "va-offset", 1)
	gw := pendingGateway("s3cret")
	gw.response.Raw = []byte(`{"virtualAccount":{"dueDate":"` + time.Now().Add(-time.Hour).Format(time.RFC3339) + `"}}`)
	if _, err := s.ConfirmPayment(ctx, gw, "toss", orderNo, "pk-va", total, time.Now()); !errors.Is(err, ErrDepositPending) {
		t.Fatalf("가상계좌 발급 = %v", err)
	}
	lookup := &fakeGateway{getResponse: &Payment{PaymentKey: "pk-va", OrderNo: orderNo, Status: PaymentPending}}
	if n, err := s.ExpireDepositOrders(ctx, lookup, time.Now(), 10); err != nil || n != 1 {
		t.Fatalf("오프셋이 있는 기한 = %d, %v", n, err)
	}
}
