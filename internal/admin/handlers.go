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
	// replace 는 같은 이름의 테마를 덮어쓸지다. 활성 테마면 A-203 이 409 로
	// 거부하므로 여기까지 오지 않는다 (OPEN-42 결정).
	InstallTheme func(name string, r io.ReaderAt, size int64, replace bool) error
	// Gateway 는 A-508 이 PG 에 실제 상태를 물을 때 쓴다. nil 이면 대사가
	// 우리 기록만 보여준다 — 조회 없이 「일치」로 그리지 않는다.
	Gateway func() commerce.Gateway
	// BaseURL 은 A-206 이 **표시만** 하는 콜백 주소를 만드는 데 쓴다.
	// 요청에서 만든다 — 설정으로 두면 관리자가 그 값을 바꿔 인가 코드를
	// 다른 곳으로 돌릴 수 있고, 그것이 D19 A-206 이 콜백 URL 을 받지 않기로
	// 한 이유다.
	BaseURL func(*http.Request) string
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

// fail writes the response a store error deserves and reports whether the
// handler must stop. A nil error writes nothing and returns false, so the
// call reads as the `if err != nil` it replaces:
//
//	order, err := d.Commerce.OrderByNo(ctx, no)
//	if d.fail(w, r, err) {
//		return
//	}
//
// **"없다"는 404 다.** 없는 id 를 물어본 것은 서버 잘못이 아니고, 500 으로
// 답하면 정상적인 오탈자가 로그에서 장애와 구별되지 않는다.
//
// 그 밖의 모든 것은 500 에 **한 문장**이다. 스토어가 만든 문구는 브라우저에
// 닿지 않는다 — 제약 이름·컬럼명·SQL 조각이 그대로 새는 경로가 여기다
// (D14 2절). 원인은 로그가 받는다.
//
// 도메인 오류(ErrOutOfStock 같은 것)는 화면마다 문구와 코드가 다르므로 여기서
// 다루지 않는다. 호출자가 먼저 걸러내고, 남은 것만 넘긴다.
// isCreate reports whether this request is the "make a new one" form.
//
// **만들기 화면은 자기 주소를 갖는다** (`/admin/pages/new`), 그래서 `{id}` 는
// 비어 있다. 옛 주소(`/admin/pages/{id}` 에 `new`)로 오는 요청도 같은 뜻으로
// 받는다 — 판정이 흩어져 있으면 다섯째 화면이 그것을 잊는다.
func isCreate(id string) bool { return id == "" || id == "new" }

func (d *Deps) fail(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, commerce.ErrNotFound),
		errors.Is(err, content.ErrNotFound),
		errors.Is(err, auth.ErrNoUser):
		http.NotFound(w, r)
	default:
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
	}
	return true
}

// ---- A-201 사이트 설정 -------------------------------------------------------

// siteSettingKeys is what A-201 owns. Listing them is what keeps one screen
// from writing another screen's keys: there is no generic settings editor
// (D30 settings).
var siteSettingKeys = []string{
	"site.name", "site.meta_description", "site.og_image", "site.dev_mode", "site.type",
	"auth.email_verification_required",
	// 관리자 콘솔 배색. **부팅 때 읽던 값인데 그것을 저장하는 화면이 없었다** —
	// 다섯 방향이 스타일시트에 있는데 고를 방법이 없었다.
	"admin.theme",
}

// adminThemes 는 admin.css 가 아는 배색이다 (`data-admin-theme="1a"` … `1e`).
//
// **빈 값이 곧 기본(1b)이다.** 여기에 "1b" 를 채워 넣으면 「고르지 않았다」와
// 「1b 를 골랐다」가 같은 모양이 되고, admin.css 의 `:root:not([data-admin-theme])`
// 다크 블록이 영원히 매치되지 않는다 — 그렇게 스무 줄짜리 다크 콘솔이 한 번
// 죽은 적이 있다 (app.defaultAdminTheme 주석).
var adminThemes = map[string]bool{"": true, "1a": true, "1b": true, "1c": true, "1d": true, "1e": true}

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
		d.renderSettings(w, r, "admin/settings.html", siteSettingKeys,
			http.StatusUnprocessableEntity, "사이트 유형은 cms 또는 shop 이어야 합니다.")
		return
	}
	// 배색도 닫힌 어휘다. 모르는 값을 저장하면 `data-admin-theme` 에 그대로
	// 찍히고, admin.css 의 어느 블록과도 매치되지 않아 콘솔이 기본으로 보인다 —
	// 저장은 됐는데 아무 일도 안 일어나는 상태다.
	if t, ok := kv["admin.theme"]; ok && !adminThemes[t] {
		d.renderSettings(w, r, "admin/settings.html", siteSettingKeys,
			http.StatusUnprocessableEntity, "관리자 배색은 1a~1e 중 하나여야 합니다.")
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
	// PG 시크릿 키. 이것이 새면 상점의 모든 승인·취소를 남이 부를 수 있다.
	"pg.secret_key": true,
}

