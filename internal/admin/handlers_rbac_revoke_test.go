package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// superuserActor makes a real admin-role user and makes the screens see it as the caller.
func superuserActor(t *testing.T, d *Deps, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	me, err := d.Auth.CreateUser(ctx, "su@example.com", "h", "관리자")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE key='admin'`, me); err != nil {
		t.Fatal(err)
	}
	d.Caller = func(*http.Request) Caller {
		return &fakeCaller{perms: map[string]bool{"role.manage": true, "role.assign": true, "user.update": true}, id: me, superuser: true}
	}
	return me
}

func grantCount(t *testing.T, pool *pgxpool.Pool, role, perm string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM role_permissions rp JOIN roles r ON r.id = rp.role_id
		JOIN permissions p ON p.id = rp.permission_id WHERE r.key = $1 AND p.key = $2`, role, perm).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// **R9 (A-404): 모든 요청이 갖는 역할(익명·회원)에는 방문자 권한만 준다.** 관리 권한은 최고 관리자가 주어도 422 이고
// 저장하지 않으며, 시도는 작업 로그에 남는다(권한 상승 시도와 같은 대우). 방문자 권한과 다른 역할은 그대로 된다.
func TestGrantToEveryoneRolesIsLimitedToVisitorPermissions(t *testing.T) {
	d, pool := fixture(t, nil)
	superuserActor(t, d, pool)
	for _, role := range []string{"anonymous", "member"} {
		for _, perm := range []string{"admin.access", "post.moderate", "post.read_secret"} {
			rec := post(d.RoleGrantPermission, "/admin/roles/permissions", url.Values{"role": {role}, "permission": {perm}})
			if rec.Code != http.StatusUnprocessableEntity || grantCount(t, pool, role, perm) != 0 {
				t.Errorf("%s 에 %s = HTTP %d, 저장 %d행 (422, 0행이어야)", role, perm, rec.Code, grantCount(t, pool, role, perm))
			}
		}
	}
	// 마지막 줄만 보지 않는다 — post.read_secret 이 든 요약은 작업 로그가 자격증명처럼 보여 가린다.
	var refused int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM operation_logs
		WHERE summary LIKE '권한 부여 거부 (admin.access)%' AND summary LIKE '%R9%'`).Scan(&refused); err != nil {
		t.Fatal(err)
	}
	if refused != 2 {
		t.Errorf("admin.access 거부 기록 %d건, want 2 (익명·회원)", refused)
	}
	if rec := post(d.RoleGrantPermission, "/admin/roles/permissions", url.Values{"role": {"member"}, "permission": {"post.read"}}); rec.Code != http.StatusSeeOther {
		t.Errorf("member 에 방문자 권한 = HTTP %d, want 303", rec.Code)
	}
	if rec := post(d.RoleGrantPermission, "/admin/roles/permissions", url.Values{"role": {"operator"}, "permission": {"page.view"}}); rec.Code != http.StatusSeeOther || grantCount(t, pool, "operator", "page.view") != 1 {
		t.Errorf("operator 에 page.view = HTTP %d — 부여 대상 역할은 R9 과 상관없다", rec.Code)
	}
}

// **역할에서 권한 빼기 (A-404).** 고른 부여 한 행만 지우고, 다음 요청부터 효력이 없다. 없는 부여는 404,
// superuser 역할은 403(R5). 익명 역할에 잘못 들어간 관리 권한도 여기서 뺀다. 빼면 작업 로그에 남는다.
func TestRevokePermissionFromARole(t *testing.T) {
	d, pool := fixture(t, nil)
	superuserActor(t, d, pool)
	ctx := context.Background()
	ed, err := d.Auth.CreateUser(ctx, "ed@example.com", "h", "편집자")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE key='editor'`, ed); err != nil {
		t.Fatal(err)
	}
	revoke := func(grant string) int {
		return post(d.RoleRevokePermission, "/admin/roles/permissions/revoke", url.Values{"grant": {grant}}).Code
	}

	if code := revoke("editor|page.view||"); code != http.StatusSeeOther {
		t.Fatalf("editor 의 page.view 빼기 = HTTP %d", code)
	}
	if p, _ := d.Auth.LoadPermissions(ctx, ed); p.Can("page.view") {
		t.Error("뺐는데 다음 요청에도 page.view 가 있다")
	}
	if !strings.Contains(lastLog(t, pool), "권한 회수: page.view") {
		t.Errorf("작업 로그 = %q", lastLog(t, pool))
	}
	if code := revoke("editor|page.view||"); code != http.StatusNotFound {
		t.Errorf("없는 부여 빼기 = HTTP %d, want 404", code)
	}
	if code := revoke("admin|page.view||"); code != http.StatusForbidden {
		t.Errorf("superuser 역할에서 빼기 = HTTP %d, want 403 (R5)", code)
	}
	if code := revoke("엉터리"); code != http.StatusBadRequest {
		t.Errorf("모양이 틀린 값 = HTTP %d, want 400", code)
	}
	// 게시판 칸이 uuid 가 아니면 그런 부여는 없다 — 22P02 로 500 이 되면 안 된다.
	if code := revoke("editor|post.read|not-a-uuid|"); code != http.StatusNotFound {
		t.Errorf("uuid 가 아닌 게시판 = HTTP %d, want 404", code)
	}

	// 익명 역할에 잘못 들어간 관리 권한(R9 이 막기 전에 들어간 것)을 화면에서 뺀다.
	if _, err := pool.Exec(ctx, `INSERT INTO role_permissions (role_id, permission_id)
		SELECT r.id, p.id FROM roles r, permissions p WHERE r.key = 'anonymous' AND p.key = 'admin.access'`); err != nil {
		t.Fatal(err)
	}
	if code := revoke("anonymous|admin.access||"); code != http.StatusSeeOther || grantCount(t, pool, "anonymous", "admin.access") != 0 {
		t.Errorf("익명 역할의 admin.access 빼기 = HTTP %d", code)
	}
}

