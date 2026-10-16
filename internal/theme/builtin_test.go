package theme

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"html/template"
	"io/fs"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/emirue/ondolith/internal/content"
)

// builtinTemplates is every .html the built-in theme ships. Rendering each one
// is the point: a template that exists but does not parse is a 500 the first
// time a visitor reaches that screen, and "the file is there" is not evidence
// that it works.
func builtinTemplates(t *testing.T) []string {
	t.Helper()
	var out []string
	err := fs.WalkDir(Builtin(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".html") {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("내장 테마에 템플릿이 하나도 없다 — embed 가 아무것도 못 찾았다")
	}
	return out
}

func fullView() View {
	v := NewView(Site{
		Name: "온돌리스", MetaDescription: "설명", Type: "cms",
		Business: map[string]string{"상호": "온돌"},
	}, "/about")
	v.Meta = Meta{Title: "제목", Description: "설명"}
	v.User = &ViewUser{ID: "u1", Email: "a@example.com", DisplayName: "홍길동"}
	v.Flash = []Flash{{Kind: "success", Text: "저장했습니다"}}
	v.Menu = []*content.MenuNode{
		{MenuItem: content.MenuItem{ID: "1", Title: "회사", URL: "/about"},
			Children: []*content.MenuNode{
				{MenuItem: content.MenuItem{ID: "2", Title: "연혁", URL: "/history"}},
			}},
	}
	return v
}

func newBuiltinLoader() *Loader {
	l := New(Builtin(), "", false, nil)
	l.funcs = FuncMap(Deps{
		AssetURL: l.AssetURL,
		URLFor:   func(kind string, args ...string) string { return "/" + kind },
		Now:      func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
	})
	return l
}

// Every shipped template must parse and execute against the common view model.
func TestEveryBuiltinTemplateRenders(t *testing.T) {
	l := newBuiltinLoader()
	for _, name := range builtinTemplates(t) {
		// partials/ are fragments the layout pulls in, and base.html IS the
		// layout — neither is a page a request can ask for.
		if strings.HasPrefix(name, "partials/") || name == "base.html" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			// Each screen gets the payload its own template expects; a single
			// shared map would render some screens with the wrong shape and
			// prove nothing about the rest.
			v := fullView()
			v.Data = payloadFor(name)
			var b bytes.Buffer
			if err := l.Render(&b, name, v); err != nil {
				t.Fatalf("렌더링 실패: %v", err)
			}
			if b.Len() == 0 {
				t.Error("빈 출력")
			}
			if !strings.Contains(b.String(), "<html") {
				t.Errorf("레이아웃이 적용되지 않았다: %.120s", b.String())
			}
			// **없는 키 참조가 0건이어야 한다** (W4-04). html/template 은
			// 기본적으로 없는 맵 키를 `<no value>` 로 그리고 오류를 내지
			// 않는다 — 렌더링이 성공했다는 사실만으로는 화면이 온전한지 알 수
			// 없다. 런타임을 `missingkey=error` 로 바꾸지는 않는다: 제3자
			// 테마의 오타 하나가 사이트 전체를 500 으로 만들면 안 된다
			// (D17). 대신 **우리 테마**는 여기서 완전해야 한다.
			if i := strings.Index(b.String(), "<no value>"); i >= 0 {
				lo := max(i-120, 0)
				t.Errorf("없는 키를 참조한다 (…%s…)", b.String()[lo:min(i+40, b.Len())])
			}
		})
	}
}

// The page template renders a real page, and its body is escaped: page bodies
// are operator input and a theme must not turn them into markup (NFR-203).
func TestPageTemplateEscapesBody(t *testing.T) {
	l := newBuiltinLoader()
	v := fullView()
	v.Data = &pageLike{Title: "제목", Body: "<script>alert(1)</script>\n둘째 줄"}

	var b bytes.Buffer
	if err := l.Render(&b, "page.html", v); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if strings.Contains(out, "<script>alert") {
		t.Errorf("본문이 이스케이프되지 않았다: %s", out)
	}
	if !strings.Contains(out, "<br>") {
		t.Error("nl2br 이 적용되지 않았다")
	}
}

type pageLike struct {
	Title    string
	Body     string
	BodyHTML string
}

// Rendered mirrors content.Page.Rendered: 정화된 HTML 이거나 빈 값 (D17).
func (p pageLike) Rendered() template.HTML { return template.HTML(p.BodyHTML) }

// pagerLike is partials/pager.html's data with both links, so both branches render.
var pagerLike = map[string]any{"PageNo": 2, "PrevURL": "/shop?sort=price", "NextURL": "/shop?page=3&sort=price"}

