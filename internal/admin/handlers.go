package admin

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/commerce"
	"github.com/emirue/ondolith/internal/content"
)

// Caller is what a handler needs to know about the request's actor. The app
// package supplies it; keeping it an interface here means admin does not import
// app and the two can be tested apart.
type Caller interface {
	Can(perm string) bool
	// CanOn answers for a board-scoped permission (D15 2.4). A moderator of one
	// board is not a moderator of the next.
	CanOn(perm string, board auth.BoardID) bool
	UserID() string
	// Email is the audit snapshot: the log keeps it even after the account is
	// deleted (D30 operation_logs).
	Email() string
	IsSuperuser() bool
	NeedsReauth() bool
	// ConfirmReauth checks the password typed into the destructive screen's
	// own form and, on success, re-stamps the window (D15 5.3-1).
	//
	// **이것이 없으면 재인증 안내는 장식이다.** 화면은 비밀번호 칸을 그리는데
	// 읽는 곳이 없으면, 15분이 지난 운영자는 로그아웃했다 들어오기 전에는
	// 환불을 영원히 할 수 없다.
	ConfirmReauth(password string) bool
}

// reauthOK is the one shape every destructive handler uses: 창이 살아 있거나,
// 이 요청의 비밀번호로 되살렸거나.
func reauthOK(c Caller, r *http.Request) bool {
	return !c.NeedsReauth() || c.ConfirmReauth(r.PostFormValue("password"))
}

// Deps are the collaborators the admin screens use.
type Deps struct {
	Content *content.Store
	Auth    *auth.Store
	Caller  func(*http.Request) Caller
	Render  func(w http.ResponseWriter, r *http.Request, name string, code int, data any)
	// Version and Migrations feed A-602. They are values rather than a
	// database read so that the system screen works even when the database is
	// the thing that is unwell.
	Version    string
	Migrations func(context.Context) (applied []string, pending int, err error)
	// ValidateTheme checks a candidate theme directory before A-202 switches to
	// it, returning a warning for a check it could not make. Injected so admin
	// does not import theme.
	ValidateTheme func(name string) (warn string, err error)
	// OnThemeChange is called after A-202 records a new active theme, so the
	// renderer can pick it up without a restart (FR-303). Injected because admin
	// does not import theme.
	OnThemeChange func(name string)
	// Attachments is A-309's store. Injected because the upload directory is
	// configuration (NFR-304) and admin must not resolve it.
	// Commerce is the Phase 3 store. nil 이면 커머스 화면이 등록되지 않은
	// 것이고 (FR-710), 그 라우트는 애초에 트리에 없다.
	Commerce    *commerce.Store
	Attachments *content.Attachments
	// OpLog records D15 7절's audit entries. Injected so admin does not decide
	// where the trail lives.
	OpLog *content.OpLog
	// Logger surfaces a failed audit write. An audit trail that silently stops
	// recording is worse than one that is missing — the gap reads as "nothing
	// happened".
	Logger *slog.Logger
	// InstallTheme unpacks an uploaded theme zip (A-203). Injected so admin does
	// not import theme, and so the upload root stays configuration (NFR-304).
	InstallTheme func(name string, r io.ReaderAt, size int64) error
	// SendReset delivers a forced-reset link (A-402). Injected and fire-and-
	// forget: mail delivery must not decide whether an account operation
	// succeeded, and the raw token is never logged or rendered.
	SendReset func(email, token string)
}

// require is the per-handler permission check.
//
// Every screen calls it even though the tree gate already ran: the gate saw
// `/admin/...` and nothing else, so it cannot know that THIS request is a page
// delete rather than a page list (D15 4.2). "The middleware already checked" is
// the sentence that precedes the hole.
func (d *Deps) require(w http.ResponseWriter, r *http.Request, perm string) (Caller, bool) {
	c := d.Caller(r)
	if !c.Can(perm) {
		Forbidden(w)
		return nil, false
	}
	return c, true
}

// ---- A-201 사이트 설정 -------------------------------------------------------

