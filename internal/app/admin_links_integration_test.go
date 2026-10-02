package app

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// seedProducts 는 상품 n 개와 조합 하나씩을 넣고 가장 오래된 상품의 id 를 준다.
// 목록은 최신순이라 그 상품이 n 번째다.
func seedProducts(t *testing.T, pool *pgxpool.Pool, n int) (oldestID string) {
	t.Helper()
	ctx := context.Background()
	for i := range n {
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO products (slug, name, base_price, is_visible, created_at)
			VALUES ($1, $2, 1000, true, now() - make_interval(secs => $3))
			RETURNING id`,
			fmt.Sprintf("p%02d", i), fmt.Sprintf("상품%02d", i), i).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO product_variants (product_id, option_values, price_delta, stock)
			VALUES ($1, '{"크기":"L"}', 0, 5)`, id); err != nil {
			t.Fatal(err)
		}
		oldestID = id
	}
	return oldestID
}

// seedOrders 는 주문 n 건을 넣는다. 번호는 AD0000 부터이고 뒤 번호일수록 오래됐다.
func seedOrders(t *testing.T, pool *pgxpool.Pool, n int) {
	t.Helper()
	for i := range n {
		if _, err := pool.Exec(context.Background(), `
			INSERT INTO orders (order_no, status, receiver_name, receiver_phone, postcode,
			                    address1, orderer_email, orderer_phone, total_amount, created_at)
			VALUES ($1, '결제대기', '받는이', '010-0000-0000', '12345', '서울',
			        'a@example.com', '010-1111-1111', 15000, now() - make_interval(secs => $2))`,
			fmt.Sprintf("AD%04d", i), i); err != nil {
			t.Fatal(err)
		}
	}
}

// **이미 있는 상품·주문에 링크로 닿는다** (FR-706, W3-42, GAP-14).
//
// 문서 검사(28c)는 경로의 모양만 봐서 「새 상품」 링크와 A-502 자신의 폼
// `action` 을 「A-502 를 가리키는 링크」로 셌다. 실제로는 목록 행에 링크가 없어
// 이미 만든 상품의 편집 화면은 주소를 직접 쳐야만 열렸고, A-516 은 들어오는
// 링크가 하나도 없었다. 그래서 **실제 행을 넣고 렌더한 화면에서** 링크를 본다.
func TestAdminListsLinkToTheirDetailScreens(t *testing.T) {
	srv, pool, c := shopAdminSite(t)
	productID := seedProducts(t, pool, 1)
	seedOrders(t, pool, 1)

	_, body := mustGet(t, c, srv.URL+"/admin/products")
	if want := `<a href="/admin/products/` + productID + `">상품00</a>`; !strings.Contains(body, want) {
		t.Errorf("A-501 행에 A-502 로 가는 링크 %s 가 없다", want)
	}
	// 링크가 가리키는 화면이 실제로 열린다.
	if code, _ := mustGet(t, c, srv.URL+"/admin/products/"+productID); code != http.StatusOK {
		t.Errorf("A-502 = HTTP %d", code)
	}

	code, body := mustGet(t, c, srv.URL+"/admin/orders/AD0000")
	if code != http.StatusOK {
		t.Fatalf("A-505 = HTTP %d", code)
	}
	if want := `href="/admin/orders/AD0000/pick"`; !strings.Contains(body, want) ||
		!strings.Contains(body, "피킹 대조") {
		t.Errorf("A-505 에 A-516 「피킹 대조」 링크 %s 가 없다", want)
	}
}

