package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// adminPostForm 은 로그인한 관리자로 폼을 보낸다. Origin 은 교차 출처 검사가 본다.
func adminPostForm(t *testing.T, c *http.Client, srv *httptest.Server, path string, form url.Values) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", srv.URL)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(b)
}

// **A-502 에서 고른 카테고리가 P-302 에 나온다** (FR-615).
//
// `product_categories` 를 쓰는 코드가 없어서, A-509 로 카테고리를 만들어도
// `/shop/c/{slug}` 는 언제나 비어 있었다. 저장 함수만 부르면 폼 필드 이름이
// 어긋난 것을 못 보므로 **관리자 화면의 POST 부터 공개 화면의 GET 까지** 간다.
func TestProductCategoriesChosenOnA502ShowOnP302(t *testing.T) {
	srv, pool, c := shopAdminSite(t)
	ctx := context.Background()
	productID := seedProducts(t, pool, 1) // 상품00, slug p00

	cat := map[string]string{}
	for _, slug := range []string{"mat", "lamp"} {
		var id string
		if err := pool.QueryRow(ctx,
			`INSERT INTO categories (name, slug) VALUES ($1, $1) RETURNING id`, slug).Scan(&id); err != nil {
			t.Fatal(err)
		}
		cat[slug] = id
	}
	base := url.Values{"name": {"상품00"}, "slug": {"p00"}, "base_price": {"1000"}, "is_visible": {"on"}}
	save := func(categoryIDs ...string) (int, string) {
		form := url.Values{"category_id": categoryIDs}
		for k, v := range base {
			form[k] = v
		}
		return adminPostForm(t, c, srv, "/admin/products/"+productID, form)
	}
	listed := func(slug string) bool {
		code, body := mustGet(t, http.DefaultClient, srv.URL+"/shop/c/"+slug)
		if code != http.StatusOK {
			t.Fatalf("P-302 /shop/c/%s = HTTP %d", slug, code)
		}
		return strings.Contains(body, `href="/shop/p/p00"`)
	}

	// 지정 전에는 어느 카테고리에도 없다 — 아래 단언이 헛돌지 않는다는 것.
	if listed("mat") {
		t.Fatal("지정하기 전인데 P-302 에 나온다")
	}

	if code, body := save(cat["mat"]); code != http.StatusSeeOther {
		t.Fatalf("저장 = HTTP %d, want 303: %s", code, body)
	}
	if !listed("mat") {
		t.Error("mat 을 골랐는데 /shop/c/mat 에 없다")
	}
	if listed("lamp") {
		t.Error("고르지 않은 /shop/c/lamp 에 나온다")
	}
	// 편집 화면을 다시 열면 고른 것이 체크돼 있다. 아니면 그대로 저장하는
	// 순간 지정이 지워진다.
	_, form := mustGet(t, c, srv.URL+"/admin/products/"+productID)
	if !strings.Contains(form, `value="`+cat["mat"]+`" checked`) {
		t.Error("A-502 가 저장된 카테고리를 체크해 그리지 않는다")
	}
	if strings.Contains(form, `value="`+cat["lamp"]+`" checked`) {
		t.Error("A-502 가 고르지 않은 카테고리를 체크해 그린다")
	}

	// 없는 카테고리는 422 이고, **기존 지정과 다른 필드가 그대로다** (한 트랜잭션).
	base["name"] = []string{"바뀐 이름"}
	if code, _ := save(cat["lamp"], "00000000-0000-0000-0000-000000000000"); code != http.StatusUnprocessableEntity {
		t.Errorf("없는 카테고리 = HTTP %d, want 422", code)
	}
	if code, _ := save("not-a-uuid"); code != http.StatusUnprocessableEntity {
		t.Errorf("형식이 깨진 카테고리 = HTTP %d, want 422", code)
	}
	var name string
	if err := pool.QueryRow(ctx, `SELECT name FROM products WHERE id = $1`, productID).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "상품00" {
		t.Errorf("거부된 저장이 이름을 %q 로 바꿨다", name)
	}
	if !listed("mat") || listed("lamp") {
		t.Error("거부된 저장이 카테고리 지정을 바꿨다")
	}
	base["name"] = []string{"상품00"}

	// 둘 다 고르면 둘 다에 나온다 (복수 지정).
	if code, _ := save(cat["mat"], cat["lamp"]); code != http.StatusSeeOther {
		t.Fatalf("복수 지정 = HTTP %d", code)
	}
	if !listed("mat") || !listed("lamp") {
		t.Error("둘을 골랐는데 한쪽에 없다")
	}

	// 하나도 안 고르고 저장하면 미분류다: P-302 에서 빠지고 P-301 에는 남는다.
	if code, _ := save(); code != http.StatusSeeOther {
		t.Fatalf("지정 해제 = HTTP %d", code)
	}
	if listed("mat") || listed("lamp") {
		t.Error("지정을 지웠는데 P-302 에 남아 있다")
	}
	if _, body := mustGet(t, http.DefaultClient, srv.URL+"/shop"); !strings.Contains(body, `href="/shop/p/p00"`) {
		t.Error("미분류 상품이 P-301 에서 사라졌다")
	}

	// 새 상품도 만들 때 함께 지정된다.
	code, body := adminPostForm(t, c, srv, "/admin/products/new", url.Values{
		"name": {"새 상품"}, "slug": {"fresh"}, "base_price": {"500"}, "is_visible": {"on"},
		"category_id": {cat["lamp"]}})
	if code != http.StatusSeeOther {
		t.Fatalf("생성 = HTTP %d: %s", code, body)
	}
	if _, body := mustGet(t, http.DefaultClient, srv.URL+"/shop/c/lamp"); !strings.Contains(body, `href="/shop/p/fresh"`) {
		t.Error("만들 때 고른 카테고리의 P-302 에 새 상품이 없다")
	}
	// 없는 카테고리로 만들기를 거부하면 상품도 생기지 않는다.
	code, _ = adminPostForm(t, c, srv, "/admin/products/new", url.Values{
		"name": {"유령"}, "slug": {"ghost"}, "base_price": {"500"},
		"category_id": {"00000000-0000-0000-0000-000000000000"}})
	if code != http.StatusUnprocessableEntity {
		t.Errorf("없는 카테고리로 생성 = HTTP %d, want 422", code)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM products WHERE slug = 'ghost'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("거부된 생성이 상품 행을 남겼다")
	}
}