// siteSettingKeys is what A-201 owns. Listing them is what keeps one screen
// from writing another screen's keys: there is no generic settings editor
// (D30 settings).
var siteSettingKeys = []string{
	"site.name", "site.meta_description", "site.og_image", "site.dev_mode", "site.type",
	"auth.email_verification_required",
}

func (d *Deps) SettingsForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "settings.update"); !ok {
		return
	}
	kv, err := d.Content.Settings(r.Context(), siteSettingKeys...)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.Render(w, r, "admin/settings.html", http.StatusOK, map[string]any{"Settings": kv})
}

func (d *Deps) SettingsSave(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "settings.update"); !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	kv := map[string]string{}
	for _, k := range siteSettingKeys {
		if v := r.PostFormValue(k); v != "" || r.PostForm.Has(k) {
			kv[k] = v
		}
	}
	// FR-710: the site type is a closed vocabulary. An unknown value would
	// leave the router assembling a tree nobody described.
	if t, ok := kv["site.type"]; ok && t != "cms" && t != "shop" {
		d.Render(w, r, "admin/settings.html", http.StatusUnprocessableEntity,
			map[string]any{"Error": "사이트 유형은 cms 또는 shop 이어야 합니다."})
		return
	}
	if err := d.Content.PutSettings(r.Context(), kv); err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}

// ---- A-205 / A-206 자격증명 설정 ---------------------------------------------

// secretKeys never travel back to the browser. The value is written once and
// read only by the code that uses it (D19 A-205): re-displaying it turns every
// admin screen view into a credential disclosure, and "it is masked in the UI"
// is not the same as "it was never sent".
var secretKeys = map[string]bool{
	"mail.smtp_password": true,
}

var mailSettingKeys = []string{
	"mail.smtp_host", "mail.smtp_port", "mail.smtp_user", "mail.smtp_password",
	"mail.tls_mode", "mail.from_address", "mail.from_name",
}

func (d *Deps) MailSettingsForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "settings.update"); !ok {
		return
	}
	kv, err := d.Content.Settings(r.Context(), mailSettingKeys...)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	shown := map[string]string{}
	saved := map[string]bool{}
	for k, v := range kv {
		if secretKeys[k] {
			// Only whether it is set, never the value.
			saved[k] = v != ""
			continue
		}
		shown[k] = v
	}
	d.Render(w, r, "admin/mail.html", http.StatusOK,
		map[string]any{"Settings": shown, "SecretSaved": saved})
}

func (d *Deps) MailSettingsSave(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "settings.update"); !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	kv := map[string]string{}
	for _, k := range mailSettingKeys {
		v := r.PostFormValue(k)
		if secretKeys[k] && v == "" {
			// An empty secret means "leave it alone", not "erase it": the form
			// cannot show the current value, so an empty box is the normal
			// state of a screen the operator opened to change something else.
			continue
		}
		kv[k] = v
	}
	if m, ok := kv["mail.tls_mode"]; ok && m != "none" && m != "starttls" && m != "tls" {
		d.Render(w, r, "admin/mail.html", http.StatusUnprocessableEntity,
			map[string]any{"Error": "TLS 모드 값이 올바르지 않습니다."})
		return
	}
	if err := d.Content.PutSettings(r.Context(), kv); err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/settings/mail", http.StatusSeeOther)
}

// ---- A-301 / A-302 / A-303 페이지 --------------------------------------------

func (d *Deps) PageList(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "page.view"); !ok {
		return
	}
	pages, err := d.Content.Pages(r.Context())
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.Render(w, r, "admin/pages.html", http.StatusOK, map[string]any{"Pages": pages})
}

