package admin

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/content"
	"github.com/emirue/ondolith/internal/theme"
)

// ---- A-204 메뉴 관리 ---------------------------------------------------------

func (d *Deps) MenuList(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "menu.manage"); !ok {
		return
	}
	items, err := d.Content.MenuItems(r.Context())
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	d.Render(w, r, "admin/menus.html", http.StatusOK, map[string]any{"Items": items})
}

// menuError redraws A-204 with the list. 목록 없이 그리면 「메뉴 항목이 없습니다」가 뜨고
// 부모 선택칸도 비어, 고쳐 다시 보낼 수가 없다.
func (d *Deps) menuError(w http.ResponseWriter, r *http.Request, msg string) {
	items, err := d.Content.MenuItems(r.Context())
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	d.Render(w, r, "admin/menus.html", http.StatusUnprocessableEntity, map[string]any{"Items": items, "Error": msg})
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
		d.refuse(w, r, http.StatusBadRequest, "잘못된 요청입니다.")
		return
	}
	ctx := r.Context()
	item := content.MenuItem{
		Title:    r.PostFormValue("title"),
		URL:      r.PostFormValue("url"),
		ParentID: r.PostFormValue("parent_id"),
	}
	if item.Title == "" || content.TooLong(item.Title, content.MaxMenuTitleLen) {
		d.menuError(w, r, "메뉴 제목은 1~50자입니다.")
		return
	}
	var ok bool
	if item.Sort, ok = menuSort(r); !ok {
		d.menuError(w, r, "순서는 정수로 입력하세요.")
		return
	}

	// No cycle pre-check here, deliberately: a new leaf cannot close a loop,
	// and a parent that does not exist is refused by the foreign key. The check
	// lives in MenuUpdate, which is the operation that can build one.
	if _, err := d.Content.CreateMenuItem(ctx, item); err != nil {
		d.menuError(w, r, "저장하지 못했습니다.")
		return
	}
	http.Redirect(w, r, "/admin/menus", http.StatusSeeOther)
}

// menuSort reads A-204's 순서. 비면 0 이다. 이 칸이 없던 때 sort_order 는 모두 0 이었고
// 메뉴는 id(UUID) 순 — 추가한 순서와 무관하게 — 로 나왔다.
func menuSort(r *http.Request) (int, bool) {
	v := strings.TrimSpace(r.PostFormValue("sort_order"))
	if v == "" {
		return 0, true
	}
	// sort_order 는 int32 다 — 그 밖의 값은 저장 때 뒤집혀(2147483648 → 음수) 맨 앞으로 간다.
	n, err := strconv.ParseInt(v, 10, 32)
	return int(n), err == nil
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
		d.refuse(w, r, http.StatusBadRequest, "잘못된 요청입니다.")
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
	if item.Title == "" || content.TooLong(item.Title, content.MaxMenuTitleLen) {
		d.menuError(w, r, "메뉴 제목은 1~50자입니다.")
		return
	}
	var ok bool
	if item.Sort, ok = menuSort(r); !ok {
		d.menuError(w, r, "순서는 정수로 입력하세요.")
		return
	}

	existing, err := d.Content.MenuItems(ctx)
	if err != nil {
		d.serverError(w, r, err)
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
		d.refuse(w, r, http.StatusNotFound, "항목을 찾을 수 없습니다.")
		return
	}
	if _, err := content.BuildMenu(probe, nil); err != nil {
		d.menuError(w, r, d.refusal(r, menuMessage(err), err))
		return
	}

	if err := d.Content.UpdateMenuItem(ctx, id, item); err != nil {
		d.menuError(w, r, "저장하지 못했습니다.")
		return
	}
	http.Redirect(w, r, "/admin/menus", http.StatusSeeOther)
}

func (d *Deps) MenuDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "menu.manage"); !ok {
		return
	}
	// 없는 메뉴를 지우려 한 것은 404 다 — `d.fail` 이 그 판정을 갖고 있다.
	if err := d.Content.DeleteMenuItem(r.Context(), r.PathValue("id")); d.fail(w, r, err) {
		return
	}
	http.Redirect(w, r, "/admin/menus", http.StatusSeeOther)
}

