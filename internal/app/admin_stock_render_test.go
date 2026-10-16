package app

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/commerce"
	"github.com/emirue/ondolith/internal/content"
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

// 게시판 목록의 삭제 폼은 확인 값을 싣지 않는다 — 실으면 확인 없이 글이 통째로 지워진다.
func TestBoardListDeleteDoesNotPreConfirm(t *testing.T) {
	out := renderAdmin(t, "admin/boards.html", map[string]any{
		"Boards": []map[string]any{{"ID": "b1", "Name": "공지", "Slug": "notice"}}})
	if strings.Contains(out, `name="confirm"`) {
		t.Error("게시판 목록의 삭제 폼이 확인 값을 미리 싣는다")
	}
}

// 테마 화면이 설치된 테마를 나열하고 업로드 화면으로 가는 길을 둔다 — 디렉터리 이름을
// 직접 쳐야 했고, 업로드 화면은 주소를 아는 사람만 닿았다.
func TestThemesScreenListsInstalledThemes(t *testing.T) {
	out := renderAdmin(t, "admin/themes.html", map[string]any{"Active": "warm", "Installed": []string{"dark", "warm"}})
	for _, want := range []string{`href="/admin/themes/upload"`, `name="theme" value="dark"`, `name="theme" value=""`} {
		if !strings.Contains(out, want) {
			t.Errorf("테마 화면에 %q 가 없다", want)
		}
	}
	if strings.Contains(out, `name="theme" value="warm"`) {
		t.Error("이미 쓰는 테마에 「활성화」가 있다")
	}
}

// 메뉴 화면은 순서 칸과, 부모를 제목으로 고르는 목록을 둔다 — 부모 칸에 화면 어디에도
// 안 보이는 UUID 를 쳐야 해서 하위 메뉴를 사실상 만들 수 없었다.
func TestMenusScreenPicksParentsByTitle(t *testing.T) {
	out := renderAdmin(t, "admin/menus.html", map[string]any{"Items": []content.MenuItem{
		{ID: "m1", Title: "고객센터", URL: "/help", Sort: 3},
		{ID: "m2", Title: "공지", URL: "/board/notice", ParentID: "m1", Sort: 1}}})
	for _, want := range []string{`name="sort_order"`, `value="3"`, `<option value="m1">고객센터</option>`,
		`<option value="m1" selected>고객센터</option>`} {
		if !strings.Contains(out, want) {
			t.Errorf("메뉴 화면에 %q 가 없다", want)
		}
	}
	if strings.Contains(out, `placeholder="(최상위)"`) {
		t.Error("부모를 아직 텍스트(UUID)로 받는다")
	}
}

// 글·댓글·첨부 관리는 게시판을 목록에서 고른다 — 주소를 직접 쳐야 했고, 댓글 관리에는
// 고르는 칸조차 없었다. 고르기 전과 고른 게시판이 비었을 때를 다르게 말한다.
func TestModerationScreensPickABoard(t *testing.T) {
	boards := []content.Board{{Slug: "notice", Name: "공지"}, {Slug: "free", Name: "자유"}}
	for _, name := range []string{"admin/posts.html", "admin/comments.html", "admin/attachments.html"} {
		picked := renderAdmin(t, name, map[string]any{"Boards": boards, "BoardSlug": "free"})
		if !strings.Contains(picked, `<option value="free" selected>자유</option>`) {
			t.Errorf("%s: 게시판을 목록에서 고를 수 없다", name)
		}
		if strings.Contains(picked, `placeholder="게시판 주소"`) || strings.Contains(picked, "게시판 주소를 입력하세요") {
			t.Errorf("%s: 아직 게시판 주소를 치게 한다", name)
		}
		if !strings.Contains(picked, "없습니다") {
			t.Errorf("%s: 고른 게시판이 비었다는 말이 없다", name)
		}
		if none := renderAdmin(t, name, map[string]any{"Boards": boards}); !strings.Contains(none, "게시판을 고르세요") {
			t.Errorf("%s: 고르기 전에 무엇을 하라는지 말하지 않는다", name)
		}
	}
}

// 글 관리의 동작은 그 글의 상태에 맞는 것만, 「삭제」를 맨 뒤에 둔다 — 다섯 개가 상태와
// 무관하게 다 나왔고(공개 글에도 「공개」, 고정·고정 해제 동시) 순서가 영문 키의
// 알파벳순이라 「삭제」가 맨 앞이었다. 제목은 글로 가는 링크다.
func TestPostRowsOfferOnlyTheirActions(t *testing.T) {
	out := renderAdmin(t, "admin/posts.html", map[string]any{"BoardSlug": "free", "PageNo": 1,
		"Posts": []content.Post{
			{ID: "p1", Title: "고정 공지", IsPinned: true, Status: "published"},
			{ID: "p2", Title: "숨긴 글", Status: "hidden"}}})
	row := func(id string) string {
		i := strings.Index(out, `value="`+id+`"`)
		j := strings.Index(out[i:], "</tr>")
		return out[i : i+j]
	}
	p1, p2 := row("p1"), row("p2")
	for _, c := range []struct {
		row, want, not string
	}{{p1, ">고정 해제<", ">고정<"}, {p1, ">숨김<", ">공개<"}, {p2, ">고정<", ">고정 해제<"}, {p2, ">공개<", ">숨김<"}} {
		if !strings.Contains(c.row, c.want) || strings.Contains(c.row, c.not) {
			t.Errorf("행에 %s 가 있고 %s 가 없어야 한다", c.want, c.not)
		}
	}
	if strings.Index(p1, ">삭제<") < strings.Index(p1, ">숨김<") {
		t.Error("「삭제」가 맨 뒤가 아니다")
	}
	if !strings.Contains(out, `href="/board/free/p1"`) {
		t.Error("제목이 글로 가는 링크가 아니다")
	}
}

