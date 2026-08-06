package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
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

// **이름과 어댑터가 갈라지지 않는다** — 빈 이름과 nil 어댑터는 항상 함께다.
//
// 이 불변식이 깨진 상태가 정확히 이 저장소에서 한 번 있었다: 이름 쪽만
// 「사용 안 함」을 반영해서 웹훅과 `payments.pg` 라벨은 닫히고 승인 경로는
// 열려 있었다. 둘을 한 함수에서 내되, 그 사실을 여기서 고정한다.
func TestProviderNameAndGatewayNeverDisagree(t *testing.T) {
	_, pool := liveSite(t)
	ctx := context.Background()

	for _, tc := range []struct {
		provider string
		want     bool // 결제를 받을 수 있어야 하는가
	}{
		{"toss", true},
		{"", false},
		{"stripe", false}, // 등록되지 않은 이름
		{"TOSS", false},   // 대소문자가 다르면 다른 이름이다
		{"toss-x", false},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO settings (key, value) VALUES
				('site.type','shop'), ('pg.provider',$1), ('pg.secret_key','test_sk_LEFTOVER')
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, tc.provider); err != nil {
			t.Fatal(err)
		}
		srv := restartOnSameSchema(t).URL

		// 웹훅은 이름으로 갈린다.
		resp := postWebhook(t, srv, "toss",
			`{"eventType":"X","data":{"orderId":"없는주문","paymentKey":"pk"}}`)
		resp.Body.Close()
		gotHook := resp.StatusCode == http.StatusOK

		// 결제창은 어댑터로 갈린다. 둘이 같은 답을 내야 한다.
		c := client()
		got, err := c.Get(srv + "/checkout")
		if err != nil {
			t.Fatal(err)
		}
		got.Body.Close()
		gotPay := got.StatusCode != http.StatusServiceUnavailable

		if gotHook != tc.want || gotPay != tc.want {
			t.Errorf("provider=%q: 웹훅 열림=%v · 결제 열림=%v, 둘 다 %v 여야 한다",
				tc.provider, gotHook, gotPay, tc.want)
		}
	}
}

