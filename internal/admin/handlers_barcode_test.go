package admin

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emirue/ondolith/internal/commerce"
)

// lastLog 는 가장 최근 작업 로그의 요약이다. 없으면 빈 문자열.
func lastLog(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var s string
	_ = pool.QueryRow(context.Background(),
		`SELECT summary FROM operation_logs ORDER BY created_at DESC, id DESC LIMIT 1`).Scan(&s)
	return s
}

// pickFixture 는 「티셔츠 L」 2개짜리 결제된 주문과 그 조합의 id 를 준다.
// 조합에는 SKU 와 바코드가 붙어 있다.
func pickFixture(t *testing.T) (d *Deps, pool *pgxpool.Pool, orderNo, variantID string) {
	t.Helper()
	caller := &fakeCaller{perms: map[string]bool{"order.update": true}, email: "op@example.com"}
	d, pool = fixture(t, caller)
	order, _ := paidAdminOrder(t, d, pool)
	if err := pool.QueryRow(context.Background(), `
		UPDATE product_variants SET sku = 'SKU-TEE', barcode = '8801234567890'
		WHERE id = (SELECT variant_id FROM order_items oi JOIN orders o ON o.id = oi.order_id
		            WHERE o.order_no = $1) RETURNING id`, order.OrderNo).Scan(&variantID); err != nil {
		t.Fatal(err)
	}
	return d, pool, order.OrderNo, variantID
}

// pick 은 A-516 에 한 번 스캔하고, 화면이 받은 대조 현황과 안내 문구를 돌려준다.
func pick(t *testing.T, d *Deps, orderNo string, form url.Values) (code int, tally map[string]int, notice string) {
	t.Helper()
	d.Render = func(w http.ResponseWriter, _ *http.Request, _ string, c int, data any) {
		w.WriteHeader(c)
		m := data.(map[string]any)
		tally, _ = m["Scanned"].(map[string]int)
		notice, _ = m["Notice"].(string)
	}
	rec := postAdmin(t, d.PickCheck, "/admin/orders/"+orderNo+"/pick",
		map[string]string{"no": orderNo}, form)
	return rec.Code, tally, notice
}

// **SKU·바코드로 피킹 대조가 된다** (FR-623, FR-627, W3-43).
//
// 앞 판은 조합 uuid 만 받았는데 uuid 는 관리자 화면 어디에도 표시되지 않는다 —
// QR 을 읽는 스캐너가 없으면 피킹을 할 수 없었다.
func TestPickCheckAcceptsSkuAndBarcode(t *testing.T) {
	d, pool, orderNo, variantID := pickFixture(t)

	// 수량 칸을 보내지 않으면 1개다.
	code, tally, notice := pick(t, d, orderNo, url.Values{"scanned": {"SKU-TEE"}})
	if code != http.StatusOK || tally[variantID] != 1 || notice != "" {
		t.Fatalf("SKU 스캔 = HTTP %d, 대조 %v, 안내 %q — want 200, 1개, 완료 아님", code, tally, notice)
	}
	code, tally, notice = pick(t, d, orderNo, url.Values{
		"count_" + variantID: {"1"}, "scanned": {"8801234567890"}, "quantity": {"1"}})
	if code != http.StatusOK || tally[variantID] != 2 {
		t.Fatalf("바코드 스캔 = HTTP %d, 대조 %v — want 200, 2개", code, tally)
	}
	if !strings.Contains(notice, "대조 완료") {
		t.Errorf("전 품목을 세었는데 안내가 %q 다", notice)
	}
	if got := lastLog(t, pool); !strings.Contains(got, "대조 완료") {
		t.Errorf("대조 완료가 로그에 남지 않았다: %q", got)
	}
	// QR(id)도 여전히 된다.
	if code, tally, _ = pick(t, d, orderNo, url.Values{"scanned": {variantID}}); code != http.StatusOK || tally[variantID] != 1 {
		t.Errorf("QR 스캔 = HTTP %d, 대조 %v", code, tally)
	}
}

