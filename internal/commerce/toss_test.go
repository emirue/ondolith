package commerce

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testSecret = "test_sk_0123456789abcdef"

// **콜론을 빠뜨리면 인증이 실패한다.** base64 대상은 `시크릿키:` 이지
// `시크릿키` 가 아니다 — 공식 문서도 이것을 따로 경고한다 (D50).
//
// 값을 직접 단언한다. "콜론이 있다" 만 보면 `키:` 대신 `:키` 도 통과한다.
func TestBasicAuthEncodesSecretWithTrailingColon(t *testing.T) {
	tp := NewToss(testSecret, "https://example.invalid", time.Second)
	got := tp.authHeader()

	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(testSecret+":"))
	if got != want {
		t.Fatalf("인증 헤더 = %q, want %q", got, want)
	}

	// 디코드해서 원문을 확인한다 — 위 단언은 구현과 같은 식을 쓰므로, 식 자체가
	// 틀렸을 때 둘 다 같이 틀린다.
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(got, "Basic "))
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != testSecret+":" {
		t.Errorf("base64 대상 = %q, want %q", decoded, testSecret+":")
	}
	// UTF-8 BOM 이 섞이면 결과가 `77u/` 로 시작한다 (D50).
	if strings.HasPrefix(strings.TrimPrefix(got, "Basic "), "77u/") {
		t.Error("base64 값이 BOM 으로 시작한다")
	}
}

// 시크릿이 로그·오류 메시지에 없다. %v 한 번이면 나가므로, 구조체를 통째로
// 찍어 보고 확인한다.
func TestSecretDoesNotAppearInFormattedOutputOrErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"INVALID_REQUEST","message":"` + testSecret + `"}`))
	}))
	defer srv.Close()

	tp := NewToss(testSecret, srv.URL, time.Second)

	// PG 가 시크릿을 본문에 되돌려 보내도 오류 메시지에 실리지 않는다 — 응답
	// 본문을 메시지에 넣지 않기 때문이다.
	_, err := tp.Confirm(context.Background(), ConfirmRequest{
		PaymentKey: "pk", OrderNo: "20260805-AAAAAAAAAA", Amount: 1000, IdempotencyKey: "idem-1",
	})
	if err == nil {
		t.Fatal("400 인데 오류가 없다")
	}
	if strings.Contains(err.Error(), testSecret) {
		t.Errorf("오류 메시지에 시크릿이 있다: %q", err)
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		out := fmt.Sprintf(format, tp)
		if strings.Contains(out, testSecret) {
			t.Errorf("%s 로 찍으면 시크릿이 나온다: %q", format, out)
		}
	}
}

// 성공 경로. 요청이 D50 표대로 나가는지까지 본다 — 응답만 보면 우리가 무엇을
// 보냈는지는 아무도 확인하지 않는다.
func TestConfirmSendsWhatD50Specifies(t *testing.T) {
	var gotPath, gotAuth, gotIdem, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		gotIdem = r.Header.Get("Idempotency-Key")
		buf := make([]byte, 512)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		_, _ = w.Write([]byte(`{"paymentKey":"pk-1","orderId":"20260805-AAAAAAAAAA",
			"status":"DONE","totalAmount":26000,"secret":"wsk-1"}`))
	}))
	defer srv.Close()

	tp := NewToss(testSecret, srv.URL, time.Second)
	p, err := tp.Confirm(context.Background(), ConfirmRequest{
		PaymentKey: "pk-1", OrderNo: "20260805-AAAAAAAAAA", Amount: 26000,
		IdempotencyKey: "idem-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	if gotPath != "/v1/payments/confirm" {
		t.Errorf("경로 %q", gotPath)
	}
	if gotAuth != "Basic "+base64.StdEncoding.EncodeToString([]byte(testSecret+":")) {
		t.Errorf("인증 헤더 %q", gotAuth)
	}
	// 모든 POST 에 붙는다 (D50). 없으면 재시도가 두 번 승인된다.
	if gotIdem != "idem-1" {
		t.Errorf("Idempotency-Key = %q, want idem-1", gotIdem)
	}
	for _, want := range []string{`"paymentKey"`, `"orderId"`, `"amount"`} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("본문에 %s 가 없다: %s", want, gotBody)
		}
	}

	if p.Status != PaymentApproved || p.Amount != 26000 || p.Secret != "wsk-1" {
		t.Errorf("= %+v", p)
	}
	// 응답 원문을 통째로 보관한다 — 사후 대조의 유일한 근거다 (D50).
	if !strings.Contains(string(p.Raw), "pk-1") {
		t.Errorf("응답 원문이 비었다: %q", p.Raw)
	}
}

// 실패 경로. 만료는 재승인 금지 신호이므로 다른 실패와 구분돼야 한다.
func TestErrorPathsAreDistinguished(t *testing.T) {
	cases := []struct {
		code    string
		status  int
		wantErr error
	}{
		{"EXPIRED_PAYMENT", http.StatusBadRequest, ErrPaymentExpired},
		{"IDEMPOTENT_REQUEST_PROCESSING", http.StatusConflict, ErrPaymentUnknown},
		{"", http.StatusInternalServerError, ErrPaymentUnknown},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(`{"code":"` + c.code + `"}`))
		}))
		tp := NewToss(testSecret, srv.URL, time.Second)
		_, err := tp.Confirm(context.Background(), ConfirmRequest{PaymentKey: "pk", Amount: 1})
		if !errors.Is(err, c.wantErr) {
			t.Errorf("%s/%d = %v, want %v", c.code, c.status, err, c.wantErr)
		}
		srv.Close()
	}

	// 일반 거부는 만료도 불명도 아니다 — 만료로 접으면 재결제 안내가 나가고,
	// 불명으로 접으면 대사 목록이 거부 건으로 찬다.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"INVALID_CARD"}`))
	}))
	defer srv.Close()
	tp := NewToss(testSecret, srv.URL, time.Second)
	_, err := tp.Confirm(context.Background(), ConfirmRequest{PaymentKey: "pk", Amount: 1})
	if errors.Is(err, ErrPaymentExpired) || errors.Is(err, ErrPaymentUnknown) {
		t.Errorf("일반 거부 = %v — 만료·불명과 섞였다", err)
	}
	if err == nil {
		t.Error("일반 거부에 오류가 없다")
	}
}