// payloadFor returns the .Data a given screen is written against (D12/D13 pin
// this down per screen).
func payloadFor(name string) any {
	switch name {
	case "page.html":
		return &pageLike{Title: "회사 소개", Body: "본문입니다.\n둘째 줄"}
	case "shop/list.html":
		return map[string]any{
			"Products": []productLike{
				{ID: "p1", Slug: "tee", Name: "티셔츠", BasePrice: 12000, MinDelta: 1000, InStock: true, ImageID: "i1"},
				{ID: "p2", Slug: "cap", Name: "모자", BasePrice: 9000, InStock: false},
			},
			"Categories": []categoryLike{{ID: "c1", Slug: "top", Name: "상의"}},
			"Sort":       "price",
			"Page":       1,
			"Pager":      pagerLike,
		}
	case "shop/search.html":
		return map[string]any{
			"Query":    "티",
			"Products": []productLike{{ID: "p1", Slug: "tee", Name: "티셔츠", BasePrice: 12000}},
			"Pager":    pagerLike,
		}
	case "shop/product.html":
		return map[string]any{
			"Product": productLike{ID: "p1", Slug: "tee", Name: "티셔츠",
				Description: "설명", BasePrice: 12000},
			"Variants": []variantLike{
				{ID: "v1", OptionValues: map[string]string{"크기": "L"}, PriceDelta: 1000, Stock: 3},
				{ID: "v2", OptionValues: map[string]string{"크기": "M"}, Stock: 0},
			},
			"Options": []optionGroupLike{{Name: "크기", Values: []string{"L", "M"}}},
			"Images":  []imageLike{{ID: "i1"}, {ID: "i2"}},
			"Error":   "담을 수 없습니다",
		}
	case "shop/variant.html":
		return map[string]any{
			"Variant": &variantLike{ID: "v1", OptionValues: map[string]string{"크기": "L"},
				PriceDelta: 1000, Stock: 3},
			"Picked": map[string]string{"크기": "L"},
		}
	case "shop/checkout.html":
		return map[string]any{
			"Items": []cartItemLike{{ID: "ci1", Name: "티셔츠",
				Option: map[string]string{"크기": "L"}, UnitPrice: 13000, Quantity: 2}},
			"Goods": 26000, "Fee": 3000, "Total": 29000,
			"Form":   url.Values{"orderer_email": {"a@example.com"}},
			"Agreed": map[string]bool{},
			"Terms":  []termLike{{ID: "t1", Kind: "이용약관", Version: "v1", Required: true}},
			"Error":  "필수 약관에 동의해야 합니다",
		}
	case "shop/terms.html":
		return map[string]any{
			"Terms": termLike{ID: "t1", Kind: "이용약관", Version: "1.0", Body: "제1조\n제2조",
				EffectiveAt: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)},
		}
	case "shop/pay.html", "shop/complete.html":
		return map[string]any{
			"Order": orderLike{OrderNo: "20260805-ABCDEFGHJK", Status: "결제대기",
				Total: 29000, ReceiverName: "받는이", ReceiverPhone: "010-0000-0000",
				Postcode: "12345", Address1: "서울시 어딘가",
				Items: []orderItemLike{{ProductName: "티셔츠", OptionLabel: "크기: L",
					UnitPrice: 13000, Quantity: 2, LineAmount: 26000}}},
			"ClientKey": "test_ck_public",
		}
	case "order/list.html":
		return map[string]any{
			"Orders": []orderLike{{OrderNo: "20260805-ABCDEFGHJK", Status: "결제완료", Total: 29000,
				CreatedAt: time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)}},
			"Page":  1,
			"Pager": pagerLike,
		}
	case "order/view.html":
		return map[string]any{
			"Order": orderLike{OrderNo: "20260805-ABCDEFGHJK", Status: "배송중", Total: 29000,
				CreatedAt:    time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC),
				ReceiverName: "받는이", ReceiverPhone: "010-0000-0000",
				Postcode: "12345", Address1: "서울시 어딘가",
				Items: []orderItemLike{{ProductName: "티셔츠", OptionLabel: "크기: L",
					UnitPrice: 13000, Quantity: 2, LineAmount: 26000}}},
		}
	case "order/shipping.html":
		return map[string]any{
			"Order": orderLike{OrderNo: "20260805-ABCDEFGHJK", Status: "배송중"},
			"Shipments": []shipmentLike{{Kind: "최초발송", Carrier: "cj",
				TrackingNo: "T-1",
				ShippedAt:  time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)}},
		}
	case "order/refunds.html":
		return map[string]any{
			"Order": orderLike{OrderNo: "20260805-ABCDEFGHJK", Status: "결제완료", Discount: 1000,
				Items: []orderItemLike{{ID: "oi1", ProductName: "티셔츠", OptionLabel: "크기: L",
					UnitPrice: 13000, Quantity: 3, LineAmount: 39000, Discount: 1000, Settled: 1}}},
			"Refunds": []refundLike{{Status: "요청", Requester: "구매자", Amount: 5000,
				Reason: "단순 변심", CreatedAt: time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)}},
			"Approved": 29000, "Refunded": 5000, "Remaining": 24000, "CanCancel": true,
			"Error": "환불 가능 금액을 넘었습니다.",
		}
	case "order/receipt.html":
		return map[string]any{
			"Order": orderLike{OrderNo: "20260805-ABCDEFGHJK", Status: "구매확정", Total: 29000,
				CreatedAt:    time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC),
				ReceiverName: "받는이", ReceiverPhone: "010-0000-0000",
				Postcode: "12345", Address1: "서울시 어딘가",
				OrdererEmail: "a@example.com", OrdererPhone: "010-1111-1111",
				Items: []orderItemLike{{ProductName: "티셔츠", OptionLabel: "크기: L",
					UnitPrice: 13000, Quantity: 2, LineAmount: 26000}}},
		}
	case "order/return-form.html":
		return map[string]any{
			"Kind": "교환",
			"Order": orderLike{OrderNo: "20260805-ABCDEFGHJK", Status: "배송완료",
				Items: []orderItemLike{{ID: "oi1", ProductName: "티셔츠", OptionLabel: "크기: L",
					UnitPrice: 13000, Quantity: 2, LineAmount: 26000, Settled: 1}}},
			"Exchange": []exchangeGroupLike{{
				Item: orderItemLike{ID: "oi1", ProductName: "티셔츠", OptionLabel: "크기: L"},
				Variants: []variantLike{{ID: "v2",
					OptionValues: map[string]string{"크기": "M"}, PriceDelta: 1000, Stock: 3}}}},
			"Error": "이미 처리 중인 요청이 있습니다.",
		}
	case "order/returns.html":
		return map[string]any{
			"Order": orderLike{OrderNo: "20260805-ABCDEFGHJK", Status: "반품수거"},
			"Returns": []returnLike{{ReturnNo: "R20260805-ABCDEFGHJK", Kind: "반품",
				Status: "반품수거", Reason: "단순 변심", Fault: "구매자",
				FeePolicy: "차감", FeeAmount: 3000,
				CreatedAt: time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC),
				Items:     []returnItemLike{{ProductName: "티셔츠", OptionLabel: "크기: L", Quantity: 1}}}},
		}
	case "order/exchange-pay.html":
		// 차액은 확정된 값이다 — 화면이 계산하지 않는다 (FR-607).
		return map[string]any{
			"Order":  orderLike{OrderNo: "20260805-ABCDEFGHJK", Status: "차액결제대기"},
			"Return": exchangeDiffLike{ReturnNo: "R20260805-ABCDEFGHJK", Amount: 5000},
		}
	case "account/delete.html":
		return map[string]any{"Error": "비밀번호가 올바르지 않습니다."}
	case "account/connections.html":
		return map[string]any{
			"Connections": []connectionLike{
				{Provider: "google", Label: "구글", Linked: true, CanDisconnect: true},
				{Provider: "kakao", Label: "카카오", Linked: true, CanDisconnect: false},
				{Provider: "naver", Label: "네이버"},
			},
		}
	case "order/guest-form.html":
		return map[string]any{"Error": "주문 정보를 찾을 수 없습니다."}
	case "shop/fail.html":
		return map[string]any{"Message": "결제를 취소하셨습니다."}
	case "shop/cart.html":
		return map[string]any{
			"Items": []cartItemLike{{ID: "ci1", VariantID: "v1", ProductID: "p1",
				Slug: "tee", Name: "티셔츠", Option: map[string]string{"크기": "L"},
				UnitPrice: 13000, Quantity: 2, Stock: 3, Sellable: true, ProductVisible: true}},
			"Goods": 26000, "Fee": 3000, "Total": 29000,
			"Error": "수량이 재고를 넘습니다",
		}
	case "error.html":
		return map[string]any{"Detail": "자세한 내용"}
	case "board/list.html":
		return map[string]any{
			"Board": boardLike{ID: "b1", Slug: "free", Name: "자유게시판", PerPage: 20},
			"Posts": []postLike{{
				ID: "p1", Title: "첫 글", AuthorName: "홍길동", ViewCount: 12,
				CommentCount: 3, HasAttachment: true, IsPinned: true,
				CustomFields: map[string]any{"color": "빨강"},
				CreatedAt:    time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC),
			}},
			"Total":    int64(1),
			"Query":    struct{ Search string }{Search: "검색어"},
			"Pager":    pagerFixture("/board/free", 1),
			"Columns":  []fieldLike{{Key: "color", Label: "색상"}},
			"CanWrite": true,
		}
	case "board/view.html":
		return map[string]any{
			"Board": boardLike{ID: "b1", Slug: "free", Name: "자유게시판"},
			// 첨부가 있는 글이다 — 목록이 비면 첨부 절이 통째로 안 그려져
			// 그 조각은 렌더링 검사를 지나가지 않는다.
			"Attachments": []attachmentLike{{ID: "a1", OriginalName: "설명서.pdf", ByteSize: 20480}},
			// 에디터로 쓴 글이다 — 평문 갈래는 page.html 이 지난다.
			"Post": postLike{ID: "p1", Title: "첫 글", Body: "본문\n둘째 줄", BodyHTML: "<p><strong>서식</strong> 본문</p>",
				AuthorName: "홍길동", ViewCount: 12,
				CustomFields: map[string]any{"color": "빨강"},
				CreatedAt:    time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)},
			"Comments": []commentLike{
				{ID: "c1", AuthorID: "u1", AuthorName: "홍길동", Body: "댓글",
					CreatedAt: time.Date(2026, 8, 5, 9, 30, 0, 0, time.UTC)},
				{ID: "c2", ParentID: "c1", Body: "",
					DeletedAt: time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC),
					CreatedAt: time.Date(2026, 8, 5, 9, 40, 0, 0, time.UTC)},
			},
			"Fields":     []fieldLike{{Key: "color", Label: "색상"}},
			"CanComment": true, "CanEdit": true, "CanModerate": true,
			"CommentForm": map[string]any{"Action": "/board/free/p1/comments"},
		}
	case "search.html":
		return map[string]any{
			"Query":  struct{ Search string }{Search: "검색어"},
			"Total":  int64(1),
			"Boards": map[string]boardLike{"b1": {ID: "b1", Slug: "free", Name: "자유게시판"}},
			"Results": []postLike{{ID: "p1", BoardID: "b1", Title: "찾은 글", Body: "본문",
				CreatedAt: time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)}},
			"Pager": pagerFixture("/search", 2),
		}
	case "comment/form.html":
		return map[string]any{
			"Board":   boardLike{ID: "b1", Slug: "free", Name: "자유게시판"},
			"Comment": commentLike{ID: "c1", PostID: "p1", Body: "고칠 댓글"},
			"Error":   "오류 메시지",
		}
	case "board/form.html":
		return map[string]any{
			// 첨부를 허용하는 게시판이다 — false 면 파일 칸과 enctype 이
			// 그려지지 않아 그 분기가 검사되지 않는다 (FR-506).
			"Board": boardLike{ID: "b1", Slug: "free", Name: "자유게시판", AllowAttachments: true},
			"Post": &postLike{Title: "고칠 글", Body: "본문",
				CustomFields: map[string]any{"color": "빨강"}},
			"Inputs": []inputLike{
				{Field: fieldLike{Key: "memo", Label: "메모", Type: "text"}, Value: "적어둔 값"},
				{Field: fieldLike{Key: "color", Label: "색상", Type: "select",
					Options: []string{"빨강", "파랑"}, Required: true}, Value: "빨강"},
				{Field: fieldLike{Key: "agree", Label: "동의", Type: "checkbox"}, Value: true},
				{Field: fieldLike{Key: "due", Label: "기한", Type: "date"}, Value: "2026-08-05"},
				{Field: fieldLike{Key: "site", Label: "링크", Type: "url"}, Value: "https://example.com"},
				{Field: fieldLike{Key: "qty", Label: "수량", Type: "number"}, Value: 3},
				{Field: fieldLike{Key: "tags", Label: "태그", Type: "multiselect",
					Options: []string{"A", "B"}}, Value: []any{"B"}},
				{Field: fieldLike{Key: "detail", Label: "상세", Type: "textarea"}, Value: "여러 줄"},
			},
			"Fields": []fieldLike{
				{Key: "memo", Label: "메모", Type: "text"},
				{Key: "detail", Label: "상세", Type: "textarea"},
				{Key: "qty", Label: "수량", Type: "number"},
				{Key: "color", Label: "색상", Type: "select", Options: []string{"빨강", "파랑"}, Required: true},
				{Key: "tags", Label: "태그", Type: "multiselect", Options: []string{"A", "B"}},
				{Key: "agree", Label: "동의", Type: "checkbox"},
				{Key: "due", Label: "기한", Type: "date"},
				{Key: "site", Label: "링크", Type: "url"},
			},
			"CanSecret": true, "Error": "오류 메시지",
		}
	default:
		return map[string]any{
			"Error": "오류 메시지", "Email": "a@example.com", "Next": "/admin",
		}
	}
}

