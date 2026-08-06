package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/content"
)

// R4 and R5 are enforced on the server. Hiding the superuser row in the UI is
// not a check — a POST reaches the handler regardless of what was drawn.
func TestSuperuserRoleCannotBeEditedFromTheServer(t *testing.T) {
	d, pool := fixture(t, &fakeCaller{perms: map[string]bool{"role.manage": true}, id: "me"})
	ctx := context.Background()

	// The caller is a genuine superuser, so R4 could not refuse this — only R5
	// can, which is the point: the role is not an editable object for anyone.
	me, err := d.Auth.CreateUser(ctx, "me@example.com", "h", "나")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE key='admin'`,
		me); err != nil {
		t.Fatal(err)
	}
	d.Caller = func(*http.Request) Caller {
		return &fakeCaller{perms: map[string]bool{"role.manage": true}, id: me, superuser: true}
	}

	rec := post(d.RoleGrantPermission, "/admin/roles/grant", url.Values{
		"role": {"admin"}, "permission": {"page.view"},
	})
	if rec.Code != http.StatusForbidden {
		t.Errorf("superuser 역할 편집이 HTTP %d 로 통과했다 (R5)", rec.Code)
	}
}

// R4: a caller cannot put a permission they do not hold onto any role.
func TestCannotGrantAPermissionYouDoNotHold(t *testing.T) {
	d, pool := fixture(t, nil)
	ctx := context.Background()
	me, err := d.Auth.CreateUser(ctx, "me@example.com", "h", "나")
	if err != nil {
		t.Fatal(err)
	}
	// editor holds page.* but not settings.update.
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE key='editor'`,
		me); err != nil {
		t.Fatal(err)
	}
	d.Caller = func(*http.Request) Caller {
		return &fakeCaller{perms: map[string]bool{"role.manage": true}, id: me}
	}

	if rec := post(d.RoleGrantPermission, "/admin/roles/grant", url.Values{
		"role": {"editor"}, "permission": {"settings.update"},
	}); rec.Code != http.StatusForbidden {
		t.Errorf("미보유 권한 부여가 HTTP %d 로 통과했다 (R4)", rec.Code)
	}
	// ...while a permission they do hold goes through.
	if rec := post(d.RoleGrantPermission, "/admin/roles/grant", url.Values{
		"role": {"editor"}, "permission": {"page.view"},
	}); rec.Code != http.StatusSeeOther {
		t.Errorf("보유 권한 부여가 거부됐다: HTTP %d (%s)", rec.Code, rec.Body.String())
	}
}

// R1: no exception, not even for a superuser. `role.assign` alone would
// otherwise be a path to any role at all.
func TestCannotAssignARoleToYourself(t *testing.T) {
	d, pool := fixture(t, nil)
	ctx := context.Background()
	me, err := d.Auth.CreateUser(ctx, "me@example.com", "h", "나")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE key='admin'`,
		me); err != nil {
		t.Fatal(err)
	}
	d.Caller = func(*http.Request) Caller {
		return &fakeCaller{perms: map[string]bool{"role.assign": true}, id: me, superuser: true}
	}

	if rec := post(d.RoleAssign, "/admin/users/assign", url.Values{
		"user_id": {me}, "role": {"operator"},
	}); rec.Code != http.StatusForbidden {
		t.Errorf("자기 자신에게 역할을 부여했다: HTTP %d (R1)", rec.Code)
	}
}

// D15 2.4: only is_scoped permissions may carry a board. The database cannot
// express it, so the handler and the seed share one function.
func TestScopedGrantOnlyForScopedPermissions(t *testing.T) {
	d, pool := fixture(t, nil)
	ctx := context.Background()
	me, err := d.Auth.CreateUser(ctx, "me@example.com", "h", "나")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE key='admin'`,
		me); err != nil {
		t.Fatal(err)
	}
	d.Caller = func(*http.Request) Caller {
		return &fakeCaller{perms: map[string]bool{"role.manage": true}, id: me, superuser: true}
	}

	// settings.update is not scoped (no scoped permission ships in Phase 1).
	if rec := post(d.RoleGrantPermission, "/admin/roles/grant", url.Values{
		"role": {"operator"}, "permission": {"settings.update"}, "board_id": {"free"},
	}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("비스코프 권한에 게시판이 붙었다: HTTP %d", rec.Code)
	}
}

