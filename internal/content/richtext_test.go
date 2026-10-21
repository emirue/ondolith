package content

import (
	"strings"
	"testing"
)

// **정화를 지난 HTML 에는 실행할 수 있는 것이 남지 않는다** (FR-513, D60).
//
// 본문 HTML 은 브라우저가 보낸 것이다 — 에디터가 만들었다는 보장이 없다. 각 줄은
// 알려진 XSS 입력 하나와, 결과에 **남으면 안 되는** 조각이다.
func TestSanitizeRichTextRemovesScriptVectors(t *testing.T) {
	for _, tc := range []struct{ name, in, mustNot string }{
		{"script 요소", `<p>안녕</p><script>alert(1)</script>`, "<script"},
		{"이벤트 속성", `<img src="/media/x" onerror="alert(1)">`, "onerror"},
		{"javascript: 링크", `<a href="javascript:alert(1)">눌러</a>`, "javascript:"},
		{"대소문자·공백 섞은 javascript:", `<a href=" JaVaScRiPt:alert(1)">x</a>`, "alert(1)"},
		{"data: 이미지", `<img src="data:image/png;base64,AAAA">`, "data:"},
		{"style 의 url()", `<p style="background-image:url(javascript:alert(1))">x</p>`, "url("},
		{"style 의 expression", `<p style="color:expression(alert(1))">x</p>`, "expression"},
		{"허용 밖 iframe", `<iframe src="https://evil.example/x"></iframe>`, "evil.example"},
		{"iframe javascript:", `<iframe src="javascript:alert(1)"></iframe>`, "<iframe"},
		{"svg", `<svg><script>alert(1)</script></svg>`, "<svg"},
		{"object", `<object data="x.swf"></object>`, "<object"},
		{"form", `<form action="https://evil.example"><input name="pw"></form>`, "<form"},
		{"meta refresh", `<meta http-equiv="refresh" content="0;url=https://evil.example">`, "<meta"},
		{"base", `<base href="https://evil.example/">`, "<base"},
		{"style 요소", `<style>body{display:none}</style>`, "<style"},
		{"class 로 화면 흉내", `<p class="adm-alert">관리자 공지</p>`, "class="},
		// 지도는 키 방식 지도 블록으로 통일했다(FR-716) — 키 없는 구글 iframe 은 남지 않는다.
		{"구글 지도 iframe", `<iframe src="https://www.google.com/maps/embed?pb=!1m18!1m12"></iframe>`, "<iframe"},
		{"모르는 지도사", `<img src="/static/img/map.svg" data-map="evil" data-lat="1" data-lng="2">`, `data-map="evil"`},
		{"좌표 자리의 글", `<img src="/static/img/map.svg" data-map="kakao" data-lat="javascript:alert(1)">`, "javascript"},
		{"이미지 밖의 지도 속성", `<p data-map="kakao" data-lat="37.5">글</p>`, "data-map"},
	} {
		got := SanitizeRichText(tc.in)
		if strings.Contains(strings.ToLower(got), strings.ToLower(tc.mustNot)) {
			t.Errorf("%s: %q 가 남았다 → %s", tc.name, tc.mustNot, got)
		}
	}
}

// 에디터가 만드는 서식은 남는다 — 정화가 다 지우면 에디터를 붙인 의미가 없다.
func TestSanitizeRichTextKeepsEditorFormatting(t *testing.T) {
	for _, tc := range []struct{ name, in, must string }{
		{"굵게·기울임·밑줄·취소선", `<p><strong>굵게</strong> <em>기울임</em> <u>밑줄</u> <s>취소</s></p>`, "<u>밑줄</u>"},
		{"제목", `<h2>시공 사례</h2>`, "<h2>시공 사례</h2>"},
		{"목록", `<ul><li>하나</li></ul>`, "<li>하나</li>"},
		{"인용", `<blockquote>말</blockquote>`, "<blockquote>"},
		{"표", `<table><tbody><tr><td>칸</td></tr></tbody></table>`, "<td>칸</td>"},
		{"글자 색", `<span style="color: #ff0000">빨강</span>`, "color: #ff0000"},
		{"가운데 정렬", `<p style="text-align: center">가운데</p>`, "text-align: center"},
		{"우리 사진", `<img src="/media/0b6f3c3e-0000-4000-8000-000000000000" alt="거실">`, `src="/media/0b6f3c3e-0000-4000-8000-000000000000"`},
		{"외부 링크는 새 창·nofollow", `<a href="https://example.com/">링크</a>`, `rel="nofollow`},
		{"유튜브", `<iframe src="https://www.youtube.com/embed/dQw4w9WgXcQ" allowfullscreen></iframe>`, `src="https://www.youtube.com/embed/dQw4w9WgXcQ"`},
		{"지도 블록 (FR-716)", `<img src="/static/img/map.svg" alt="지도: 서울시청" data-map="kakao" data-lat="37.5665" data-lng="126.978" data-zoom="3" data-label="서울시청">`,
			`data-map="kakao" data-lat="37.5665" data-lng="126.978" data-zoom="3" data-label="서울시청"`},
	} {
		if got := SanitizeRichText(tc.in); !strings.Contains(got, tc.must) {
			t.Errorf("%s: %q 가 없다 → %s", tc.name, tc.must, got)
		}
	}
}