// D17: base.html is the one file a theme MUST provide, so the built-in must
// have it or every fallback is broken.
func TestBuiltinHasRequiredFiles(t *testing.T) {
	for _, name := range []string{"base.html", "home.html", "page.html", "error.html"} {
		if _, err := fs.Stat(Builtin(), name); err != nil {
			t.Errorf("내장 테마에 %s 가 없다: %v", name, err)
		}
	}
	// The vendored htmx and the stylesheet are served, not templated.
	for _, name := range []string{"static/css/style.css", "static/js/htmx.min.js"} {
		if _, err := fs.Stat(Builtin(), name); err != nil {
			t.Errorf("내장 자산 %s 가 없다: %v", name, err)
		}
	}
}

// A logged-out visitor must render too: the header branches on .User, and a nil
// there is the common case, not an edge one.
func TestTemplatesRenderForAnonymousVisitor(t *testing.T) {
	l := newBuiltinLoader()
	v := NewView(Site{Name: "온돌리스"}, "/")
	// No user, no menu, no flash — all zero values.
	for _, name := range []string{"home.html", "page.html", "error.html", "auth/login.html"} {
		var b bytes.Buffer
		if err := l.Render(&b, name, v); err != nil {
			t.Errorf("%s: 익명 방문자에게 렌더링 실패: %v", name, err)
		}
		if strings.Contains(b.String(), "로그아웃") {
			t.Errorf("%s: 미로그인인데 로그아웃 버튼이 있다", name)
		}
	}
}

// The vendored htmx must be the version DEC-2.2 pinned, and its hash file must
// agree — otherwise the record and the file drift and nobody notices.
func TestHtmxVersionFileMatchesTheVendoredFile(t *testing.T) {
	ver, err := fs.ReadFile(Builtin(), "static/js/htmx.VERSION")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ver), "2.0.9") {
		t.Errorf("VERSION 파일이 2.0.9 를 가리키지 않는다:\n%s", ver)
	}
	js, err := fs.ReadFile(Builtin(), "static/js/htmx.min.js")
	if err != nil {
		t.Fatal(err)
	}
	if len(js) < 10_000 {
		t.Errorf("htmx 파일이 %d 바이트뿐이다 — 받다 만 것 같다", len(js))
	}
}

