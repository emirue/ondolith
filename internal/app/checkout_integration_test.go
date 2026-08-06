package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// shopSite brings up the real tree in shop mode, with one product in stock.
func shopSite(t *testing.T) (*httptest.Server, *pgxpool.Pool, string) {
	t.Helper()
	_, pool := liveSite(t)
	ctx := context.Background()
	// **결제사를 고른다.** A-209 의 빈 값은 「사용 안 함」이라 결제 경로가
	// 닫힌다 — 결제를 쓰는 헬퍼이므로 여기서 고른다.
	if _, err := pool.Exec(ctx,
		`INSERT INTO settings (key, value) VALUES ('site.type','shop'), ('pg.provider','toss')
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`); err != nil {
		t.Fatal(err)
	}
	var productID, variantID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO products (slug,name,base_price,is_visible)
		 VALUES ('tee','티셔츠',12000,true) RETURNING id`).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO product_variants (product_id,option_values,price_delta,stock)
		 VALUES ($1,'{"크기":"L"}',1000,5) RETURNING id`, productID).Scan(&variantID); err != nil {
		t.Fatal(err)
	}
	return restartOnSameSchema(t), pool, variantID
}

func post(t *testing.T, c *http.Client, u string, form url.Values) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// CrossOriginProtection 은 Sec-Fetch-Site 를 본다 (NFR-205). 브라우저가
	// 같은 출처에 보낼 때와 같은 헤더를 붙인다.
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// 장바구니 → 주문서 → 주문 생성 → 결제 → 완료. 실제 트리를 걷는다.
func TestCheckoutWalkthrough(t *testing.T) {
	srv, pool, variant := shopSite(t)
	c := client()

	// 담기.
	resp := post(t, c, srv.URL+"/cart/items", url.Values{
		"variant_id": {variant}, "quantity": {"2"},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("담기 HTTP %d", resp.StatusCode)
	}

	// 주문서.
	got, err := c.Get(srv.URL + "/checkout")
	if err != nil {
		t.Fatal(err)
	}
	got.Body.Close()
	if got.StatusCode != http.StatusOK {
		t.Fatalf("주문서 HTTP %d", got.StatusCode)
	}

	// 주문 생성.
	resp = post(t, c, srv.URL+"/checkout", url.Values{
		"receiver_name": {"받는이"}, "receiver_phone": {"010-0000-0000"},
		"postcode": {"12345"}, "address1": {"서울시 어딘가"},
		"orderer_email": {"a@example.com"}, "orderer_phone": {"010-1111-1111"},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("주문 생성 HTTP %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/checkout/pay" {
		t.Errorf("주문 뒤 이동 %q", loc)
	}

	// 주문이 실제로 생겼고 금액은 서버가 계산했다: (12000+1000)×2 = 26000.
	// 배송비 설정이 없으므로 0 이다.
	ctx := context.Background()
	var orderNo string
	var total int
	if err := pool.QueryRow(ctx,
		`SELECT order_no, total_amount FROM orders`).Scan(&orderNo, &total); err != nil {
		t.Fatal(err)
	}
	if total != 26000 {
		t.Errorf("총액 %d, want 26000", total)
	}

	// 재고가 줄었다.
	var stock int
	if err := pool.QueryRow(ctx,
		`SELECT stock FROM product_variants WHERE id = $1`, variant).Scan(&stock); err != nil {
		t.Fatal(err)
	}
	if stock != 3 {
		t.Errorf("재고 %d, want 3", stock)
	}

	// 결제 화면이 열린다.
	got, err = c.Get(srv.URL + "/checkout/pay")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, got)
	if got.StatusCode != http.StatusOK {
		t.Fatalf("결제 화면 HTTP %d", got.StatusCode)
	}
	if !strings.Contains(body, orderNo) {
		t.Errorf("결제 화면에 주문번호가 없다")
	}
	// 시크릿이 화면에 오지 않는다 (D19 P-407).
	if strings.Contains(body, "pg.secret") || strings.Contains(body, "test_sk") {
		t.Error("결제 화면에 시크릿 흔적이 있다")
	}

	// 완료 화면은 스냅샷만 그린다.
	got, err = c.Get(srv.URL + "/checkout/complete")
	if err != nil {
		t.Fatal(err)
	}
	body = readAll(t, got)
	if got.StatusCode != http.StatusOK {
		t.Fatalf("완료 화면 HTTP %d", got.StatusCode)
	}
	for _, want := range []string{orderNo, "티셔츠", "크기: L"} {
		if !strings.Contains(body, want) {
			t.Errorf("완료 화면에 %q 가 없다", want)
		}
	}
}

// **콜백의 orderId 로 주문을 조회하지 않는다** (D19 P-408).
//
// 조회 키로 쓰면 남의 주문번호를 적어 넣어 그 주문을 승인시킬 수 있다.
// 세션이 무엇을 승인할지 정하고, 콜백 값은 대조에만 쓰인다.
func TestCallbackOrderIdIsOnlyCompared(t *testing.T) {
	srv, pool, variant := shopSite(t)
	ctx := context.Background()

	// 피해자의 주문을 하나 만든다.
	victim := client()
	post(t, victim, srv.URL+"/cart/items", url.Values{"variant_id": {variant}}).Body.Close()
	post(t, victim, srv.URL+"/checkout", url.Values{
		"receiver_name": {"피해자"}, "receiver_phone": {"010-0000-0000"},
		"postcode": {"12345"}, "address1": {"서울"},
		"orderer_email": {"v@example.com"}, "orderer_phone": {"010-2222-2222"},
	}).Body.Close()

	var victimOrder string
	if err := pool.QueryRow(ctx, `SELECT order_no FROM orders`).Scan(&victimOrder); err != nil {
		t.Fatal(err)
	}

	// 공격자는 주문이 없다. 피해자의 주문번호를 콜백에 실어 보낸다.
	attacker := client()
	resp, err := attacker.Get(srv.URL + "/checkout/success?orderId=" +
		url.QueryEscape(victimOrder) + "&amount=26000&paymentKey=pk-evil")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("세션에 주문이 없는데 HTTP %d, want 404", resp.StatusCode)
	}

	// 피해자의 주문은 그대로 결제대기다.
	var status string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM orders WHERE order_no = $1`, victimOrder).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "결제대기" {
		t.Errorf("피해자 주문 상태 %q", status)
	}
	// 결제 행도 생기지 않았다.
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM payments`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("결제 행 %d건", n)
	}
}

// 자기 세션이 있어도 다른 주문번호를 실으면 거부된다 — 대조가 실제로 일어난다.
func TestCallbackOrderIdMustMatchTheSession(t *testing.T) {
	srv, _, variant := shopSite(t)
	c := client()
	post(t, c, srv.URL+"/cart/items", url.Values{"variant_id": {variant}}).Body.Close()
	post(t, c, srv.URL+"/checkout", url.Values{
		"receiver_name": {"나"}, "receiver_phone": {"010-0000-0000"},
		"postcode": {"12345"}, "address1": {"서울"},
		"orderer_email": {"a@example.com"}, "orderer_phone": {"010-1111-1111"},
	}).Body.Close()

	resp, err := c.Get(srv.URL + "/checkout/success?orderId=20260101-XXXXXXXXXX&amount=26000&paymentKey=pk")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp)
	if !strings.Contains(body, "맞지 않습니다") {
		t.Errorf("다른 주문번호가 통과했다: %.200s", body)
	}
}