var mailSettingKeys = []string{
	"mail.smtp_host", "mail.smtp_port", "mail.smtp_user", "mail.smtp_password",
	"mail.tls_mode", "mail.from_address", "mail.from_name",
}

// renderSettings draws a settings screen — **정상 경로와 오류 경로가 같은
// 함수를 쓴다.**
//
// 앞 판은 갈라져 있었다: 정상 경로는 `Settings` 와 `SecretSaved` 를 넘기고
// 오류 경로는 `{"Error": …}` 만 넘겼는데, 화면은 `{{index .Settings "…"}}` 로
// 값을 그리므로 템플릿이 nil 맵을 색인하다 터졌다 — **검증에 걸릴 때마다
// 422 대신 500** 이 나갔다. 오류 경로는 정상 경로보다 훨씬 덜 지나가므로
// 이런 것이 오래 남는다. 한 함수로 두면 갈라질 자리가 없다.
//
// 비밀값은 **설정됐는지만** 나간다. 화면이 그 값을 보여 줄 수 없으므로 빈 칸이
// 정상 상태이고, 빈 칸을 저장으로 받으면 지워진다 (MailSettingsSave).
func (d *Deps) renderSettings(w http.ResponseWriter, r *http.Request,
	screen string, keys []string, code int, msg string,
) {
	kv, err := d.Content.Settings(r.Context(), keys...)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	shown := map[string]string{}
	saved := map[string]bool{}
	for k, v := range kv {
		if secretKeys[k] {
			saved[k] = v != ""
			continue
		}
		shown[k] = v
	}
	d.Render(w, r, screen, code,
		map[string]any{"Settings": shown, "SecretSaved": saved, "Error": msg})
}

func (d *Deps) MailSettingsForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "settings.update"); !ok {
		return
	}
	d.renderSettings(w, r, "admin/mail.html", mailSettingKeys, http.StatusOK, "")
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
		d.renderSettings(w, r, "admin/mail.html", mailSettingKeys,
			http.StatusUnprocessableEntity, "TLS 모드 값이 올바르지 않습니다.")
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
	if isCreate(id) {
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
// dashboardItems 는 위젯 하나가 보여 줄 줄 수다. 대시보드는 훑어보는 화면이지
// 목록 화면이 아니다 — 길어지면 아래의 진짜 목록 화면과 하는 일이 겹친다.
const dashboardItems = 5

func (d *Deps) Dashboard(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "admin.access")
	if !ok {
		return
	}
	data := map[string]any{}

	// **각 위젯은 그 데이터에 대한 권한이 있을 때만 조회한다** (D13 A-101).
	//
	// 「그릴 때 숨긴다」가 아니라 **묻지 않는다**. 데이터를 가져다 놓고
	// 템플릿에서 가리면, 그 템플릿을 고치는 날 편집자에게 매출이 보인다 —
	// 화면은 권한 검사의 자리가 아니다 (D15 4.4).
	if c.Can("post.view") {
		if boards, err := d.Content.Boards(r.Context()); err == nil {
			ids := make([]string, 0, len(boards))
			for _, b := range boards {
				ids = append(ids, b.ID)
			}
			// **비밀글은 넣지 않는다.** 관리자 화면이라도 목록 요약에 남의
			// 비밀글 제목이 뜨면 그 글은 비밀이 아니다 — 여는 것은 게시판
			// 화면에서 권한을 다시 보고 한다. secretIn 을 비워 넘긴다.
			if posts, err := d.Content.RecentPosts(r.Context(), ids, nil, "", dashboardItems); err == nil {
				data["Posts"] = posts
			}
		}
	}
	// 주문 위젯은 커머스가 켜져 있고 order.view 가 있을 때만이다. Commerce 가
	// nil 인 것은 조립 시점에 커머스를 끈 사이트다 (FR-710).
	if d.Commerce != nil && c.Can("order.view") {
		if orders, err := d.Commerce.AdminOrders(r.Context(), "", 1); err == nil {
			if len(orders) > dashboardItems {
				orders = orders[:dashboardItems]
			}
			data["Orders"] = orders
		}
	}
	// **shop 모드에서 표시 의무 항목이 비어 있으면 알린다** (FR-711).
	//
	// 저장을 막지 않기로 했으므로 (설치 직후는 항상 비어 있다) 알리는 자리가
	// 없으면 그 결정이 "아무도 모르는 채 빠져 있다" 가 된다. 대시보드는
	// 관리자가 반드시 지나는 화면이다.
	kv, err := d.Content.Settings(r.Context(), append(
		[]string{"site.type"}, commerce.BusinessKeys...)...)
	if err == nil && kv["site.type"] == "shop" {
		if missing := commerce.MissingBusinessKeys(kv); len(missing) > 0 {
			data["Warning"] = "사업자 정보가 비어 있습니다: " +
				strings.Join(missing, ", ") + " — 전자상거래법 표시 의무 항목입니다."
		}
	}
	d.Render(w, r, "admin/dashboard.html", http.StatusOK, data)
}