// 게시판 화면이 받는 모양. 실제 content 타입을 쓰지 않는 이유는 pageLike 와
// 같다 — 테마 패키지가 content 에 의존하면 테마 계약이 저장소 구조를 따라
// 움직인다. 필드 이름이 어긋나면 이 테스트가 렌더링에서 실패한다.
type boardLike struct {
	ID, Slug, Name, Skin string
	// ListStyle 은 목록 모양이다 — "list"·"gallery" (FR-515).
	ListStyle string
	// AllowAttachments 는 글쓰기 폼이 파일 칸과 enctype 을 낼지 정한다
	// (FR-506). 픽스처에 없던 동안 그 조건은 렌더링 검사를 통과하지 못했다.
	AllowAttachments bool
	AllowComments    bool
	PerPage          int
}

// Rendered mirrors content.Post.Rendered.
func (p postLike) Rendered() template.HTML { return template.HTML(p.BodyHTML) }

type postLike struct {
	ID, BoardID, Title, Body, AuthorName string
	BodyHTML, CoverMediaID               string
	CustomFields                         map[string]any
	IsPinned, IsSecret                   bool
	ViewCount, CommentCount              int64
	HasAttachment                        bool
	CreatedAt                            time.Time
}

type commentLike struct {
	// AuthorID 는 「내 댓글인가」를 화면이 판단하는 근거다 (P-209·P-210).
	ID, PostID, ParentID, AuthorID, AuthorName, Body string
	DeletedAt, CreatedAt                             time.Time
}

// attachmentLike 는 글에 딸린 파일 하나다 (FR-506).
type attachmentLike struct {
	ID, OriginalName string
	ByteSize         int64
}

type fieldLike struct {
	Key, Label, Type string
	Options          []string
	Required         bool
}

type inputLike struct {
	Field fieldLike
	Value any
}

// Has mirrors content.FieldInput.Has — field.html 이 부른다 (M14).
func (in inputLike) Has(opt string) bool {
	return content.FieldInput{Value: in.Value}.Has(opt)
}

// 커머스 화면이 받는 모양. 실물 타입을 쓰지 않는 이유는 이 패키지가
// internal/commerce 를 import 하지 않기 위해서다 — 테마는 데이터 모양만 알면
// 되고, 그 반대 방향 의존은 테마를 커머스에 묶는다.
type productLike struct {
	ID, Slug, Name, Description string
	BasePrice, MinDelta         int
	InStock, HasVariants        bool
	// ImageID 가 빈 것과 있는 것을 둘 다 그려 본다 (P-306).
	ImageID string
}

type imageLike struct{ ID string }

type categoryLike struct{ ID, Slug, Name string }

type variantLike struct {
	ID           string
	OptionValues map[string]string
	PriceDelta   int
	Stock        int
}

type optionGroupLike struct {
	Name   string
	Values []string
}

type cartItemLike struct {
	// Slug 는 상품 화면의 주소다. ProductID 로 링크를 그리면 라우트가
	// `/shop/p/{slug}` 이므로 눌렀을 때 404 가 난다 — 실제로 그랬다.
	ID, VariantID, ProductID, Slug, Name string
	Option                               map[string]string
	UnitPrice, Quantity, Stock           int
	Sellable, ProductVisible             bool
}

type termLike struct {
	ID, Kind, Version, Body string
	Required                bool
	EffectiveAt             time.Time
}

// exchangeGroupLike mirrors the per-item group P-512 draws (handler_returns.go).
type exchangeGroupLike struct {
	Item     orderItemLike
	Variants []variantLike
}

type orderItemLike struct {
	ID                              string
	ProductName, OptionLabel        string
	UnitPrice, Quantity, LineAmount int
	Discount, Settled               int
}

// RemainingQty mirrors commerce.OrderItem.RemainingQty — 템플릿이 부르는
// 메서드는 표본에도 있어야 렌더링 테스트가 그 경로를 지나간다. **이름이
// 실물과 다르면 그 경로는 렌더링되지 않고, 검사는 통과한다** (M14).
func (it orderItemLike) RemainingQty() int { return it.Quantity - it.Settled }

// Goods·ShippingFee mirror commerce.OrderDetail's — 주문 뒤 화면이 부른다.
func (o orderLike) Goods() int {
	n := 0
	for _, it := range o.Items {
		n += it.LineAmount
	}
	return n
}
func (o orderLike) ShippingFee() int { return o.Total + o.Discount - o.Goods() }

type orderLike struct {
	OrderNo, Status                                           string
	Total                                                     int
	ReceiverName, ReceiverPhone, Postcode, Address1, Address2 string
	CreatedAt                                                 time.Time
	OrdererEmail, OrdererPhone                                string
	DeliveryMemo                                              string
	Discount                                                  int
	Items                                                     []orderItemLike
}

// **ShippedAt 은 time.Time 이다** (commerce.Shipment). 여기서 string 으로
// 두었더니 템플릿이 `{{.ShippedAt}}` 로 그대로 찍는 동안 검사가 통과했고,
// 운영에서는 `2026-08-07 23:54:32.711708 +0900 KST` 가 화면에 나왔다.
// 픽스처 타입이 실물과 다르면 그 필드에 대한 검사는 전부 헛돈다 (M14).
type shipmentLike struct {
	Kind, Carrier, TrackingNo string
	ShippedAt                 time.Time
}

type refundLike struct {
	Status, Requester, Reason string
	Amount                    int
	CreatedAt                 time.Time
}

type returnItemLike struct {
	ProductName, OptionLabel string
	Quantity                 int
	IsOpen                   bool
}

type returnLike struct {
	ReturnNo, Kind, Status string
	Reason, RejectReason   string
	Fault, FeePolicy       string
	NewOption              string
	FeeAmount, PriceDiff   int
	CreatedAt              time.Time
	Items                  []returnItemLike
}

// exchangeDiffLike mirrors commerce.ExchangeDiff's shape for P-514.
type exchangeDiffLike struct {
	ReturnNo string
	Amount   int
}

