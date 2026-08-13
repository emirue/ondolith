package app

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// send is like post but for the methods D11 assigns to P-403/P-404.
// PATCH·DELETE 를 걷는 테스트가 저장소에 하나도 없었다 — 그래서 이 두 화면의
// 분기는 전부 검토 없이 지나갔다.
func send(t *testing.T, c *http.Client, method, u string, form url.Values) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, u, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// cartItem reads the one row the fixture puts in the cart. 화면이 아니라 표를
// 본다 — 「400 이 났다」와 「행이 남아 있다」는 다른 주장이고, 이 버그는 두 번째
// 쪽에서만 보인다. 응답만 보면 삭제된 뒤에도 통과한다.
func cartItem(t *testing.T, pool *pgxpool.Pool) (id string, qty int, exists bool) {
	t.Helper()
	err := pool.QueryRow(context.Background(),
		`SELECT id::text, quantity FROM cart_items`).Scan(&id, &qty)
	if err != nil {
		return "", 0, false
	}
	return id, qty, true
}

// P-403 은 수량만 바꾼다. **0 이하·비정수는 400 이다** (D19 P-403 오류 응답표) —
// 삭제는 P-404(DELETE)가 한다.
//
// 앞선 판은 `qty, _ = strconv.Atoi(...)` 로 파싱 오류를 삼켰다. `quantity=abc`
// 가 0 이 되고, 0 은 UpdateCartItem 의 삭제 경로였다 — 오타 한 번에 항목이
// 사라지는데 화면은 성공으로 보였다.
func TestCartQuantityRejectsBadInputWithoutDeletingTheItem(t *testing.T) {
	srv, pool, variant := shopSite(t)
	c := client()

	resp := send(t, c, http.MethodPost, srv.URL+"/cart/items", url.Values{
		"variant_id": {variant}, "quantity": {"2"},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("담기 HTTP %d", resp.StatusCode)
	}
	id, qty, ok := cartItem(t, pool)
	if !ok || qty != 2 {
		t.Fatalf("담기 뒤 장바구니 상태가 예상과 다르다 (있음=%v 수량=%d)", ok, qty)
	}

	for _, c1 := range []struct{ name, quantity string }{
		{"비정수", "abc"},
		{"0", "0"},
		{"음수", "-1"},
		{"빈 값", ""},
		{"소수", "1.5"},
		// 앞뒤 공백은 값이 아니라 입력 방식의 문제다 — 잘라 낸 뒤 판정한다.
		{"공백뿐", "   "},
	} {
		t.Run(c1.name, func(t *testing.T) {
			r := send(t, c, http.MethodPatch, srv.URL+"/cart/items/"+id,
				url.Values{"quantity": {c1.quantity}})
			r.Body.Close()
			if r.StatusCode != http.StatusBadRequest {
				t.Errorf("quantity=%q → HTTP %d, 기대 400", c1.quantity, r.StatusCode)
			}
			// **이것이 이 테스트의 이유다.** 400 을 돌려주면서 행을 지우면
			// 위 단언만으로는 통과한다.
			gotID, gotQty, exists := cartItem(t, pool)
			if !exists {
				t.Fatalf("quantity=%q 가 거부됐는데 항목이 사라졌다", c1.quantity)
			}
			if gotID != id || gotQty != 2 {
				t.Errorf("quantity=%q 가 거부됐는데 항목이 바뀌었다 (id=%s 수량=%d)",
					c1.quantity, gotID, gotQty)
			}
		})
	}
}

// 거부가 넓게 잡혀 정상 변경까지 막으면 화면이 죽는다. 같은 자리에서 잠근다.
func TestCartQuantityAcceptsAValidChange(t *testing.T) {
	srv, pool, variant := shopSite(t)
	c := client()

	send(t, c, http.MethodPost, srv.URL+"/cart/items",
		url.Values{"variant_id": {variant}, "quantity": {"1"}}).Body.Close()
	id, _, ok := cartItem(t, pool)
	if !ok {
		t.Fatal("담기가 되지 않았다")
	}

	r := send(t, c, http.MethodPatch, srv.URL+"/cart/items/"+id,
		url.Values{"quantity": {"3"}})
	r.Body.Close()
	if r.StatusCode != http.StatusSeeOther {
		t.Fatalf("정상 변경 HTTP %d, 기대 303", r.StatusCode)
	}
	if _, qty, _ := cartItem(t, pool); qty != 3 {
		t.Errorf("수량이 %d 다 — 3 이어야 한다", qty)
	}
}

// **삭제는 DELETE 만 한다** (P-404). 수량 0 으로 지우던 경로를 막았으므로,
// 진짜 삭제 경로가 여전히 도는지 같이 확인한다 — 아니면 지울 방법이 없어진다.
func TestCartDeleteStillRemovesTheItem(t *testing.T) {
	srv, pool, variant := shopSite(t)
	c := client()

	send(t, c, http.MethodPost, srv.URL+"/cart/items",
		url.Values{"variant_id": {variant}, "quantity": {"2"}}).Body.Close()
	id, _, ok := cartItem(t, pool)
	if !ok {
		t.Fatal("담기가 되지 않았다")
	}

	r := send(t, c, http.MethodDelete, srv.URL+"/cart/items/"+id, nil)
	r.Body.Close()
	if r.StatusCode != http.StatusSeeOther {
		t.Fatalf("삭제 HTTP %d, 기대 303", r.StatusCode)
	}
	if _, _, exists := cartItem(t, pool); exists {
		t.Error("DELETE 했는데 항목이 남아 있다")
	}
}

// 재고 초과는 파싱이 아니라 재고 판정이다 — 새 가드가 그 경로를 가로채
// 삼키면 「재고가 부족합니다」가 사라진다.
//
// **400 과 422 의 경계가 여기서 갈린다** (D19 0.3): `abc` 는 읽지 못하므로 400,
// `999` 는 읽었는데 재고가 5 라 받아들일 수 없으므로 422 다. D19 P-403 의 재고
// 행은 오래 `400` 으로 적혀 있었고 구현은 네 화면 모두 422 였다 — 표가 0.3 의
// 규약을 어긴 쪽이었고, 이제 checkdocs 30-1 이 그 어긋남을 잡는다.
func TestCartQuantityOverStockStillReportsStock(t *testing.T) {
	srv, pool, variant := shopSite(t)
	c := client()

	send(t, c, http.MethodPost, srv.URL+"/cart/items",
		url.Values{"variant_id": {variant}, "quantity": {"1"}}).Body.Close()
	id, _, ok := cartItem(t, pool)
	if !ok {
		t.Fatal("담기가 되지 않았다")
	}

	// 픽스처의 재고는 5 다.
	r := send(t, c, http.MethodPatch, srv.URL+"/cart/items/"+id,
		url.Values{"quantity": {"999"}})
	r.Body.Close()
	if r.StatusCode == http.StatusBadRequest {
		t.Fatal("재고 초과가 400 으로 나왔다 — 수량 파싱 가드가 재고 판정을 가로챘다")
	}
	if r.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("재고 초과 HTTP %d, 기대 422", r.StatusCode)
	}
	if _, qty, _ := cartItem(t, pool); qty != 1 {
		t.Errorf("재고 초과가 거부됐는데 수량이 %d 로 바뀌었다", qty)
	}
}
