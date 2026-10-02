package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emirue/ondolith/internal/commerce"
)

// stockFixture 는 재고 10 인 「티셔츠 크기: L」(SKU-TEE · 바코드 8801234567890)과
// 재고 3 인 「티셔츠 모자 색: 검정」을 넣는다. 호출자는 perms 만 가진다.
func stockFixture(t *testing.T, perms ...string) (d *Deps, pool *pgxpool.Pool, tee, cap string) {
	t.Helper()
	have := map[string]bool{}
	for _, p := range perms {
		have[p] = true
	}
	d, pool = fixture(t, &fakeCaller{perms: have, email: "op@example.com"})
	ctx := context.Background()
	mk := func(slug, name, opts, sku, barcode string, stock int) (id string) {
		if err := pool.QueryRow(ctx, `
			WITH p AS (INSERT INTO products (slug,name,base_price,is_visible)
			           VALUES ($1,$2,1000,false) RETURNING id)
			INSERT INTO product_variants (product_id,option_values,price_delta,stock,sku,barcode)
			SELECT id,$3::jsonb,0,$6,NULLIF($4,''),NULLIF($5,'') FROM p RETURNING id`,
			slug, name, opts, sku, barcode, stock).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	tee = mk("tee", "티셔츠", `{"크기":"L"}`, "SKU-TEE", "8801234567890", 10)
	cap = mk("cap", "티셔츠 모자", `{"색":"검정"}`, "", "", 3)
	return d, pool, tee, cap
}

// stockPage 는 A-517 을 열어 화면이 받은 값을 돌려준다.
func stockPage(t *testing.T, d *Deps, target string) (code int, data map[string]any) {
	t.Helper()
	d.Render = func(w http.ResponseWriter, _ *http.Request, _ string, c int, v any) {
		w.WriteHeader(c)
		data = v.(map[string]any)
	}
	rec := httptest.NewRecorder()
	d.Stock(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec.Code, data
}

func rowIDs(data map[string]any) []string {
	rows, _ := data["Rows"].([]commerce.ScannedVariant)
	var out []string
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

func stockOf(t *testing.T, pool *pgxpool.Pool, variantID string) (n int) {
	t.Helper()
	if err := pool.QueryRow(context.Background(),
		`SELECT stock FROM product_variants WHERE id = $1`, variantID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// **스캐너 없이 상품명·SKU·바코드 검색으로 조합을 찾는다** (FR-624, W3-44).
//
// 값이 식별 값으로 정확히 한 조합에 맞으면 그 행의 입고 칸에 포커스가 가고
// 행의 폼은 검색어를 싣지 않는다. 그 밖에는 목록 상태를 싣는다.
func TestStockSearchFindsByNameSkuAndBarcode(t *testing.T) {
	d, _, tee, cap := stockFixture(t, "product.view")

	// 검색어가 없으면 전체다. **숨긴 상품도 나온다** — 창고에는 있다.
	code, data := stockPage(t, d, "/admin/stock")
	if code != http.StatusOK || len(rowIDs(data)) != 2 {
		t.Fatalf("전체 목록 = HTTP %d, %d행 — want 200, 2행", code, len(rowIDs(data)))
	}
	if data["Focus"] != "" || data["CarryPage"] != 1 {
		t.Errorf("전체 목록: Focus=%v CarryPage=%v — 행의 폼이 목록 상태를 실어야 한다", data["Focus"], data["CarryPage"])
	}

	for _, q := range []string{"SKU-TEE", "8801234567890", tee} {
		_, data := stockPage(t, d, "/admin/stock?q="+url.QueryEscape(q))
		if got := rowIDs(data); len(got) != 1 || got[0] != tee {
			t.Errorf("q=%q → %v, want %s 하나", q, got, tee)
		}
		if data["Focus"] != tee {
			t.Errorf("q=%q: Focus=%v — 정확히 한 조합이면 그 행의 입고 칸에 포커스가 간다", q, data["Focus"])
		}
		// 스캔 흐름에서는 행의 폼이 검색어를 싣지 않는다.
		if data["CarryQ"] != nil {
			t.Errorf("q=%q: 행의 폼이 검색어 %v 를 실었다 — 돌아오면 검색창이 비어 있어야 한다", q, data["CarryQ"])
		}
	}

	// 상품명 부분 일치는 여러 행이고, 포커스는 검색창에 남는다.
	_, data = stockPage(t, d, "/admin/stock?q="+url.QueryEscape("티셔츠"))
	if got := rowIDs(data); len(got) != 2 {
		t.Errorf("상품명 검색 → %v, want 두 조합", got)
	}
	if data["Focus"] != "" || data["CarryQ"] != "티셔츠" {
		t.Errorf("상품명 검색: Focus=%v CarryQ=%v", data["Focus"], data["CarryQ"])
	}
	// 이름이 한 조합에만 맞아도 포커스를 옮기지 않는다 — 정확 일치가 아니다.
	_, data = stockPage(t, d, "/admin/stock?q="+url.QueryEscape("모자"))
	if got := rowIDs(data); len(got) != 1 || got[0] != cap || data["Focus"] != "" {
		t.Errorf("이름 한 건: %v Focus=%v", got, data["Focus"])
	}
}

// **맞는 것이 없으면 오류가 아니라 빈 결과다** (W3-44). uuid 가 아닌 값도 같다 —
// 422 는 POST 의 `variant_id` 검증으로 옮겨졌다.
func TestStockSearchWithNoMatchIsAnEmptyResult(t *testing.T) {
	d, _, _, _ := stockFixture(t, "product.view")
	for _, target := range []string{
		"/admin/stock?q=not-a-uuid",
		"/admin/stock?q=00000000-0000-4000-8000-000000000000",
		"/admin/stock?product=not-a-uuid",
		"/admin/stock?product=00000000-0000-4000-8000-000000000000",
	} {
		code, data := stockPage(t, d, target)
		if code != http.StatusOK || len(rowIDs(data)) != 0 {
			t.Errorf("%s = HTTP %d, %d행 — want 200, 빈 결과", target, code, len(rowIDs(data)))
		}
		if data["Error"] != nil {
			t.Errorf("%s: 오류 문구 %v 가 떴다", target, data["Error"])
		}
	}
}

// 상품으로 좁히면 그 상품의 조합만 나오고, 다음 쪽 링크가 조건을 싣는다.
func TestStockNarrowsByProductAndPages(t *testing.T) {
	d, pool, tee, _ := stockFixture(t, "product.view")
	ctx := context.Background()
	var productID string
	if err := pool.QueryRow(ctx,
		`SELECT product_id FROM product_variants WHERE id = $1`, tee).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	_, data := stockPage(t, d, "/admin/stock?product="+productID)
	if got := rowIDs(data); len(got) != 1 || got[0] != tee {
		t.Fatalf("상품으로 좁힘 → %v, want %s", got, tee)
	}
	if data["CarryProduct"] != productID {
		t.Errorf("행의 폼이 product 를 싣지 않는다: %v", data["CarryProduct"])
	}

	// 그 상품에 조합을 20개 더 넣으면 21개 — 다음 쪽이 생긴다.
	if _, err := pool.Exec(ctx, `
		INSERT INTO product_variants (product_id, option_values, price_delta, stock)
		SELECT $1, jsonb_build_object('번호', n::text), n, 0 FROM generate_series(1, 20) n`,
		productID); err != nil {
		t.Fatal(err)
	}
	_, data = stockPage(t, d, "/admin/stock?product="+productID)
	if want := "/admin/stock?page=2&product=" + productID; data["NextURL"] != want {
		t.Errorf("다음 링크 %v, want %s", data["NextURL"], want)
	}
	_, data = stockPage(t, d, "/admin/stock?product="+productID+"&page=2")
	if len(rowIDs(data)) != 1 || data["NextURL"] != nil || data["PrevURL"] != "/admin/stock?product="+productID {
		t.Errorf("2쪽: %d행, 다음 %v, 이전 %v", len(rowIDs(data)), data["NextURL"], data["PrevURL"])
	}
}

// **`product.view` 만 있으면 행 폼이 그려지지 않고 POST 는 403 이다** (W3-44).
// 숨기는 것은 UX 이고 거부하는 것은 서버다 (D15 4.3).
func TestStockRowFormsNeedProductManage(t *testing.T) {
	d, pool, tee, _ := stockFixture(t, "product.view")

	_, data := stockPage(t, d, "/admin/stock")
	if data["CanManage"] != false {
		t.Errorf("CanManage=%v — product.view 만으로 행 폼이 그려진다", data["CanManage"])
	}
	for name, h := range map[string]http.HandlerFunc{"입고": d.StockReceive, "조사": d.Stocktake} {
		rec := postAdmin(t, h, "/admin/stock/x", nil, url.Values{
			"variant_id": {tee}, "quantity": {"5"}, "ledger": {"10"}, "counted": {"7"}})
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s = HTTP %d, want 403", name, rec.Code)
		}
	}
	if got := stockOf(t, pool, tee); got != 10 {
		t.Errorf("재고 %d — 거부된 요청이 반영됐다", got)
	}

	d2, _, _, _ := stockFixture(t, "product.view", "product.manage")
	if _, data := stockPage(t, d2, "/admin/stock"); data["CanManage"] != true {
		t.Errorf("CanManage=%v — product.manage 가 있는데 행 폼이 없다", data["CanManage"])
	}
}

// **입고는 고른 조합의 `variant_id` 로 반영되고 A-517 로 돌아온다** (FR-621).
// 결과 문구와 작업 로그에 상품·조합이 있다 — 없으면 잘못 스캔해도 모른다.
func TestStockReceiveAddsAndNamesTheVariant(t *testing.T) {
	d, pool, tee, _ := stockFixture(t, "product.view", "product.manage")

	rec := postAdmin(t, d.StockReceive, "/admin/stock/receive", nil, url.Values{
		"variant_id": {tee}, "quantity": {"5"}, "q": {"티셔츠"}, "page": {"1"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("입고 = HTTP %d (%s)", rec.Code, rec.Body.String())
	}
	if got := stockOf(t, pool, tee); got != 15 {
		t.Fatalf("재고 %d, want 15", got)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || loc.Path != "/admin/stock" || loc.Host != "" {
		t.Fatalf("돌아갈 주소 %q — A-517 이 아니다", rec.Header().Get("Location"))
	}
	// 목록 상태가 돌아갈 주소에 실린다.
	if loc.Query().Get("q") != "티셔츠" || loc.Query().Get("page") != "1" {
		t.Errorf("돌아갈 주소 %s 에 목록 상태가 없다", loc)
	}
	_, data := stockPage(t, d, loc.String())
	if want := "티셔츠 크기: L 입고 +5 → 현재 15개"; data["Notice"] != want {
		t.Errorf("결과 문구 %q, want %q", data["Notice"], want)
	}
	log := lastLog(t, pool)
	for _, want := range []string{"티셔츠 크기: L", "입고 +5", "재고 10 → 15"} {
		if !strings.Contains(log, want) {
			t.Errorf("작업 로그에 %q 가 없다: %q", want, log)
		}
	}
}

// **`variant_id` 가 uuid 가 아니면 422, 없는 조합은 404, 수량 범위 밖은 422** (D19 A-514).
// 거부된 요청은 재고를 바꾸지 않고 로그도 남기지 않는다.
func TestStockReceiveValidatesItsInput(t *testing.T) {
	d, pool, tee, _ := stockFixture(t, "product.view", "product.manage")
	for name, tc := range map[string]struct {
		form url.Values
		code int
	}{
		"uuid 아님":  {url.Values{"variant_id": {"SKU-TEE"}, "quantity": {"1"}}, 422},
		"빈 조합":     {url.Values{"quantity": {"1"}}, 422},
		"없는 조합":    {url.Values{"variant_id": {"00000000-0000-4000-8000-000000000000"}, "quantity": {"1"}}, 404},
		"수량 0":     {url.Values{"variant_id": {tee}, "quantity": {"0"}}, 422},
		"수량 문자":    {url.Values{"variant_id": {tee}, "quantity": {"많이"}}, 422},
		"수량 상한 초과": {url.Values{"variant_id": {tee}, "quantity": {"10001"}}, 422},
	} {
		if rec := postAdmin(t, d.StockReceive, "/admin/stock/receive", nil, tc.form); rec.Code != tc.code {
			t.Errorf("%s = HTTP %d, want %d", name, rec.Code, tc.code)
		}
	}
	if got := stockOf(t, pool, tee); got != 10 {
		t.Errorf("재고 %d — 거부된 요청이 반영됐다", got)
	}
	if log := lastLog(t, pool); log != "" {
		t.Errorf("거부된 입고가 로그에 남았다: %q", log)
	}
}

// **동시 입고 2건이 합산된다** — 경로를 바꾼 뒤에도 (FR-621, W3-38).
func TestStockReceiveConcurrentPostsAccumulate(t *testing.T) {
	d, pool, tee, _ := stockFixture(t, "product.view", "product.manage")
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = postAdmin(t, d.StockReceive, "/admin/stock/receive", nil,
				url.Values{"variant_id": {tee}, "quantity": {"7"}}).Code
		}()
	}
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusSeeOther {
			t.Errorf("%d번 입고 = HTTP %d", i, c)
		}
	}
	if got := stockOf(t, pool, tee); got != 24 {
		t.Errorf("재고 %d, want 24 — 하나가 다른 하나를 덮어썼다", got)
	}
}

// **실사는 행이 실어 온 장부 값으로 잠그고, 메모가 로그에 함께 남는다** (FR-622, W3-44).
func TestStocktakeUsesTheCarriedLedgerAndLogsTheMemo(t *testing.T) {
	d, pool, tee, _ := stockFixture(t, "product.view", "product.manage")

	rec := postAdmin(t, d.Stocktake, "/admin/stock/stocktake", nil, url.Values{
		"variant_id": {tee}, "ledger": {"10"}, "counted": {"7"}, "memo": {"  파손 3개  "}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("실사 = HTTP %d (%s)", rec.Code, rec.Body.String())
	}
	if got := stockOf(t, pool, tee); got != 7 {
		t.Fatalf("재고 %d, want 7", got)
	}
	want := "티셔츠 크기: L 재고 조사 장부 10 · 실측 7 · 조정 -3"
	if log := lastLog(t, pool); log != want+" · 메모: 파손 3개" {
		t.Errorf("작업 로그 %q, want %q + 메모", log, want)
	}
	_, data := stockPage(t, d, rec.Header().Get("Location"))
	if data["Notice"] != want {
		t.Errorf("결과 문구 %q, want %q", data["Notice"], want)
	}

	// 차이가 없다는 확인도 실사의 결과다 — 남긴다. 메모는 선택이다.
	rec = postAdmin(t, d.Stocktake, "/admin/stock/stocktake", nil, url.Values{
		"variant_id": {tee}, "ledger": {"7"}, "counted": {"7"}})
	if log := lastLog(t, pool); rec.Code != http.StatusSeeOther || !strings.HasSuffix(log, "조정 0 (차이 없음)") {
		t.Errorf("차이 없음 = HTTP %d, 로그 %q", rec.Code, log)
	}

	// 200자까지는 받고 201자는 422 다. 거부된 실사는 재고를 바꾸지 않는다.
	long := strings.Repeat("가", 201)
	rec = postAdmin(t, d.Stocktake, "/admin/stock/stocktake", nil, url.Values{
		"variant_id": {tee}, "ledger": {"7"}, "counted": {"5"}, "memo": {long}})
	if rec.Code != http.StatusUnprocessableEntity || stockOf(t, pool, tee) != 7 {
		t.Errorf("201자 메모 = HTTP %d, 재고 %d — want 422, 7", rec.Code, stockOf(t, pool, tee))
	}
	rec = postAdmin(t, d.Stocktake, "/admin/stock/stocktake", nil, url.Values{
		"variant_id": {tee}, "ledger": {"7"}, "counted": {"5"}, "memo": {long[:len(long)-len("가")]}})
	if rec.Code != http.StatusSeeOther || !strings.Contains(lastLog(t, pool), "메모: 가가") {
		t.Errorf("200자 메모 = HTTP %d, 로그 %q", rec.Code, lastLog(t, pool))
	}
}

// **장부가 그 사이 바뀌면 409 이고, 그 조합 한 행으로 현재 재고와 함께 다시 그린다**
// (FR-622, W3-39). 새 장부 값은 행이 다시 싣는다 — 사용자는 실측만 다시 넣는다.
func TestStocktakeLedgerMismatchRedrawsThatRow(t *testing.T) {
	d, pool, tee, _ := stockFixture(t, "product.view", "product.manage")
	// 세는 동안 주문이 들어와 2개가 팔렸다.
	if _, err := pool.Exec(context.Background(),
		`UPDATE product_variants SET stock = stock - 2 WHERE id = $1`, tee); err != nil {
		t.Fatal(err)
	}

	var data map[string]any
	d.Render = func(w http.ResponseWriter, _ *http.Request, _ string, c int, v any) {
		w.WriteHeader(c)
		data = v.(map[string]any)
	}
	rec := postAdmin(t, d.Stocktake, "/admin/stock/stocktake", nil, url.Values{
		"variant_id": {tee}, "ledger": {"10"}, "counted": {"10"}, "q": {"티셔츠"}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("장부 불일치 = HTTP %d, want 409", rec.Code)
	}
	if got := stockOf(t, pool, tee); got != 8 {
		t.Errorf("재고 %d, want 8 — 조정이 판매분을 지웠다", got)
	}
	rows, _ := data["Rows"].([]commerce.ScannedVariant)
	if len(rows) != 1 || rows[0].ID != tee || rows[0].Stock != 8 {
		t.Errorf("다시 그린 목록 %+v — want 그 조합 한 행, 현재 재고 8", rows)
	}
	if log := lastLog(t, pool); log != "" {
		t.Errorf("거부된 실사가 로그에 남았다: %q", log)
	}
}

// `variant_id` 형식 오류 422 · 없는 조합 404 · 음수 실측 422 · 장부 값 없음 422 (D19 A-515).
func TestStocktakeValidatesItsInput(t *testing.T) {
	d, pool, tee, _ := stockFixture(t, "product.view", "product.manage")
	for name, tc := range map[string]struct {
		form url.Values
		code int
	}{
		"uuid 아님": {url.Values{"variant_id": {"SKU-TEE"}, "ledger": {"10"}, "counted": {"7"}}, 422},
		"없는 조합":   {url.Values{"variant_id": {"00000000-0000-4000-8000-000000000000"}, "ledger": {"10"}, "counted": {"7"}}, 404},
		"음수 실측":   {url.Values{"variant_id": {tee}, "ledger": {"10"}, "counted": {"-1"}}, 422},
		"실측 없음":   {url.Values{"variant_id": {tee}, "ledger": {"10"}}, 422},
		"장부 없음":   {url.Values{"variant_id": {tee}, "counted": {"7"}}, 422},
	} {
		if rec := postAdmin(t, d.Stocktake, "/admin/stock/stocktake", nil, tc.form); rec.Code != tc.code {
			t.Errorf("%s = HTTP %d, want %d", name, rec.Code, tc.code)
		}
	}
	if got := stockOf(t, pool, tee); got != 10 {
		t.Errorf("재고 %d — 거부된 요청이 반영됐다", got)
	}
}

// **메뉴의 재고 항목은 「재고」 하나다** (W3-44). 입고·조사는 그 목록의 행이
// 보내는 폼이라 메뉴가 없다.
func TestNavHasOneStockEntry(t *testing.T) {
	var titles []string
	for _, g := range Nav(func(string) bool { return true }, true) {
		for _, it := range g.Items {
			if strings.HasPrefix(it.Path, "/admin/stock") || strings.Contains(it.Path, "/scan/") ||
				it.Screen == "A-514" || it.Screen == "A-515" || it.Screen == "A-517" {
				titles = append(titles, it.Title+" "+it.Path)
			}
		}
	}
	if len(titles) != 1 || titles[0] != "재고 /admin/stock" {
		t.Errorf("재고 메뉴 %v, want [재고 /admin/stock]", titles)
	}
}