// **커머스 템플릿은 금액을 `money` 로 낸다** (W3-27 완료 기준).
//
// 원시 정수를 그대로 그리면 26000 이 되고, 자릿수를 눈으로 세는 화면이 된다.
// 더 중요한 것은 형식이 한 곳에 모인다는 점이다 — 템플릿마다 다르게 쓰면
// 통화 표기를 바꿀 때 빠뜨리는 화면이 생긴다.
func TestCommerceTemplatesFormatMoney(t *testing.T) {
	var raw []string
	for _, name := range builtinTemplates(t) {
		if !strings.HasPrefix(name, "shop/") && !strings.HasPrefix(name, "order/") {
			continue
		}
		b, err := builtinFS.ReadFile("builtin/" + name)
		if err != nil {
			t.Fatal(err)
		}
		// `{{ .Foo }} 원` 처럼 공백이 있어도 잡는다. 앞의 정규식은 공백
		// 하나만 넣으면 빠져나갔다 — 그 형태로 고쳐 두면 검사가 통과한 채
		// 화면에는 `29000원` 이 나온다.
		if regexp.MustCompile(`\{\{\s*[.$][A-Za-z][A-Za-z0-9.]*\s*\}\}\s*원`).Match(b) {
			raw = append(raw, name)
		}
	}
	if len(raw) > 0 {
		t.Errorf("금액을 money 없이 그리는 템플릿: %v", raw)
	}

	// **실제로 통화 표기가 나오는지 — 금액을 그리는 템플릿 전부에서 본다.**
	// 한 템플릿만 렌더링하면 나머지는 정규식에만 기대게 되고, 정규식은
	// 빠져나갈 형태가 늘 남는다.
	l := newBuiltinLoader()
	rendered := 0
	for _, name := range builtinTemplates(t) {
		if !strings.HasPrefix(name, "shop/") && !strings.HasPrefix(name, "order/") {
			continue
		}
		v := fullView()
		v.Data = payloadFor(name)
		var b bytes.Buffer
		if err := l.Render(&b, name, v); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out := b.String()
		// 자릿수 구분 없이 4자리 이상 숫자에 곧바로 「원」이 붙으면 money 를
		// 거치지 않은 것이다 (money 는 1,000 단위로 끊는다).
		if m := regexp.MustCompile(`[^,\d](\d{4,})\s*원`).FindStringSubmatch(out); m != nil {
			t.Errorf("%s 가 통화 표기 없이 %s원 을 그렸다", name, m[1])
		}
		if strings.Contains(out, "원") {
			rendered++
		}
	}
	if rendered == 0 {
		t.Fatal("금액을 그리는 템플릿을 하나도 렌더링하지 못했다 — 검사가 헛돌았다")
	}
}

// **모바일 폭에서 레이아웃이 깨지지 않는다** (W4-04, FR-301).
//
// 브라우저 없이 확인할 수 있는 것은 두 가지다: 뷰포트 선언이 있는가(없으면
// 모바일 브라우저가 데스크톱 폭으로 축소해 그린다), 그리고 스타일시트에 폭에
// 반응하는 규칙이 있는가. 실제 렌더링은 사람이 봐야 하지만, **이 둘이 빠진
// 채로는 볼 것도 없다.**
func TestBuiltinThemeIsResponsive(t *testing.T) {
	// **파일이 아니라 렌더링 결과를 본다.** 선언이 partial 로 옮겨 가도
	// 브라우저가 받는 것은 같아야 하고, 검사가 봐야 하는 것도 그쪽이다.
	l := newBuiltinLoader()
	v := fullView()
	v.Data = payloadFor("page.html")
	var b bytes.Buffer
	if err := l.Render(&b, "page.html", v); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, `name="viewport"`) {
		t.Error("뷰포트 선언이 없다 — 모바일 브라우저가 데스크톱 폭으로 축소해 그린다")
	}
	if !strings.Contains(out, "width=device-width") {
		t.Error("뷰포트가 기기 폭을 따르지 않는다")
	}

	css, err := builtinFS.ReadFile("builtin/static/css/style.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), "@media") {
		t.Error("스타일시트에 폭에 반응하는 규칙이 없다")
	}
	// 고정 픽셀 폭은 좁은 화면에서 가로 스크롤을 만든다.
	if regexp.MustCompile(`(?m)^\s*width:\s*\d{3,}px`).Match(css) {
		t.Error("고정 픽셀 폭이 있다 — 좁은 화면에서 가로 스크롤이 생긴다")
	}
}

// D17 이 요구하는 템플릿이 **폴백 없이 전부** 내장 테마에 있다 (W4-04, FR-301).
func TestBuiltinThemeHasEveryContractTemplate(t *testing.T) {
	doc, err := os.ReadFile("../../docs/17-theme-contract.md")
	if err != nil {
		t.Skipf("문서를 읽을 수 없다: %v", err)
	}
	re := regexp.MustCompile("(?m)^\\| `([a-z0-9/_.-]+\\.html)` \\|")
	var want []string
	for _, m := range re.FindAllStringSubmatch(string(doc), -1) {
		want = append(want, m[1])
	}
	if len(want) < 40 {
		t.Fatalf("계약 템플릿을 %d개만 읽었다 — 문서 형식이 바뀌었다", len(want))
	}
	have := map[string]bool{}
	for _, n := range builtinTemplates(t) {
		have[n] = true
	}
	var missing []string
	for _, n := range want {
		if !have[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		t.Errorf("내장 테마에 없는 계약 템플릿 %d종: %v", len(missing), missing)
	}
}

// connectionLike mirrors what P-111 hands the theme.
type connectionLike struct {
	Provider      string
	Label         string
	Linked        bool
	CanDisconnect bool
}

// **결제 화면은 결제창을 실제로 연다.** P-407 은 공개 키와 금액을 `data-`
// 속성으로 내고 「테마의 JS 가 읽는다」고 적어 두었는데, 그 JS 가 없었다 —
// 승인 콜백만 있고 결제창이 뜨지 않아 돈이 오가는 길이 끊겨 있었다.
func TestBuiltinPayPageOpensTheTossWidget(t *testing.T) {
	b, err := fs.ReadFile(Builtin(), "shop/pay.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`https://js.tosspayments.com/v2/standard`, // 공식 v2 SDK
		`widgets.requestPayment(`,                 // 결제창 호출
		`"/checkout/success"`, `"/checkout/fail"`, // P-408·P-409 로 돌아온다
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("shop/pay.html 에 %s 가 없다 — 결제창이 열리지 않는다", want)
		}
	}
}

// **머리글의 관리자 링크는 권한이 있는 사람에게만 있고, 있으면 눌린다.**
// 표시 이름을 흐린 글자(`<span class="who">`)로만 두었더니 이름이 「관리자」인
// 계정에서 「눌리지 않는 관리자 버튼」으로 읽혔다 — 이름은 내 정보 링크이고,
// 관리자 화면 링크는 admin.access 가 있을 때만 그려진다.
func TestHeaderAdminLinkIsForAdminsAndClickable(t *testing.T) {
	l := newBuiltinLoader()
	render := func(admin bool) string {
		v := fullView()
		v.User.DisplayName = "관리자"
		v.Can = map[string]bool{"admin.access": admin}
		var b bytes.Buffer
		if err := l.Render(&b, "page.html", v); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	const adminLink = `<a href="/admin/">관리자 화면</a>`

	admin := render(true)
	if !strings.Contains(admin, adminLink) {
		t.Error("admin.access 가 있는데 관리자 화면 링크가 없다")
	}
	member := render(false)
	if strings.Contains(member, `href="/admin/"`) {
		t.Error("admin.access 가 없는데 관리자 화면 링크가 그려졌다")
	}
	for name, html := range map[string]string{"관리자": admin, "회원": member} {
		if strings.Contains(html, `<span class="who"`) {
			t.Errorf("%s: 표시 이름이 눌리지 않는 글자다", name)
		}
		if !strings.Contains(html, `<a class="who" href="/me"`) {
			t.Errorf("%s: 표시 이름이 내 정보 링크가 아니다", name)
		}
	}
	// 로그인하지 않으면 둘 다 없다.
	v := fullView()
	v.User = nil
	var b bytes.Buffer
	if err := l.Render(&b, "page.html", v); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), `href="/admin/"`) || strings.Contains(b.String(), `class="who"`) {
		t.Error("비로그인 화면에 계정 영역이 그려졌다")
	}
}

