package app

import (
	"context"
	"net/http"
	"testing"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/content"
)

// **게시판 프리셋이 모든 요청이 갖는 역할에 주는 것은 방문자 권한뿐이다** (R9). 불러올 때 그 밖의 것은
// 버리므로, 프리셋이 언젠가 다른 것을 주게 되면 그 부여는 조용히 꺼진다 — 여기서 먼저 잡는다.
func TestPresetsGiveEveryoneOnlyVisitorPermissions(t *testing.T) {
	seen := 0
	for _, p := range content.Presets() {
		grants, err := content.PresetGrants(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range grants {
			if !auth.EveryoneRole(g.Role) {
				continue
			}
			seen++
			if !auth.IsVisitorPermission(g.Permission) {
				t.Errorf("프리셋 %q 가 %s 에 %s 를 준다 — 방문자 권한이 아니라 불러올 때 버려진다", p, g.Role, g.Permission)
			}
		}
	}
	if seen == 0 {
		t.Fatal("프리셋에서 익명·회원 부여를 하나도 못 찾았다 — 검사가 헛돌았다")
	}
}

// **모든 요청이 갖는 역할에 관리 권한이 들어가 있어도, 역할 없는 회원에게 관리자 트리가 열리지 않는다** (R9).
// 그 부여는 불러올 때 버려진다 — 부여 화면이 막기 전에 들어간 것까지. 실제 앱에 로그인해 확인한다.
func TestEveryoneRoleAdminGrantDoesNotOpenTheAdminTree(t *testing.T) {
	srv, pool := liveSite(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO role_permissions (role_id, permission_id)
		SELECT r.id, p.id FROM roles r, permissions p
		WHERE r.key IN ('anonymous', 'member') AND p.key IN ('admin.access', 'post.moderate', 'comment.moderate')`); err != nil {
		t.Fatal(err)
	}
	mkUser(t, pool, "plain@example.com")
	c := client()
	login(t, srv.URL, "plain@example.com", c)
	for _, path := range []string{"/admin/", "/admin/posts", "/admin/comments"} {
		resp, err := c.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("역할 없는 회원의 %s = HTTP %d, want 403", path, resp.StatusCode)
		}
	}
	// 로그아웃 방문자는 지금처럼 로그인으로 간다.
	resp, err := client().Get(srv.URL + "/admin/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") == "" {
		t.Errorf("방문자의 /admin/ = HTTP %d (%s), want 로그인으로 303", resp.StatusCode, resp.Header.Get("Location"))
	}
	// 관리자 역할이 있는 사람은 그대로 들어간다 — 버리는 것은 모두가 갖는 역할의 부여뿐이다.
	ed := mkUser(t, pool, "editor@example.com")
	if err := auth.NewStore(pool).AssignRole(ctx, ed, "editor"); err != nil {
		t.Fatal(err)
	}
	ce := client()
	login(t, srv.URL, "editor@example.com", ce)
	resp, err = ce.Get(srv.URL + "/admin/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("editor 의 /admin/ = HTTP %d, want 200", resp.StatusCode)
	}
}
