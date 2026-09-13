package app

import (
	"context"
	"net/http"
	"net/url"
	"testing"
)

// A-404·A-405 의 폼이 **등록된 라우트에 실제로 닿는지** 본다. 경로에 `{id}` 가
// 있던 판에서는 guardID 가 uuid 를 요구해 A-403 의 폼(`-` 자리표시)이 404 였고,
// 역할 키는 uuid 일 수 없으므로 A-404 는 어떤 입력으로도 닿지 않았다 — 설치자
// 외에는 아무도 관리자가 될 수 없었다. 핸들러 단위 테스트는 레지스트리를
// 지나지 않아 그것을 보지 못했다.
func TestRoleFormsReachTheirHandlers(t *testing.T) {
	srv, pool := liveSite(t)
	ctx := context.Background()
	// admin 이 무엇을 들고 있든 관계없이 「닿는가」만 보므로 필요한 권한을 준다.
	if _, err := pool.Exec(ctx, `
		INSERT INTO role_permissions (role_id, permission_id)
		SELECT r.id, p.id FROM roles r, permissions p
		WHERE r.key = 'admin' AND p.key IN ('role.manage', 'role.assign')
		ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	_, post := adminSession(t, srv, pool)
	var target string
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, display_name) VALUES ('t@example.com','h','t') RETURNING id`).
		Scan(&target); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		path string
		form url.Values
	}{
		{"/admin/roles/permissions", url.Values{"role": {"editor"}, "permission": {"page.view"}}},
		{"/admin/users/roles", url.Values{"user_id": {target}, "role": {"editor"}}},
	} {
		resp := post(tc.path, tc.form)
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			t.Errorf("POST %s → 404: 폼이 라우트에 닿지 않는다", tc.path)
		} else if resp.StatusCode != http.StatusSeeOther {
			t.Errorf("POST %s → HTTP %d, want 303", tc.path, resp.StatusCode)
		}
	}
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM user_roles ur JOIN roles r ON r.id = ur.role_id
		 WHERE ur.user_id = $1 AND r.key = 'editor'`, target).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("역할이 부여되지 않았다 (rows=%d)", n)
	}
}