func (d *Deps) PageSave(w http.ResponseWriter, r *http.Request) {
	// **만들기와 고치기는 다른 권한이다** (D15 2.2: page.create / page.update).
	//
	// 둘 다 page.update 로 받고 있었다. 그래서 `page.create` 는 역할 편집기에
	// 나타나고 부여할 수 있는데 아무 데서도 판정되지 않는 죽은 권한이었고,
	// 반대로 「고칠 수만 있고 만들 수는 없는」 역할을 만들 방법이 없었다.
	// 부팅 자체 점검이 매 부팅 「어떤 라우트도 쓰지 않는 권한」으로 말하고
	// 있었지만, 게시판 스코프 권한 6건의 거짓 경고에 묻혀 있었다.
	perm := "page.update"
	if isCreate(r.PathValue("id")) {
		perm = "page.create"
	}
	if _, ok := d.require(w, r, perm); !ok {
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
	if id := r.PathValue("id"); !isCreate(id) {
		err = d.Content.UpdatePage(r.Context(), id, p)
	} else {
		_, err = d.Content.CreatePage(r.Context(), p)
	}
	if errors.Is(err, content.ErrSlugTaken) {
		d.Render(w, r, "admin/page-edit.html", http.StatusConflict,
			map[string]any{"Error": "이미 사용 중인 슬러그입니다."})
		return
	}
	// **없는 페이지를 고치려 한 것은 404 다.** `d.fail` 이 그 판정을 갖고 있는데
	// 여기서는 쓰지 않아, 지운 페이지의 편집 폼을 다시 제출하면 500 이 났다.
	if d.fail(w, r, err) {
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
		d.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/pages", http.StatusSeeOther)
}

// PageDelete is its own permission too.
func (d *Deps) PageDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "page.delete"); !ok {
		return
	}
	if err := d.Content.DeletePage(r.Context(), r.PathValue("id")); d.fail(w, r, err) {
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
	// **한 행 더 읽어 다음 쪽이 있는지 본다.** 앞 판은 「다음」을 조건 없이
	// 그려서, 사용자가 한 명뿐인 사이트도 무한히 다음 쪽을 제시했다 — 링크를
	// 따라가는 검사가 35쪽까지 걸어가 요청 제한에 걸렸다. 전체 개수를 세는
	// COUNT 를 따로 치지 않는 이유는 그 값이 화면에 필요하지 않기 때문이다.
	users, err := d.Auth.ListUsers(r.Context(), userPageSize+1, page*userPageSize)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	// **자르는 조건은 길이로 판단한다.** `if hasNext` 로 자르면 그 판정이
	// 틀리는 날 `users[:userPageSize]` 가 범위를 벗어나 **패닉**이 된다 —
	// 잘못된 페이지 표시가 500 으로 커지는 것이고, 변이를 심어 보고 알았다.
	// 길이로 자르면 판정이 무엇이든 이 줄은 안전하다.
	hasNext := len(users) > userPageSize
	if len(users) > userPageSize {
		users = users[:userPageSize]
	}
	prev := page - 1
	if prev < 0 {
		prev = 0
	}
	// **목록에 보이기로 한 항목만 열이 된다** (A-406 `show_in_list`). 이것이
	// 없는 동안 그 체크박스는 아무 일도 하지 않는 스위치였다.
	fields, err := d.Content.UserFields(r.Context())
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	var columns []content.FieldSchema
	for _, f := range fields {
		if f.ShowInList {
			columns = append(columns, f)
		}
	}
	d.Render(w, r, "admin/users.html", http.StatusOK, map[string]any{
		"Users": users, "Page": page, "HasNext": hasNext, "Columns": columns,
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
		// `Total` 이 아니라 `Count` 다 — 이 화면의 숫자는 금액이 아니라 건수이고,
		// 「…Total」 이라는 이름은 금액 표시 검사가 찾는 이름이다. 이름 하나로
		// 금액과 건수가 섞이면 그 검사는 예외를 달기 시작하고, 예외가 붙은
		// 검사는 다음 화면에서 진짜 금액을 놓친다.
		"Entries": entries, "Count": total,
		"PageNo": page + 1, "PrevPage": prev, "NextPage": page + 1,
		"HasPrev": page > 0, "HasNext": int64((page+1)*oplogPageSize) < total,
	})
}

// ---- A-406 회원 프로필 항목 (FR-215) ---------------------------------------
//
// **게시판 커스텀 필드(A-306)와 같은 기계를 쓴다** (DEC-3.9). 정의는 행이고
// 값은 JSONB 이므로 **개수 제한이 없다** — 여분 칸을 열 개 만들어 두는 방식은
// 열한 번째에서 ALTER TABLE 을 요구한다.

// UserFieldList is A-406 GET.
func (d *Deps) UserFieldList(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "user.update"); !ok {
		return
	}
	fields, err := d.Content.UserFields(r.Context())
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.Render(w, r, "admin/user-fields.html", http.StatusOK, map[string]any{
		"Fields": fields, "Types": content.FieldTypes(),
	})
}