// 타임아웃 경로. 결과 불명이므로 재승인하지 않는다 (D50 「10분 만료와 복구」).
func TestTimeoutIsUnknownNotFailure(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-block
	}))
	defer func() { close(block); srv.Close() }()

	tp := NewToss(testSecret, srv.URL, 50*time.Millisecond)
	_, err := tp.Confirm(context.Background(), ConfirmRequest{PaymentKey: "pk", Amount: 1})
	if !errors.Is(err, ErrPaymentUnknown) {
		t.Fatalf("타임아웃 = %v, want ErrPaymentUnknown", err)
	}
	// 원인 error 를 감싸면 URL 이 담기고, 그 URL 에는 paymentKey 가 들어 있다.
	if strings.Contains(err.Error(), srv.URL) {
		t.Errorf("오류에 URL 이 실렸다: %v", err)
	}
}

// 전액 취소는 cancelAmount 를 **생략**한다. 0 을 보내면 "0원 취소" 라는 다른
// 뜻이 된다.
func TestCancelOmitsAmountForFullCancel(t *testing.T) {
	var body, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		buf := make([]byte, 512)
		n, _ := r.Body.Read(buf)
		body = string(buf[:n])
		_, _ = w.Write([]byte(`{"paymentKey":"pk","status":"CANCELED","totalAmount":0}`))
	}))
	defer srv.Close()

	tp := NewToss(testSecret, srv.URL, time.Second)
	if _, err := tp.Cancel(context.Background(),
		CancelRequest{PaymentKey: "pk", Reason: "고객 요청"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "cancelAmount") {
		t.Errorf("전액 취소인데 cancelAmount 를 보냈다: %s", body)
	}
	if path != "/v1/payments/pk/cancel" {
		t.Errorf("경로 %q", path)
	}

	if _, err := tp.Cancel(context.Background(),
		CancelRequest{PaymentKey: "pk", Reason: "부분", Amount: 5000}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "cancelAmount") {
		t.Errorf("부분 취소인데 cancelAmount 가 없다: %s", body)
	}
}

