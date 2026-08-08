package theme

import (
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// layoutClasses is the closed set of containers a screen may sit in.
//
// **테마에 표면(surface)이 몇 종류인지 정해 두는 것이 이 목록의 일이다.**
// 이것이 없던 동안 인증 화면 11개는 좁은 단(`auth`) 안에, 게시판 6개는 흰
// 패널(`page`) 안에 있었고, **커머스·주문 17개는 아무 상자에도 들어 있지
// 않았다** — 제목과 표가 배경 위에 그대로 떠 있었다. 브라우저에서 재보니
// 상품 상세의 `<h1>` 은 배경 투명·테두리 0·패딩 0 으로 972px 를 가로지르고
// 흰 상자는 폼 하나뿐이었다. 「클릭해서 들어가면 이상하다」의 정체다.
//
// 늘릴 때는 **그 상자가 무엇을 위한 것인지** 함께 적는다. 이름만 늘리면 이
// 목록은 「지금 쓰이는 클래스의 목록」이 되고, 그때부터 아무것도 막지 않는다.
var layoutClasses = map[string]string{
	"page":       "문서 패널 — 본문·폼·표가 있는 화면",
	"auth":       "좁은 단 — 로그인·가입·내 계정",
	"listing":    "격자 목록 — 항목 자체가 카드라 패널로 또 감싸지 않는다",
	"comments":   "댓글 묶음 — 글 본문 패널 아래에 따로 선다",
	"hero":       "홈의 첫 화면",
	"cards":      "홈의 요약 묶음",
	"error-page": "오류 화면",
}

// fragments render into another screen's markup, so they must NOT carry a
// container — the container is already there.
var fragments = map[string]string{
	"shop/variant.html": "P-304 — 상품 화면의 한 조각을 htmx 가 갈아 끼운다",
}

// **모든 화면의 최상위 요소는 선언된 상자 안에 있다.**
//
// 「첫 요소만」이 아니라 최상위 **전부**를 본다. 글 보기 화면은 본문 패널과
// 댓글이 형제인데, 첫 요소만 보면 댓글이 배경 위에 그대로 떠 있어도 통과한다.
func TestEveryScreenSitsInADeclaredContainer(t *testing.T) {
	files := bodyTemplates(t)
	if len(files) < 30 {
		t.Fatalf("본문 템플릿을 %d 개밖에 못 찾았다 — 검사가 헛돌았다", len(files))
	}
	for _, name := range files {
		src, err := fs.ReadFile(Builtin(), name)
		if err != nil {
			t.Fatal(err)
		}
		body, ok := bodyBlock(string(src))
		if !ok {
			continue // "body" 를 정의하지 않는 파일은 화면이 아니다
		}
		tops := topLevelTags(body)

		if why, isFragment := fragments[name]; isFragment {
			if len(tops) > 0 {
				t.Errorf("%s 는 조각인데 상자를 갖고 있다 (%s): %v", name, why, tops)
			}
			continue
		}
		if len(tops) == 0 {
			t.Errorf("%s 의 본문에 요소가 하나도 없다", name)
			continue
		}
		for _, tag := range tops {
			if !slices.ContainsFunc(classesOf(tag), func(c string) bool {
				_, ok := layoutClasses[c]
				return ok
			}) {
				t.Errorf("%s 의 최상위 %s 가 선언된 상자 밖에 있다 — "+
					"layoutClasses 중 하나를 고르거나, 새 상자면 그 목록에 뜻과 함께 적을 것",
					name, tag)
			}
		}
	}
}

// bodyTemplates lists every built-in .html outside partials/.
func bodyTemplates(t *testing.T) []string {
	t.Helper()
	var out []string
	err := fs.WalkDir(Builtin(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".html") {
			return nil
		}
		if p == "base.html" || path.Dir(p) == "partials" {
			return nil
		}
		out = append(out, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

// bodyBlock returns the text between `{{define "body"}}` and its matching
// `{{end}}`. 짝을 세는 이유는 본문 안에 if·with·range 가 있고, 첫 `{{end}}` 를
// 끝으로 보면 본문이 잘려 최상위 요소를 놓치기 때문이다.
func bodyBlock(src string) (string, bool) {
	const open = `{{define "body"}}`
	i := strings.Index(src, open)
	if i < 0 {
		return "", false
	}
	rest := src[i+len(open):]
	depth := 1
	for _, m := range actionRe.FindAllStringIndex(rest, -1) {
		switch word := firstWord(rest[m[0]:m[1]]); word {
		case "if", "with", "range", "define", "block":
			depth++
		case "end":
			depth--
			if depth == 0 {
				return rest[:m[0]], true
			}
		}
	}
	return rest, true
}

// **`(?s)` 가 있어야 한다.** 템플릿 주석은 여러 줄이고, 줄바꿈을 건너뛰지 않는
// 정규식은 그 주석을 지우지 못한다 — 주석 안에 예시로 적어 둔 마크업이 그대로
// 남아 아래 검사들이 있지도 않은 태그를 보고 운다. 액션은 `}}` 를 품을 수
// 없으므로 non-greedy 로 안전하다.
var actionRe = regexp.MustCompile(`(?s)\{\{.*?\}\}`)

func firstWord(action string) string {
	s := strings.TrimSuffix(strings.TrimPrefix(action, "{{"), "}}")
	s = strings.TrimLeft(s, "-/* \t\n")
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// voidTags never nest, so they must not push the depth counter.
var voidTags = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true,
	"hr": true, "img": true, "input": true, "link": true, "meta": true,
	"source": true, "track": true, "wbr": true,
}

var tagRe = regexp.MustCompile(`</?([a-zA-Z][a-zA-Z0-9]*)\b[^>]*>`)

// topLevelTags returns the opening tags that sit at depth 0 of the fragment.
func topLevelTags(body string) []string {
	// 액션을 지운다 — 속성 안의 `{{if}}` 가 `>` 를 품고 있어 태그 경계를
	// 흐트러뜨린다. 주석 안의 예시 마크업도 여기서 함께 사라진다.
	clean := actionRe.ReplaceAllString(body, " ")
	var out []string
	depth := 0
	for _, m := range tagRe.FindAllStringSubmatchIndex(clean, -1) {
		tag := clean[m[0]:m[1]]
		name := strings.ToLower(clean[m[2]:m[3]])
		switch {
		case voidTags[name] || strings.HasSuffix(tag, "/>"):
			if depth == 0 {
				out = append(out, tag)
			}
		case strings.HasPrefix(tag, "</"):
			depth--
		default:
			if depth == 0 {
				out = append(out, tag)
			}
			depth++
		}
	}
	return out
}

var classAttrRe = regexp.MustCompile(`class="([^"]*)"`)

func classesOf(tag string) []string {
	m := classAttrRe.FindStringSubmatch(tag)
	if m == nil {
		return nil
	}
	return strings.Fields(m[1])
}

// blockInP 는 `<p>` 안에 들어가면 안 되는 요소다.
//
// `<p>` 의 내용 모델은 phrasing content 다. 파서는 이 중 하나를 만나면 **문단을
// 먼저 닫고** 그 요소를 형제로 만든다 — 마크업은 그럴듯한데 DOM 이 다르다.
// 실제로 글 보기 화면의 `<p class="actions">` 안에 있던 삭제 폼이 그렇게 밖으로
// 밀려나, 버튼이 동작 줄에서 떨어져 홀로 그려졌다. **브라우저에서 재 보고서야
// 알았다** — 렌더링은 성공하고 응답은 200 이므로 다른 어떤 검사도 울지 않는다.
var blockInP = []string{
	"form", "div", "ul", "ol", "table", "section", "article", "nav",
	"h1", "h2", "h3", "h4", "h5", "h6", "p", "dl", "blockquote", "fieldset",
}

var pBlockRe = func() *regexp.Regexp {
	return regexp.MustCompile(`(?s)<p\b[^>]*>(.*?)</p>`)
}()

// **`<p>` 안에 블록 요소를 넣지 않는다.**
func TestNoBlockElementSitsInsideAParagraph(t *testing.T) {
	files := bodyTemplates(t)
	files = append(files, partialTemplates(t)...)
	if len(files) < 30 {
		t.Fatalf("템플릿을 %d 개밖에 못 찾았다 — 검사가 헛돌았다", len(files))
	}
	checked := 0
	for _, name := range files {
		src, err := fs.ReadFile(Builtin(), name)
		if err != nil {
			t.Fatal(err)
		}
		clean := actionRe.ReplaceAllString(string(src), " ")
		for _, m := range pBlockRe.FindAllStringSubmatch(clean, -1) {
			checked++
			for _, tag := range blockInP {
				if regexp.MustCompile(`<` + tag + `\b`).MatchString(m[1]) {
					t.Errorf("%s: `<p>` 안에 <%s> 가 있다 — 파서가 문단을 먼저 닫아 "+
						"그 요소가 형제가 된다. <div> 로 바꿀 것", name, tag)
				}
			}
		}
	}
	// 헛돌기 방지: `<p>…</p>` 쌍을 하나도 못 찾았으면 아무것도 검사하지 않았다.
	if checked < 10 {
		t.Fatalf("`<p>` 쌍을 %d 개밖에 못 찾았다 — 검사가 헛돌았다", checked)
	}
}

// partialTemplates lists the shared fragments.
func partialTemplates(t *testing.T) []string {
	t.Helper()
	entries, err := fs.ReadDir(Builtin(), "partials")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".html") {
			out = append(out, "partials/"+e.Name())
		}
	}
	return out
}