// PageForm is A-302's GET. The id "new" is the empty form rather than a second
// screen: D11 gives A-302 one path, and a separate /new route would be a screen
// with no id in the inventory.
func (d *Deps) PageForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "page.update"); !ok {
		return
	}
	id := r.PathValue("id")
	if id == "new" {
		d.Render(w, r, "admin/page-edit.html", http.StatusOK, nil)
		return
	}
	p, err := d.Content.PageByID(r.Context(), id)
	if errors.Is(err, content.ErrNotFound) {
		http.Error(w, "페이지를 찾을 수 없습니다.", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.Render(w, r, "admin/page-edit.html", http.StatusOK, map[string]any{"Page": p})
}

// Dashboard is A-101. It shows nothing but the shell: FR-702 makes the panel
// optional, and an empty screen beats numbers nobody specified.
func (d *Deps) Dashboard(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "admin.access"); !ok {
		return
	}
	d.Render(w, r, "admin/dashboard.html", http.StatusOK, nil)
}

func (d *Deps) PageSave(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "page.update"); !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	slug := strings.TrimSpace(r.PostFormValue("slug"))
	if err := content.ValidateSlug(slug); err != nil {
		d.Render(w, r, "admin/page-edit.html", http.StatusUnprocessableEntity,
			map[string]any{"Error": err.Error()})
		return
	}
	p := content.Page{
		Slug:     slug,
		Title:    r.PostFormValue("title"),
		Body:     r.PostFormValue("body"),
		Template: r.PostFormValue("template"),
	}
	// Status is absent on purpose: publishing is page.publish, and letting an
	// edit carry a status would hand page.update the other permission's power.
	var err error
	if id := r.PathValue("id"); id != "" && id != "new" {
		err = d.Content.UpdatePage(r.Context(), id, p)
	} else {
		_, err = d.Content.CreatePage(r.Context(), p)
	}
	if errors.Is(err, content.ErrSlugTaken) {
		d.Render(w, r, "admin/page-edit.html", http.StatusConflict,
			map[string]any{"Error": "이미 사용 중인 슬러그입니다."})
		return
	}
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/pages", http.StatusSeeOther)
}

// PagePublish is A-303. It requires page.publish specifically — reaching A-302
// is not enough, because the two permissions exist to be separable.
func (d *Deps) PagePublish(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "page.publish"); !ok {
		return
	}
	to := content.PageStatus(r.PostFormValue("status"))
	if err := d.Content.SetPageStatus(r.Context(), r.PathValue("id"), to); err != nil {
		if errors.Is(err, content.ErrTransitionBase) || errors.Is(err, content.ErrStatusUnknown) {
			http.Error(w, "허용되지 않는 상태 전이입니다.", http.StatusUnprocessableEntity)
			return
		}
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/pages", http.StatusSeeOther)
}

// PageDelete is its own permission too.
func (d *Deps) PageDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "page.delete"); !ok {
		return
	}
	if err := d.Content.DeletePage(r.Context(), r.PathValue("id")); err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/pages", http.StatusSeeOther)
}

// ---- A-402 사용자 -------------------------------------------------------------

// userPageSize bounds A-401. An unbounded list is one seed script away from
// loading every account into memory to draw one screen.
const userPageSize = 50

// UserList is A-401. It needs user.view, which A-402 does not imply — the two
// permissions exist separately in D15 2.2 and nothing here may join them.
func (d *Deps) UserList(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "user.view"); !ok {
		return
	}
	page := 0
	if n, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && n > 0 {
		page = n
	}
	users, err := d.Auth.ListUsers(r.Context(), userPageSize, page*userPageSize)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	prev := page - 1
	if prev < 0 {
		prev = 0
	}
	d.Render(w, r, "admin/users.html", http.StatusOK, map[string]any{
		"Users": users, "Page": page,
		// Pre-computed rather than arithmetic in the template: html/template has
		// no maths, and adding an `add` function to the map is a function the
		// next screen will also reach for something it should not compute.
		"PageNo": page + 1, "PrevPage": prev, "NextPage": page + 1,
	})
}

