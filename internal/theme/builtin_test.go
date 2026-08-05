package theme

import (
	"bytes"
	"io/fs"
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
	Title string
	Body  string
}

// payloadFor returns the .Data a given screen is written against (D12/D13 pin
// this down per screen).
func payloadFor(name string) any {
	switch name {
	case "page.html":
		return &pageLike{Title: "회사 소개", Body: "본문입니다.\n둘째 줄"}
	case "shop/list.html":
		return map[string]any{
			"Products": []productLike{
				{ID: "p1", Slug: "tee", Name: "티셔츠", BasePrice: 12000, MinDelta: 1000, InStock: true},
				{ID: "p2", Slug: "cap", Name: "모자", BasePrice: 9000, InStock: false},
			},
			"Categories": []categoryLike{{ID: "c1", Slug: "top", Name: "상의"}},
			"Sort":       "price",
			"Page":       1,
		}
	case "shop/search.html":
		return map[string]any{
			"Query":    "티",
			"Products": []productLike{{ID: "p1", Slug: "tee", Name: "티셔츠", BasePrice: 12000}},
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
			"Email": "a@example.com",
			"Terms": []termLike{{ID: "t1", Kind: "이용약관", Version: "v1", Required: true}},
			"Error": "필수 약관에 동의해야 합니다",
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
			"Page": 1,
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
				TrackingNo: "T-1", ShippedAt: "2026-08-05"}},
		}
	case "order/guest-form.html":
		return map[string]any{"Error": "주문 정보를 찾을 수 없습니다."}
	case "shop/fail.html":
		return map[string]any{"Message": "결제를 취소하셨습니다."}
	case "shop/cart.html":
		return map[string]any{
			"Items": []cartItemLike{{ID: "ci1", VariantID: "v1", ProductID: "p1",
				Name: "티셔츠", Option: map[string]string{"크기": "L"},
				UnitPrice: 13000, Quantity: 2, Stock: 3, Sellable: true}},
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
			"Total": int64(1),
			"Query": struct{ Search string }{Search: "검색어"},
			"Pager": map[string]any{
				"Base": "/board/free", "Query": struct{ Search string }{Search: "검색어"},
				"Total": int64(1), "PageNo": 1, "PrevPage": 0, "NextPage": 2,
				"HasPrev": false, "HasNext": true,
			},
			"Columns":  []fieldLike{{Key: "color", Label: "색상"}},
			"CanWrite": true,
		}
	case "board/view.html":
		return map[string]any{
			"Board": boardLike{ID: "b1", Slug: "free", Name: "자유게시판"},
			"Post": postLike{ID: "p1", Title: "첫 글", Body: "본문\n둘째 줄",
				AuthorName: "홍길동", ViewCount: 12,
				CustomFields: map[string]any{"color": "빨강"},
				CreatedAt:    time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)},
			"Comments": []commentLike{
				{ID: "c1", AuthorName: "홍길동", Body: "댓글",
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
		}
	case "comment/form.html":
		return map[string]any{
			"Board":   boardLike{ID: "b1", Slug: "free", Name: "자유게시판"},
			"Comment": commentLike{ID: "c1", PostID: "p1", Body: "고칠 댓글"},
			"Error":   "오류 메시지",
		}
	case "board/form.html":
		return map[string]any{
			"Board": boardLike{ID: "b1", Slug: "free", Name: "자유게시판"},
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
					Options: []string{"A", "B"}}, Value: nil},
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
	AllowComments        bool
	PerPage              int
}

type postLike struct {
	ID, BoardID, Title, Body, AuthorName string
	CustomFields                         map[string]any
	IsPinned, IsSecret                   bool
	ViewCount, CommentCount              int64
	HasAttachment                        bool
	CreatedAt                            time.Time
}

type commentLike struct {
	ID, PostID, ParentID, AuthorName, Body string
	DeletedAt, CreatedAt                   time.Time
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

// 커머스 화면이 받는 모양. 실물 타입을 쓰지 않는 이유는 이 패키지가
// internal/commerce 를 import 하지 않기 위해서다 — 테마는 데이터 모양만 알면
// 되고, 그 반대 방향 의존은 테마를 커머스에 묶는다.
type productLike struct {
	ID, Slug, Name, Description string
	BasePrice, MinDelta         int
	InStock                     bool
}

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
	ID, VariantID, ProductID, Name string
	Option                         map[string]string
	UnitPrice, Quantity, Stock     int
	Sellable                       bool
}

type termLike struct {
	ID, Kind, Version, Body string
	Required                bool
}

type orderItemLike struct {
	ProductName, OptionLabel        string
	UnitPrice, Quantity, LineAmount int
}

type orderLike struct {
	OrderNo, Status                                           string
	Total                                                     int
	ReceiverName, ReceiverPhone, Postcode, Address1, Address2 string
	CreatedAt                                                 time.Time
	Items                                                     []orderItemLike
}

type shipmentLike struct{ Kind, Carrier, TrackingNo, ShippedAt string }
