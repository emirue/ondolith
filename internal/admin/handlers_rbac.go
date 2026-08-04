package admin

import (
	"errors"
	"net/http"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/content"
)

// ---- A-204 메뉴 관리 ---------------------------------------------------------

func (d *Deps) MenuList(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "menu.manage"); !ok {
		return
	}
	items, err := d.Content.MenuItems(r.Context())
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.Render(w, r, "admin/menus.html", http.StatusOK, map[string]any{"Items": items})
}

// MenuCreate adds one entry.
//
// The cycle check runs on the assembled tree, not on the row: a foreign key
// cannot see a cycle (D30 3절), and the row being inserted is legal on its own.
// Building the tree with the new row in it is the only way to find out.
func (d *Deps) MenuCreate(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "menu.manage"); !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	item := content.MenuItem{
		Title:    r.PostFormValue("title"),
		URL:      r.PostFormValue("url"),
		ParentID: r.PostFormValue("parent_id"),
	}
	if item.Title == "" {
		d.Render(w, r, "admin/menus.html", http.StatusUnprocessableEntity,
			map[string]any{"Error": "제목을 입력하세요."})
		return
	}

	// No cycle pre-check here, deliberately: a new leaf cannot close a loop,
	// and a parent that does not exist is refused by the foreign key. The check
	// lives in MenuUpdate, which is the operation that can build one.
	if _, err := d.Content.CreateMenuItem(ctx, item); err != nil {
		d.Render(w, r, "admin/menus.html", http.StatusUnprocessableEntity,
			map[string]any{"Error": "저장하지 못했습니다."})
		return
	}
	http.Redirect(w, r, "/admin/menus", http.StatusSeeOther)
}

// MenuUpdate edits an entry, re-parenting included.
//
// The cycle check lives here and not in MenuCreate: a new leaf cannot close a
// loop, but moving an entry under one of its own descendants can, and no
// constraint sees it (D30 3절). The tree is assembled with the change applied
// BEFORE the row is written — writing first would leave a menu that cannot be
// rendered, and the screen that fixes it sits behind that menu.
func (d *Deps) MenuUpdate(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "menu.manage"); !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	item := content.MenuItem{
		ID:       id,
		Title:    r.PostFormValue("title"),
		URL:      r.PostFormValue("url"),
		ParentID: r.PostFormValue("parent_id"),
	}
	if item.Title == "" {
		d.Render(w, r, "admin/menus.html", http.StatusUnprocessableEntity,
			map[string]any{"Error": "제목을 입력하세요."})
		return
	}

	existing, err := d.Content.MenuItems(ctx)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	found := false
	probe := make([]content.MenuItem, 0, len(existing))
	for _, e := range existing {
		if e.ID == id {
			e.ParentID, e.Title, e.URL = item.ParentID, item.Title, item.URL
			found = true
		}
		probe = append(probe, e)
	}
	if !found {
		http.Error(w, "항목을 찾을 수 없습니다.", http.StatusNotFound)
		return
	}
	if _, err := content.BuildMenu(probe, nil); err != nil {
		d.Render(w, r, "admin/menus.html", http.StatusUnprocessableEntity,
			map[string]any{"Error": "메뉴 구조가 올바르지 않습니다: " + err.Error()})
		return
	}

	if err := d.Content.UpdateMenuItem(ctx, id, item); err != nil {
		d.Render(w, r, "admin/menus.html", http.StatusUnprocessableEntity,
			map[string]any{"Error": "저장하지 못했습니다."})
		return
	}
	http.Redirect(w, r, "/admin/menus", http.StatusSeeOther)
}

func (d *Deps) MenuDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "menu.manage"); !ok {
		return
	}
	if err := d.Content.DeleteMenuItem(r.Context(), r.PathValue("id")); err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/menus", http.StatusSeeOther)
}

// ---- A-403 / A-404 / A-405 역할·권한 ------------------------------------------

func (d *Deps) RoleList(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "role.view"); !ok {
		return
	}
	roles, err := d.Auth.Roles(r.Context())
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.Render(w, r, "admin/roles.html", http.StatusOK, map[string]any{"Roles": roles})
}