// **사용자에게서 역할 빼기 (A-405).** 다음 요청부터 그 역할의 권한이 없다. 자기 역할은 R1 로 403, 갖지 않은
// 역할은 404. 빼면 작업 로그에 남는다.
func TestRevokeRoleFromAUser(t *testing.T) {
	d, pool := fixture(t, nil)
	me := superuserActor(t, d, pool)
	ctx := context.Background()
	u, err := d.Auth.CreateUser(ctx, "u@example.com", "h", "사용자")
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"editor", "operator"} {
		if _, err := pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE key=$2`, u, role); err != nil {
			t.Fatal(err)
		}
	}
	revoke := func(user, role string) int {
		return post(d.RoleRevoke, "/admin/users/roles/revoke", url.Values{"user_id": {user}, "role": {role}}).Code
	}
	if code := revoke(u, "editor"); code != http.StatusSeeOther {
		t.Fatalf("editor 빼기 = HTTP %d", code)
	}
	if keys, _ := d.Auth.UserRoleKeys(ctx, u); strings.Join(keys, ",") != "operator" {
		t.Errorf("뺀 뒤 역할 = %v", keys)
	}
	if !strings.Contains(lastLog(t, pool), "역할 회수: editor") {
		t.Errorf("작업 로그 = %q", lastLog(t, pool))
	}
	if code := revoke(u, "editor"); code != http.StatusNotFound {
		t.Errorf("갖지 않은 역할 빼기 = HTTP %d, want 404", code)
	}
	if code := revoke(me, "admin"); code != http.StatusForbidden {
		t.Errorf("자기 역할 빼기 = HTTP %d, want 403 (R1)", code)
	}
	// 사용자 칸이 uuid 가 아니면 그런 사용자는 없다 — 22P02 로 500 이 되면 안 된다.
	if code := revoke("not-a-uuid", "editor"); code != http.StatusNotFound {
		t.Errorf("uuid 가 아닌 사용자 = HTTP %d, want 404", code)
	}
}

// **A-402 는 지금 가진 역할을 보인다** — 「역할 부여」 선택 칸만 있어 무엇을 갖고 있는지 알 수 없었다.
// A-403 은 부여의 범위를 보이고, 모든 요청이 갖는 역할에 든 관리 권한은 「효력 없음」으로 표시한다(R9).
func TestRoleScreensShowWhatIsHeld(t *testing.T) {
	d, pool := fixture(t, nil)
	superuserActor(t, d, pool)
	ctx := context.Background()
	u, err := d.Auth.CreateUser(ctx, "u@example.com", "h", "사용자")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE key='editor'`, u); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO role_permissions (role_id, permission_id)
		SELECT r.id, p.id FROM roles r, permissions p WHERE r.key = 'anonymous' AND p.key IN ('admin.access', 'post.read')`); err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	d.Render = func(w http.ResponseWriter, _ *http.Request, _ string, c int, v any) {
		w.WriteHeader(c)
		data = v.(map[string]any)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/users/"+u, nil)
	req.SetPathValue("id", u)
	d.UserDetail(httptest.NewRecorder(), req)
	if held, _ := data["Held"].([]string); strings.Join(held, ",") != "editor" {
		t.Errorf("A-402 의 가진 역할 = %v", data["Held"])
	}
	_, data = view(t, d, d.RoleList, "/admin/roles")
	grants, _ := data["Grants"].(map[string][]GrantView)
	inert := map[string]bool{}
	for _, g := range grants["anonymous"] {
		inert[g.Permission] = g.Inert
	}
	if !inert["admin.access"] || inert["post.read"] {
		t.Errorf("익명 역할 표시 = %+v — admin.access 만 효력 없음이어야 한다", grants["anonymous"])
	}
}