// **내장한 에디터 파일이 기록한 해시와 같다** (DEC-2.2, FR-513). 버전 파일만 고치고 파일을
// 안 바꾸거나 그 반대면 무엇이 들어 있는지 아무도 모른다 — 해시로 대조한다.
func TestSunEditorVersionFileMatchesTheVendoredFiles(t *testing.T) {
	ver, err := fs.ReadFile(Builtin(), "static/vendor/suneditor/SUNEDITOR.VERSION")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, line := range strings.Split(string(ver), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || f[0] != "sha256" {
			continue
		}
		b, err := fs.ReadFile(Builtin(), "static/vendor/suneditor/"+f[2])
		if err != nil {
			t.Fatalf("%s: %v", f[2], err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(b)); got != f[1] {
			t.Errorf("%s 의 sha256 = %s, 기록은 %s", f[2], got, f[1])
		}
		checked++
	}
	if checked != 3 {
		t.Errorf("해시를 대조한 파일 %d 개, want 3 (js·css·ko)", checked)
	}
}

// **갤러리 목록은 카드마다 썸네일을 그리고, 비밀글은 사진을 내지 않는다** (FR-515).
// 목록은 비밀글 제목을 누구에게나 보이므로 썸네일까지 그리면 본문 사진이 샌다.
func TestGalleryListShowsCoversButNotSecretOnes(t *testing.T) {
	l := newBuiltinLoader()
	data := payloadFor("board/list.html").(map[string]any)
	data["Board"] = boardLike{ID: "b1", Slug: "works", Name: "시공 사례", ListStyle: "gallery"}
	data["Posts"] = []postLike{
		{ID: "p1", Title: "거실 마루", CoverMediaID: "0b6f3c3e-0000-4000-8000-000000000001"},
		{ID: "p2", Title: "비밀 현장", IsSecret: true, CoverMediaID: "0b6f3c3e-0000-4000-8000-000000000002"},
	}
	v := fullView()
	v.Data = data
	var b bytes.Buffer
	if err := l.Render(&b, "board/list.html", v); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, `src="/media/0b6f3c3e-0000-4000-8000-000000000001/thumb"`) {
		t.Error("갤러리 카드에 썸네일이 없다")
	}
	if strings.Contains(out, "0b6f3c3e-0000-4000-8000-000000000002") {
		t.Error("비밀글의 사진이 갤러리에 나왔다")
	}
	if strings.Contains(out, `class="board-list"`) {
		t.Error("갤러리 게시판이 표 목록까지 그렸다")
	}
}

