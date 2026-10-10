package app

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/content"
)

// **공개 목록은 다음 쪽으로 갈 수 있다** (D12 P-301·P-305·P-501·P-212).
//
// 앞 판의 상품 목록·상품 검색·주문 내역·통합 검색은 `?page=` 를 읽기만 하고
// 쪽 이동 링크를 그리지 않았다. 한 쪽은 20개라, 21번째 상품부터는 주소를 직접
// 고치지 않는 한 방문자에게 보이지 않았다.
func TestShopListsLinkToTheirNextPage(t *testing.T) {
	srv, pool, _ := shopSite(t) // 티셔츠 한 개
	seedProducts(t, pool, 21)   // 합 22개 — 한 쪽 20개를 넘는다
	c := client()

	_, first := mustGet(t, c, srv.URL+"/shop?sort=name")
	if !strings.Contains(first, `href="/shop?page=2&amp;sort=name"`) {
		t.Errorf("상품 목록 1쪽에 정렬을 지킨 「다음」이 없다:\n%s", first)
	}
	if strings.Contains(first, ">이전<") {
		t.Error("1쪽에 「이전」이 있다")
	}
	_, second := mustGet(t, c, srv.URL+"/shop?sort=name&page=2")
	if !strings.Contains(second, `href="/shop?sort=name"`) || strings.Contains(second, ">다음<") {
		t.Errorf("상품 목록 2쪽(마지막)은 「이전」만 있어야 한다:\n%s", second)
	}

	_, found := mustGet(t, c, srv.URL+"/shop/search?q="+url.QueryEscape("상품"))
	if !strings.Contains(found, `href="/shop/search?page=2&amp;q=`) {
		t.Errorf("상품 검색 1쪽에 검색어를 지킨 「다음」이 없다:\n%s", found)
	}
}

func TestMyOrdersLinkToTheNextPage(t *testing.T) {
	srv, pool, _ := shopSite(t)
	userID := mkUser(t, pool, "buyer@example.com")
	for i := range 21 {
		if _, err := pool.Exec(context.Background(), `
			INSERT INTO orders (order_no, user_id, status, receiver_name, receiver_phone, postcode,
			                    address1, orderer_email, orderer_phone, total_amount, created_at)
			VALUES ($1, $2, '결제대기', '받는이', '010-0000-0000', '12345', '서울',
			        'buyer@example.com', '010-1111-1111', 15000, now() - make_interval(secs => $3))`,
			fmt.Sprintf("MY%04d", i), userID, i); err != nil {
			t.Fatal(err)
		}
	}
	c := client()
	login(t, srv.URL, "buyer@example.com", c)
	_, body := mustGet(t, c, srv.URL+"/orders")
	if !strings.Contains(body, `href="/orders?page=2"`) {
		t.Errorf("주문 내역 1쪽에 「다음」이 없다:\n%s", body)
	}
}

func TestSiteSearchLinksToTheNextPage(t *testing.T) {
	srv, pool := liveSite(t)
	boardID := mkBoard(t, pool, "free", content.PresetPublic)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO posts (board_id, title, body)
		SELECT $1, '검색글 ' || g, '본문' FROM generate_series(1, 25) g`, boardID); err != nil {
		t.Fatal(err)
	}
	_, body := mustGet(t, client(), srv.URL+"/search?q="+url.QueryEscape("검색글"))
	if !strings.Contains(body, `href="/search?page=2&amp;q=`) && !strings.Contains(body, `href="/search?page=2&q=`) {
		t.Errorf("통합 검색 1쪽에 「다음」이 없다:\n%s", body)
	}
}

// **쪽 번호가 커도 500 이 아니다.** 앞 판은 쪽 번호에 상한이 없어, OFFSET 을
// int32 로 바꿀 때 넘쳐 음수가 되고 PostgreSQL 이 거부했다 — 주소 한 줄로 500.
func TestHugePageNumbersDoNotBreakPublicLists(t *testing.T) {
	srv, _, _ := shopSite(t)
	c := client()
	for _, path := range []string{
		"/shop?page=99999999999",
		"/shop/search?q=x&page=99999999999",
	} {
		if code, _ := mustGet(t, c, srv.URL+path); code != http.StatusOK {
			t.Errorf("%s = HTTP %d, want 200", path, code)
		}
	}
}

func TestHugePageNumbersDoNotBreakAdminLists(t *testing.T) {
	srv, _, c := shopAdminSite(t)
	for _, path := range []string{
		"/admin/products?page=150000001", // (쪽-1)×20 이 int32 를 넘겨 음수가 되는 값
		"/admin/orders?page=150000001",
		"/admin/webhooks?page=30000001", // (쪽-1)×100
		"/admin/users?page=50000000",    // 쪽×50 (0부터 센다)
		"/admin/oplog?page=30000000",    // 쪽×100
	} {
		if code, _ := mustGet(t, c, srv.URL+path); code != http.StatusOK {
			t.Errorf("%s = HTTP %d, want 200", path, code)
		}
	}
}