// P-409: PG 가 붙인 message 를 화면에 그리지 않는다 (D19).
func TestFailScreenDoesNotEchoTheGatewayMessage(t *testing.T) {
	srv, _, _ := shopSite(t)
	c := client()

	const injected = "여기를_클릭하세요_hxxp_evil"
	resp, err := c.Get(srv.URL + "/checkout/fail?code=UNKNOWN_CODE&message=" +
		url.QueryEscape(injected))
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp)
	if strings.Contains(body, injected) {
		t.Errorf("PG 메시지가 화면에 그려졌다: %.200s", body)
	}
	if !strings.Contains(body, "결제가 완료되지 않았습니다") {
		t.Errorf("일반 실패 문구가 없다: %.200s", body)
	}

	// 아는 코드는 우리 문장으로 바뀐다.
	resp, err = c.Get(srv.URL + "/checkout/fail?code=PAY_PROCESS_CANCELED")
	if err != nil {
		t.Fatal(err)
	}
	if body = readAll(t, resp); !strings.Contains(body, "취소하셨습니다") {
		t.Errorf("사유 코드 매핑이 적용되지 않았다: %.200s", body)
	}
}

// 빈 장바구니로 주문서를 열면 장바구니로 돌려보낸다.
func TestCheckoutWithEmptyCartRedirects(t *testing.T) {
	srv, _, _ := shopSite(t)
	c := client()
	resp, err := c.Get(srv.URL + "/checkout")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/cart" {
		t.Errorf("빈 장바구니 주문서 = HTTP %d → %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// 세션에 결제할 주문이 없으면 결제 화면이 404 다.
func TestPayScreenNeedsAPendingOrder(t *testing.T) {
	srv, _, _ := shopSite(t)
	c := client()
	for _, path := range []string{"/checkout/pay", "/checkout/complete"} {
		resp, err := c.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s = HTTP %d, want 404", path, resp.StatusCode)
		}
	}
}

// readAll drains and closes the body.
func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