// **재고 화면(A-517)이 실제로 그려지고, 행의 폼으로 입고·조사가 끝난다** (W3-44).
//
// 핸들러 테스트는 화면이 받은 값만 본다. 여기서는 렌더된 HTML 을 본다: 정확히
// 한 조합에 맞으면 그 행의 입고 칸에 `autofocus` 가 있고, 실사 폼에는 장부 입력
// 칸이 없이 행이 읽은 재고가 숨은 값으로 실린다. A-501·A-502 의 「재고」 링크가
// 그 상품으로 좁혀 연다.
func TestStockScreenRendersRowForms(t *testing.T) {
	srv, pool, c := shopAdminSite(t)
	ctx := context.Background()
	productID := seedProducts(t, pool, 1)
	var variantID string
	if err := pool.QueryRow(ctx, `
		UPDATE product_variants SET sku = 'SKU-00' WHERE product_id = $1 RETURNING id`,
		productID).Scan(&variantID); err != nil {
		t.Fatal(err)
	}
	stockLink := `href="/admin/stock?product=` + productID + `"`

	// 메뉴에 「재고」가 있고, A-501·A-502 가 그 상품의 재고로 간다.
	_, body := mustGet(t, c, srv.URL+"/admin/products")
	if !strings.Contains(body, `href="/admin/stock"`) {
		t.Error("메뉴에 「재고」(/admin/stock) 가 없다")
	}
	if strings.Contains(body, "/admin/scan/") {
		t.Error("옛 /admin/scan/ 경로가 화면에 남아 있다")
	}
	if !strings.Contains(body, stockLink) {
		t.Errorf("A-501 행에 재고 링크 %s 가 없다", stockLink)
	}
	if _, body := mustGet(t, c, srv.URL+"/admin/products/"+productID); !strings.Contains(body, stockLink) {
		t.Errorf("A-502 에 재고 링크 %s 가 없다", stockLink)
	}

	// 상품으로 좁힌 목록: 행의 폼이 목록 상태를 싣고, 포커스는 검색창에 있다.
	code, body := mustGet(t, c, srv.URL+"/admin/stock?product="+productID)
	if code != http.StatusOK {
		t.Fatalf("A-517 = HTTP %d", code)
	}
	for _, want := range []string{
		`action="/admin/stock/receive"`, `action="/admin/stock/stocktake"`,
		`<input type="hidden" name="variant_id" value="` + variantID + `">`,
		`<input type="hidden" name="ledger" value="5">`,
		`<input type="hidden" name="product" value="` + productID + `">`,
		`name="memo" maxlength="200"`, "상품00", "SKU-00",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("A-517 에 %s 가 없다", want)
		}
	}
	// **장부 입력 칸이 없다** — ledger 는 숨은 값 하나뿐이다.
	if n := strings.Count(body, `name="ledger"`); n != 1 {
		t.Errorf(`name="ledger" 가 %d 번 나온다, want 숨은 값 1번`, n)
	}
	if !strings.Contains(body, `placeholder="상품명 · SKU · 바코드 · QR" autofocus>`) {
		t.Error("목록 화면에서 검색창에 포커스가 없다")
	}
	if strings.Count(body, "autofocus") != 1 {
		t.Errorf("autofocus 가 %d 개다, want 1", strings.Count(body, "autofocus"))
	}

	// 스캔 흐름: SKU 가 정확히 한 조합에 맞으면 그 행의 입고 칸에 포커스가 가고,
	// 행의 폼은 검색어를 싣지 않는다.
	_, body = mustGet(t, c, srv.URL+"/admin/stock?q=SKU-00")
	if !strings.Contains(body, `placeholder="수량" style="width:6em" autofocus>`) {
		t.Error("정확히 한 조합인데 입고 수량 칸에 autofocus 가 없다")
	}
	if strings.Count(body, "autofocus") != 1 {
		t.Errorf("autofocus 가 %d 개다, want 1 (입고 칸)", strings.Count(body, "autofocus"))
	}
	if strings.Contains(body, `<input type="hidden" name="q"`) {
		t.Error("스캔 흐름의 행 폼이 검색어를 실었다 — 돌아오면 검색창이 비어 있어야 한다")
	}

	// 맞는 것이 없으면 오류가 아니라 빈 결과다.
	code, body = mustGet(t, c, srv.URL+"/admin/stock?q=not-a-uuid")
	if code != http.StatusOK || !strings.Contains(body, "일치하는 조합이 없습니다") {
		t.Errorf("없는 검색어 = HTTP %d, 빈 결과 문구가 없다", code)
	}
}