// **에디터로 넣은 유튜브가 글 화면에서 재생된다** (FR-513). 플레이어는 스크립트로 돈다 — sandbox 가
// 비어 있으면(`sandbox=""`) 「자바스크립트를 실행할 수 없습니다」만 보인다. 에디터는 sandbox 없이 보내고,
// 보낸 sandbox 값은 믿지 않는다. 저장된 글은 그릴 때 다시 정화하므로 그 경로도 본다.
func TestYouTubeEmbedCanRunItsPlayer(t *testing.T) {
	const want = `sandbox="allow-scripts allow-same-origin allow-popups allow-presentation"`
	src := `src="https://www.youtube.com/embed/dQw4w9WgXcQ"`
	for name, in := range map[string]string{
		"에디터가 보낸 모양":        `<iframe frameborder="0" allowfullscreen="" ` + src + ` width="100%"></iframe>`,
		"빈 sandbox 로 저장된 글": `<iframe ` + src + ` sandbox=""></iframe>`,
		"넓히려는 sandbox":      `<iframe ` + src + ` sandbox="allow-top-navigation allow-forms"></iframe>`,
	} {
		for path, got := range map[string]string{"정화": SanitizeRichText(in), "그리기": string(renderRich(in))} {
			if !strings.Contains(got, want) || strings.Count(got, "sandbox=") != 1 {
				t.Errorf("%s (%s): %s", name, path, got)
			}
		}
	}
}

// **평문은 검색·요약·OG 설명이 쓴다.** 문단은 줄로 갈라지고, 엔티티는 글자로
// 돌아오고, 한글은 그대로다 — 태그가 섞이면 「<p>시공」 이 검색어가 된다.
func TestRichTextPlainText(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"문단", `<p>첫 줄</p><p>둘째 줄</p>`, "첫 줄\n둘째 줄"},
		{"줄바꿈", `<p>한<br>글</p>`, "한\n글"},
		{"엔티티", `<p>A &amp; B &lt;태그&gt;</p>`, "A & B <태그>"},
		{"서식은 글자만", `<p><strong>강조</strong>된 말</p>`, "강조된 말"},
		{"목록", `<ul><li>하나</li><li>둘</li></ul>`, "하나\n둘"},
		{"빈 문단", `<p><br></p><p>내용</p>`, "내용"},
	} {
		if got := RichTextPlain(tc.in); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// **갤러리 카드의 사진은 본문의 첫 「우리」 사진이다** (FR-515). 바깥 주소의 그림은
// 썸네일이 없으므로 건너뛰고, 모양이 uuid 가 아니면 사진으로 치지 않는다.
func TestCoverOfPicksTheFirstUploadedPhoto(t *testing.T) {
	const id = "0b6f3c3e-0000-4000-8000-000000000001"
	for _, tc := range []struct{ name, html, want string }{
		{"우리 사진", `<p>글</p><img src="/media/` + id + `">`, id},
		{"바깥 그림 다음", `<img src="https://example.com/a.jpg"><img src="/media/` + id + `">`, id},
		{"썸네일 주소", `<img src="/media/` + id + `/thumb">`, id},
		{"사진 없음", `<p>글만</p>`, ""},
		{"uuid 아님", `<img src="/media/../../etc">`, ""},
	} {
		if got := CoverOf(tc.html); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// **허용 밖 주소의 iframe 은 요소째 빠진다.** 다른 허용 속성(id·title)이 있으면 src 만
// 빠진 빈 iframe 이 sandbox 를 달고 남았다 — 실을 것은 없지만 주석이 말한 동작이 아니었다
// (2026-10-21 점검에서 실행으로 확인).
func TestDisallowedIframeLeavesNothing(t *testing.T) {
	for _, in := range []string{
		`<iframe id="x" src="https://evil.example/x"></iframe>`,
		`<iframe title="t" src="https://evil.example/x">대체 글</iframe>`,
		`<p>앞</p><iframe id="y"></iframe><p>뒤</p>`,
		`<p>앞</p><iframe id="z">`, // 닫는 태그 없이 끝나도
	} {
		if got := SanitizeRichText(in); strings.Contains(got, "<iframe") {
			t.Errorf("%q → %q — 빈 iframe 이 남았다", in, got)
		}
	}
	yt := `<iframe id="v" src="https://www.youtube.com/embed/abc123"></iframe>`
	if got := SanitizeRichText(yt); !strings.Contains(got, `src="https://www.youtube.com/embed/abc123"`) {
		t.Errorf("유튜브가 빠졌다: %q", got)
	}
}

// **본문의 태그는 본문 안에서 닫힌다.** bluemonday 는 짝을 맞추지 않는다 — 닫지 않은
// `<b style="font-size:0px">` 는 브라우저가 본문 뒤의 댓글·버튼·바닥글 글자마다 다시 만들어
// 다른 방문자에게 안 보이게 했고, 닫지 않은 유튜브 iframe 은 그 뒤 화면 전체를 iframe 의 글자로
// 삼켰다 (2026-10-21 3차 점검). 스크립트는 아니지만 남의 화면을 바꾸는 길이다.
func TestRichTextClosesWhatItOpens(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`<p>글</p><b style="font-size:0px">`, `</b>`},
		{`<p>글</p><span style="color:#ffffff">`, `</span>`},
		{`<iframe src="https://www.youtube.com/embed/abc123">`, `</iframe>`},
		{`<ul><li>하나`, `</ul>`},
	} {
		if got := SanitizeRichText(tc.in); !strings.HasSuffix(got, tc.want) {
			t.Errorf("%q → %q — %s 로 닫히지 않았다", tc.in, got, tc.want)
		}
	}
	// 남는 닫는 태그도 본문 밖의 요소를 닫지 못한다.
	if got := SanitizeRichText(`글</div></main><p>밖</p>`); strings.Contains(got, "</div>") || strings.Contains(got, "</main>") {
		t.Errorf("짝 없는 닫는 태그가 남았다: %q", got)
	}
}