// ---- A-403 / A-404 / A-405 역할·권한 ------------------------------------------

func (d *Deps) RoleList(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "role.view")
	if !ok {
		return
	}
	d.renderRoles(w, r, c, http.StatusOK, "")
}

// renderRoles draws A-403, whose forms post to A-404·A-405. 재인증이 모자라면
// 그 두 화면이 이것을 403 으로 다시 그린다 (D15 5.3-1, D19 C7).
func (d *Deps) renderRoles(w http.ResponseWriter, r *http.Request, c Caller, code int, msg string) {
	ctx := r.Context()
	roles, err := d.Auth.Roles(ctx)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	// 고르는 목록이다 — 역할 키·권한 키·게시판 id 를 직접 치게 하던 칸들이다.
	// 무엇을 줄 수 있는지는 여전히 서버가 판정한다 (R1~R5).
	perms, err := d.Auth.PermissionKeys(ctx)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	boards, err := d.Content.Boards(ctx)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	d.Render(w, r, "admin/roles.html", code, map[string]any{
		"Roles": roles, "Permissions": perms, "Boards": boards,
		"NeedsReauth": c.NeedsReauth(), "Error": msg})
}

// RoleGrantPermission is A-404. R4 and R5 are enforced HERE, on the server —
// hiding the superuser row in the UI is not the check (D15 5.1).
func (d *Deps) RoleGrantPermission(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "role.manage")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		d.refuse(w, r, http.StatusBadRequest, "잘못된 요청입니다.")
		return
	}
	// 권한 상승의 최종 단계다 — 재인증 (D15 5.3-1).
	if !reauthOK(c, r) {
		d.renderRoles(w, r, c, http.StatusForbidden, "비밀번호를 다시 입력하세요.")
		return
	}
	ctx := r.Context()
	// The role is named in the body (D11 A-404 is /admin/roles/permissions).
	// 경로에 `{id}` 를 두었을 때는 guardID 가 uuid 를 요구해 이 핸들러에 도달할
	// 수 없었다 — 역할 키는 `^[a-z][a-z0-9_]*$` 라 uuid 일 수 없다 (00002).
	roleKey := r.PostFormValue("role")
	permKey := r.PostFormValue("permission")
	board := auth.BoardID(r.PostFormValue("board_id"))

	role, err := d.Auth.RoleByKey(ctx, roleKey)
	if errors.Is(err, auth.ErrNoRole) {
		d.refuse(w, r, http.StatusNotFound, "역할을 찾을 수 없습니다.")
		return
	}
	if err != nil {
		d.serverError(w, r, err)
		return
	}

	// The caller's own permissions decide, so they are loaded rather than
	// inferred from the request: R4 compares the grant against what the caller
	// actually holds.
	actorPerms, err := d.Auth.LoadPermissions(ctx, c.UserID())
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	actor := auth.Actor{UserID: c.UserID(), Perms: actorPerms}
	if err := auth.CanAddPermissionToRole(actor, role, permKey); err != nil {
		// R4·R5 위반은 권한 상승 시도다 — 남긴다 (D19 A-404).
		d.log(r, c, "role.manage", "role", role.Key, "권한 부여 거부 ("+permKey+") — "+err.Error())
		d.forbid(w, r)
		return
	}

	// D15 2.4: only is_scoped permissions may carry a board. The database
	// cannot express this, so the handler and the seed share this one function.
	scoped, err := d.Auth.PermissionIsScoped(ctx, permKey)
	if err != nil {
		d.refuse(w, r, http.StatusNotFound, "권한을 찾을 수 없습니다.")
		return
	}
	if err := auth.ValidateGrantScope(scoped, board); err != nil {
		d.refuse(w, r, http.StatusUnprocessableEntity, "이 권한은 게시판 단위로 부여할 수 없습니다.")
		return
	}

	if err := d.Auth.GrantPermission(ctx, role.Key, permKey, board); err != nil {
		d.serverError(w, r, err)
		return
	}
	d.log(r, c, "role.manage", "role", role.Key, "권한 부여: "+permKey)
	http.Redirect(w, r, "/admin/roles", http.StatusSeeOther)
}

