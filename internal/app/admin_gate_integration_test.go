package app

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// 회원이 관리자 주소를 열면 403 이되 사이트 레이아웃 안에서 말한다 — 「권한이 없습니다.」
// 24바이트 흰 화면이었다.
func TestMemberAtAdminGetsALaidOutRefusal(t *testing.T) {
	srv, pool := liveSite(t)
	if _, err := pool.Exec(context.Background(), `INSERT INTO settings (key, value) VALUES ('auth.email_verification_required', '')
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`); err != nil {
		t.Fatal(err)
	}
	memberSession(t, srv, pool, "nosy@example.com")
	c := client()
	login(t, srv.URL, "nosy@example.com", c)
	resp, err := c.Get(srv.URL + "/admin/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	code, page := resp.StatusCode, string(b)
	if code != http.StatusForbidden || !strings.Contains(page, "<html") || !strings.Contains(page, "권한") {
		t.Errorf("회원의 /admin/ = HTTP %d, 레이아웃 %v", code, strings.Contains(page, "<html"))
	}
}
