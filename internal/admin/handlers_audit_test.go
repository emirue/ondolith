package admin

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// 가격은 integer 컬럼에 int32 변환을 거쳐 들어간다. 범위를 넘는 값을 그대로
// 넘기면 4294967297 이 1원으로 저장됐다 (2026-10 감사 실측) — 422 로 거부한다.
func TestProductSaveRefusesPricesTheColumnCannotHold(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"product.manage": true},
		id: "u1", email: "op@example.com"}
	d, pool := fixture(t, caller)
	ctx := context.Background()
	var productID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO products (slug,name,base_price,is_visible)
		 VALUES ('tee','티셔츠',12000,true) RETURNING id`).Scan(&productID); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{"4294967297", "2000000001", "2147483648"} {
		// 수정
		rec := postAdmin(t, d.ProductSave, "/admin/products/"+productID,
			map[string]string{"id": productID},
			url.Values{"name": {"티셔츠"}, "slug": {"tee"}, "base_price": {bad}})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("수정: 가격 %s = HTTP %d, want 422", bad, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "20억") {
			t.Errorf("수정: 가격 %s 의 오류 문구에 상한이 없다: %q", bad, rec.Body.String())
		}
		// 생성
		rec = postAdmin(t, d.ProductSave, "/admin/products/new",
			map[string]string{"id": "new"},
			url.Values{"name": {"새 상품"}, "slug": {"new-" + bad}, "base_price": {bad}})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("생성: 가격 %s = HTTP %d, want 422", bad, rec.Code)
		}
	}
	var n, price int
	if err := pool.QueryRow(ctx,
		`SELECT count(*), max(base_price) FROM products`).Scan(&n, &price); err != nil {
		t.Fatal(err)
	}
	if n != 1 || price != 12000 {
		t.Errorf("상품 %d개·가격 %d — 거부된 값이 저장됐다 (want 1개·12000)", n, price)
	}

	// 상한 그 자체는 들어간다.
	rec := postAdmin(t, d.ProductSave, "/admin/products/"+productID,
		map[string]string{"id": productID},
		url.Values{"name": {"티셔츠"}, "slug": {"tee"}, "base_price": {"2000000000"}})
	if rec.Code != http.StatusSeeOther {
		t.Errorf("상한값 = HTTP %d (%q), want 303", rec.Code, rec.Body.String())
	}
}

// A-503: 가격 차액·재고 증감도 같은 컬럼 폭이고, SKU 는 64자다. SKU 가 길면
// 「재고가 0 보다 작아집니다」가 나왔다 — CHECK 위반을 전부 재고 부족으로 읽었다.
func TestVariantSaveRefusesOutOfRangeValuesAndLongSku(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"product.manage": true},
		id: "u1", email: "op@example.com"}
	d, pool := fixture(t, caller)
	ctx := context.Background()
	var productID, variantID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO products (slug,name,base_price,is_visible)
		 VALUES ('tee','티셔츠',12000,true) RETURNING id`).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO product_variants (product_id,option_values,price_delta,stock)
		 VALUES ($1,'{"크기":"L"}',0,7) RETURNING id`, productID).Scan(&variantID); err != nil {
		t.Fatal(err)
	}
	save := func(delta, priceDelta, sku string) (int, string) {
		rec := postAdmin(t, d.VariantSave, "/admin/products/"+productID+"/variants",
			map[string]string{"id": productID},
			url.Values{"variant_id": {variantID}, "delta_" + variantID: {delta},
				"price_delta_" + variantID: {priceDelta}, "sku_" + variantID: {sku}})
		return rec.Code, rec.Body.String()
	}

	for name, c := range map[string]struct{ delta, priceDelta, sku, wantMsg string }{
		"가격 차액 초과":  {"0", "4294967297", "", "가격 차액"},
		"가격 차액 음수쪽": {"0", "-2000000001", "", "가격 차액"},
		"재고 증감 초과":  {"4294967297", "0", "", "재고 증감"},
		"긴 SKU":     {"0", "0", strings.Repeat("S", 65), "SKU"},
	} {
		code, body := save(c.delta, c.priceDelta, c.sku)
		if code != http.StatusUnprocessableEntity {
			t.Errorf("%s = HTTP %d, want 422", name, code)
		}
		if !strings.Contains(body, c.wantMsg) || strings.Contains(body, "백오더") {
			t.Errorf("%s 의 오류 문구가 원인을 말하지 않는다: %q", name, body)
		}
	}
	var stock, priceDelta int
	var sku *string
	if err := pool.QueryRow(ctx, `SELECT stock, price_delta, sku FROM product_variants WHERE id = $1`,
		variantID).Scan(&stock, &priceDelta, &sku); err != nil {
		t.Fatal(err)
	}
	if stock != 7 || priceDelta != 0 || sku != nil {
		t.Errorf("거부된 값이 저장됐다: 재고 %d, 차액 %d, SKU %v", stock, priceDelta, sku)
	}

	// 64자 SKU 는 들어간다.
	if code, body := save("0", "0", strings.Repeat("S", 64)); code != http.StatusSeeOther {
		t.Errorf("64자 SKU = HTTP %d (%q), want 303", code, body)
	}
}
