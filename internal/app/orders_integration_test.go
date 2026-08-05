package app

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// makeGuestOrder places one guest order and returns its number.
func makeGuestOrder(t *testing.T, srv string, variant, phone, email string) (*http.Client, string) {
	t.Helper()
	c := client()
	post(t, c, srv+"/cart/items", url.Values{"variant_id": {variant}}).Body.Close()
	resp := post(t, c, srv+"/checkout", url.Values{
		"receiver_name": {"받는이"}, "receiver_phone": {"010-0000-0000"},
		"postcode": {"12345"}, "address1": {"서울"},
		"orderer_email": {email}, "orderer_phone": {phone},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("주문 생성 HTTP %d", resp.StatusCode)
	}
	got, err := c.Get(srv + "/checkout/complete")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, got)
	// 주문번호는 완료 화면이 보여준다.
	i := strings.Index(body, "주문번호 <strong>")
	if i < 0 {
		t.Fatalf("완료 화면에서 주문번호를 찾지 못했다: %.300s", body)
	}
	rest := body[i+len("주문번호 <strong>"):]
	return c, rest[:strings.Index(rest, "<")]
}

// **비회원 조회 grant 는 그 주문 하나로 제한된다** (SC-3 4항).
//
// 목록을 열어 두면 조회 한 번이 여러 주문의 열쇠가 되고, 세션이 살아 있는
// 동안 계속 열린다.
func TestGuestGrantIsScopedToOneOrder(t *testing.T) {
	srv, _, variant := shopSite(t)

	_, mine := makeGuestOrder(t, srv.URL, variant, "010-1111-1111", "a@example.com")
	_, other := makeGuestOrder(t, srv.URL, variant, "010-2222-2222", "b@example.com")

	c := client()
	resp := post(t, c, srv.URL+"/orders/guest", url.Values{
		"order_no": {mine}, "phone": {"010-1111-1111"},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("조회 HTTP %d", resp.StatusCode)
	}

	// 조회한 주문은 열린다.
	got, err := c.Get(srv.URL + "/orders/" + mine)
	if err != nil {
		t.Fatal(err)
	}
	got.Body.Close()
	if got.StatusCode != http.StatusOK {
		t.Errorf("조회한 주문 = HTTP %d, want 200", got.StatusCode)
	}

	// 다른 주문은 같은 세션으로도 열리지 않는다.
	got, err = c.Get(srv.URL + "/orders/" + other)
	if err != nil {
		t.Fatal(err)
	}
	got.Body.Close()
	if got.StatusCode != http.StatusNotFound {
		t.Errorf("다른 주문 = HTTP %d, want 404 — grant 범위가 넓다", got.StatusCode)
	}
	// 배송 조회도 마찬가지다. 화면마다 판정하면 한쪽만 고쳐진다.
	got, err = c.Get(srv.URL + "/orders/" + other + "/shipping")
	if err != nil {
		t.Fatal(err)
	}
	got.Body.Close()
	if got.StatusCode != http.StatusNotFound {
		t.Errorf("다른 주문 배송조회 = HTTP %d, want 404", got.StatusCode)
	}
}

// 조회하지 않은 세션은 주문번호를 알아도 열지 못한다.
func TestOrderDetailNeedsAGrantOrOwnership(t *testing.T) {
	srv, _, variant := shopSite(t)
	_, orderNo := makeGuestOrder(t, srv.URL, variant, "010-1111-1111", "a@example.com")

	stranger := client()
	got, err := stranger.Get(srv.URL + "/orders/" + orderNo)
	if err != nil {
		t.Fatal(err)
	}
	got.Body.Close()
	if got.StatusCode != http.StatusNotFound {
		t.Errorf("주문번호만 아는 세션 = HTTP %d, want 404 — 번호가 곧 열쇠다", got.StatusCode)
	}
}

// 실패는 전부 같은 답이다 (D19 P-504). 구분하면 주문번호를 훑어 어느 것이
// 존재하는지 알 수 있다.
func TestGuestLookupFailuresAreIndistinguishable(t *testing.T) {
	srv, _, variant := shopSite(t)
	_, orderNo := makeGuestOrder(t, srv.URL, variant, "010-1111-1111", "a@example.com")

	cases := []struct {
		why  string
		form url.Values
	}{
		{"없는 주문번호", url.Values{"order_no": {"20260101-ZZZZZZZZZZ"}, "phone": {"010-1111-1111"}}},
		{"연락처 불일치", url.Values{"order_no": {orderNo}, "phone": {"010-9999-9999"}}},
		{"형식 오류", url.Values{"order_no": {"!!!"}, "phone": {"x"}}},
		{"연락처 없음", url.Values{"order_no": {orderNo}}},
	}
	var first string
	for i, c := range cases {
		// 주문번호별 제한(3회/시간)에 걸리지 않도록 매번 다른 번호를 쓰는
		// 경우만 반복한다 — 같은 번호로 두 번 이상 시도하는 것은 아래 별도
		// 테스트가 본다.
		cl := client()
		resp := post(t, cl, srv.URL+"/orders/guest", c.form)
		body := readAll(t, resp)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s = HTTP %d, want 400", c.why, resp.StatusCode)
		}
		if !strings.Contains(body, "주문 정보를 찾을 수 없습니다") {
			t.Errorf("%s: 단일 실패 문구가 아니다", c.why)
		}
		if i == 0 {
			first = body
			continue
		}
		if body != first {
			t.Errorf("%s: 응답이 다른 실패와 다르다 — 어느 것이 존재하는지 샌다", c.why)
		}
	}
}

