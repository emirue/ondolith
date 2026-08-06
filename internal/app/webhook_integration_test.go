package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func postWebhook(t *testing.T, srv, pg string, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv+"/webhooks/payment/"+pg,
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// waitForWebhookStatus polls until the row leaves '수신'. 수신과 처리를 분리
// 했으므로 (D19 P-905) 200 이 돌아온 시점에는 아직 처리 전이다.
func waitForWebhookStatus(t *testing.T, pool *pgxpool.Pool, eventID string) string {
	t.Helper()
	ctx := context.Background()
	for range 200 {
		var status string
		err := pool.QueryRow(ctx,
			`SELECT status FROM webhook_events WHERE event_id = $1`, eventID).Scan(&status)
		if err == nil && status != "수신" {
			return status
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("웹훅 %q 이 처리되지 않았다", eventID)
	return ""
}

// **P-905 는 본 트리 밖이다** (D15 SC-8 1항).
//
// 세션도 CSRF 토큰도 없는 요청이 통과해야 한다 — PG 의 서버에는 줄 쿠키가
// 없다. 통과하는 이유가 `CrossOriginProtection` 의 우연이 아니라 별도 문이라는
// 것을, **Origin 을 붙인 교차 출처 요청도 통과하는지**로 확인한다. 본 트리
// 안이었다면 그 요청은 차단된다.
func TestWebhookIsOutsideTheMainTree(t *testing.T) {
	srvT, _, _ := shopSite(t)
	srv := srvT.URL

	req, _ := http.NewRequest(http.MethodPost, srv+"/webhooks/payment/toss",
		strings.NewReader(`{"eventType":"PAYMENT_STATUS_CHANGED","data":{"orderId":"없는주문","paymentKey":"pk"}}`))
	req.Header.Set("Content-Type", "application/json")
	// 브라우저가 붙이는 교차 출처 표시. 본 트리라면 여기서 차단된다.
	req.Header.Set("Origin", "https://evil.example.com")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("교차 출처 웹훅 = HTTP %d, want 200 — 본 트리의 CSRF 보호에 걸렸다", resp.StatusCode)
	}
}

// 등록되지 않은 PG 는 404 다. 어떤 PG 를 쓰는지 알려주지 않는다.
func TestWebhookRejectsUnknownGateway(t *testing.T) {
	srvT, _, _ := shopSite(t)
	srv := srvT.URL
	resp := postWebhook(t, srv, "stripe", `{"eventType":"x","data":{"orderId":"y"}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("모르는 PG = HTTP %d, want 404", resp.StatusCode)
	}
}

// **검증 실패는 조용히 버린다** — 사유를 응답에 담지 않는다 (D15 SC-8 2항).
// 어디가 틀렸는지 알려주는 응답은 곧 위조 안내서다.
func TestWebhookVerificationFailureSaysNothing(t *testing.T) {
	srvT, pool, _ := shopSite(t)
	srv := srvT.URL
	for _, body := range []string{"", "not json", `{"eventType":""}`, `{"data":{"orderId":"a"}}`} {
		resp := postWebhook(t, srv, "toss", body)
		buf := make([]byte, 512)
		n, _ := resp.Body.Read(buf)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("본문 %q = HTTP %d, want 400", body, resp.StatusCode)
		}
		if n != 0 {
			t.Errorf("본문 %q 에 사유가 실려 나갔다: %q", body, buf[:n])
		}
	}
	// 거부된 것은 기록도 남기지 않는다 — 검증 전 페이로드로 표를 채우면
	// 아무나 우리 DB 를 채울 수 있다.
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM webhook_events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("검증 실패한 요청이 %d행 기록됐다", n)
	}
}

// 본문 크기 상한을 넘으면 413 이다. 서명 검증 전에 파싱하지 않는다는 규칙은
// 크기 상한이 있어야 의미가 있다 — 없으면 파싱 전에 메모리를 다 쓴다.
func TestWebhookRejectsOversizedBody(t *testing.T) {
	srvT, _, _ := shopSite(t)
	srv := srvT.URL
	resp := postWebhook(t, srv, "toss", strings.Repeat("a", maxWebhookBody+1))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("과대 본문 = HTTP %d, want 413", resp.StatusCode)
	}
}

// **재전송에 멱등이다** (FR-610). 같은 이벤트가 두 번 와도 행은 하나다.
func TestWebhookIsIdempotentOnResend(t *testing.T) {
	srvT, pool, _ := shopSite(t)
	srv := srvT.URL
	body := `{"eventType":"PAYMENT_STATUS_CHANGED","data":{"orderId":"없는주문","paymentKey":"pk-1"}}`
	for i := range 2 {
		resp := postWebhook(t, srv, "toss", body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%d번째 = HTTP %d, want 200", i+1, resp.StatusCode)
		}
	}
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM webhook_events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("재전송으로 %d행이 생겼다, want 1", n)
	}
}

// **금액은 대조만 하고 저장하지 않는다** (D19 P-905 받지 않는 필드).
// 불일치는 200 으로 답하되 `실패` 로 남긴다 — 재전송 폭주를 부르지 않으면서
// A-508 대사 대상으로 보이게 한다.
func TestWebhookAmountIsOnlyComparedNeverStored(t *testing.T) {
	srvT, pool, _ := shopSite(t)
	srv := srvT.URL
	ctx := context.Background()
	orderNo, total := seedPaidOrderForWebhook(t, pool)

	payload := map[string]any{"eventType": "PAYMENT_STATUS_CHANGED",
		"data": map[string]any{"orderId": orderNo, "paymentKey": "pk-wh",
			"secret": "wh-secret", "totalAmount": total + 5000}}
	b, _ := json.Marshal(payload)
	resp := postWebhook(t, srv, "toss", string(b))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("금액 불일치 = HTTP %d, want 200 (재전송 폭주를 부르지 않는다)", resp.StatusCode)
	}

	status := waitForWebhookStatus(t, pool,
		"PAYMENT_STATUS_CHANGED:"+orderNo+":pk-wh")
	if status != "실패" {
		t.Errorf("금액 불일치인데 상태가 %q — A-508 대사 대상으로 보이지 않는다", status)
	}
	// 주문 금액은 그대로다. 웹훅이 알려준 값이 저장되면 통보하는 쪽이 결제액을 정한다.
	var stored int
	if err := pool.QueryRow(ctx,
		`SELECT total_amount FROM orders WHERE order_no = $1`, orderNo).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != total {
		t.Errorf("주문 금액이 %d → %d 로 바뀌었다 — 웹훅 금액이 채택됐다", total, stored)
	}
}

// **웹훅은 주문 상태를 옮기지 않는다** (D19 P-905 받지 않는 필드).
// 외부가 `구매확정` 을 지정할 수 있으면 취소·환불 경로가 임의로 닫힌다.
func TestWebhookNeverMovesOrderStatus(t *testing.T) {
	srvT, pool, _ := shopSite(t)
	srv := srvT.URL
	ctx := context.Background()
	orderNo, total := seedPaidOrderForWebhook(t, pool)

	var before string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM orders WHERE order_no = $1`, orderNo).Scan(&before); err != nil {
		t.Fatal(err)
	}

	payload := map[string]any{"eventType": "PAYMENT_STATUS_CHANGED",
		"data": map[string]any{"orderId": orderNo, "paymentKey": "pk-wh",
			"secret": "wh-secret", "totalAmount": total, "status": "구매확정"}}
	b, _ := json.Marshal(payload)
	resp := postWebhook(t, srv, "toss", string(b))
	resp.Body.Close()

	if got := waitForWebhookStatus(t, pool,
		"PAYMENT_STATUS_CHANGED:"+orderNo+":pk-wh"); got != "처리완료" {
		t.Fatalf("정상 웹훅 처리 상태 %q", got)
	}
	var after string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM orders WHERE order_no = $1`, orderNo).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("웹훅이 주문 상태를 %q → %q 로 옮겼다", before, after)
	}
}

// secret 이 승인 응답과 다르면 우리 결제에 대한 알림이 아니다.
func TestWebhookSecretMustMatchTheApproval(t *testing.T) {
	srvT, pool, _ := shopSite(t)
	srv := srvT.URL
	orderNo, total := seedPaidOrderForWebhook(t, pool)

	payload := map[string]any{"eventType": "PAYMENT_STATUS_CHANGED",
		"data": map[string]any{"orderId": orderNo, "paymentKey": "pk-wh",
			"secret": "남의-secret", "totalAmount": total}}
	b, _ := json.Marshal(payload)
	resp := postWebhook(t, srv, "toss", string(b))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("= HTTP %d, want 200", resp.StatusCode)
	}
	if got := waitForWebhookStatus(t, pool,
		"PAYMENT_STATUS_CHANGED:"+orderNo+":pk-wh"); got != "실패" {
		t.Errorf("secret 불일치인데 상태가 %q", got)
	}
}

// seedPaidOrderForWebhook 은 승인된 결제가 붙은 주문을 만든다. secret 은
// 승인 응답이 준 값으로 저장돼 있어야 웹훅이 대조할 상대가 생긴다.
func seedPaidOrderForWebhook(t *testing.T, pool *pgxpool.Pool) (string, int) {
	t.Helper()
	ctx := context.Background()
	var orderID, orderNo string
	var total int
	if err := pool.QueryRow(ctx, `
		INSERT INTO orders (order_no, status, receiver_name, receiver_phone,
		                    postcode, address1, orderer_email, orderer_phone,
		                    total_amount)
		VALUES ('WH'||substr(md5(random()::text),1,12), '결제완료', '받는이',
		        '010-0000-0000', '12345', '서울', 'a@example.com', '010-1111-1111',
		        15000)
		RETURNING id, order_no, total_amount`).Scan(&orderID, &orderNo, &total); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO payments (order_id, kind, status, pg, payment_key,
		                      approved_amount, secret)
		VALUES ($1, '주문결제', '승인', 'toss', 'pk-wh', $2, 'wh-secret')`,
		orderID, total); err != nil {
		t.Fatal(err)
	}
	return orderNo, total
}