// UserFieldSave is A-406 POST — 저장과 삭제가 한 화면이다.
func (d *Deps) UserFieldSave(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "user.update")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	if key := r.PostFormValue("delete"); key != "" {
		if err := d.Content.DeleteUserField(ctx, key); err != nil {
			http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
			return
		}
		// 값은 남는다 (D14 3절 규칙 4) — 운영자가 곧 궁금해할 것이므로 적어 둔다.
		d.log(r, c, "user.update", "user_field", key,
			"회원 항목 '"+key+"' 정의 삭제 (회원이 적은 값은 보존)")
		http.Redirect(w, r, "/admin/user-fields", http.StatusSeeOther)
		return
	}

	f := content.FieldSchema{
		Key:        strings.TrimSpace(r.PostFormValue("key")),
		Label:      strings.TrimSpace(r.PostFormValue("label")),
		Type:       content.FieldType(r.PostFormValue("field_type")),
		Required:   r.PostFormValue("is_required") != "",
		ShowInList: r.PostFormValue("show_in_list") != "",
		Options:    splitOptions(r.PostFormValue("options")),
	}
	if n, err := strconv.Atoi(r.PostFormValue("sort_order")); err == nil {
		f.Sort = n
	}
	// 화면에서도 먼저 거른다 — 저장소가 마지막 방어선이고, 여기서 걸러야
	// 운영자가 무엇이 왜 거부됐는지 그 화면에서 본다.
	if err := content.ValidateUserFieldKey(f.Key); err != nil {
		fields, _ := d.Content.UserFields(ctx)
		d.Render(w, r, "admin/user-fields.html", http.StatusUnprocessableEntity,
			map[string]any{"Fields": fields, "Types": content.FieldTypes(),
				"Error": err.Error()})
		return
	}
	if err := d.Content.SaveUserField(ctx, f); err != nil {
		fields, _ := d.Content.UserFields(ctx)
		d.Render(w, r, "admin/user-fields.html", http.StatusUnprocessableEntity,
			map[string]any{"Fields": fields, "Types": content.FieldTypes(),
				"Error": "항목을 저장하지 못했습니다: " + err.Error()})
		return
	}
	d.log(r, c, "user.update", "user_field", f.Key, "회원 항목 '"+f.Key+"' 저장")
	http.Redirect(w, r, "/admin/user-fields", http.StatusSeeOther)
}
