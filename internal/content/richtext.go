package content

import (
	"html/template"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
)

// richPolicy is the allow-list every rich-text body passes through (FR-513, D60).
//
// **정화는 직접 만들지 않는다** (CLAUDE.md 규칙 6과 같은 이유) — 허용목록 정화기
// bluemonday 의 UGC 정책에서 출발해 에디터가 쓰는 것만 더한다. 본문 HTML 은
// 브라우저가 보낸 것이라 에디터가 만들었다는 보장이 없다.
//
//   - 인라인 스타일은 **속성별로 값까지** 검사한다. 색·정렬·글자 크기·크기만이고
//     `url(`·`expression(` 이 들어갈 자리가 없다
//   - `class` 는 없다(UGC 기본). 사이트의 클래스(`adm-alert` 등)를 빌려 화면을
//     흉내 내는 길이다
//   - iframe 은 **유튜브·구글 지도의 embed 주소일 때만** 남는다. src 가 허용 밖이면
//     남는 속성이 없어 요소째 빠진다
//   - 이미지는 http(s)·상대 주소만. `data:` 는 받지 않는다 — 사진은 업로드로 온다(FR-514)
var richPolicy = func() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AllowURLSchemes("http", "https", "mailto", "tel")
	p.RequireNoFollowOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)

	color := regexp.MustCompile(`(?i)^(#[0-9a-f]{3,8}|rgba?\(\s*\d{1,3}\s*,\s*\d{1,3}\s*,\s*\d{1,3}\s*(,\s*(0|1|0?\.\d+)\s*)?\))$`)
	p.AllowStyles("color", "background-color").Matching(color).Globally()
	p.AllowStyles("text-align").MatchingEnum("left", "right", "center", "justify").Globally()
	p.AllowStyles("font-size").Matching(regexp.MustCompile(`^\d{1,3}(\.\d+)?(px|pt|em|rem|%)$`)).Globally()
	size := regexp.MustCompile(`^\d{1,4}(\.\d+)?(px|%)?$`)
	p.AllowStyles("width", "height", "max-width").Matching(size).OnElements("img", "figure", "iframe", "table", "td", "th")
	p.AllowAttrs("width", "height").Matching(size).OnElements("img", "iframe")

	p.AllowElements("u", "s", "figcaption")
	p.AllowAttrs("src").Matching(embedSrc).OnElements("iframe")
	p.AllowAttrs("allowfullscreen").Matching(regexp.MustCompile(`^(|allowfullscreen|true)$`)).OnElements("iframe")
	p.AllowAttrs("frameborder").Matching(regexp.MustCompile(`^0$`)).OnElements("iframe")
	// 유튜브·지도는 스크립트가 있어야 돈다. 출처가 우리와 다른 iframe 이라
	// allow-same-origin 은 **그 서비스 자신의** 출처를 뜻한다 — 우리 쿠키에 닿지 않는다.
	p.RequireSandboxOnIFrame(bluemonday.SandboxAllowScripts, bluemonday.SandboxAllowSameOrigin,
		bluemonday.SandboxAllowPopups, bluemonday.SandboxAllowPresentation)
	return p
}()

// embedSrc is the iframe allow-list: YouTube embeds and Google Maps embeds.
// 지도는 「오시는 길」(FR-714 와 함께 적은 요구) 때문이고, 둘 다 공유 메뉴의
// 「퍼가기」가 주는 주소 그대로다.
var embedSrc = regexp.MustCompile(`^https://(www\.youtube(-nocookie)?\.com/embed/[A-Za-z0-9_-]{1,64}(\?[A-Za-z0-9_=&;.%-]*)?|www\.google\.com/maps/embed\?pb=[A-Za-z0-9!._%:-]+)$`)

// SanitizeRichText returns body HTML with only what richPolicy allows.
func SanitizeRichText(raw string) string {
	return strings.TrimSpace(richPolicy.Sanitize(raw))
}

// blockTags end a line in the plain-text form.
var blockTags = map[string]bool{
	"p": true, "div": true, "br": true, "li": true, "tr": true, "h1": true, "h2": true,
	"h3": true, "h4": true, "h5": true, "h6": true, "blockquote": true, "figure": true,
	"figcaption": true, "table": true, "ul": true, "ol": true, "hr": true, "pre": true,
}

// RichTextPlain is the text of body HTML: 검색(tsvector)·요약·OG 설명·사이트맵이
// 쓴다 (FR-513). 블록이 끝나면 줄을 바꾸고, 엔티티는 글자로 돌아오고, 빈 줄은
// 접는다. 태그가 섞이면 「<p>시공」 이 검색어가 된다.
func RichTextPlain(body string) string {
	var b strings.Builder
	z := html.NewTokenizer(strings.NewReader(body))
	for {
		switch z.Next() {
		case html.ErrorToken: // 끝(io.EOF) 이거나 깨진 입력 — 둘 다 여기까지다
			return collapseLines(b.String())
		case html.TextToken:
			b.Write(z.Text())
		case html.StartTagToken, html.EndTagToken, html.SelfClosingTagToken:
			if name, _ := z.TagName(); blockTags[string(name)] {
				b.WriteByte('\n')
			}
		}
	}
}

func collapseLines(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// Rendered is the post body for a theme: 에디터로 쓴 글이면 HTML, 아니면 빈 값이다.
// 테마는 `{{with .Post.Rendered}}…{{else}}{{nl2br .Post.Body}}{{end}}` 로 가른다 (D17).
func (p Post) Rendered() template.HTML { return renderRich(p.BodyHTML) }

// Rendered is Post.Rendered for a page.
func (p Page) Rendered() template.HTML { return renderRich(p.BodyHTML) }

// renderRich re-sanitises on the way out. 저장할 때 이미 정화했지만, 정책을 좁힌 뒤에도
// 예전에 저장된 HTML 이 예전 정책으로 나가지 않게 그릴 때마다 지금 정책을 다시 건다.
func renderRich(stored string) template.HTML {
	if stored == "" {
		return ""
	}
	// template.HTML: 방금 richPolicy 로 정화한 값이다 — 허용목록 밖의 요소·속성·
	// URL 은 이 줄에 닿기 전에 사라졌다 (CLAUDE.md 규칙 5).
	return template.HTML(SanitizeRichText(stored))
}

// RichBody turns submitted editor HTML into what is stored: 정화된 HTML 과 그 평문.
// 정화 뒤에 남는 글자가 없으면 빈 두 값이다 — 호출자가 「본문을 쓰세요」로 다룬다.
func RichBody(submitted string) (html, plain string) {
	html = SanitizeRichText(submitted)
	plain = RichTextPlain(html)
	if plain == "" && !strings.Contains(html, "<img") && !strings.Contains(html, "<iframe") {
		return "", ""
	}
	return html, plain
}

// FormBody is what a post or page form stores (FR-513). 에디터가 붙어 있으면
// body_html 을 보내고, JS 가 없거나 꺼져 있으면 body 평문만 온다. HTML 이 정화 뒤에
// 아무것도 남기지 않으면 평문으로 돌아간다 — 에디터가 함께 보내는 평문 사본이다.
func FormBody(plain, submittedHTML string) (body, bodyHTML string) {
	if strings.TrimSpace(submittedHTML) != "" {
		if h, p := RichBody(submittedHTML); h != "" {
			return p, h
		}
	}
	return plain, ""
}
