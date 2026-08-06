package admin

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

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
	// The role comes from the path (D11 A-404 is /admin/roles/{id}/permissions).
	// "-" is the form's placeholder when the screen has no role selected yet, in
	// which case the body names it.
	roleKey := r.PathValue("id")
	if roleKey == "" || roleKey == "-" {
		roleKey = r.PostFormValue("role")
	}
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
	// D11 A-405 is /admin/users/{id}/roles.
	targetUser := r.PathValue("id")
	if targetUser == "" || targetUser == "-" {
		targetUser = r.PostFormValue("user_id")
	}
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

// ---- A-203 테마 업로드 ---------------------------------------------------------

// ThemeUploadForm is A-203's GET.
func (d *Deps) ThemeUploadForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "theme.upload"); !ok {
		return
	}
	d.Render(w, r, "admin/theme-upload.html", http.StatusOK, nil)
}

// ThemeUpload is A-203's POST.
//
// This is the highest-risk screen in the product: an upload here is arbitrary
// file write and a written file is executed as a template (D60). Two gates sit
// in front of the unpacker — re-authentication (D15 5.3-1) and a request body
// cap — and the unpacker itself applies the five defences.
func (d *Deps) ThemeUpload(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "theme.upload")
	if !ok {
		return
	}
	// D15 5.3-1: uploading executable-ish content is the last step of a takeover.
	if !reauthOK(c, r) {
		http.Error(w, "비밀번호를 다시 입력하세요.", http.StatusForbidden)
		return
	}
	if d.InstallTheme == nil {
		http.Error(w, "테마를 설치할 수 없습니다.", http.StatusInternalServerError)
		return
	}

	// The body is capped before anything reads it. Without this the multipart
	// parser buffers whatever arrives, which is a denial of service that never
	// reaches the zip limits at all.
	r.Body = http.MaxBytesReader(w, r.Body, maxThemeUploadBytes)
	if err := r.ParseMultipartForm(maxThemeMemoryBytes); err != nil {
		d.Render(w, r, "admin/theme-upload.html", http.StatusRequestEntityTooLarge,
			map[string]any{"Error": "파일이 너무 크거나 형식이 올바르지 않습니다."})
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()

	name := strings.TrimSpace(r.PostFormValue("name"))
	// **활성 테마는 덮어쓰지 않는다** (OPEN-42 결정). 덮어쓰는 동안 사이트가
	// 그 디렉터리를 그리고 있고, 새 zip 이 옛 테마의 파셜을 갖고 있지 않으면
	// 그 순간의 방문자는 오류 더미를 본다. 비활성 테마는 아무도 안 보므로
	// 덮어쓴다 — 거부하면 이름을 바꿔 올리게 되고 목록에 쓰레기가 쌓인다.
	kv, err := d.Content.Settings(r.Context(), "theme.active")
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	if name != "" && name == kv["theme.active"] {
		d.Render(w, r, "admin/theme-upload.html", http.StatusConflict,
			map[string]any{"Error": "활성 테마는 덮어쓸 수 없습니다. 다른 테마로 바꾼 뒤 올리세요."})
		return
	}

	file, header, err := r.FormFile("theme")
	if err != nil {
		d.Render(w, r, "admin/theme-upload.html", http.StatusUnprocessableEntity,
			map[string]any{"Error": "파일을 고르세요."})
		return
	}
	defer file.Close()

	// zip.NewReader needs an io.ReaderAt with a known size. Multipart gives one
	// only when the part was spooled to disk; buffer otherwise.
	ra, size, err := readerAt(file, header.Size)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}

	if err := d.InstallTheme(name, ra, size, true); err != nil {
		d.Render(w, r, "admin/theme-upload.html", http.StatusUnprocessableEntity,
			map[string]any{"Error": "설치하지 못했습니다: " + err.Error()})
		return
	}
	// D15 7절: 테마 변경은 사이트 전체에 영향을 준다.
	d.log(r, c, "theme.upload", "theme", name, "테마 '"+name+"' 업로드")
	http.Redirect(w, r, "/admin/themes", http.StatusSeeOther)
}

const (
	// maxThemeUploadBytes bounds the whole request. It is larger than D60's
	// 20 MiB archive limit because multipart adds framing — the archive limit
	// is what actually decides, this only stops the socket.
	maxThemeUploadBytes = 24 << 20
	// maxThemeMemoryBytes is what ParseMultipartForm keeps in RAM; the rest
	// spools to a temp file. NFR-101's tier has 512MB.
	maxThemeMemoryBytes = 1 << 20
)

// readerAt adapts the uploaded part. A multipart file that spooled to disk is
// already an io.ReaderAt; one held in memory is not, so it is copied into a
// buffer bounded by the same cap the body already enforced.
func readerAt(f multipart.File, size int64) (io.ReaderAt, int64, error) {
	if ra, ok := f.(io.ReaderAt); ok && size > 0 {
		return ra, size, nil
	}
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(f, maxThemeUploadBytes))
	if err != nil {
		return nil, 0, err
	}
	return bytes.NewReader(buf.Bytes()), n, nil
}