// **A-503 에 바코드 칸이, A-516 에 기본값 1 인 수량 칸이 그려진다** (FR-627, W3-43).
//
// 핸들러 테스트는 폼 값을 직접 보내므로 화면에 그 칸이 없어도 통과한다.
func TestBarcodeAndPickQuantityFieldsRender(t *testing.T) {
	srv, pool, c := shopAdminSite(t)
	ctx := context.Background()
	productID := seedProducts(t, pool, 1)
	seedOrders(t, pool, 1)
	var variantID string
	if err := pool.QueryRow(ctx, `
		UPDATE product_variants SET barcode = '8801234567890' WHERE product_id = $1 RETURNING id`,
		productID).Scan(&variantID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO order_items (order_id, product_id, variant_id, product_name, unit_price, quantity)
		SELECT id, $1, $2, '상품00', 1000, 2 FROM orders WHERE order_no = 'AD0000'`,
		productID, variantID); err != nil {
		t.Fatal(err)
	}

	code, body := mustGet(t, c, srv.URL+"/admin/products/"+productID+"/variants")
	if want := `name="barcode_` + variantID + `" maxlength="64" value="8801234567890"`; code != http.StatusOK || !strings.Contains(body, want) {
		t.Errorf("A-503 = HTTP %d, 바코드 칸 %s 가 없다", code, want)
	}

	code, body = mustGet(t, c, srv.URL+"/admin/orders/AD0000/pick")
	if want := `<input type="number" name="quantity" min="1" step="1" value="1" required>`; code != http.StatusOK || !strings.Contains(body, want) {
		t.Errorf("A-516 = HTTP %d, 수량 칸 %s 가 없다", code, want)
	}
}

// **21번째 상품·주문에 이전·다음 링크로 닿는다** (FR-706, W3-42).
//
// 서버는 20건씩 나누는데 화면에 링크가 없어 21번째부터는 `?page=2` 를 직접
// 쳐야 했다. 마지막 쪽에는 「다음」이 없다 — 조건 없이 그리면 빈 쪽이 끝없이
// 이어진다.
func TestAdminListsReachTheTwentyFirstRow(t *testing.T) {
	srv, pool, c := shopAdminSite(t)
	lastProduct := seedProducts(t, pool, 21)
	seedOrders(t, pool, 21)

	for _, tc := range []struct{ path, last string }{
		{"/admin/products", `/admin/products/` + lastProduct + `"`},
		{"/admin/orders", `/admin/orders/AD0020"`},
	} {
		_, first := mustGet(t, c, srv.URL+tc.path)
		next := `<a href="` + tc.path + `?page=2" aria-label="다음">`
		if !strings.Contains(first, next) {
			t.Errorf("%s: 1쪽에 다음 링크 %s 가 없다", tc.path, next)
		}
		if strings.Contains(first, `aria-label="이전"`) {
			t.Errorf("%s: 1쪽에 이전 링크가 있다", tc.path)
		}
		if strings.Contains(first, tc.last) {
			t.Errorf("%s: 21번째 행이 1쪽에 있다 — 검사가 헛돌았다", tc.path)
		}

		_, second := mustGet(t, c, srv.URL+tc.path+"?page=2")
		if !strings.Contains(second, tc.last) {
			t.Errorf("%s: 2쪽에 21번째 행(%s)이 없다", tc.path, tc.last)
		}
		if prev := `<a href="` + tc.path + `" aria-label="이전">`; !strings.Contains(second, prev) {
			t.Errorf("%s: 2쪽에 이전 링크 %s 가 없다", tc.path, prev)
		}
		if strings.Contains(second, `aria-label="다음"`) {
			t.Errorf("%s: 마지막 쪽에 다음 링크가 있다", tc.path)
		}
	}

	// 상태 필터가 다음 쪽으로 따라간다. 빠지면 2쪽에서 필터가 풀린다.
	_, body := mustGet(t, c, srv.URL+"/admin/orders?status=결제대기")
	if want := `href="/admin/orders?page=2&amp;status=%ea%b2%b0%ec%a0%9c%eb%8c%80%ea%b8%b0"`; !strings.Contains(strings.ToLower(body), want) {
		t.Errorf("A-504 다음 링크가 상태 필터를 싣지 않는다 (want %s)", want)
	}
}
