package commerce

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeGateway records what it was asked and answers what the test wants.
type fakeGateway struct {
	mu       sync.Mutex
	calls    int
	lastReq  ConfirmRequest
	response *Payment
	err      error
	// onConfirm 은 승인 API 왕복 중에 일어나는 일을 흉내낸다.
	onConfirm func()
}

func (g *fakeGateway) Confirm(_ context.Context, req ConfirmRequest) (*Payment, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	g.lastReq = req
	if g.onConfirm != nil {
		g.onConfirm()
	}
	if g.err != nil {
		return nil, g.err
	}
	res := *g.response
	if res.Amount == 0 {
		res.Amount = req.Amount
	}
	return &res, nil
}
func (g *fakeGateway) Cancel(context.Context, CancelRequest) (*Payment, error) { return nil, nil }
func (g *fakeGateway) Get(context.Context, string) (*Payment, error)           { return nil, nil }
func (g *fakeGateway) VerifyWebhook(context.Context, []byte) (*WebhookEvent, error) {
	return nil, nil
}
func (g *fakeGateway) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

func okGateway() *fakeGateway {
	return &fakeGateway{response: &Payment{
		PaymentKey: "pk-1", Status: PaymentApproved,
		Raw: []byte(`{"paymentKey":"pk-1","status":"DONE","totalAmount":29000}`),
	}}
}

