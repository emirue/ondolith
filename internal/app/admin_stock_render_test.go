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

// A-508 의 「PG 결과로 반영」은 '대기' 주문결제를 PG 가 승인·실패로 답한 줄에만,
// order.update 가 있을 때만 그린다 (D19 A-508, D13).
func TestReconcileOffersResolveOnlyWhereThePGAnswered(t *testing.T) {
	rows := []commerce.ReconcileRow{
		{PaymentID: "p-approved", Kind: "주문결제", OurStatus: "대기", TheirStatus: "승인"},
		{PaymentID: "p-failed", Kind: "주문결제", OurStatus: "대기", TheirStatus: "실패"},
		{PaymentID: "p-still", Kind: "주문결제", OurStatus: "대기", TheirStatus: "대기"},
		{PaymentID: "p-done", Kind: "주문결제", OurStatus: "승인", TheirStatus: "승인"},
		{PaymentID: "p-diff", Kind: "교환차액", OurStatus: "대기", TheirStatus: "승인"},
	}
	ids := func(can bool) []string {
		view := renderAdmin(t, "admin/reconcile.html", map[string]any{
			"Rows": rows, "Mismatched": 0, "From": "2026-10-01", "To": "2026-10-07",
			"Quick": nil, "CanResolve": can})
		var out []string
		for _, m := range regexp.MustCompile(`name="payment_id" value="([^"]+)"`).FindAllStringSubmatch(view, -1) {
			out = append(out, m[1])
		}
		return out
	}
	if got := ids(true); !reflect.DeepEqual(got, []string{"p-approved", "p-failed"}) {
		t.Errorf("반영 버튼 = %v, want [p-approved p-failed]", got)
	}
	if got := ids(false); got != nil {
		t.Errorf("order.update 없이 반영 버튼 = %v, want 없음", got)
	}
}

// **메일 설정 화면에 시험 발송이 있다** (D13·D19 A-205). 라우트만 있고 누를 자리가
// 없으면 운영자는 여전히 저장한 설정이 맞는지 모른다. 받는 주소 칸은 없다 — 본인
// 주소로만 보낸다(D13). 저장과 같은 문이라 재확인 칸이 그 폼 안에 있어야 한다.
func TestMailScreenOffersATestSendToTheAdminOnly(t *testing.T) {
	body := renderAdmin(t, "admin/mail.html", map[string]any{
		"Settings": map[string]string{}, "SecretSaved": map[string]bool{}})
	i := strings.Index(body, `action="/admin/settings/mail/test"`)
	if i < 0 {
		t.Fatal("메일 설정 화면에 시험 발송 폼이 없다")
	}
	form := body[i:]
	form = form[:strings.Index(form, "</form>")]
	if !strings.Contains(form, `name="password"`) {
		t.Error("시험 발송 폼에 비밀번호 재확인 칸이 없다 — 재인증 창이 지나면 늘 거부된다")
	}
	if strings.Contains(form, `name="to"`) || strings.Contains(form, `type="email"`) {
		t.Error("시험 발송 폼이 받는 주소를 받는다 — 임의 주소로 쏘는 중계가 된다 (D13)")
	}
}

// 주문서가 받은 배송 메모와 주문자 연락처를 운영자가 본다. 주문 상세와 송장
// 입력 어디에도 없어서, 「부재 시 경비실」은 DB 에만 있었다.
func TestAdminOrderScreensShowDeliveryMemo(t *testing.T) {
	order := &commerce.OrderDetail{OrderNo: "20261016-ABCDEFGHJK", Status: commerce.StatusPaid,
		Total: 29000, ReceiverName: "받는이", ReceiverPhone: "010-0000-0000",
		Postcode: "12345", Address1: "서울시 어딘가", DeliveryMemo: "부재 시 경비실",
		OrdererEmail: "buyer@example.com", OrdererPhone: "010-1111-1111",
		Items: []commerce.OrderItem{{ProductName: "티셔츠", UnitPrice: 13000, Quantity: 2, LineAmount: 26000}}}

	detail := renderAdmin(t, "admin/order.html", map[string]any{"Order": order})
	for _, want := range []string{"부재 시 경비실", "buyer@example.com", "010-1111-1111"} {
		if !strings.Contains(detail, want) {
			t.Errorf("관리자 주문 상세에 %q 가 없다", want)
		}
	}
	if ship := renderAdmin(t, "admin/shipping.html", map[string]any{"Order": order}); !strings.Contains(ship, "부재 시 경비실") {
		t.Error("송장 입력 화면에 배송 메모가 없다")
	}
}

// 운영자는 교환 건마다 무엇을 보내야 하는지 본다. 조합 ID 만 저장돼 있고 화면에
// 없어서, 교환을 받고도 보낼 물건을 알 수 없었다.
func TestAdminReturnsShowTheExchangeOption(t *testing.T) {
	data := map[string]any{"Order": &commerce.OrderDetail{OrderNo: "N1"},
		"Returns": []commerce.Return{{ReturnNo: "R1", Kind: commerce.KindExchange,
			Status: commerce.StatusExchangeOpen, NewOption: "색상: 흰색",
			Items: []commerce.ReturnItem{{ProductName: "베개", OptionLabel: "색상: 회색", Quantity: 1}}}}}
	if out := renderAdmin(t, "admin/returns.html", data); !strings.Contains(out, "색상: 흰색") {
		t.Error("반품·교환 처리 화면에 교환으로 보낼 옵션이 없다")
	}
}

// 약관 「종류」는 고객이 주문서에서 읽는 이름이다. 영문 코드만 받는 입력 형식이면
// 「service (1.0) 필수」가 나간다. 등록한 본문도 다시 볼 수 있어야 한다.
func TestAdminTermsTakeAReadableKindAndShowTheBody(t *testing.T) {
	out := renderAdmin(t, "admin/terms.html", map[string]any{
		"Terms": []commerce.Terms{{Kind: "개인정보 처리방침", Version: "1.0", Body: "수집 항목: 이름"}}})
	if strings.Contains(out, `pattern="[a-z0-9-]+"`) {
		t.Error("종류 칸이 영문 코드만 받는다")
	}
	if !strings.Contains(out, "수집 항목: 이름") {
		t.Error("약관 목록에서 본문을 볼 수 없다")
	}
}
