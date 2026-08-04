package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/content"
)

// Caller is what a handler needs to know about the request's actor. The app
// package supplies it; keeping it an interface here means admin does not import
// app and the two can be tested apart.
type Caller interface {
	Can(perm string) bool
	UserID() string
	IsSuperuser() bool
	NeedsReauth() bool
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
	d.Render(w, r, "admin/pages.html", http.StatusOK, nil)
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
	if id := r.PostFormValue("id"); id != "" {
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

// UserDeactivate is the destructive account operation R6 and 5.2 both guard.
func (d *Deps) UserDeactivate(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "user.update")
	if !ok {
		return
	}
	// D15 5.3-1: destructive, so the password is re-confirmed. The session
	// being open is not the same as the operator being present.
	if c.NeedsReauth() {
		http.Error(w, "비밀번호를 다시 입력하세요.", http.StatusForbidden)
		return
	}
	target := r.PathValue("id")

	// R6: only a superuser may switch off a superuser holder. Without it R3 is
	// theatre — the role survives while its holder is turned off.
	holds, err := d.Auth.HoldsSuperuser(r.Context(), target)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	actor := auth.Actor{UserID: c.UserID(), Perms: auth.NewPermissions(c.IsSuperuser(), nil)}
	if err := auth.CanOperateOnAccount(actor, holds); err != nil {
		Forbidden(w)
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
