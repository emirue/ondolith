package admin

import (
	"context"
	"errors"
	"github.com/emirue/ondolith/internal/auth"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/commerce"
	"github.com/emirue/ondolith/internal/content"
	"github.com/emirue/ondolith/internal/theme"
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
	} else {
		refusalShown(t, rec.Body.String(), "자기 자신이나 하위 메뉴를 상위 메뉴로 지정할 수 없습니다.")
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
			return "", theme.ErrNoBase
		}
		return "", nil
	}

	if rec := post(d.ThemeActivate, "/admin/themes", url.Values{
		"theme": {"broken"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("base.html 없는 테마가 HTTP %d 로 활성화됐다", rec.Code)
	} else {
		refusalShown(t, rec.Body.String(), "테마를 활성화하지 못했습니다. 테마에 base.html 파일이 없습니다.")
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

// A-404 의 `board_id` 는 저장되는 값이다. 검증만 하고 버리던 판에서는 게시판
// 단위로 준 post.moderate 가 전역으로 들어갔다.
func TestScopedGrantIsStoredScoped(t *testing.T) {
	d, pool := fixture(t, nil)
	ctx := context.Background()
	me, err := d.Auth.CreateUser(ctx, "me@example.com", "h", "나")
	if err != nil {
		t.Fatal(err)
	}
	// R4: the caller must hold what they grant; admin holds post.moderate.
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE key='admin'`, me); err != nil {
		t.Fatal(err)
	}
	var board string
	if err := pool.QueryRow(ctx,
		`INSERT INTO boards (slug, name) VALUES ('x', 'X') RETURNING id`).Scan(&board); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO roles (key, name) VALUES ('mod_x', 'X')`); err != nil {
		t.Fatal(err)
	}
	target, err := d.Auth.CreateUser(ctx, "mod@example.com", "h", "모드")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE key='mod_x'`, target); err != nil {
		t.Fatal(err)
	}
	d.Caller = func(*http.Request) Caller {
		return &fakeCaller{perms: map[string]bool{"role.manage": true}, id: me}
	}

	rec := post(d.RoleGrantPermission, "/admin/roles/permissions", url.Values{
		"role": {"mod_x"}, "permission": {"post.moderate"}, "board_id": {board},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body.String())
	}
	p, err := d.Auth.LoadPermissions(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if !p.CanOn("post.moderate", auth.BoardID(board)) {
		t.Error("그 게시판에 post.moderate 가 없다")
	}
	if p.Can("post.moderate") {
		t.Error("게시판 하나에 준 권한이 전역으로 저장됐다")
	}
}

// **역할·권한 변경(A-404·A-405)은 재인증을 거친다** (D15 5.3-1 — 권한 상승의 최종
// 단계). 미충족이면 403 과 A-403 폼 재표시이고 아무것도 바뀌지 않는다. 성공과
// R 규칙 위반은 작업 로그에 남는다 (D19 A-404·A-405 오류 표).
func TestRoleChangesAreReauthedAndLogged(t *testing.T) {
	d, pool := fixture(t, nil)
	d.OpLog = content.NewStore(pool).OpLog()
	ctx := context.Background()
	me, _ := d.Auth.CreateUser(ctx, "me@example.com", "h", "나")
	target, _ := d.Auth.CreateUser(ctx, "t@example.com", "h", "대상")
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE key='admin'`, me); err != nil {
		t.Fatal(err)
	}
	d.Caller = func(*http.Request) Caller {
		return &fakeCaller{perms: map[string]bool{"role.manage": true, "role.assign": true}, id: me,
			email: "me@example.com", superuser: true, reauth: true, password: "correct horse battery"}
	}
	grant := url.Values{"role": {"operator"}, "permission": {"page.view"}}
	assign := url.Values{"user_id": {target}, "role": {"operator"}}

	for name, call := range map[string]func() *httptest.ResponseRecorder{
		"A-404": func() *httptest.ResponseRecorder {
			return post(d.RoleGrantPermission, "/admin/roles/permissions", grant)
		},
		"A-405": func() *httptest.ResponseRecorder { return post(d.RoleAssign, "/admin/users/roles", assign) },
	} {
		rec := call()
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "admin/roles.html") {
			t.Errorf("%s 재인증 없이 = HTTP %d (%q), want 403 과 폼 재표시", name, rec.Code, rec.Body.String())
		}
	}
	if perms, _ := d.Auth.LoadPermissions(ctx, target); perms.Can("admin.access") {
		t.Fatal("재인증 없이 역할이 부여됐다")
	}

	grant.Set("password", "correct horse battery")
	assign.Set("password", "correct horse battery")
	if rec := post(d.RoleGrantPermission, "/admin/roles/permissions", grant); rec.Code != http.StatusSeeOther {
		t.Errorf("A-404 재인증 뒤 = HTTP %d (%q)", rec.Code, rec.Body.String())
	}
	if rec := post(d.RoleAssign, "/admin/users/roles", assign); rec.Code != http.StatusSeeOther {
		t.Errorf("A-405 재인증 뒤 = HTTP %d (%q)", rec.Code, rec.Body.String())
	}
	self := url.Values{"user_id": {me}, "role": {"operator"}, "password": {"correct horse battery"}}
	if rec := post(d.RoleAssign, "/admin/users/roles", self); rec.Code != http.StatusForbidden {
		t.Errorf("자기 계정 = HTTP %d, want 403 (R1)", rec.Code)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operation_logs
		WHERE action IN ('role.manage', 'role.assign')`).Scan(&n); err != nil || n != 3 {
		t.Errorf("작업 로그 %d건 (%v), want 3 — 부여 둘과 R1 위반", n, err)
	}
}

// A-202 은 설치된 테마를 화면에 넘긴다.
func TestThemeListPassesInstalledThemes(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"theme.view": true}})
	d.InstalledThemes = func() ([]string, error) { return []string{"warm"}, nil }
	var got any
	d.Render = func(w http.ResponseWriter, _ *http.Request, _ string, code int, data any) {
		got = data.(map[string]any)["Installed"]
		w.WriteHeader(code)
	}
	d.ThemeList(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/admin/themes", nil))
	if names, _ := got.([]string); len(names) != 1 || names[0] != "warm" {
		t.Errorf("설치된 테마 = %v, want [warm]", got)
	}
}

// **메뉴 순서는 화면에서 정한다** (D19 A-204). 폼에 순서 칸이 없어 sort_order 가 모두 0
// 이었고, 메뉴는 추가한 순서와 무관하게 id(UUID) 순으로 나왔다. 수정할 때마다 순서가
// 0 으로 돌아가는 것도 같은 원인이었다.
func TestMenuOrderIsSetFromTheScreen(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"menu.manage": true}})
	ctx := context.Background()
	for _, m := range []struct{ title, sort string }{{"회사 소개", "2"}, {"상품", "1"}} {
		if rec := post(d.MenuCreate, "/admin/menus", url.Values{
			"title": {m.title}, "url": {"/"}, "sort_order": {m.sort}}); rec.Code != http.StatusSeeOther {
			t.Fatalf("%s 추가 = HTTP %d", m.title, rec.Code)
		}
	}
	titles := func() string {
		items, err := d.Content.MenuItems(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, it := range items {
			out = append(out, it.Title)
		}
		return strings.Join(out, ",")
	}
	if got := titles(); got != "상품,회사 소개" {
		t.Errorf("순서 = %s, want 상품,회사 소개", got)
	}
	for _, bad := range []string{"첫째", "2147483648"} { // 둘째는 int32 를 넘는다 — 뒤집혀 맨 앞으로 갔다
		if rec := post(d.MenuCreate, "/admin/menus", url.Values{
			"title": {"x"}, "url": {"/"}, "sort_order": {bad}}); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("순서 %q = HTTP %d, want 422", bad, rec.Code)
		}
	}

	items, _ := d.Content.MenuItems(ctx)
	about := items[1].ID // 회사 소개
	req := httptest.NewRequest(http.MethodPost, "/admin/menus/"+about, strings.NewReader(url.Values{
		"title": {"회사 소개"}, "url": {"/"}, "sort_order": {"0"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", about)
	rec := httptest.NewRecorder()
	d.MenuUpdate(rec, req)
	if rec.Code != http.StatusSeeOther || titles() != "회사 소개,상품" {
		t.Errorf("순서 수정 = HTTP %d, 순서 %s", rec.Code, titles())
	}
}

// 메뉴·테마 화면은 거절할 때도 목록을 그린다 — 목록 없이 그리면 메뉴는 「항목이 없습니다」·
// 빈 부모 선택칸, 테마는 「내장 테마 — 사용 중」이 거짓으로 찍혔다.
func TestRefusalsRedrawTheLists(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"menu.manage": true, "theme.activate": true}})
	ctx := context.Background()
	if _, err := d.Content.CreateMenuItem(ctx, contentItem("상품", "/shop", "")); err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	d.Render = func(w http.ResponseWriter, _ *http.Request, _ string, code int, v any) {
		data, _ = v.(map[string]any)
		w.WriteHeader(code)
	}
	post(d.MenuCreate, "/admin/menus", url.Values{"title": {""}, "url": {"/"}})
	if items, _ := data["Items"].([]content.MenuItem); len(items) != 1 {
		t.Errorf("메뉴 거절 화면의 목록 %v, want 1개", data["Items"])
	}
	d.InstalledThemes = func() ([]string, error) { return []string{"warm"}, nil }
	d.ValidateTheme = func(string) (string, error) { return "", errors.New("base.html 없음") }
	post(d.ThemeActivate, "/admin/themes", url.Values{"theme": {"warm"}})
	if names, _ := data["Installed"].([]string); len(names) != 1 {
		t.Errorf("테마 거절 화면의 설치 목록 %v, want [warm]", data["Installed"])
	}
	if _, ok := data["Active"]; !ok {
		t.Error("테마 거절 화면이 지금 테마를 모른다")
	}
}

// 글·댓글·첨부 관리는 고르는 목록을 그리도록 게시판 목록을 넘긴다.
func TestModerationScreensReceiveBoards(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"admin.access": true, "comment.moderate": true, "post.moderate": true}})
	if _, err := d.Content.CreateBoard(context.Background(), content.Board{Slug: "notice", Name: "공지", PerPage: 20}, content.PresetPublic); err != nil {
		t.Fatal(err)
	}
	for name, h := range map[string]http.HandlerFunc{"글": d.PostList, "댓글": d.CommentList, "첨부": d.AttachmentList} {
		var got any
		d.Render = func(w http.ResponseWriter, _ *http.Request, _ string, code int, v any) {
			got = v.(map[string]any)["Boards"]
			w.WriteHeader(code)
		}
		h(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/admin/x", nil))
		if bs, _ := got.([]content.Board); len(bs) != 1 {
			t.Errorf("%s 관리: 게시판 목록 %v, want 1개", name, got)
		}
	}
}

// 「수정」(?edit=키)은 그 필드를 폼에 넘긴다. 없는 키면 빈 폼이다.
func TestFieldEditorsLoadTheFieldToEdit(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"user.update": true}})
	ctx := context.Background()
	if err := d.Content.SaveUserField(ctx, content.FieldSchema{Key: "color", Label: "색상",
		Type: content.FieldSelect, Options: []string{"빨강", "파랑"}}); err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	d.Render = func(w http.ResponseWriter, _ *http.Request, _ string, code int, v any) {
		data, _ = v.(map[string]any)
		w.WriteHeader(code)
	}
	d.UserFieldList(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/admin/user-fields?edit=color", nil))
	if f, _ := data["Edit"].(*content.FieldSchema); f == nil || f.Label != "색상" || data["EditOptions"] != "빨강\n파랑" {
		t.Errorf("수정할 필드 %v / 선택지 %q", data["Edit"], data["EditOptions"])
	}
	d.UserFieldList(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/admin/user-fields?edit=nope", nil))
	if _, ok := data["Edit"]; ok {
		t.Error("없는 키인데 수정 폼이 채워졌다")
	}
}

// **카테고리 이름·순서를 고친다** (D19 A-509). 라우트가 만들기·지우기·상위 바꾸기뿐이라
// 오타 하나도 지우고 다시 만들어야 했다 — 그 카테고리에 묶인 상품의 연결까지 끊긴다.
func TestCategoryNameAndOrderCanBeEdited(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"product.manage": true}})
	ctx := context.Background()
	id, err := d.Commerce.CreateCategory(ctx, commerce.Category{Name: "매뜨", Slug: "mat"})
	if err != nil {
		t.Fatal(err)
	}
	edit := func(form url.Values) int { return postID(d.CategoryUpdate, id, form).Code }
	if code := edit(url.Values{"name": {"매트"}, "sort_order": {"3"}}); code != http.StatusSeeOther {
		t.Fatalf("고치기 = HTTP %d", code)
	}
	cats, err := d.Commerce.Categories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cats) != 1 || cats[0].Name != "매트" || cats[0].SortOrder != 3 || cats[0].Slug != "mat" {
		t.Errorf("= %+v, want 매트 / 순서 3 / 주소 그대로", cats)
	}
	for _, bad := range []url.Values{{"name": {""}, "sort_order": {"0"}}, {"name": {strings.Repeat("가", 101)}},
		{"name": {"매트"}, "sort_order": {"셋"}}} {
		if code := edit(bad); code != http.StatusUnprocessableEntity {
			t.Errorf("%v = HTTP %d, want 422", bad, code)
		}
	}
}

// **역할은 이메일로 줄 수 있다** (D19 A-405). 화면에 어디에도 안 보이는 사용자 UUID 를
// 쳐야 했다. 없는 이메일은 404 이고, 준 뒤에는 그 사용자의 상세로 간다.
func TestRoleAssignTakesAnEmail(t *testing.T) {
	d, pool := fixture(t, nil)
	ctx := context.Background()
	me, _ := d.Auth.CreateUser(ctx, "me@example.com", "h", "나")
	target, _ := d.Auth.CreateUser(ctx, "staff@example.com", "h", "직원")
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE key='admin'`, me); err != nil {
		t.Fatal(err)
	}
	d.Caller = func(*http.Request) Caller {
		return &fakeCaller{perms: map[string]bool{"role.assign": true}, id: me, superuser: true}
	}
	if rec := post(d.RoleAssign, "/admin/users/roles", url.Values{
		"email": {"nobody@example.com"}, "role": {"operator"}}); rec.Code != http.StatusNotFound {
		t.Errorf("없는 이메일 = HTTP %d, want 404", rec.Code)
	}
	// 이메일로 찾은 뒤에도 R1(자기 자신에게 부여 금지)이 그대로다.
	if rec := post(d.RoleAssign, "/admin/users/roles", url.Values{
		"email": {"me@example.com"}, "role": {"operator"}}); rec.Code != http.StatusForbidden {
		t.Errorf("자기 이메일로 부여 = HTTP %d, want 403", rec.Code)
	}
	rec := post(d.RoleAssign, "/admin/users/roles", url.Values{"email": {" Staff@Example.com "}, "role": {"operator"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/users/"+target {
		t.Fatalf("이메일로 부여 = HTTP %d → %q", rec.Code, rec.Header().Get("Location"))
	}
	perms, err := d.Auth.LoadPermissions(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if !perms.Can("admin.access") {
		t.Error("이메일로 준 역할이 붙지 않았다")
	}
}