// RoleGrantPermission is A-404. R4 and R5 are enforced HERE, on the server —
// hiding the superuser row in the UI is not the check (D15 5.1).
func (d *Deps) RoleGrantPermission(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "role.manage")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	roleKey := r.PostFormValue("role")
	permKey := r.PostFormValue("permission")
	board := auth.BoardID(r.PostFormValue("board_id"))

	role, err := d.Auth.RoleByKey(ctx, roleKey)
	if errors.Is(err, auth.ErrNoRole) {
		http.Error(w, "역할을 찾을 수 없습니다.", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}

	// The caller's own permissions decide, so they are loaded rather than
	// inferred from the request: R4 compares the grant against what the caller
	// actually holds.
	actorPerms, err := d.Auth.LoadPermissions(ctx, c.UserID())
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	actor := auth.Actor{UserID: c.UserID(), Perms: actorPerms}
	if err := auth.CanAddPermissionToRole(actor, role, permKey); err != nil {
		Forbidden(w)
		return
	}

	// D15 2.4: only is_scoped permissions may carry a board. The database
	// cannot express this, so the handler and the seed share this one function.
	scoped, err := d.Auth.PermissionIsScoped(ctx, permKey)
	if err != nil {
		http.Error(w, "권한을 찾을 수 없습니다.", http.StatusNotFound)
		return
	}
	if err := auth.ValidateGrantScope(scoped, board); err != nil {
		http.Error(w, "이 권한은 게시판 단위로 부여할 수 없습니다.", http.StatusUnprocessableEntity)
		return
	}

	if err := d.Auth.GrantPermission(ctx, role.Key, permKey); err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/roles", http.StatusSeeOther)
}

// RoleAssign is A-405. R1, R2 and R3 all apply.
func (d *Deps) RoleAssign(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "role.assign")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	targetUser := r.PostFormValue("user_id")
	roleKey := r.PostFormValue("role")

	role, err := d.Auth.RoleByKey(ctx, roleKey)
	if errors.Is(err, auth.ErrNoRole) {
		http.Error(w, "역할을 찾을 수 없습니다.", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	actorPerms, err := d.Auth.LoadPermissions(ctx, c.UserID())
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	actor := auth.Actor{UserID: c.UserID(), Perms: actorPerms}
	if err := auth.CanAssignRole(actor, targetUser, role); err != nil {
		Forbidden(w)
		return
	}
	if err := d.Auth.AssignRole(ctx, targetUser, role.Key); err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

// ---- A-202 테마 -------------------------------------------------------------

// themeChanged notifies the renderer, if anyone is listening.
func (d *Deps) themeChanged(name string) {
	if d.OnThemeChange != nil {
		d.OnThemeChange(name)
	}
}

// ThemeList is A-202's read. It shows which theme is active; discovering what
// is installed is A-203's job (Phase 2), so this does not walk the disk.
func (d *Deps) ThemeList(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "theme.view"); !ok {
		return
	}
	kv, err := d.Content.Settings(r.Context(), "theme.active")
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.Render(w, r, "admin/themes.html", http.StatusOK,
		map[string]any{"Active": kv["theme.active"]})
}

// ThemeActivate switches the active theme.
//
// base.html is checked BEFORE the switch: activating a theme without it leaves
// every page unrenderable, and the screen that would switch back is one of
// those pages (D17).
func (d *Deps) ThemeActivate(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "theme.activate"); !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	name := r.PostFormValue("theme")
	if name == "" {
		// Empty means the built-in theme, which always renders.
		if err := d.Content.PutSettings(r.Context(), map[string]string{"theme.active": ""}); err != nil {
			http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
			return
		}
		d.themeChanged("")
		http.Redirect(w, r, "/admin/themes", http.StatusSeeOther)
		return
	}
	if d.ValidateTheme == nil {
		http.Error(w, "테마를 확인할 수 없습니다.", http.StatusInternalServerError)
		return
	}
	warn, err := d.ValidateTheme(name)
	if err != nil {
		d.Render(w, r, "admin/themes.html", http.StatusUnprocessableEntity,
			map[string]any{"Error": "이 테마는 활성화할 수 없습니다: " + err.Error()})
		return
	}
	if err := d.Content.PutSettings(r.Context(), map[string]string{"theme.active": name}); err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	// Only after the write: telling the renderer about a theme the database
	// does not hold would survive until the next restart and then vanish.
	d.themeChanged(name)
	if warn != "" {
		// D17: a dev build skips the version floor. The operator is told,
		// because the alternative is a theme that misbehaves for no visible
		// reason on the machine where it was least expected to.
		d.Render(w, r, "admin/themes.html", http.StatusOK,
			map[string]any{"Warning": warn, "Active": name})
		return
	}
	http.Redirect(w, r, "/admin/themes", http.StatusSeeOther)
}