// **관리자 화면의 삭제는 한 번 더 묻는다.** 공개 화면은 글·댓글·장바구니 빼기에 확인창이
// 있는데 관리자 삭제 버튼 12개(게시판·페이지·카테고리·메뉴·댓글·첨부·상품·이미지·회원·
// 필드…)는 없었다 — 더 위험한 쪽이 더 쉽게 지워졌다. 「삭제」 위험 버튼마다 confirm 이 있다.
func TestAdminDeleteButtonsAsk(t *testing.T) {
	files, err := filepath.Glob("templates/admin/*.html")
	if err != nil || len(files) == 0 {
		t.Fatalf("관리자 템플릿을 찾지 못했다: %v", err)
	}
	n := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, "adm-btn--danger") && strings.Contains(line, "삭제</button>") {
				n++
				if !strings.Contains(line, `onclick="return confirm(`) {
					t.Errorf("%s:%d 삭제 버튼에 확인창이 없다", f, i+1)
				}
			}
		}
	}
	if n < 12 {
		t.Errorf("삭제 버튼 %d개 — 검사가 버튼을 다 찾지 못했다", n)
	}
}

// 필드 편집기(A-306·A-406)는 타입을 한국어로 부르고, 줄마다 「수정」이 있어 고르면 폼이
// 그 필드 값으로 채워진다 — 「같은 키로 저장하면 갱신」이라 키·라벨·선택지를 처음부터
// 다시 쳐야 했다.
func TestFieldEditorsPrefillAndNameTypes(t *testing.T) {
	f := content.FieldSchema{Key: "color", Label: "색상", Type: content.FieldMultiselect,
		Options: []string{"빨강", "파랑"}, Required: true, Sort: 2}
	for _, name := range []string{"admin/board-fields.html", "admin/user-fields.html"} {
		out := renderAdmin(t, name, map[string]any{"Board": &content.Board{ID: "b1", Name: "공지", Slug: "notice"},
			"Fields": []content.FieldSchema{f}, "Types": content.FieldTypes(), "Edit": &f, "EditOptions": "빨강\n파랑"})
		for _, want := range []string{"<td>여러 개 선택</td>", `href="?edit=color#field-form"`,
			`name="key" value="color"`, `name="label" value="색상"`, `<option value="multiselect" selected>여러 개 선택</option>`,
			">빨강\n파랑</textarea>", `name="sort_order" value="2"`} {
			if !strings.Contains(out, want) {
				t.Errorf("%s 에 %q 가 없다", name, want)
			}
		}
	}
}

// 카테고리 줄마다 이름·순서를 고치는 폼이 있다.
func TestCategoryRowsCanBeEdited(t *testing.T) {
	out := renderAdmin(t, "admin/categories.html", map[string]any{"Categories": []commerce.Category{
		{ID: "c1", Name: "매트", Slug: "mat", SortOrder: 2}}})
	for _, want := range []string{`action="/admin/categories/c1"`, `name="name" value="매트"`, `name="sort_order" value="2"`} {
		if !strings.Contains(out, want) {
			t.Errorf("카테고리 줄에 %q 가 없다", want)
		}
	}
}

// 대시보드 「최근 글」: 제목은 글로 가는 링크이고, 옆 링크는 글 관리다(게시판 관리가 아니다).
// 긴 제목은 칸 안에서 접힌다 — 작성자·작성일 열을 카드 밖으로 밀어냈다.
func TestDashboardRecentPostsLinkAndWrap(t *testing.T) {
	out := renderAdmin(t, "admin/dashboard.html", map[string]any{
		"Posts":      []content.Post{{ID: "p1", BoardID: "b1", Title: strings.Repeat("긴제목", 30)}},
		"BoardSlugs": map[string]string{"b1": "notice"}})
	for _, want := range []string{`href="/board/notice/p1"`, `href="/admin/posts">글 관리 →</a>`, `class="adm-wrap"`} {
		if !strings.Contains(out, want) {
			t.Errorf("대시보드에 %q 가 없다", want)
		}
	}
}

// 운영자 화면에 내부 문서·화면 번호와 권한 키를 보이지 않는다 — 「(D15 7절)」 「회원가입
// (P-103)」 「(page.publish)」 「shop 모드」. 작업 로그의 동작은 한국어 이름이다(코드는 title).
func TestAdminTextHidesInternalCodes(t *testing.T) {
	pages := map[string]map[string]any{
		"admin/oplog.html": {"Entries": []content.LogEntry{{Action: "settings.update", Summary: "설정"}},
			"ActionLabels": map[string]string{"settings.update": "설정 변경"}, "Count": int64(1), "PageNo": 1},
		"admin/user-fields.html": {"Fields": nil, "Types": content.FieldTypes()},
		"admin/page-edit.html":   {"Page": &content.Page{ID: "p1", Title: "소개", Slug: "about"}},
		"admin/business.html":    {"Business": map[string]string{}},
	}
	for name, data := range pages {
		out := renderAdmin(t, name, data)
		for _, leak := range []string{"(D15", "(P-103)", "(P-109)", "(page.publish)", "shop 모드"} {
			if strings.Contains(out, leak) {
				t.Errorf("%s 에 내부 표기 %q 가 보인다", name, leak)
			}
		}
	}
	if out := renderAdmin(t, "admin/oplog.html", pages["admin/oplog.html"]); !strings.Contains(out, `title="settings.update">설정 변경</span>`) {
		t.Error("작업 로그의 동작이 한국어 이름이 아니다")
	}
}