// **수량 칸으로 여러 개를 한 번에 세고, 누적이 주문 수량을 넘으면 422 다** (W3-43).
func TestPickCheckCountsAQuantityAndRefusesOverCount(t *testing.T) {
	d, pool, orderNo, variantID := pickFixture(t)

	code, tally, notice := pick(t, d, orderNo, url.Values{"scanned": {"SKU-TEE"}, "quantity": {"2"}})
	if code != http.StatusOK || tally[variantID] != 2 || !strings.Contains(notice, "대조 완료") {
		t.Fatalf("2개를 한 번에 = HTTP %d, 대조 %v, 안내 %q", code, tally, notice)
	}

	for name, form := range map[string]url.Values{
		"한 번에 3개":    {"scanned": {"SKU-TEE"}, "quantity": {"3"}},
		"1개 센 뒤 2개":  {"count_" + variantID: {"1"}, "scanned": {"SKU-TEE"}, "quantity": {"2"}},
		"다 센 뒤 1개 더": {"count_" + variantID: {"2"}, "scanned": {"SKU-TEE"}},
	} {
		code, tally, _ := pick(t, d, orderNo, form)
		if code != http.StatusUnprocessableEntity {
			t.Errorf("%s = HTTP %d, want 422", name, code)
		}
		// 거부된 스캔은 세지 않는다 — 대조 현황은 폼이 실어 온 그대로다.
		carried, _ := strconv.Atoi(form.Get("count_" + variantID))
		if tally[variantID] != carried {
			t.Errorf("%s: 거부됐는데 대조가 %d → %d 로 바뀌었다", name, carried, tally[variantID])
		}
		if got := lastLog(t, pool); !strings.Contains(got, "피킹 대조 거부") || !strings.Contains(got, "주문 수량(2개)") {
			t.Errorf("%s: 거부가 로그에 남지 않았다: %q", name, got)
		}
	}

	// 수량이 숫자가 아니거나 1 미만이면 422 이고 세지 않는다.
	for _, bad := range []string{"0", "-1", "abc"} {
		if code, tally, _ := pick(t, d, orderNo, url.Values{"scanned": {"SKU-TEE"}, "quantity": {bad}}); code != http.StatusUnprocessableEntity || tally[variantID] != 0 {
			t.Errorf("수량 %q = HTTP %d, 대조 %v — want 422, 0개", bad, code, tally)
		}
	}
}

// **둘 이상의 조합에 맞는 값은 422 로 거부되고 로그에 남는다** (D19 A-516).
//
// 하나를 추측해 고르면 오출고가 대조 완료로 기록된다. 상품명으로도 고르지 않는다.
func TestPickCheckRefusesAnAmbiguousCodeAndAName(t *testing.T) {
	d, pool, orderNo, variantID := pickFixture(t)
	ctx := context.Background()
	// 다른 상품의 조합이 이 조합의 SKU 와 같은 바코드를 갖는다 — DB 는 막지 않는다.
	if _, err := pool.Exec(ctx, `
		WITH p AS (INSERT INTO products (slug,name,base_price,is_visible)
		           VALUES ('cap','모자',5000,true) RETURNING id)
		INSERT INTO product_variants (product_id,option_values,price_delta,stock,sku,barcode)
		SELECT id,'{"색":"검정"}',0,3,'SKU-CAP','SKU-TEE' FROM p`); err != nil {
		t.Fatal(err)
	}

	code, tally, _ := pick(t, d, orderNo, url.Values{"scanned": {"SKU-TEE"}})
	if code != http.StatusUnprocessableEntity || tally[variantID] != 0 {
		t.Errorf("모호한 코드 = HTTP %d, 대조 %v — want 422, 0개", code, tally)
	}
	if got := lastLog(t, pool); !strings.Contains(got, "피킹 대조 거부") || !strings.Contains(got, "여러 상품") {
		t.Errorf("모호한 코드의 거부가 로그에 남지 않았다: %q", got)
	}

	// 주문에 없는 조합은 상품명과 함께 거부된다.
	code, _, _ = pick(t, d, orderNo, url.Values{"scanned": {"SKU-CAP"}})
	if got := lastLog(t, pool); code != http.StatusUnprocessableEntity || !strings.Contains(got, "이 주문에 없는 상품입니다: 모자") {
		t.Errorf("주문에 없는 조합 = HTTP %d, 로그 %q", code, got)
	}

	// 상품명 부분 일치로는 세지 않는다 — 이름이 그 조합 하나에만 맞아도.
	code, tally, _ = pick(t, d, orderNo, url.Values{"scanned": {"티셔"}})
	if code != http.StatusUnprocessableEntity || tally[variantID] != 0 {
		t.Errorf("상품명 = HTTP %d, 대조 %v — want 422, 0개", code, tally)
	}
}