// **결제사를 고르지 않으면 웹훅이 전부 404 다** (A-209 「사용 안 함」).
//
// 화면은 껐다고 하는데 실제로는 켜져 있는 상태를 막는다.
func TestUnsetProviderClosesTheWebhookRoute(t *testing.T) {
	_, pool := liveSite(t)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO settings (key, value) VALUES ('site.type','shop'), ('pg.provider','')
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`); err != nil {
		t.Fatal(err)
	}
	srv := restartOnSameSchema(t).URL

	for _, pg := range []string{"toss", "otherpg", "x"} {
		resp := postWebhook(t, srv, pg, `{"eventType":"X","data":{"orderId":"a","paymentKey":"pk"}}`)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("사용 안 함인데 %s = HTTP %d, want 404", pg, resp.StatusCode)
		}
	}
}

// **A-209 가 저장한 클라이언트 키가 결제 화면(P-407)에 실린다.**
//
// 설정값을 DB 에서 다시 읽어 비교하면 PostgreSQL 을 시험하는 것이지 배선을
// 시험하는 것이 아니다 — 하드코딩으로 되돌려도 통과한다. **화면이 실제로 그
// 값을 그리는지**를 본다.
func TestConfiguredClientKeyReachesTheCheckoutScreen(t *testing.T) {
	srvT, pool, variantID := shopSite(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO settings (key, value) VALUES
			('pg.client_key','test_ck_FROM_ADMIN'), ('pg.secret_key','test_sk_NEVER_SHOWN')
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`); err != nil {
		t.Fatal(err)
	}
	_ = srvT
	srv := restartOnSameSchema(t)

	c := client()
	// 장바구니 → 주문서 → 결제 화면. 결제 화면이 클라이언트 키를 싣는다.
	resp := post(t, c, srv.URL+"/cart/items",
		url.Values{"variant_id": {variantID}, "quantity": {"1"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("담기 HTTP %d", resp.StatusCode)
	}
	resp = post(t, c, srv.URL+"/checkout", url.Values{
		"receiver_name": {"받는이"}, "receiver_phone": {"010-0000-0000"},
		"postcode": {"12345"}, "address1": {"서울시 어딘가"},
		"orderer_email": {"a@example.com"}, "orderer_phone": {"010-1111-1111"},
	})
	// 주문서 제출은 결제 화면(P-407)으로 리다이렉트한다. 클라이언트 키는
	// 거기 실린다 — 결제창을 여는 것이 그 화면이다.
	loc := resp.Header.Get("Location")
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || loc == "" {
		t.Fatalf("주문서 제출 = HTTP %d, Location=%q", resp.StatusCode, loc)
	}
	pay, err := c.Get(srv.URL + loc)
	if err != nil {
		t.Fatal(err)
	}
	defer pay.Body.Close()
	b, err := io.ReadAll(pay.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)

	if !strings.Contains(body, "test_ck_FROM_ADMIN") {
		t.Errorf("결제 화면(%s)에 A-209 의 클라이언트 키가 없다 (HTTP %d)\n%.400s",
			loc, pay.StatusCode, body)
	}
	// **시크릿은 어떤 경로로도 오지 않는다** (D19 P-407).
	if strings.Contains(body, "test_sk_NEVER_SHOWN") {
		t.Error("시크릿 키가 결제 화면에 실렸다")
	}
}

// **「사용 안 함」은 실제 결제를 막는다** (A-209, D19).
//
// 처음 고쳤을 때 `pgName()` 만 바꿔서 `payments.pg` 라벨과 웹훅 경로만 닫히고
// **승인 경로는 그대로 열려 있었다** — 관리자는 껐다고 믿는데 고객은 결제를
// 끝까지 완료할 수 있었다. 시크릿 키는 「그대로 두라」라 남아 있으므로,
// 결제사만 비우면 그 상태가 된다.
func TestDisabledProviderRefusesTheWholePaymentPath(t *testing.T) {
	srvT, pool, variantID := shopSite(t)
	_ = srvT
	ctx := context.Background()
	// 시크릿·클라이언트 키는 남긴 채 결제사만 비운다 — 실제 상황이다.
	if _, err := pool.Exec(ctx, `
		INSERT INTO settings (key, value) VALUES
			('pg.provider',''), ('pg.client_key','test_ck_LEFTOVER'),
			('pg.secret_key','test_sk_LEFTOVER')
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`); err != nil {
		t.Fatal(err)
	}
	srv := restartOnSameSchema(t)

	c := client()
	// 담기는 된다 — 구경과 담기는 결제가 아니다.
	resp := post(t, c, srv.URL+"/cart/items",
		url.Values{"variant_id": {variantID}, "quantity": {"1"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("담기 HTTP %d — 결제와 무관한 경로가 막혔다", resp.StatusCode)
	}

	// 주문서·결제창·승인 콜백이 전부 막힌다.
	for _, tc := range []struct{ name, path string }{
		{"주문서(P-405)", "/checkout"},
		{"결제창(P-407)", "/checkout/pay"},
		{"승인 콜백(P-408)", "/checkout/success"},
	} {
		got, err := c.Get(srv.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(got.Body)
		got.Body.Close()
		if got.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s = HTTP %d, want 503", tc.name, got.StatusCode)
		}
		// 남아 있는 클라이언트 키가 화면으로 나가지 않는다.
		if strings.Contains(string(body), "test_ck_LEFTOVER") {
			t.Errorf("%s 가 꺼진 상태에서 클라이언트 키를 실었다", tc.name)
		}
	}

	// 주문 생성(P-406)도 막힌다 — **재고가 묶이지 않는다.**
	var stockBefore int
	if err := pool.QueryRow(ctx,
		`SELECT stock FROM product_variants WHERE id = $1`, variantID).Scan(&stockBefore); err != nil {
		t.Fatal(err)
	}
	resp = post(t, c, srv.URL+"/checkout", url.Values{
		"receiver_name": {"받는이"}, "receiver_phone": {"010-0000-0000"},
		"postcode": {"12345"}, "address1": {"서울시 어딘가"},
		"orderer_email": {"a@example.com"}, "orderer_phone": {"010-1111-1111"},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("주문 생성 = HTTP %d, want 503", resp.StatusCode)
	}

	var orders, payments, stockAfter int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM orders`).Scan(&orders); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM payments`).Scan(&payments); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT stock FROM product_variants WHERE id = $1`, variantID).Scan(&stockAfter); err != nil {
		t.Fatal(err)
	}
	if orders != 0 {
		t.Errorf("결제사가 꺼졌는데 주문 %d건이 생겼다", orders)
	}
	if payments != 0 {
		t.Errorf("결제사가 꺼졌는데 결제 %d건이 생겼다", payments)
	}
	if stockAfter != stockBefore {
		t.Errorf("재고가 %d → %d 로 묶였다 — 팔 수 없는 재고다", stockBefore, stockAfter)
	}
}

// 결제사를 다시 고르면 열린다 — 위 검사가 "결제가 늘 막힌다" 를 본 것이
// 아니라는 것.
func TestReenablingTheProviderOpensPaymentAgain(t *testing.T) {
	srvT, pool, variantID := shopSite(t)
	_ = srvT
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO settings (key, value) VALUES ('pg.provider',''), ('pg.client_key','ck')
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`); err != nil {
		t.Fatal(err)
	}
	srv := restartOnSameSchema(t)
	c := client()
	resp := post(t, c, srv.URL+"/cart/items",
		url.Values{"variant_id": {variantID}, "quantity": {"1"}})
	resp.Body.Close()
	if got, _ := c.Get(srv.URL + "/checkout"); got.StatusCode != http.StatusServiceUnavailable {
		got.Body.Close()
		t.Fatalf("꺼진 상태 주문서 = HTTP %d", got.StatusCode)
	}

	if _, err := pool.Exec(context.Background(),
		`UPDATE settings SET value = 'toss' WHERE key = 'pg.provider'`); err != nil {
		t.Fatal(err)
	}
	srv2 := restartOnSameSchema(t)
	c2 := client()
	resp = post(t, c2, srv2.URL+"/cart/items",
		url.Values{"variant_id": {variantID}, "quantity": {"1"}})
	resp.Body.Close()
	got, err := c2.Get(srv2.URL + "/checkout")
	if err != nil {
		t.Fatal(err)
	}
	got.Body.Close()
	if got.StatusCode != http.StatusOK {
		t.Errorf("다시 켠 뒤 주문서 = HTTP %d, want 200", got.StatusCode)
	}
}

// **교환 차액 결제(P-514)도 「사용 안 함」에 막힌다.**
//
// 주문 결제와 다른 경로라, 한쪽만 막으면 여기로 결제가 새어 나간다.
func TestDisabledProviderRefusesExchangeDiffPayment(t *testing.T) {
	srvT, pool, _ := shopSite(t)
	_ = srvT
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO settings (key, value) VALUES ('pg.provider',''), ('pg.secret_key','test_sk_LEFTOVER')
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`); err != nil {
		t.Fatal(err)
	}
	srv := restartOnSameSchema(t)

	// 주문·교환 건이 없어도 **가드가 먼저다** — 조회보다 앞에서 거부해야
	// 꺼진 상점에서 남의 주문번호를 넣어 보는 경로도 열리지 않는다.
	c := client()
	got, err := c.Get(srv.URL + "/orders/NO-SUCH-ORDER/exchange/RN-1/pay")
	if err != nil {
		t.Fatal(err)
	}
	got.Body.Close()
	if got.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("교환 차액 화면 = HTTP %d, want 503", got.StatusCode)
	}

	resp := post(t, c, srv.URL+"/orders/NO-SUCH-ORDER/exchange/RN-1/pay", url.Values{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("교환 차액 승인 = HTTP %d, want 503", resp.StatusCode)
	}
	var payments int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM payments`).Scan(&payments); err != nil {
		t.Fatal(err)
	}
	if payments != 0 {
		t.Errorf("꺼진 상태에서 결제 %d건이 생겼다", payments)
	}
}
