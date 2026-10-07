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
		{"구글 지도", `<iframe src="https://www.google.com/maps/embed?pb=!1m18!1m12"></iframe>`, `src="https://www.google.com/maps/embed?pb=!1m18!1m12"`},
	} {
		if got := SanitizeRichText(tc.in); !strings.Contains(got, tc.must) {
			t.Errorf("%s: %q 가 없다 → %s", tc.name, tc.must, got)
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