// UserDetail is the A-402 read. D19 puts it behind user.update, not user.view:
// user.view is the list (A-401), and the two are separate rows in D15 2.2.
func (d *Deps) UserDetail(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "user.update"); !ok {
		return
	}
	u, err := d.Auth.FindUserByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, auth.ErrNoUser) {
		http.Error(w, "사용자를 찾을 수 없습니다.", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	// The hash is not in User, and nothing here puts it back: D19 lists it
	// under "받지 않는 필드" in both directions.
	d.Render(w, r, "admin/user-detail.html", http.StatusOK, map[string]any{"User": u})
}

// guardAccountOp is the shared front door for the destructive A-402 actions —
// deactivate, delete and forced password reset.
//
// All three are the last step of an account takeover, so D19 puts the same
// three refusals on each: re-authentication (D15 5.3-1), R6 (only a superuser
// may operate on a superuser holder), and no self-targeting. Writing them once
// is what keeps a later fourth action from getting two of the three.
func (d *Deps) guardAccountOp(w http.ResponseWriter, r *http.Request, c Caller, target string) bool {
	if !reauthOK(c, r) {
		http.Error(w, "비밀번호를 다시 입력하세요.", http.StatusForbidden)
		return false
	}
	if target == c.UserID() {
		// Locking yourself out, or deleting the actor the log points at.
		http.Error(w, "자기 계정에는 이 작업을 할 수 없습니다.", http.StatusForbidden)
		return false
	}
	holds, err := d.Auth.HoldsSuperuser(r.Context(), target)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return false
	}
	actor := auth.Actor{UserID: c.UserID(), Perms: auth.NewPermissions(c.IsSuperuser(), nil)}
	if err := auth.CanOperateOnAccount(actor, holds); err != nil {
		Forbidden(w)
		return false
	}
	return true
}

// UserDeactivate is the destructive account operation R6 and 5.2 both guard.
func (d *Deps) UserDeactivate(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "user.update")
	if !ok {
		return
	}
	target := r.PathValue("id")
	if !d.guardAccountOp(w, r, c, target) {
		return
	}
	// 5.2: the last superuser cannot be switched off, and two administrators
	// doing it at once must not both succeed. The store serialises.
	if err := d.Auth.SetActive(r.Context(), target, false); err != nil {
		if errors.Is(err, auth.ErrLastSuperuser) {
			http.Error(w, "마지막 관리자는 비활성화할 수 없습니다.", http.StatusConflict)
			return
		}
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

// UserDelete needs user.delete — reaching A-402 with user.update is not enough,
// because D19 gives each action its own row.
func (d *Deps) UserDelete(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "user.delete")
	if !ok {
		return
	}
	target := r.PathValue("id")
	if !d.guardAccountOp(w, r, c, target) {
		return
	}
	err := d.Auth.DeleteUser(r.Context(), target)
	switch {
	case errors.Is(err, auth.ErrLastSuperuser):
		http.Error(w, "마지막 관리자는 삭제할 수 없습니다.", http.StatusConflict)
	case errors.Is(err, auth.ErrUserInUse):
		// FR-212: an order needs its buyer to exist for settlement and disputes.
		// Deactivation is the answer, which is why D15 5.3 makes it the default.
		http.Error(w, "기록이 남아 있어 삭제할 수 없습니다. 비활성화하세요.", http.StatusConflict)
	case errors.Is(err, auth.ErrNoUser):
		http.Error(w, "사용자를 찾을 수 없습니다.", http.StatusNotFound)
	case err != nil:
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
	default:
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	}
}