// paymentKey 가 경로에 들어간다. 값의 출처를 신뢰하는 것과 형태를 신뢰하는
// 것은 다르고, `../` 하나가 다른 엔드포인트를 부른다.
func TestPaymentKeyIsEscapedInThePath(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{"status":"DONE"}`))
	}))
	defer srv.Close()

	tp := NewToss(testSecret, srv.URL, time.Second)
	if _, err := tp.Get(context.Background(), "../../v1/other"); err != nil {
		t.Fatal(err)
	}
	assertConfined(t, path)

	// 취소도 같은 경로 조립을 한다. 조회만 검사하면 한쪽만 고쳐진다.
	if _, err := tp.Cancel(context.Background(),
		CancelRequest{PaymentKey: "../../v1/other", Reason: "x"}); err != nil {
		t.Fatal(err)
	}
	assertConfined(t, path)
}

// assertConfined: 이스케이프된 `..` 자체는 해롭지 않다 — 한 세그먼트의
// 리터럴이다. 지켜야 하는 것은 "다른 엔드포인트에 닿지 않는다" 이다.
func assertConfined(t *testing.T, path string) {
	t.Helper()
	const prefix = "/v1/payments/"
	rest, ok := strings.CutPrefix(path, prefix)
	if !ok {
		t.Fatalf("경로가 %s 밖으로 나갔다: %q", prefix, path)
	}
	// 취소는 뒤에 /cancel 이 붙는다. 그 하나 말고 다른 구분자가 있으면 키가
	// 세그먼트를 넘은 것이다.
	rest = strings.TrimSuffix(rest, "/cancel")
	if strings.Contains(rest, "/") {
		t.Errorf("paymentKey 가 세그먼트를 넘었다: %q", path)
	}
}

// 모르는 상태는 '대기' 다. '실패' 로 접으면 승인된 결제를 실패로 기록해 물건이
// 나가지 않고, '승인' 으로 접으면 안 된 결제로 물건이 나간다.
func TestUnknownGatewayStatusFoldsToPending(t *testing.T) {
	cases := map[string]PaymentStatus{
		"DONE":                PaymentApproved,
		"CANCELED":            PaymentFailed,
		"PARTIAL_CANCELED":    PaymentFailed,
		"ABORTED":             PaymentFailed,
		"EXPIRED":             PaymentFailed,
		"IN_PROGRESS":         PaymentPending,
		"WAITING_FOR_DEPOSIT": PaymentPending,
		"":                    PaymentPending,
		"NEW_STATUS_2027":     PaymentPending,
	}
	for in, want := range cases {
		if got := tossStatus(in); got != want {
			t.Errorf("%q → %s, want %s", in, got, want)
		}
	}
}

// 웹훅은 서명이 없다 (D50, 2026-08-05 확인). 여기서 하는 것은 형태 검증까지고,
// secret 대조는 호출자가 한다.
func TestVerifyWebhookParsesAndBuildsAnEventID(t *testing.T) {
	tp := NewToss(testSecret, "https://example.invalid", time.Second)
	ctx := context.Background()

	ev, err := tp.VerifyWebhook(ctx, []byte(
		`{"eventType":"DEPOSIT_CALLBACK","data":{"paymentKey":"pk-1","orderId":"o-1","secret":"wsk"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != "DEPOSIT_CALLBACK" || ev.OrderNo != "o-1" || ev.Secret != "wsk" {
		t.Errorf("= %+v", ev)
	}
	// 같은 이벤트는 같은 ID 다 — 재전송 멱등의 키다 (FR-610).
	again, _ := tp.VerifyWebhook(ctx, []byte(
		`{"eventType":"DEPOSIT_CALLBACK","data":{"paymentKey":"pk-1","orderId":"o-1","secret":"다름"}}`))
	if ev.EventID != again.EventID {
		t.Errorf("같은 이벤트에 다른 ID: %q vs %q", ev.EventID, again.EventID)
	}
	// 다른 주문은 다른 ID 다. 아니면 두 번째 입금이 중복으로 버려진다.
	other, _ := tp.VerifyWebhook(ctx, []byte(
		`{"eventType":"DEPOSIT_CALLBACK","data":{"paymentKey":"pk-2","orderId":"o-2"}}`))
	if ev.EventID == other.EventID {
		t.Errorf("다른 주문에 같은 ID: %q", other.EventID)
	}

	for _, bad := range []string{``, `not json`, `{}`, `{"eventType":"X"}`,
		`{"data":{"orderId":"o"}}`} {
		if _, err := tp.VerifyWebhook(ctx, []byte(bad)); !errors.Is(err, ErrWebhookUnverified) {
			t.Errorf("%q = %v, want ErrWebhookUnverified", bad, err)
		}
	}

	// 상한을 넘는 본문은 파싱하지 않는다. 자원 한계이지 규칙은 아니지만, 검사가
	// 있는 이상 무는지 확인한다 — 안 물면 지우는 것이 맞다.
	huge := append([]byte(`{"eventType":"X","data":{"orderId":"o","pad":"`),
		bytes.Repeat([]byte("a"), tossMaxBody)...)
	huge = append(huge, []byte(`"}}`)...)
	if _, err := tp.VerifyWebhook(ctx, huge); !errors.Is(err, ErrWebhookUnverified) {
		t.Errorf("%d바이트 본문 = %v, want ErrWebhookUnverified", len(huge), err)
	}
}