// Assigning a role must show up on the caller's very next request (D15 4.3-1).
func TestRoleAssignmentIsImmediate(t *testing.T) {
	d, pool := fixture(t, nil)
	ctx := context.Background()
	me, _ := d.Auth.CreateUser(ctx, "me@example.com", "h", "나")
	target, _ := d.Auth.CreateUser(ctx, "t@example.com", "h", "대상")
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE key='admin'`,
		me); err != nil {
		t.Fatal(err)
	}
	d.Caller = func(*http.Request) Caller {
		return &fakeCaller{perms: map[string]bool{"role.assign": true}, id: me, superuser: true}
	}

	before, err := d.Auth.LoadPermissions(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if before.Can("admin.access") {
		t.Fatal("부여 전인데 권한이 있다")
	}
	if rec := post(d.RoleAssign, "/admin/users/assign", url.Values{
		"user_id": {target}, "role": {"operator"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("부여 실패: HTTP %d (%s)", rec.Code, rec.Body.String())
	}
	after, err := d.Auth.LoadPermissions(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Can("admin.access") {
		t.Error("부여했는데 다음 조회에 반영되지 않았다")
	}
}

// anonymous and member are granted implicitly, so a user_roles row pointing at
// them would be a second, contradictory source of the same fact (D30).
func TestUnassignableRolesAreNotAssigned(t *testing.T) {
	d, pool := fixture(t, nil)
	ctx := context.Background()
	me, _ := d.Auth.CreateUser(ctx, "me@example.com", "h", "나")
	target, _ := d.Auth.CreateUser(ctx, "t@example.com", "h", "대상")
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE key='admin'`,
		me); err != nil {
		t.Fatal(err)
	}
	if err := d.Auth.AssignRole(ctx, target, "member"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM user_roles ur JOIN roles r ON r.id = ur.role_id
		WHERE ur.user_id = $1 AND r.key = 'member'`, target).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("암묵 역할 member 가 %d행 부여됐다", n)
	}
}

// A cycle cannot be caught by a constraint (D30 3절), and writing first would
// leave a menu that cannot render — behind which sits the screen that fixes it.
func TestMenuCycleIsRefusedBeforeWriting(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"menu.manage": true}})
	ctx := context.Background()

	parent, err := d.Content.CreateMenuItem(ctx, contentItem("부모", "/p", ""))
	if err != nil {
		t.Fatal(err)
	}

	// A normal child is accepted.
	if rec := post(d.MenuCreate, "/admin/menus", url.Values{
		"title": {"자식"}, "url": {"/c"}, "parent_id": {parent},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("정상 항목이 거부됐다: HTTP %d (%s)", rec.Code, rec.Body.String())
	}

	// A parent that does not exist is a dangling reference: the tree cannot be
	// assembled, so it must be refused BEFORE the row lands. Writing first
	// leaves a menu that cannot render, and the screen that fixes it sits
	// behind that menu.
	missing := "00000000-0000-0000-0000-000000000000"
	rec := post(d.MenuCreate, "/admin/menus", url.Values{
		"title": {"고아"}, "url": {"/x"}, "parent_id": {missing},
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("존재하지 않는 부모가 HTTP %d 로 통과했다", rec.Code)
	}

	items, err := d.Content.MenuItems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Errorf("거부됐는데 %d행이 남았다 (want 2행) — 쓰고 나서 검사했다", len(items))
	}
}

// Re-parenting is the operation that can build a cycle: pointing an entry at
// one of its own descendants is legal for the row and invisible to the foreign
// key (D30 3절). It shows up only when the tree is assembled.
func TestReParentingUnderOwnDescendantIsRefusedBeforeWriting(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"menu.manage": true}})
	ctx := context.Background()

	// 조부 → 부모 → 자식
	grand, err := d.Content.CreateMenuItem(ctx, contentItem("조부", "/g", ""))
	if err != nil {
		t.Fatal(err)
	}
	parent, err := d.Content.CreateMenuItem(ctx, contentItem("부모", "/p", grand))
	if err != nil {
		t.Fatal(err)
	}
	child, err := d.Content.CreateMenuItem(ctx, contentItem("자식", "/c", parent))
	if err != nil {
		t.Fatal(err)
	}

	update := func(id, newParent string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/admin/menus/"+id,
			strings.NewReader(url.Values{
				"title": {"수정됨"}, "url": {"/u"}, "parent_id": {newParent},
			}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		d.MenuUpdate(rec, req)
		return rec
	}

	// 조부를 자기 손자 밑으로: 세 항목이 서로를 가리키는 고리가 된다.
	if rec := update(grand, child); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("자손 밑으로 옮기는 요청이 HTTP %d 로 통과했다", rec.Code)
	}
	// 자기 자신을 부모로.
	if rec := update(child, child); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("자기 자신을 부모로 지정하는 요청이 HTTP %d 로 통과했다", rec.Code)
	}

	// 거부됐으면 트리는 그대로여야 한다 — 쓰고 나서 검사하면 여기서 드러난다.
	items, err := d.Content.MenuItems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		switch it.ID {
		case grand:
			if it.ParentID != "" {
				t.Errorf("거부됐는데 조부의 부모가 %q 로 바뀌었다", it.ParentID)
			}
		case child:
			if it.ParentID != parent {
				t.Errorf("거부됐는데 자식의 부모가 %q 로 바뀌었다", it.ParentID)
			}
		}
	}
	if _, err := content.BuildMenu(items, nil); err != nil {
		t.Errorf("거부 후에도 트리가 조립되지 않는다: %v", err)
	}

	// ...그리고 정상적인 이동은 된다.
	if rec := update(child, grand); rec.Code != http.StatusSeeOther {
		t.Errorf("정상적인 재부모 지정이 거부됐다: HTTP %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestMenuRejectsEmptyTitle(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"menu.manage": true}})
	if rec := post(d.MenuCreate, "/admin/menus", url.Values{
		"url": {"/x"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("빈 제목이 HTTP %d 로 통과했다", rec.Code)
	}
}

// D17: base.html is checked BEFORE the switch. Activating a theme without it
// leaves every page unrenderable, including the one that would switch back.
func TestThemeActivationRefusesAnInvalidTheme(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"theme.activate": true}})
	d.ValidateTheme = func(name string) (string, error) {
		if name == "broken" {
			return "", errors.New("base.html 이 없습니다")
		}
		return "", nil
	}

	if rec := post(d.ThemeActivate, "/admin/themes", url.Values{
		"theme": {"broken"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("base.html 없는 테마가 HTTP %d 로 활성화됐다", rec.Code)
	}
	// ...and it must not have been recorded either. Refusing the response while
	// storing the value would leave the site pointed at a theme it cannot draw.
	kv0, err := d.Content.Settings(context.Background(), "theme.active")
	if err != nil {
		t.Fatal(err)
	}
	if kv0["theme.active"] == "broken" {
		t.Error("거부했는데 깨진 테마가 저장됐다")
	}
	if rec := post(d.ThemeActivate, "/admin/themes", url.Values{
		"theme": {"good"}}); rec.Code != http.StatusSeeOther {
		t.Errorf("정상 테마가 거부됐다: HTTP %d", rec.Code)
	}
	kv, err := d.Content.Settings(context.Background(), "theme.active")
	if err != nil {
		t.Fatal(err)
	}
	if kv["theme.active"] != "good" {
		t.Errorf("활성 테마 = %q", kv["theme.active"])
	}
}

// The built-in theme always renders, so switching back to it needs no check —
// and must always be possible, because it is the way out of a broken theme.
func TestSwitchingBackToBuiltinAlwaysWorks(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"theme.activate": true}})
	d.ValidateTheme = func(string) (string, error) { return "", errors.New("무조건 거부") }

	if rec := post(d.ThemeActivate, "/admin/themes", url.Values{
		"theme": {""}}); rec.Code != http.StatusSeeOther {
		t.Errorf("내장 테마로 되돌리기가 막혔다: HTTP %d", rec.Code)
	}
}

func TestRbacHandlersCheckTheirOwnPermission(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"admin.access": true}})
	cases := map[string]http.HandlerFunc{
		"메뉴 목록": d.MenuList, "메뉴 추가": d.MenuCreate, "메뉴 수정": d.MenuUpdate, "메뉴 삭제": d.MenuDelete,
		"역할 목록": d.RoleList, "권한 부여": d.RoleGrantPermission,
		"역할 부여": d.RoleAssign, "테마 활성화": d.ThemeActivate,
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			if rec := post(h, "/admin/x", nil); rec.Code != http.StatusForbidden {
				t.Errorf("HTTP %d, want 403", rec.Code)
			}
		})
	}
}

func contentItem(title, url, parent string) content.MenuItem {
	return content.MenuItem{Title: title, URL: url, ParentID: parent}
}