// **가입을 닫으면 가입으로 가는 길이 화면에서 사라진다** (FR-217). 머리글·로그인 화면의
// 링크와 가입 폼이다 — 남겨 두면 누른 사람은 403 을 만난다. 막는 것은 서버이고 이것은 안내다.
func TestClosedSignupHidesTheWayIn(t *testing.T) {
	l := newBuiltinLoader()
	render := func(name string, closed bool) string {
		v := fullView()
		v.User = nil
		v.Site.SignupClosed = closed
		var b bytes.Buffer
		if err := l.Render(&b, name, v); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	for _, name := range []string{"page.html", "auth/login.html", "auth/signup.html"} {
		if open := render(name, false); !strings.Contains(open, `"/signup"`) {
			t.Errorf("%s: 가입이 열려 있는데 가입 링크·폼이 없다", name)
		}
		if closed := render(name, true); strings.Contains(closed, `"/signup"`) {
			t.Errorf("%s: 가입을 닫았는데 가입 링크·폼이 남았다", name)
		}
	}
	if !strings.Contains(render("auth/signup.html", true), "회원가입을 받지 않습니다") {
		t.Error("닫힌 가입 화면이 이유를 말하지 않는다")
	}
}

// 가입 접수 화면은 새 가입과 중복 가입이 함께 쓴다 (FR-210). 인증이 꺼진
// 사이트에서는 확인 메일이 가지 않으므로 「확인 메일을 보냈습니다」라고 하면
// 거짓이다 — 그때는 로그인을 안내한다.
func TestSignupSentCopyFollowsVerificationSetting(t *testing.T) {
	l := newBuiltinLoader()
	render := func(data map[string]any) string {
		v := fullView()
		v.User = nil
		v.Data = data
		var b bytes.Buffer
		if err := l.Render(&b, "auth/signup-sent.html", v); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	on := render(map[string]any{"Email": "a@example.com"})
	if !strings.Contains(on, "확인 메일을 보냈습니다") {
		t.Error("인증이 켜진 사이트의 접수 화면이 확인 메일을 말하지 않는다")
	}
	off := render(map[string]any{"Email": "a@example.com", "NoVerify": true})
	if strings.Contains(off, "확인 메일") {
		t.Error("인증이 꺼진 사이트에서 「확인 메일」을 말한다 — 보내지 않는 메일이다")
	}
	if !strings.Contains(off, "로그인</a>해 주세요") {
		t.Error("인증이 꺼진 사이트의 접수 화면에 로그인 안내가 없다")
	}
}

// 인증 메일 재발송(P-113)은 세션의 계정으로 보낸다 — 주소를 받지 않는다. 가입
// 직후에는 로그인 전이라, 버튼을 그리면 누르는 순간 로그인 화면으로 튕긴다.
// 로그인 전에는 버튼 대신 로그인 안내, 로그인 뒤(재발송 결과 화면)에는 버튼.
func TestSignupSentOffersResendOnlyWhenLoggedIn(t *testing.T) {
	l := newBuiltinLoader()
	render := func(loggedIn bool) string {
		v := fullView()
		if !loggedIn {
			v.User = nil
		}
		v.Data = map[string]any{"Email": "a@example.com"}
		var b bytes.Buffer
		if err := l.Render(&b, "auth/signup-sent.html", v); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	anon := render(false)
	if strings.Contains(anon, `action="/verify/resend"`) {
		t.Error("로그인 전인데 재발송 버튼을 그린다 — 누르면 로그인 화면으로 튕긴다")
	}
	// 머리글에도 로그인 링크가 있으므로 링크가 아니라 안내 문장을 찾는다. 인증 전 계정은
	// 로그인되지 않고, 로그인 시도가 인증 메일을 다시 보낸다 (FR-214).
	if !strings.Contains(anon, "인증 메일을 다시 보내") {
		t.Error("로그인 전 접수 화면에 재발송 방법(로그인) 안내가 없다")
	}
	if !strings.Contains(render(true), `action="/verify/resend"`) {
		t.Error("로그인한 미인증 회원에게 재발송 버튼이 없다")
	}
}

// pagerFixture is what app.pagerFor hands partials/pagination.html: numbered
// links with one current, and every edge link set, so that each branch of the
// partial is drawn — an empty edge would skip its {{with}}.
func pagerFixture(base string, cur int) map[string]any {
	type pageLink struct {
		No      int
		URL     string
		Current bool
	}
	var pages []pageLink
	for n := 1; n <= 3; n++ {
		pages = append(pages, pageLink{No: n, URL: base + "?page=" + string(rune('0'+n)), Current: n == cur})
	}
	return map[string]any{
		"Base": base, "Query": struct{ Search string }{Search: "검색어"},
		"Total": int64(60), "PageNo": cur, "PrevPage": cur - 1, "NextPage": cur + 1,
		"HasPrev": cur > 1, "HasNext": true, "LastPage": 3, "Pages": pages,
		"FirstURL": base, "PrevURL": base, "NextURL": base + "?page=3", "LastURL": base + "?page=3",
		// 검색은 상한을 넘은 경우로 그린다 — 「10,000건 이상」 분기도 렌더링 검사를 지난다.
		"Capped": base == "/search", "CapTotal": int64(10000),
	}
}

// 글 제목·댓글 칸에 글자 수 상한이 있다 — 없으면 붙여넣기 한 번에 서버 거절까지 간다.
func TestWritingInputsCarryTheirLimits(t *testing.T) {
	l := newBuiltinLoader()
	for name, want := range map[string]string{
		"board/form.html":   `maxlength="200"`,
		"comment/form.html": `name="body" maxlength="2000"`,
	} {
		v := fullView()
		v.Data = payloadFor(name)
		var b bytes.Buffer
		if err := l.Render(&b, name, v); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(b.String(), want) {
			t.Errorf("%s 에 %s 가 없다", name, want)
		}
	}
}

// 수정 폼이 「여러 개 선택」의 저장값을 고른 채 그린다 — 안 그리면 제목만 고쳐
// 저장해도 그 값이 지워졌다 (실측: "tags":["빨강","파랑"] 이 사라짐).
func TestMultiselectKeepsStoredChoices(t *testing.T) {
	l := newBuiltinLoader()
	v := fullView()
	v.Data = payloadFor("board/form.html") // tags = ["B"]
	var b bytes.Buffer
	if err := l.Render(&b, "board/form.html", v); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, `<option value="B" selected>`) || strings.Contains(out, `<option value="A" selected>`) {
		t.Error("여러 개 선택의 저장값이 되살아나지 않았다")
	}
}

// 로그인한 머리글에 내 정보로 가는 링크는 이름 하나다 — 「내 정보」를 따로 두었더니
// 같은 곳으로 두 번 가는 링크가 생겼고, 1280px 에서도 계정 링크가 둘째 줄로 밀렸다.
func TestHeaderLinksToProfileOnce(t *testing.T) {
	l := newBuiltinLoader()
	v := fullView()
	var b bytes.Buffer
	if err := l.Render(&b, "page.html", v); err != nil {
		t.Fatal(err)
	}
	header := b.String()
	if i := strings.Index(header, "</header>"); i > 0 {
		header = header[:i]
	}
	if n := strings.Count(header, `href="/me"`); n != 1 {
		t.Errorf("머리글의 내 정보 링크 %d개, want 1", n)
	}
}

// 주문 뒤 화면은 배송비를 따로 보인다. 결제 금액만 있으면 품목 합계와 결제
// 금액이 맞지 않는데 왜 다른지 화면 어디에도 없다.
func TestOrderPagesShowShippingFee(t *testing.T) {
	l := newBuiltinLoader()
	for _, name := range []string{"order/view.html", "order/receipt.html"} {
		v := fullView()
		v.Data = payloadFor(name) // 품목 26,000원 · 결제 29,000원
		var b bytes.Buffer
		if err := l.Render(&b, name, v); err != nil {
			t.Fatal(err)
		}
		out := b.String()
		for _, want := range []string{"<dt>상품 합계</dt><dd>26,000원</dd>", "<dt>배송비</dt><dd>3,000원</dd>"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s 에 %q 가 없다", name, want)
			}
		}
	}
}

// 주문서가 받은 배송 메모를 구매자도 다시 본다 — 무엇을 요청했는지 확인할 곳이
// 없으면 받기만 하는 칸이다. 메모가 없으면 줄도 없다.
func TestOrderPagesShowDeliveryMemo(t *testing.T) {
	l := newBuiltinLoader()
	for _, name := range []string{"order/view.html", "order/receipt.html"} {
		for _, memo := range []string{"부재 시 경비실", ""} {
			v := fullView()
			data := payloadFor(name).(map[string]any)
			o := data["Order"].(orderLike)
			o.DeliveryMemo = memo
			data["Order"] = o
			v.Data = data
			var b bytes.Buffer
			if err := l.Render(&b, name, v); err != nil {
				t.Fatal(err)
			}
			has := strings.Contains(b.String(), "배송 메모")
			if memo != "" && (!has || !strings.Contains(b.String(), memo)) {
				t.Errorf("%s 에 배송 메모 %q 가 없다", name, memo)
			}
			if memo == "" && has {
				t.Errorf("%s: 메모가 없는데 「배송 메모」 줄이 있다", name)
			}
		}
	}
}

// 바꿀 옵션이 하나도 없는 교환 폼은 이유를 말하고 보낼 버튼을 그리지 않는다 —
// 전에는 선택칸만 조용히 사라지고 버튼은 남아, 누르면 422 였다.
func TestExchangeFormWithoutOptionsSaysWhy(t *testing.T) {
	l := newBuiltinLoader()
	v := fullView()
	data := payloadFor("order/return-form.html").(map[string]any)
	data["Exchange"] = nil
	data["Error"] = ""
	v.Data = data
	var b bytes.Buffer
	if err := l.Render(&b, "order/return-form.html", v); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "교환할 수 있는 다른 옵션이 없습니다") {
		t.Error("옵션 없는 교환 폼이 이유를 말하지 않는다")
	}
	if strings.Contains(out, `교환 요청</button>`) {
		t.Error("옵션 없는 교환 폼에 보내기 버튼이 있다")
	}
}

// 교환 건은 무엇으로 바꿔 받는지 보인다 — 구매자가 요청한 것을 다시 확인할 곳이다.
func TestReturnsPageShowsTheExchangeOption(t *testing.T) {
	l := newBuiltinLoader()
	v := fullView()
	data := payloadFor("order/returns.html").(map[string]any)
	rets := data["Returns"].([]returnLike)
	rets[0].Kind, rets[0].NewOption = "교환", "색상: 흰색"
	v.Data = data
	var b bytes.Buffer
	if err := l.Render(&b, "order/returns.html", v); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "색상: 흰색") {
		t.Error("반품·교환 내역에 교환으로 받을 옵션이 없다")
	}
}

