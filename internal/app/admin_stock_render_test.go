package app

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/commerce"
)

// renderAdmin 은 관리자 템플릿 하나를 실제 렌더러로 그린다. DB 가 필요 없다.
func renderAdmin(t *testing.T, name string, data map[string]any) string {
	t.Helper()
	r, err := newAdminRenderer(func() string { return "s" }, "", true, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.Render(rec, httptest.NewRequest(http.MethodGet, "/admin/x", nil), name, http.StatusOK, data)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s 렌더 = HTTP %d: %s", name, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// **`product.manage` 가 없으면 행의 입고·조사 폼이 그려지지 않는다** (D13 A-517, W3-44).
//
// 재고를 확인만 하는 사람이 실수로 입고를 찍지 않게 하려는 것이다. 판정은
// A-514·A-515 가 서버에서 다시 한다 — 숨기는 것은 UX 이지 보안이 아니다.
func TestStockRowFormsAreDrawnOnlyForManagers(t *testing.T) {
	rows := []commerce.ScannedVariant{{ID: "v1", ProductName: "티셔츠",
		OptionValues: map[string]string{"크기": "L"}, SKU: "SKU-TEE", Barcode: "8801234567890", Stock: 7}}
	data := func(manage bool) map[string]any {
		return map[string]any{"Rows": rows, "Q": "", "Product": "", "Focus": "",
			"CanManage": manage, "CarryPage": 1, "PageNo": 1}
	}

	view := renderAdmin(t, "admin/stock.html", data(false))
	for _, want := range []string{"티셔츠", "크기: L", "SKU-TEE", "8801234567890", "7개"} {
		if !strings.Contains(view, want) {
			t.Errorf("조회 권한만 있는 화면에 %q 가 없다", want)
		}
	}
	for _, no := range []string{"/admin/stock/receive", "/admin/stock/stocktake", `name="ledger"`} {
		if strings.Contains(view, no) {
			t.Errorf("product.manage 가 없는데 %q 가 그려졌다", no)
		}
	}

	manage := renderAdmin(t, "admin/stock.html", data(true))
	for _, want := range []string{`action="/admin/stock/receive"`, `action="/admin/stock/stocktake"`,
		`<input type="hidden" name="ledger" value="7">`} {
		if !strings.Contains(manage, want) {
			t.Errorf("product.manage 가 있는 화면에 %q 가 없다", want)
		}
	}
}

// **A-507 폼이 화면이 발급한 요청 키를 되돌려 보낸다** (D19 A-507).
//
// 키가 폼에 없으면 서버는 재전송을 알아보지 못한다 — 그래서 POST 는 키 없는
// 요청을 422 로 거절하고, 이 필드가 빠지면 환불 화면 자체가 쓸 수 없게 된다.
func TestRefundFormCarriesTheIssuedRequestKey(t *testing.T) {
	order := &commerce.OrderDetail{OrderNo: "20261007-ABCDEFGHJK"}
	// renderRefund 가 넘기는 값 전부다 — 빠지면 템플릿이 nil 을 그리다 실패한다.
	view := renderAdmin(t, "admin/refund.html", map[string]any{
		"Order": order, "Refunds": []commerce.Refund{}, "RequestKey": "Key_issued-by-the-server-0123456",
		"Approved": 0, "Refunded": 0, "Remaining": 0, "NeedsReauth": false, "Error": ""})
	if !strings.Contains(view, `<input type="hidden" name="request_key" value="Key_issued-by-the-server-0123456">`) {
		t.Error("환불 폼에 발급된 요청 키가 실리지 않았다")
	}
}

// A-507 의 환불 내역은 상태마다 할 수 있는 것만 그린다 (D19 A-507): '요청' 은 실행·거부,
// '승인'(결과 불명)은 다시 보내기, 끝난 건은 아무것도.
func TestRefundRowsOfferOnlyTheActionsTheirStateAllows(t *testing.T) {
	view := renderAdmin(t, "admin/refund.html", map[string]any{
		"Order": &commerce.OrderDetail{OrderNo: "20261007-ABCDEFGHJK"},
		"Refunds": []commerce.Refund{
			{ID: "r-req", Status: "요청"}, {ID: "r-unk", Status: "승인"}, {ID: "r-done", Status: "완료"}},
		"RequestKey": "k", "Approved": 0, "Refunded": 0, "Remaining": 0, "NeedsReauth": true, "Error": ""})
	forms := regexp.MustCompile(`(?s)<form method="post" class="adm-form">.*?</form>`).FindAllString(view, -1)
	got := map[string][]string{}
	for _, f := range forms {
		id := regexp.MustCompile(`name="refund_id" value="([^"]+)"`).FindStringSubmatch(f)
		act := regexp.MustCompile(`name="action" value="([^"]+)"`).FindStringSubmatch(f)
		if id == nil || act == nil {
			continue // 새 환불 접수 폼
		}
		got[id[1]] = append(got[id[1]], act[1])
		if act[1] == "execute" && !strings.Contains(f, `name="password"`) {
			t.Errorf("%s 실행 폼에 재인증 칸이 없다 — 돈이 나간다", id[1])
		}
	}
	want := map[string][]string{"r-req": {"execute", "reject"}, "r-unk": {"execute"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("행별 동작 = %v, want %v", got, want)
	}
}
