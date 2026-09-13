package app

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"net/http/httptest"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FR-214: 설정이 켜져 있으면 인증 전 계정은 글을 쓸 수 없다 (D19 P-205: 400).
// 앞선 판은 가입 직후 자동 로그인만 막았고, 로그인해 버리면 글·댓글·주문이 전부
// 열려 있었다 — 남의 주소로 가입한 계정이 그 이름으로 활동할 수 있었다.
func TestUnverifiedAccountCannotWriteWhenRequired(t *testing.T) {
	srv, pool := liveSite(t)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO settings (key, value) VALUES ('auth.email_verification_required', '1')
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		`INSERT INTO boards (slug, name) VALUES ('free', '자유')`,
		`INSERT INTO role_permissions (role_id, permission_id)
		 SELECT r.id, p.id FROM roles r, permissions p
		 WHERE r.key = 'member' AND p.key IN ('post.write', 'post.read') ON CONFLICT DO NOTHING`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	post := memberSession(t, srv, pool, "new@example.com")

	form := url.Values{"title": {"첫 글"}, "body": {"본문"}}
	resp := post("/board/free/write", form)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("미인증 계정의 글쓰기 → HTTP %d, want 400: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "이메일 인증") {
		t.Errorf("이유가 없다: %s", body)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM posts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("거부됐는데 글이 %d 건 저장됐다", n)
	}

	// 인증하면 같은 요청이 통과한다 — 검사가 「전부 거부」가 아니라는 증거다.
	if _, err := pool.Exec(ctx,
		`UPDATE users SET email_verified_at = now() WHERE email = 'new@example.com'`); err != nil {
		t.Fatal(err)
	}
	resp = post("/board/free/write", form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("인증 뒤 글쓰기 → HTTP %d, want 303", resp.StatusCode)
	}
}

// memberSession signs up a plain member (no role beyond member) and returns a
// POST helper carrying the session and the Origin header.
func memberSession(t *testing.T, srv *httptest.Server, pool *pgxpool.Pool, email string) func(string, url.Values) *http.Response {
	t.Helper()
	ctx := context.Background()
	store := auth.NewStore(pool)
	hash, err := auth.HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateUser(ctx, email, hash, "회원"); err != nil {
		t.Fatal(err)
	}
	c := client()
	post := func(path string, form url.Values) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", srv.URL)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		return resp
	}
	resp := post("/login", url.Values{"email": {email}, "password": {"correct horse battery"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("로그인 HTTP %d", resp.StatusCode)
	}
	return post
}