// **A-503 이 조합별 바코드를 받고, 중복은 409 다** (FR-627, D19 A-503).
func TestVariantSaveStoresABarcodeAndRefusesADuplicate(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"product.manage": true}, email: "op@example.com"}
	d, pool := fixture(t, caller)
	ctx := context.Background()

	mk := func(slug string) (productID, variantID string) {
		if err := pool.QueryRow(ctx, `INSERT INTO products (slug,name,base_price,is_visible)
			VALUES ($1,$1,1000,true) RETURNING id`, slug).Scan(&productID); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO product_variants (product_id,option_values,price_delta,stock)
			VALUES ($1,'{"크기":"L"}',0,5) RETURNING id`, productID).Scan(&variantID); err != nil {
			t.Fatal(err)
		}
		return productID, variantID
	}
	save := func(productID, variantID, barcode string) (int, string) {
		rec := postAdmin(t, d.VariantSave, "/admin/products/"+productID+"/variants",
			map[string]string{"id": productID}, url.Values{
				"variant_id": {variantID}, "delta_" + variantID: {"0"},
				"price_delta_" + variantID: {"0"}, "barcode_" + variantID: {barcode}})
		return rec.Code, rec.Body.String()
	}
	p1, v1 := mk("tee")
	p2, v2 := mk("cap")

	// 앞뒤 공백은 잘려 저장된다 — 스캐너가 붙인 공백으로 유일 제약을 지나가지 못한다.
	if code, body := save(p1, v1, "  8801234567890 "); code != http.StatusSeeOther {
		t.Fatalf("바코드 저장 = HTTP %d (%s)", code, body)
	}
	found, err := d.Commerce.FindVariants(ctx, commerce.VariantQuery{Q: "8801234567890"})
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Rows) != 1 || found.Rows[0].ID != v1 {
		t.Fatalf("저장한 바코드로 조합을 찾지 못한다: %+v", found.Rows)
	}

	code, body := save(p2, v2, "8801234567890")
	if code != http.StatusConflict || !strings.Contains(body, "이미 쓰이는 바코드") {
		t.Errorf("중복 바코드 = HTTP %d (%s), want 409 「이미 쓰이는 바코드」", code, body)
	}
	if code, _ := save(p2, v2, strings.Repeat("9", 65)); code != http.StatusUnprocessableEntity {
		t.Errorf("65자 바코드 = HTTP %d, want 422", code)
	}
	// 비우면 다른 조합이 그 바코드를 쓸 수 있다.
	if code, _ := save(p1, v1, ""); code != http.StatusSeeOther {
		t.Fatalf("바코드 비우기 = HTTP %d", code)
	}
	if code, body := save(p2, v2, "8801234567890"); code != http.StatusSeeOther {
		t.Errorf("비운 뒤 재사용 = HTTP %d (%s)", code, body)
	}
}