// 상품 설명의 줄바꿈이 살아 있다 — 관리자는 여러 줄 칸에 쓰는데 상품 화면이 그대로
// 찍어 한 문단으로 붙었다(게시판 본문·댓글·페이지는 줄바꿈을 살린다).
func TestProductDescriptionKeepsLineBreaks(t *testing.T) {
	l := newBuiltinLoader()
	v := fullView()
	data := payloadFor("shop/product.html").(map[string]any)
	p := data["Product"].(productLike)
	p.Description = "첫 줄\n둘째 줄"
	data["Product"] = p
	v.Data = data
	var b bytes.Buffer
	if err := l.Render(&b, "shop/product.html", v); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "첫 줄<br>둘째 줄") {
		t.Error("상품 설명의 줄바꿈이 사라졌다")
	}
}

// 상품 목록은 상세와 같은 말을 쓴다 — 조합이 없으면 「판매 준비 중」, 있는데 재고가
// 없으면 「품절」. 조합 없는 상품을 목록은 「품절」, 상세는 「판매 준비 중」이라 했다.
func TestProductListNamesStockStateLikeTheDetail(t *testing.T) {
	l := newBuiltinLoader()
	v := fullView()
	data := payloadFor("shop/list.html").(map[string]any)
	data["Products"] = []productLike{
		{ID: "p1", Slug: "ready", Name: "준비중", HasVariants: false},
		{ID: "p2", Slug: "out", Name: "품절품", HasVariants: true, InStock: false},
		{ID: "p3", Slug: "on", Name: "판매품", HasVariants: true, InStock: true}}
	v.Data = data
	var b bytes.Buffer
	if err := l.Render(&b, "shop/list.html", v); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if strings.Count(out, "판매 준비 중") != 1 || strings.Count(out, ">품절<") != 1 {
		t.Errorf("판매 준비 중 %d개·품절 %d개, want 1·1", strings.Count(out, "판매 준비 중"), strings.Count(out, ">품절<"))
	}
}

// 영수증은 증빙에 필요한 칸을 갖는다 — 결제수단, 판매자(사업자 정보), 받는 분·연락처
// 라벨. 주문 상세와 거의 같은 표뿐이라 영수증이라 부르기에 모자랐다.
func TestReceiptCarriesWhatAReceiptNeeds(t *testing.T) {
	l := newBuiltinLoader()
	v := fullView() // Site.Business = {상호: 온돌}
	data := payloadFor("order/receipt.html").(map[string]any)
	data["PayMethod"] = "카드"
	v.Data = data
	var b bytes.Buffer
	if err := l.Render(&b, "order/receipt.html", v); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	main := out[strings.Index(out, "<h1>주문서·영수증</h1>"):]
	if i := strings.Index(main, "</section>"); i > 0 {
		main = main[:i]
	}
	for _, want := range []string{"<dt>결제수단</dt><dd>카드</dd>", "<dt>상호</dt><dd>온돌</dd>",
		"<dt>받는 분</dt>", "<dt>연락처</dt>", "<dt>주문자</dt>"} {
		if !strings.Contains(main, want) {
			t.Errorf("영수증 본문에 %q 가 없다", want)
		}
	}
}

// 취소·환불 화면은 할 수 있는 것만 낸다 — 남은 금액이 0원이면 환불 요청 폼이 없고,
// 상태 기계가 취소를 허락하지 않으면 「주문 취소」가 없다. 남은 0개에도 두 버튼이 눌렸다.
func TestRefundsPageOffersOnlyWhatIsPossible(t *testing.T) {
	l := newBuiltinLoader()
	render := func(remaining int, canCancel bool) string {
		v := fullView()
		data := payloadFor("order/refunds.html").(map[string]any)
		data["Remaining"], data["CanCancel"] = remaining, canCancel
		v.Data = data
		var b bytes.Buffer
		if err := l.Render(&b, "order/refunds.html", v); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	done := render(0, false)
	if strings.Contains(done, "환불 요청</button>") || strings.Contains(done, "주문 취소</button>") {
		t.Error("남은 금액 0원·취소 불가인데 요청 버튼이 있다")
	}
	if !strings.Contains(done, "환불할 수 있는 금액이 남아 있지 않습니다") {
		t.Error("왜 요청할 수 없는지 말하지 않는다")
	}
	open := render(24000, true)
	if !strings.Contains(open, "환불 요청</button>") || !strings.Contains(open, "주문 취소</button>") {
		t.Error("남은 금액이 있고 취소할 수 있는데 버튼이 없다")
	}
}

// 메일을 보낼 수 없는 사이트의 재설정 화면은 이유를 말하고 폼을 그리지 않는다.
func TestResetRequestWithoutMailShowsNoForm(t *testing.T) {
	l := newBuiltinLoader()
	v := fullView()
	v.User = nil
	v.Data = map[string]any{"NoMail": true, "Error": "이 사이트는 아직 메일을 보낼 수 없어"}
	var b bytes.Buffer
	if err := l.Render(&b, "auth/password-reset-request.html", v); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "이 사이트는 아직 메일을 보낼 수 없어") || strings.Contains(out, `action="/password/reset"`) {
		t.Error("메일 미설정 재설정 화면이 이유를 말하지 않거나 폼을 그린다")
	}
}

// 가입이 거절돼도 쓴 이메일·표시 이름이 남는다(비밀번호는 아니다). 핸들러는 Form 을 넘기는데
// 템플릿이 쓰지 않아 칸이 비었다. 비밀번호 확인 칸과 로그인 링크가 있다.
func TestSignupKeepsWhatWasTyped(t *testing.T) {
	type formLike struct{ Email, Password, DisplayName string }
	l := newBuiltinLoader()
	v := fullView()
	v.User = nil
	v.Data = map[string]any{"Error": "이메일 형식이 올바르지 않습니다.",
		"Form": formLike{Email: "a@example", Password: "secret-pass-1", DisplayName: "온돌"}}
	var b bytes.Buffer
	if err := l.Render(&b, "auth/signup.html", v); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{`value="a@example"`, `value="온돌"`, `name="password_confirm"`, `href="/login"`} {
		if !strings.Contains(out, want) {
			t.Errorf("가입 화면에 %q 가 없다", want)
		}
	}
	if strings.Contains(out, "secret-pass-1") {
		t.Error("가입 화면이 비밀번호를 되살렸다")
	}
}

// **통합 검색은 검색어가 없을 때만 입력칸에 초점을 준다.**
//
// 결과 화면에서도 초점이 걸려, 휴대폰에서는 들어가자마자 키보드가 올라와 결과를
// 가렸다(iPhone Safari). 검색어를 고치려는 사람은 칸을 누르면 된다.
func TestSearchFocusesOnlyWithoutQuery(t *testing.T) {
	l := newBuiltinLoader()
	render := func(q string) string {
		v := fullView()
		data := payloadFor("search.html").(map[string]any)
		data["Query"] = struct{ Search string }{Search: q}
		v.Data = data
		var b bytes.Buffer
		if err := l.Render(&b, "search.html", v); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	if strings.Contains(render("검색어"), "autofocus") {
		t.Error("결과 화면의 검색칸에 autofocus 가 있다 — 휴대폰 키보드가 결과를 가린다")
	}
	if !strings.Contains(render(""), "autofocus") {
		t.Error("빈 검색 화면에 autofocus 가 없다 — 칸을 한 번 더 눌러야 한다")
	}
}