// UserResetPassword forces a reset. It does NOT set a password: D19 keeps
// "타인의 비밀번호를 설정하지 않는다" and offers only the forced reset, so an
// administrator never learns a credential they could then use as that user.
//
// An inactive account is a legal target — it is part of coming back (D19).
func (d *Deps) UserResetPassword(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "user.reset_password")
	if !ok {
		return
	}
	target := r.PathValue("id")
	if !d.guardAccountOp(w, r, c, target) {
		return
	}
	ctx := r.Context()
	u, err := d.Auth.FindUserByID(ctx, target)
	if errors.Is(err, auth.ErrNoUser) {
		http.Error(w, "사용자를 찾을 수 없습니다.", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	// Sessions end first. Issuing the token first would leave a window where
	// the takeover being undone is still logged in (D15 5.4).
	if err := d.Auth.InvalidateSessions(ctx, target); err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	raw, err := d.Auth.IssueToken(ctx, auth.KindPasswordReset, target)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	if d.SendReset != nil {
		d.SendReset(u.Email, raw)
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

// UserCreate needs user.create, which is again its own permission (D19).
func (d *Deps) UserCreate(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "user.create"); !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	email, err := content.ValidateEmail(r.PostFormValue("email"))
	if err != nil {
		d.Render(w, r, "admin/user-detail.html", http.StatusUnprocessableEntity,
			map[string]any{"Error": err.Error()})
		return
	}
	password := r.PostFormValue("password")
	if err := content.ValidatePassword(password); err != nil {
		d.Render(w, r, "admin/user-detail.html", http.StatusUnprocessableEntity,
			map[string]any{"Error": err.Error()})
		return
	}
	name := strings.TrimSpace(r.PostFormValue("display_name"))
	if n := len([]rune(name)); n < 1 || n > 50 {
		d.Render(w, r, "admin/user-detail.html", http.StatusUnprocessableEntity,
			map[string]any{"Error": "표시 이름은 1~50자입니다."})
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	// No role is assigned here. Handing user.create the ability to pick a role
	// would make it a way to mint an administrator (A-405 owns that).
	if _, err := d.Auth.CreateUser(r.Context(), email, hash, name); err != nil {
		if errors.Is(err, auth.ErrEmailTaken) {
			d.Render(w, r, "admin/user-detail.html", http.StatusConflict,
				map[string]any{"Error": "이미 사용 중인 이메일입니다."})
			return
		}
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

// ---- A-602 시스템 정보 --------------------------------------------------------

// System reports version and migration state. It does NOT report the DSN: that
// string carries the database password, and an admin screen is exactly where
// someone would paste a screenshot from (C5).
func (d *Deps) System(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "settings.view"); !ok {
		return
	}
	applied, pending, err := d.Migrations(r.Context())
	data := map[string]any{
		"Version":    d.Version,
		"Applied":    applied,
		"Pending":    pending,
		"DBReadable": err == nil,
	}
	d.Render(w, r, "admin/system.html", http.StatusOK, data)
}

// log writes one audit entry (D15 7절).
//
// A failure is logged and swallowed rather than failing the operation: the
// change already happened, and refusing to report success would tell the
// operator to do it again. The Logger is what makes the gap visible.
func (d *Deps) log(r *http.Request, c Caller, action, targetType, targetID, summary string) {
	if d.OpLog == nil {
		return
	}
	err := d.OpLog.Record(r.Context(), content.Entry{
		ActorID: c.UserID(), ActorEmail: c.Email(),
		Action: action, TargetType: targetType, TargetID: targetID,
		Summary: summary, IP: r.RemoteAddr,
	})
	if err != nil && d.Logger != nil {
		d.Logger.Error("작업 로그 기록 실패", "action", action, "target", targetID, "err", err)
	}
}

// ---- A-601 작업 로그 -----------------------------------------------------------

// oplogPageSize bounds the log screen. The table only grows (D15 7절 keeps it
// forever), so an unbounded read is a query that gets slower every day.
const oplogPageSize = 100

// OpLogList is A-601. It is read-only by construction: OpLog has no Update and
// no Delete, and the database refuses both anyway (D30).
func (d *Deps) OpLogList(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "log.view"); !ok {
		return
	}
	if d.OpLog == nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	page := 0
	if n, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && n > 0 {
		page = n
	}
	ctx := r.Context()
	entries, err := d.OpLog.Recent(ctx, oplogPageSize, page*oplogPageSize)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	total, err := d.OpLog.Count(ctx)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	prev := page - 1
	if prev < 0 {
		prev = 0
	}
	d.Render(w, r, "admin/oplog.html", http.StatusOK, map[string]any{
		"Entries": entries, "Total": total,
		"PageNo": page + 1, "PrevPage": prev, "NextPage": page + 1,
		"HasPrev": page > 0, "HasNext": int64((page+1)*oplogPageSize) < total,
	})
}
