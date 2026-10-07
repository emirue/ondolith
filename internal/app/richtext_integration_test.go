package app

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/content"
)

// **에디터로 쓴 글은 정화된 HTML 로 저장되고, 서식대로 그려지고, 평문으로 검색된다** (FR-513).
//
// 본문 HTML 은 브라우저가 보낸 값이다. 스크립트·이벤트 속성은 저장 전에 사라져야
// 하고, 검색(search_vector)은 태그가 아니라 글자를 봐야 한다.
func TestEditorPostIsSanitizedRenderedAndSearchable(t *testing.T) {
	srv, pool := liveSite(t)
	mkBoard(t, pool, "free", content.PresetPublic)
	mkUser(t, pool, "m@example.com")
	c := client()
	login(t, srv.URL, "m@example.com", c)

	resp := httpPost(t, c, srv.URL+"/board/free/write", url.Values{
		"title": {"거실 시공"}, "body": {"에디터가 없을 때의 평문"},
		"body_html": {`<p><strong>원목</strong> 마루 시공 <img src="/media/x" onerror="alert(1)"></p><script>alert(2)</script>`},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("글쓰기 HTTP %d", resp.StatusCode)
	}

	var body, bodyHTML string
	if err := pool.QueryRow(context.Background(),
		`SELECT body, body_html FROM posts WHERE title = '거실 시공'`).Scan(&body, &bodyHTML); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bodyHTML, "onerror") || strings.Contains(bodyHTML, "<script") {
		t.Errorf("정화되지 않은 HTML 이 저장됐다: %s", bodyHTML)
	}
	if !strings.Contains(bodyHTML, "<strong>원목</strong>") {
		t.Errorf("서식이 사라졌다: %s", bodyHTML)
	}
	if body != "원목 마루 시공" {
		t.Errorf("평문 = %q, want %q — 검색·요약은 HTML 의 글자를 쓴다", body, "원목 마루 시공")
	}

	loc := resp.Header.Get("Location")
	_, view := mustGet(t, c, srv.URL+loc)
	if !strings.Contains(view, "<strong>원목</strong>") || strings.Contains(view, "alert(2)") {
		t.Errorf("글 화면이 정화된 HTML 을 그리지 않는다:\n%s", view)
	}
	if _, found := mustGet(t, c, srv.URL+"/search?q="+url.QueryEscape("원목")); !strings.Contains(found, "거실 시공") {
		t.Error("평문으로 검색되지 않는다")
	}
}

// 에디터 이전의 글(평문)은 지금처럼 그린다 — 꺾쇠는 글자이고 줄바꿈은 살아 있다.
func TestPlainPostStillRendersAsText(t *testing.T) {
	srv, pool := liveSite(t)
	mkBoard(t, pool, "free", content.PresetPublic)
	mkUser(t, pool, "m@example.com")
	c := client()
	login(t, srv.URL, "m@example.com", c)

	resp := httpPost(t, c, srv.URL+"/board/free/write", url.Values{
		"title": {"평문"}, "body": {"<b>꺾쇠</b>\n둘째 줄"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("글쓰기 HTTP %d", resp.StatusCode)
	}
	_, view := mustGet(t, c, srv.URL+resp.Header.Get("Location"))
	if strings.Contains(view, "<b>꺾쇠</b>") || !strings.Contains(view, "&lt;b&gt;꺾쇠") {
		t.Errorf("평문 글의 꺾쇠가 태그로 그려졌다:\n%s", view)
	}
	if !strings.Contains(view, "<br>") {
		t.Errorf("평문 글의 줄바꿈이 사라졌다:\n%s", view)
	}
	var bodyHTML string
	if err := pool.QueryRow(context.Background(),
		`SELECT body_html FROM posts WHERE title = '평문'`).Scan(&bodyHTML); err != nil || bodyHTML != "" {
		t.Errorf("평문 글의 body_html = %q (%v), want 비어 있음", bodyHTML, err)
	}
}