// RoleAssign is A-405. R1, R2 and R3 all apply.
func (d *Deps) RoleAssign(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "role.assign")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		d.refuse(w, r, http.StatusBadRequest, "잘못된 요청입니다.")
		return
	}
	// 최고 위험 화면이다 — 재인증 (D15 5.3-1).
	if !reauthOK(c, r) {
		d.renderRoles(w, r, c, http.StatusForbidden, "비밀번호를 다시 입력하세요.")
		return
	}
	ctx := r.Context()
	// D11 A-405 is /admin/users/roles: the target is named in the body. A-403's
	// form has no user selected in advance, and a path placeholder ("-") never
	// passes guardID.
	targetUser := r.PostFormValue("user_id")
	roleKey := r.PostFormValue("role")
	// A-403 의 폼은 사용자를 **이메일**로 고른다 — UUID 는 화면 어디에도 보이지 않는다.
	// 사용자 상세(A-402)의 폼은 그 사용자의 id 를 싣는다.
	if targetUser == "" {
		u, _, err := d.Auth.FindActiveUserByEmail(ctx, content.NormalizeEmail(r.PostFormValue("email")))
		if errors.Is(err, auth.ErrNoUser) {
			d.refuse(w, r, http.StatusNotFound, "그 이메일을 쓰는 활성 회원이 없습니다.")
			return
		}
		if err != nil {
			d.serverError(w, r, err)
			return
		}
		targetUser = u.ID
	}

	role, err := d.Auth.RoleByKey(ctx, roleKey)
	if errors.Is(err, auth.ErrNoRole) {
		d.refuse(w, r, http.StatusNotFound, "역할을 찾을 수 없습니다.")
		return
	}
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	actorPerms, err := d.Auth.LoadPermissions(ctx, c.UserID())
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	actor := auth.Actor{UserID: c.UserID(), Perms: actorPerms}
	if err := auth.CanAssignRole(actor, targetUser, role); err != nil {
		// R1·R2·R3 위반은 남긴다 (D19 A-405).
		d.log(r, c, "role.assign", "user", targetUser, "역할 부여 거부 ("+role.Key+") — "+err.Error())
		d.forbid(w, r)
		return
	}
	if err := d.Auth.AssignRole(ctx, targetUser, role.Key); err != nil {
		d.serverError(w, r, err)
		return
	}
	d.log(r, c, "role.assign", "user", targetUser, "역할 부여: "+role.Key)
	http.Redirect(w, r, "/admin/users/"+targetUser, http.StatusSeeOther)
}

// ---- A-202 테마 -------------------------------------------------------------

// themeChanged notifies the renderer, if anyone is listening.
func (d *Deps) themeChanged(name string) {
	if d.OnThemeChange != nil {
		d.OnThemeChange(name)
	}
}

// ThemeList is A-202's read: the active theme and what is installed.
//
// 설치된 테마를 나열한다 — 디렉터리 이름을 직접 쳐야 했고, 업로드 화면(A-203)으로 가는
// 길도 없어 런타임 테마 교체를 운영자가 찾을 수 없었다.
func (d *Deps) ThemeList(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "theme.view"); !ok {
		return
	}
	kv, err := d.Content.Settings(r.Context(), "theme.active")
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	d.renderThemes(w, r, http.StatusOK, map[string]any{"Active": kv["theme.active"]})
}

