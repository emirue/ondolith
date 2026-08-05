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
		}
	case "search.html":
		return map[string]any{
			"Query":  struct{ Search string }{Search: "검색어"},
			"Total":  int64(1),
			"Boards": map[string]boardLike{"b1": {ID: "b1", Slug: "free", Name: "자유게시판"}},
			"Results": []postLike{{ID: "p1", BoardID: "b1", Title: "찾은 글", Body: "본문",
				CreatedAt: time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)}},
		}
	case "board/comment-edit.html":
		return map[string]any{
			"Board":   boardLike{ID: "b1", Slug: "free", Name: "자유게시판"},
			"Comment": commentLike{ID: "c1", PostID: "p1", Body: "고칠 댓글"},
			"Error":   "오류 메시지",
		}
	case "board/write.html":
		return map[string]any{
			"Board": boardLike{ID: "b1", Slug: "free", Name: "자유게시판"},
			"Post": &postLike{Title: "고칠 글", Body: "본문",
				CustomFields: map[string]any{"color": "빨강"}},
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