// seedOrder makes a paid-pending order and returns (orderNo, total).
func seedOrder(t *testing.T, s *Store, pool *pgxpool.Pool, slug string, qty int) (string, int) {
	t.Helper()
	ctx := context.Background()
	_, variant := seedProduct(t, pool, slug, 12000, 1000, 10)
	owner := CartOwner{GuestKey: "guest-" + slug + "-0123456789"}
	if err := s.AddToCart(ctx, owner, variant, qty); err != nil {
		t.Fatal(err)
	}
	order, err := s.CreateOrder(ctx, owner, "", testForm(), testShipping, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return order.OrderNo, order.Total
}

// FR-607: 금액이 다르면 **승인 API 를 호출하지 않는다.** 호출한 뒤에 대조하면
// 돈은 이미 나갔고, 되돌리는 것은 취소 API 이지 검증이 아니다.
func TestAmountMismatchNeverReachesTheGateway(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, total := seedOrder(t, s, pool, "tee", 2)
	gw := okGateway()

	_, err := s.ConfirmPayment(ctx, gw, "toss", orderNo, "pk-1", total-1, time.Now())
	if !errors.Is(err, ErrAmountMismatch) {
		t.Fatalf("= %v, want ErrAmountMismatch", err)
	}
	if gw.count() != 0 {
		t.Errorf("승인 API 를 %d번 불렀다 — 대조는 호출보다 앞이어야 한다", gw.count())
	}
	// payments 행도 남지 않았다.
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM payments`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("결제 행이 %d건 남았다", n)
	}
	// 금액이 맞으면 부른다 — 위 단언이 "아무것도 안 한다" 를 확인한 것이
	// 아니라는 것.
	if _, err := s.ConfirmPayment(ctx, gw, "toss", orderNo, "pk-1", total, time.Now()); err != nil {
		t.Fatal(err)
	}
	if gw.count() != 1 {
		t.Errorf("정상 승인에 호출 %d번", gw.count())
	}
}

// D50 의 10분 창. 넘으면 호출하지 않고 주문을 결제대기에 둔다.
func TestExpiredAuthWindowNeverReachesTheGateway(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, total := seedOrder(t, s, pool, "tee", 2)
	gw := okGateway()

	late := time.Now().Add(AuthWindow + time.Minute)
	if _, err := s.ConfirmPayment(ctx, gw, "toss", orderNo, "pk-1", total, late); !errors.Is(err, ErrAuthWindowClosed) {
		t.Fatalf("= %v, want ErrAuthWindowClosed", err)
	}
	if gw.count() != 0 {
		t.Errorf("만료인데 승인 API 를 %d번 불렀다", gw.count())
	}
	// 주문은 결제대기에 머문다 — 재시도 경로를 남긴다 (P-409, D14 5-1).
	assertOrderStatus(t, pool, orderNo, StatusPaymentPending)

	// 창 안이면 통과한다 — 경계가 상수로 굳어 있지 않다는 것.
	if _, err := s.ConfirmPayment(ctx, gw, "toss", orderNo, "pk-1", total,
		time.Now().Add(AuthWindow-time.Minute)); err != nil {
		t.Errorf("창 안 승인이 막혔다: %v", err)
	}
}

func assertOrderStatus(t *testing.T, pool *pgxpool.Pool, orderNo string, want Status) {
	t.Helper()
	var got string
	if err := pool.QueryRow(context.Background(),
		`SELECT status FROM orders WHERE order_no = $1`, orderNo).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("주문 상태 %q, want %q", got, want)
	}
}

// FR-608: 멱등성은 DB 유니크가 막는다. 애플리케이션 검사가 아니다.
func TestSecondConfirmIsRefusedByTheDatabase(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, total := seedOrder(t, s, pool, "tee", 2)
	gw := okGateway()

	if _, err := s.ConfirmPayment(ctx, gw, "toss", orderNo, "pk-1", total, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmPayment(ctx, gw, "toss", orderNo, "pk-2", total, time.Now()); !errors.Is(err, ErrAlreadyPaid) {
		t.Fatalf("두 번째 승인 = %v, want ErrAlreadyPaid", err)
	}
	// 두 번째는 PG 에 닿지도 않았다.
	if gw.count() != 1 {
		t.Errorf("승인 API 호출 %d번, want 1", gw.count())
	}
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM payments WHERE status <> '실패'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("살아 있는 결제 %d건, want 1", n)
	}
}

// 동시 승인 두 건 중 하나만 산다. 애플리케이션이 "이미 승인됐나?" 를 먼저
// 읽는 방식은 두 요청이 같은 답을 보고 둘 다 진행한다.
func TestConcurrentConfirmsLeaveOnePayment(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()

	const rounds = 15
	for r := 0; r < rounds; r++ {
		orderNo, total := seedOrder(t, s, pool, "race-pay-"+itoa(r), 1)
		gw := okGateway()

		var wg sync.WaitGroup
		errs := make([]error, 2)
		gate := make(chan struct{})
		for i := range errs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-gate
				_, errs[i] = s.ConfirmPayment(ctx, gw, "toss", orderNo, "pk-"+itoa(r)+"-"+itoa(i), total, time.Now())
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
			if !errors.Is(err, ErrAlreadyPaid) {
				t.Fatalf("라운드 %d: 진 쪽이 %v, want ErrAlreadyPaid", r, err)
			}
		}
		if ok != 1 {
			t.Fatalf("라운드 %d: 승인 %d건 성공, want 1 (%v)", r, ok, errs)
		}
		if gw.count() != 1 {
			t.Fatalf("라운드 %d: PG 호출 %d번 — 진 쪽이 PG 에 닿았다", r, gw.count())
		}
	}
}

// 확정된 실패는 '실패' 로 내려 재결제 경로를 연다. 결과 불명은 '대기' 로
// 남겨 A-508 대사 대상이 된다 (D50).
func TestFailureAndUnknownAreRecordedDifferently(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()

	// 확정 실패.
	orderNo, total := seedOrder(t, s, pool, "reject", 1)
	rejected := okGateway()
	rejected.err = errors.New("commerce: 결제 요청이 거부되었습니다 (HTTP 400, INVALID_CARD)")
	if _, err := s.ConfirmPayment(ctx, rejected, "toss", orderNo, "pk-1", total, time.Now()); err == nil {
		t.Fatal("거부인데 오류가 없다")
	}
	assertPaymentStatus(t, pool, orderNo, "실패")
	assertOrderStatus(t, pool, orderNo, StatusPaymentPending)

	// 실패 뒤 재결제가 된다 — 부분 유니크의 `status <> '실패'` 가 여는 길이다.
	good := okGateway()
	if _, err := s.ConfirmPayment(ctx, good, "toss", orderNo, "pk-2", total, time.Now()); err != nil {
		t.Errorf("재결제가 막혔다: %v", err)
	}
	assertOrderStatus(t, pool, orderNo, StatusPaid)

	// 결과 불명.
	orderNo2, total2 := seedOrder(t, s, pool, "unknown", 1)
	unknown := okGateway()
	unknown.err = ErrPaymentUnknown
	if _, err := s.ConfirmPayment(ctx, unknown, "toss", orderNo2, "pk-3", total2, time.Now()); !errors.Is(err, ErrPaymentUnknown) {
		t.Fatalf("= %v, want ErrPaymentUnknown", err)
	}
	// '대기' 로 남는다. '실패' 로 내리면 재결제가 열려 이중 승인이 된다 —
	// 실제로는 PG 쪽에서 승인됐을 수 있다.
	assertPaymentStatus(t, pool, orderNo2, "대기")
}

func assertPaymentStatus(t *testing.T, pool *pgxpool.Pool, orderNo, want string) {
	t.Helper()
	var got string
	if err := pool.QueryRow(context.Background(), `
		SELECT status FROM payments
		WHERE order_id = (SELECT id FROM orders WHERE order_no = $1)
		ORDER BY created_at DESC LIMIT 1`, orderNo).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("결제 상태 %q, want %q", got, want)
	}
}

// 승인 응답 원문을 보관하되 카드 정보는 지운다 (DEC-3.7, PCI DSS).
func TestRawResponseIsStoredAndMasked(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, total := seedOrder(t, s, pool, "tee", 2)

	gw := okGateway()
	gw.response.Raw = []byte(`{
		"paymentKey":"pk-1","status":"DONE",
		"card":{"number":"4111-1111-1111-1111","cvc":"123","expiryDate":"12/29"},
		"receipt":{"url":"https://example.com/r/1"},
		"message":"승인 4111111111111111 완료"}`)

	if _, err := s.ConfirmPayment(ctx, gw, "toss", orderNo, "pk-1", total, time.Now()); err != nil {
		t.Fatal(err)
	}

	var raw string
	if err := pool.QueryRow(ctx, `
		SELECT raw_response::text FROM payments
		WHERE order_id = (SELECT id FROM orders WHERE order_no = $1)`, orderNo).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"4111-1111-1111-1111", "4111111111111111", "123", "12/29"} {
		if strings.Contains(raw, secret) {
			t.Errorf("보관된 응답에 %q 가 남았다: %s", secret, raw)
		}
	}
	// 대조에 쓸 것은 남는다 — 전부 지우면 사후 대조의 근거가 사라진다 (D50).
	for _, keep := range []string{"pk-1", "DONE", "https://example.com/r/1"} {
		if !strings.Contains(raw, keep) {
			t.Errorf("보관된 응답에 %q 가 없다: %s", keep, raw)
		}
	}
}

func TestMaskCardFields(t *testing.T) {
	cases := []struct {
		why  string
		in   string
		gone []string
		kept []string
	}{
		{"키 이름으로 지운다 — 짧은 값은 모양으로 못 잡는다",
			`{"cardNumber":"1234","status":"DONE"}`, []string{"1234"}, []string{"DONE"}},
		{"중첩된 곳도 본다",
			`{"card":{"number":"4111111111111111"}}`, []string{"4111111111111111"}, nil},
		{"배열 안도 본다",
			`{"items":[{"cvc":"999"}]}`, []string{"999"}, nil},
		{"문자열 안에 섞여 있어도 지운다",
			`{"m":"승인 4111 1111 1111 1111 완료"}`, []string{"4111 1111 1111 1111"}, []string{"승인", "완료"}},
		{"금액 같은 짧은 숫자는 남긴다",
			`{"totalAmount":29000}`, nil, []string{"29000"}},
	}
	for _, c := range cases {
		got := string(MaskCardFields([]byte(c.in)))
		for _, g := range c.gone {
			if strings.Contains(got, g) {
				t.Errorf("%s: %q 가 남았다 — %s", c.why, g, got)
			}
		}
		for _, k := range c.kept {
			if !strings.Contains(got, k) {
				t.Errorf("%s: %q 가 사라졌다 — %s", c.why, k, got)
			}
		}
	}

	// 파싱 못 하는 것은 통째로 버린다. 모르는 모양을 그대로 저장하는 것이
	// 마스킹 실패의 가장 흔한 형태다.
	got := string(MaskCardFields([]byte(`카드번호 4111111111111111 입니다`)))
	if strings.Contains(got, "4111111111111111") {
		t.Errorf("파싱 실패 원문이 그대로 저장된다: %s", got)
	}
	if len(MaskCardFields(nil)) != 0 {
		t.Error("빈 입력이 무언가로 바뀌었다")
	}
}

// 상태 전이는 상태머신을 거친다. 표에 없는 전이는 승인 뒤에도 거부된다.
func TestOrderStatusMovesThroughTheStateMachine(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, total := seedOrder(t, s, pool, "tee", 2)

	// 주문을 배송중으로 옮겨 둔다. 배송중 → 결제완료는 표에 없다.
	if _, err := pool.Exec(ctx,
		`UPDATE orders SET status = '배송중' WHERE order_no = $1`, orderNo); err != nil {
		t.Fatal(err)
	}
	gw := okGateway()
	if _, err := s.ConfirmPayment(ctx, gw, "toss", orderNo, "pk-1", total, time.Now()); !errors.Is(err, ErrTransitionNotAllowed) {
		t.Fatalf("배송중에서 승인 = %v, want ErrTransitionNotAllowed", err)
	}
	// 상태가 바뀌지 않았다.
	assertOrderStatus(t, pool, orderNo, StatusShipping)
}

// 멱등키는 결제 건 ID 다. 재시도해도 같은 값이라 PG 가 첫 결과를 돌려준다.
func TestIdempotencyKeyIsStableForTheSamePayment(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, total := seedOrder(t, s, pool, "tee", 2)
	gw := okGateway()

	if _, err := s.ConfirmPayment(ctx, gw, "toss", orderNo, "pk-1", total, time.Now()); err != nil {
		t.Fatal(err)
	}
	var paymentID string
	if err := pool.QueryRow(ctx, `
		SELECT id::text FROM payments
		WHERE order_id = (SELECT id FROM orders WHERE order_no = $1)`, orderNo).Scan(&paymentID); err != nil {
		t.Fatal(err)
	}
	if gw.lastReq.IdempotencyKey != paymentID {
		t.Errorf("멱등키 %q, want 결제 건 ID %q", gw.lastReq.IdempotencyKey, paymentID)
	}
	if gw.lastReq.Amount != total {
		t.Errorf("승인 요청 금액 %d, want 저장된 %d", gw.lastReq.Amount, total)
	}
}

// PG 가 요청과 다른 금액을 확정하면 기록하지 않는다. 기록만 하고 넘어가면
// 그때부터 우리 장부와 PG 장부가 다르다.
func TestGatewayReturningADifferentAmountIsRefused(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, total := seedOrder(t, s, pool, "tee", 2)

	gw := okGateway()
	gw.response.Amount = total - 500
	if _, err := s.ConfirmPayment(ctx, gw, "toss", orderNo, "pk-1", total, time.Now()); !errors.Is(err, ErrAmountMismatch) {
		t.Fatalf("= %v, want ErrAmountMismatch", err)
	}
	assertOrderStatus(t, pool, orderNo, StatusPaymentPending)
}

// 같은 승인 키가 이미 기록돼 있는 것과 "이미 결제된 주문" 은 다르다. 한
// 오류로 접으면 사람이 엉뚱한 주문을 들여다본다.
func TestReusedPaymentKeyIsNotTheSameAsAlreadyPaid(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	first, firstTotal := seedOrder(t, s, pool, "one", 1)
	second, secondTotal := seedOrder(t, s, pool, "two", 1)
	gw := okGateway()

	if _, err := s.ConfirmPayment(ctx, gw, "toss", first, "shared-key", firstTotal, time.Now()); err != nil {
		t.Fatal(err)
	}
	// 다른 주문에 같은 승인 키.
	_, err := s.ConfirmPayment(ctx, gw, "toss", second, "shared-key", secondTotal, time.Now())
	if !errors.Is(err, ErrPaymentKeyReused) {
		t.Errorf("= %v, want ErrPaymentKeyReused", err)
	}
	if errors.Is(err, ErrAlreadyPaid) {
		t.Error("승인 키 중복이 '이미 결제됨' 으로 접혔다")
	}
	// 같은 주문에 다른 키는 '이미 결제됨' 이다.
	_, err = s.ConfirmPayment(ctx, gw, "toss", first, "other-key", firstTotal, time.Now())
	if !errors.Is(err, ErrAlreadyPaid) {
		t.Errorf("= %v, want ErrAlreadyPaid", err)
	}
}

// 승인 API 왕복 중에 주문이 취소되면 결제완료로 되돌리지 않는다.
//
// 상태는 왕복 **전에** 읽은 값이다. 조건 없는 UPDATE 는 취소된 주문을
// 결제완료로 만들고, 그것은 역전이 금지(D14)를 코드가 스스로 어기는 경로다.
func TestOrderChangedDuringApprovalIsNotOverwritten(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, total := seedOrder(t, s, pool, "tee", 2)

	gw := okGateway()
	gw.onConfirm = func() {
		if _, err := pool.Exec(ctx,
			`UPDATE orders SET status = '취소' WHERE order_no = $1`, orderNo); err != nil {
			t.Error(err)
		}
	}

	_, err := s.ConfirmPayment(ctx, gw, "toss", orderNo, "pk-1", total, time.Now())
	if !errors.Is(err, ErrTransitionNotAllowed) {
		t.Fatalf("= %v, want ErrTransitionNotAllowed", err)
	}
	assertOrderStatus(t, pool, orderNo, StatusCancelled)
}
