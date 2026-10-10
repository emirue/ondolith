package app

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/content"
)

// **숨은 칸을 채워 온 제출은 저장되지 않는다** (FR-216) — 가입·재설정 요청·글쓰기·댓글.
func TestPublicFormsDropHoneypotSubmissions(t *testing.T) {
	srv, pool := liveSite(t)
	ctx := context.Background()
	mkBoard(t, pool, "free", content.PresetPublic)
	mkUser(t, pool, "m@example.com")
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// 422 만 보면 다른 검증(정의되지 않은 커스텀 필드 등)에 걸린 것과 구별되지 않는다.
	expect := func(what string, resp *http.Response) {
		t.Helper()
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), spamRejected) {
			t.Errorf("%s: 숨은 칸을 채운 제출 = HTTP %d, want 422 「%s」", what, resp.StatusCode, spamRejected)
		}
	}
	hp := "http://spam.example/"

	anon := client()
	expect("가입", httpPost(t, anon, srv.URL+"/signup", url.Values{
		"email": {"bot@example.com"}, "password": {"0123456789ab"}, honeypotField: {hp}}))
	if n := count(`SELECT count(*) FROM users WHERE email = 'bot@example.com'`); n != 0 {
		t.Errorf("가입: 계정이 %d개 생겼다", n)
	}
	expect("재설정 요청", httpPost(t, anon, srv.URL+"/password/reset", url.Values{
		"email": {"m@example.com"}, honeypotField: {hp}}))
	if n := count(`SELECT count(*) FROM password_reset_tokens`); n != 0 {
		t.Errorf("재설정 요청: 토큰이 %d개 생겼다", n)
	}

	c := client()
	login(t, srv.URL, "m@example.com", c)
	// 위젯이 실어 보내는 토큰 칸은 커스텀 필드가 아니다 — 키가 꺼져 있어도 거부하지 않는다.
	resp := httpPost(t, c, srv.URL+"/board/free/write", url.Values{"title": {"진짜 글"}, "body": {"x"},
		honeypotField: {""}, auth.TurnstileField: {"XXXX.DUMMY.TOKEN.XXXX"}})
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("빈 숨은 칸의 글쓰기 = HTTP %d, want 303", resp.StatusCode)
	}
	expect("글쓰기", httpPost(t, c, srv.URL+"/board/free/write", url.Values{
		"title": {"스팸"}, "body": {"x"}, honeypotField: {hp}}))
	if n := count(`SELECT count(*) FROM posts WHERE title = '스팸'`); n != 0 {
		t.Errorf("글쓰기: 글이 %d개 생겼다", n)
	}
	expect("댓글", httpPost(t, c, srv.URL+loc+"/comments", url.Values{"body": {"스팸"}, honeypotField: {hp}}))
	if n := count(`SELECT count(*) FROM comments`); n != 0 {
		t.Errorf("댓글: %d개 생겼다", n)
	}
}

// **Turnstile 키를 넣으면 같은 폼이 위젯을 그리고, 토큰 없는 제출을 거부한다** (FR-216).
// 숨은 칸은 키와 무관하게 네 폼 모두에 있고, 그 이름이 코어가 읽는 이름이어야 한다 —
// 테마 조각과 핸들러가 다른 이름을 보면 숨은 칸은 있으나 마나다. 토큰이 없으면 siteverify 를
// 부르지 않으므로 이 시험은 바깥에 나가지 않는다. 키는 Cloudflare 문서의 시험용 값이다.
func TestTurnstileKeysTurnTheWidgetOn(t *testing.T) {
	srv, pool := liveSite(t)
	ctx := context.Background()
	mkBoard(t, pool, "free", content.PresetPublic)
	mkUser(t, pool, "m@example.com")
	c := client()
	login(t, srv.URL, "m@example.com", c)
	resp := httpPost(t, c, srv.URL+"/board/free/write", url.Values{"title": {"글"}, "body": {"x"}})
	resp.Body.Close()
	forms := []struct {
		path string
		c    *http.Client
	}{{"/signup", client()}, {"/password/reset", client()}, {"/board/free/write", c}, {resp.Header.Get("Location"), c}}
	const widget = `class="cf-turnstile" data-sitekey="1x00000000000000000000AA"`
	for _, f := range forms {
		_, body := mustGet(t, f.c, srv.URL+f.path)
		if !strings.Contains(body, `name="`+honeypotField+`"`) {
			t.Errorf("%s 에 숨은 칸(%s)이 없다", f.path, honeypotField)
		}
		if strings.Contains(body, "cf-turnstile") {
			t.Errorf("%s: 키가 없는데 위젯을 그린다 — 「키가 없으면 꺼져 있고 외부 호출도 없다」", f.path)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO settings (key, value) VALUES
		('turnstile.site_key', '1x00000000000000000000AA'),
		('turnstile.secret_key', '1x0000000000000000000000000000000AA')`); err != nil {
		t.Fatal(err)
	}
	for _, f := range forms {
		if _, body := mustGet(t, f.c, srv.URL+f.path); !strings.Contains(body, widget) ||
			!strings.Contains(body, "https://challenges.cloudflare.com/turnstile/v0/api.js") {
			t.Errorf("%s 에 위젯이 없다", f.path)
		}
	}
	resp = httpPost(t, client(), srv.URL+"/signup", url.Values{
		"email": {"bot@example.com"}, "password": {"0123456789ab"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("토큰 없는 가입 = HTTP %d, want 422", resp.StatusCode)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE email = 'bot@example.com'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("토큰 없는 가입이 계정을 만들었다: %d (%v)", n, err)
	}
}
