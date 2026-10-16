package app

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func getBody(t *testing.T, c *http.Client, u string) (int, string, string) {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Location"), string(b)
}

// **살 수 없는 상품이 장바구니에 있으면 장바구니에서 멈춘다.** 전에는 합계에
// 넣고 「주문하기」를 열어 둬서, 받는 분·주소를 다 쓰고 「결제하기」를 눌러야
// 거절이 나왔다. 숨긴 상품의 이름 링크는 404 로 갔다.
func TestUnsellableCartItemStopsAtTheCart(t *testing.T) {
	srv, pool, variant := shopSite(t)
	c := client()
	resp := post(t, c, srv.URL+"/cart/items", url.Values{"variant_id": {variant}, "quantity": {"1"}})
	resp.Body.Close()
	if _, err := pool.Exec(context.Background(), `UPDATE products SET is_visible = false`); err != nil {
		t.Fatal(err)
	}

	code, _, cart := getBody(t, c, srv.URL+"/cart")
	if code != http.StatusOK {
		t.Fatalf("장바구니 HTTP %d", code)
	}
	if strings.Contains(cart, `href="/checkout"`) {
		t.Error("살 수 없는 상품이 있는데 「주문하기」가 열려 있다")
	}
	if !strings.Contains(cart, "구매할 수 없는 상품") {
		t.Error("왜 주문할 수 없는지 장바구니가 말하지 않는다")
	}
	if strings.Contains(cart, `href="/shop/p/tee"`) {
		t.Error("숨긴 상품의 이름이 404 로 가는 링크다")
	}

	// 주소를 직접 쳐서 주문서로 와도 장바구니로 돌려보낸다.
	if code, loc, _ := getBody(t, c, srv.URL+"/checkout"); code != http.StatusSeeOther || loc != "/cart" {
		t.Errorf("주문서 = HTTP %d → %q, want 303 → /cart", code, loc)
	}
}

// **주문서가 거절돼도 쓴 것은 남는다.** 전에는 거절 화면의 입력칸이 전부 비어
// 받는 분·주소·연락처를 처음부터 다시 써야 했다.
func TestRejectedCheckoutKeepsWhatWasTyped(t *testing.T) {
	srv, pool, variant := shopSite(t)
	ctx := context.Background()
	var termID string
	if err := pool.QueryRow(ctx, `INSERT INTO terms (kind, version, body, effective_at, is_required)
		VALUES ('이용약관','1.0','본문',now(),true) RETURNING id`).Scan(&termID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO terms (kind, version, body, effective_at, is_required)
		VALUES ('마케팅','1.0','본문',now(),false)`); err != nil {
		t.Fatal(err)
	}
	var optional string
	if err := pool.QueryRow(ctx, `SELECT id FROM terms WHERE kind = '마케팅'`).Scan(&optional); err != nil {
		t.Fatal(err)
	}
	c := client()
	resp := post(t, c, srv.URL+"/cart/items", url.Values{"variant_id": {variant}, "quantity": {"1"}})
	resp.Body.Close()

	// 필수 약관에 동의하지 않고 보낸다 → 422.
	resp = post(t, c, srv.URL+"/checkout", url.Values{
		"receiver_name": {"홍길동"}, "receiver_phone": {"010-2222-3333"},
		"postcode": {"04524"}, "address1": {"서울시 중구 세종대로 110"}, "address2": {"3층"},
		"delivery_memo": {"부재 시 경비실"}, "orderer_email": {"keep@example.com"},
		"orderer_phone": {"010-4444-5555"}, "agreed_terms": {optional},
	})
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("약관 없이 주문 = HTTP %d, want 422", resp.StatusCode)
	}
	page := string(b)
	for _, want := range []string{`value="홍길동"`, `value="010-2222-3333"`, `value="04524"`,
		`value="서울시 중구 세종대로 110"`, `value="3층"`, `value="부재 시 경비실"`,
		`value="keep@example.com"`, `value="010-4444-5555"`} {
		if !strings.Contains(page, want) {
			t.Errorf("거절 화면에 %s 가 남지 않았다", want)
		}
	}
	// 동의했던 선택 약관은 체크된 채로, 동의하지 않은 필수 약관은 빈 채로.
	if !strings.Contains(page, `value="`+optional+`" checked`) {
		t.Error("동의했던 약관의 체크가 풀렸다")
	}
	if strings.Contains(page, `value="`+termID+`" checked`) {
		t.Error("동의하지 않은 약관이 체크돼 있다")
	}
}