// renderThemes draws A-202 with the installed list. 활성화가 거절되거나 경고가 날 때도
// 같은 표다 — 목록 없이 그리면 「내장 테마 — 사용 중」이 거짓으로 찍혔다.
func (d *Deps) renderThemes(w http.ResponseWriter, r *http.Request, code int, data map[string]any) {
	if d.InstalledThemes != nil {
		installed, err := d.InstalledThemes()
		if err != nil {
			d.serverError(w, r, err)
			return
		}
		data["Installed"] = installed
	}
	d.Render(w, r, "admin/themes.html", code, data)
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
		d.refuse(w, r, http.StatusBadRequest, "잘못된 요청입니다.")
		return
	}
	name := r.PostFormValue("theme")
	if name == "" {
		// Empty means the built-in theme, which always renders.
		if err := d.Content.PutSettings(r.Context(), map[string]string{"theme.active": ""}); err != nil {
			d.serverError(w, r, err)
			return
		}
		d.themeChanged("")
		http.Redirect(w, r, "/admin/themes", http.StatusSeeOther)
		return
	}
	if d.ValidateTheme == nil {
		d.refuse(w, r, http.StatusInternalServerError, "테마를 확인할 수 없습니다.")
		return
	}
	warn, err := d.ValidateTheme(name)
	if err != nil {
		kv, kerr := d.Content.Settings(r.Context(), "theme.active")
		if kerr != nil {
			d.serverError(w, r, kerr)
			return
		}
		d.renderThemes(w, r, http.StatusUnprocessableEntity, map[string]any{"Active": kv["theme.active"],
			"Error": d.themeRefusal(r, "테마를 활성화하지 못했습니다.", err)})
		return
	}
	if err := d.Content.PutSettings(r.Context(), map[string]string{"theme.active": name}); err != nil {
		d.serverError(w, r, err)
		return
	}
	// Only after the write: telling the renderer about a theme the database
	// does not hold would survive until the next restart and then vanish.
	d.themeChanged(name)
	if warn != "" {
		// D17: a dev build skips the version floor. The operator is told,
		// because the alternative is a theme that misbehaves for no visible
		// reason on the machine where it was least expected to.
		d.renderThemes(w, r, http.StatusOK, map[string]any{"Warning": warn, "Active": name})
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

	// The body is capped before anything reads it. Without this the multipart
	// parser buffers whatever arrives, which is a denial of service that never
	// reaches the zip limits at all. 재인증보다 먼저다 — 재인증이 폼의 비밀번호를
	// 읽는 순간 본문 전체가 상한 없이 파싱된다.
	r.Body = http.MaxBytesReader(w, r.Body, maxThemeUploadBytes)
	if err := r.ParseMultipartForm(maxThemeMemoryBytes); err != nil {
		d.Render(w, r, "admin/theme-upload.html", http.StatusRequestEntityTooLarge,
			map[string]any{"Error": "파일이 너무 크거나 형식이 올바르지 않습니다."})
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()

	// D15 5.3-1: uploading executable-ish content is the last step of a takeover.
	if !reauthOK(c, r) {
		d.refuse(w, r, http.StatusForbidden, "비밀번호를 다시 입력하세요.")
		return
	}
	if d.InstallTheme == nil {
		d.refuse(w, r, http.StatusInternalServerError, "테마를 설치할 수 없습니다.")
		return
	}

	name := strings.TrimSpace(r.PostFormValue("name"))
	// **활성 테마는 덮어쓰지 않는다** (OPEN-42 결정). 덮어쓰는 동안 사이트가
	// 그 디렉터리를 그리고 있고, 새 zip 이 옛 테마의 파셜을 갖고 있지 않으면
	// 그 순간의 방문자는 오류 더미를 본다. 비활성 테마는 아무도 안 보므로
	// 덮어쓴다 — 거부하면 이름을 바꿔 올리게 되고 목록에 쓰레기가 쌓인다.
	kv, err := d.Content.Settings(r.Context(), "theme.active")
	if err != nil {
		d.serverError(w, r, err)
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
		d.serverError(w, r, err)
		return
	}

	if err := d.InstallTheme(name, ra, size, true); err != nil {
		// 상위 경로·심볼릭 링크 항목은 공격 시도 신호다 — 작업 로그에 남긴다 (D19 A-203).
		// 재인증한 관리자만 닿는 경로라 밖에서 이 기록을 불릴 수 없다.
		if errors.Is(err, theme.ErrZipEntry) {
			d.log(r, c, "theme.upload", "theme", name, "테마 업로드 거부 — 압축 파일에 허용되지 않는 항목")
		}
		d.Render(w, r, "admin/theme-upload.html", http.StatusUnprocessableEntity,
			map[string]any{"Error": d.themeRefusal(r, "테마를 설치하지 못했습니다.", err)})
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