// NFR-207: 주문번호당 3회/시간. 여러 곳에서 한 번호의 연락처를 맞히는 것을 막는다.
func TestGuestLookupIsRateLimitedPerOrderNumber(t *testing.T) {
	srv, _, variant := shopSite(t)
	_, orderNo := makeGuestOrder(t, srv.URL, variant, "010-1111-1111", "a@example.com")

	// 매번 새 세션이라 IP 제한(5회/분)과 구분된다 — 같은 IP 이므로 IP 제한이
	// 먼저 걸리지 않도록 3회 안에서 끝난다.
	for i := 0; i < 3; i++ {
		cl := client()
		resp := post(t, cl, srv.URL+"/orders/guest", url.Values{
			"order_no": {orderNo}, "phone": {"010-0000-000" + string(rune('0'+i))},
		})
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%d번째 시도 HTTP %d", i+1, resp.StatusCode)
		}
	}
	cl := client()
	resp := post(t, cl, srv.URL+"/orders/guest", url.Values{
		"order_no": {orderNo}, "phone": {"010-1111-1111"},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("4번째 시도 HTTP %d, want 429 — 주문번호당 제한이 없다", resp.StatusCode)
	}
}

// 회원 주문은 비회원 경로로 열리지 않는다. 열리면 그 길은 남의 회원 주문에도
// 열려 있다.
func TestMemberOrdersAreNotReachableThroughTheGuestPath(t *testing.T) {
	srv, pool, variant := shopSite(t)
	ctx := context.Background()

	_, orderNo := makeGuestOrder(t, srv.URL, variant, "010-1111-1111", "a@example.com")
	// 그 주문을 회원 주문으로 바꾼다.
	var userID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email,password_hash,display_name)
		 VALUES ('m@example.com','x','회원') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE orders SET user_id = $2 WHERE order_no = $1`, orderNo, userID); err != nil {
		t.Fatal(err)
	}

	cl := client()
	resp := post(t, cl, srv.URL+"/orders/guest", url.Values{
		"order_no": {orderNo}, "phone": {"010-1111-1111"},
	})
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("회원 주문 비회원 조회 = HTTP %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(body, "주문 정보를 찾을 수 없습니다") {
		t.Errorf("다른 문구가 나왔다: %.200s", body)
	}
}

// 비회원 조회 화면에 계좌·배송지 입력란이 없다 (D19 P-504).
//
// 조회만으로 계좌를 바꿀 수 있으면 안 되고, 주문번호+전화만 알면 배송지를
// 바꿔 물건을 가로챌 수 있다.
func TestGuestFormHasNoAccountOrAddressFields(t *testing.T) {
	srv, _, _ := shopSite(t)
	c := client()
	resp, err := c.Get(srv.URL + "/orders/guest")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp)
	// 폼 안만 본다. 레이아웃에는 `/me` 같은 링크가 있고, 그것을 잡으면 검사가
	// 화면 전체의 단어 검사가 되어 곧 꺼진다.
	i := strings.Index(body, "<form")
	j := strings.Index(body, "</form>")
	if i < 0 || j < i {
		t.Fatalf("조회 폼을 찾지 못했다: %.300s", body)
	}
	form := body[i:j]
	for _, banned := range []string{
		"account", "bank", "예금주", "계좌", "refund",
		"receiver_name", "receiver_phone", "address1", "address2", "postcode", "status",
	} {
		if strings.Contains(form, banned) {
			t.Errorf("비회원 조회 폼에 %q 가 있다", banned)
		}
	}
	// 검사가 헛돌지 않는지: 있어야 하는 것은 있는가.
	for _, want := range []string{"order_no", "phone"} {
		if !strings.Contains(form, want) {
			t.Errorf("조회 폼에 %q 가 없다 — 폼을 잘못 잘랐다", want)
		}
	}
}
