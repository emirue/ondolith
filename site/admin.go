package site

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/emirue/ondolith/internal/siteext"
)

// Permission is a site permission the role editor (A-403) can grant. Key is "site.이름" (소문자·숫자·_).
type Permission struct {
	Key string
	// Name is shown next to the key in the role editor — 1~60자.
	Name string
}

// AdminPage is one site screen in the admin. 관리자 트리의 문(admin.access)과 Permission 을 코어가 먼저 확인한다.
type AdminPage struct {
	// Menu is the sidebar text under 「사이트」, up to 30 characters. 비우면 메뉴에 없다 — 목록에서 눌러 들어가는
	// 상세 화면이다. 주소에 변수가 있으면 비워야 한다.
	Menu string
	// Path follows /admin/site and may use http.ServeMux wildcards: "/workers/{id}".
	Path string
	// Permission is one of Extension.Permissions' keys.
	Permission string
	// Handler takes both GET and POST. 성공한 POST 는 Done 으로 끝낸다.
	Handler http.HandlerFunc
}

// Actor is the administrator on a site admin page.
type Actor struct {
	ID   string
	Name string
}

// RenderAdmin draws the "content" template of t with data inside the core's admin frame — header,
// sidebar, notices, the double-submit guard. status 는 그대로 나간다: 폼을 다시 그리면 422, 재확인에 실패하면 403.
// 오류 문장은 사이트 템플릿이 그린다 — 코어의 모양은 `<p class="adm-toast adm-toast--error" role="alert">` 다.
func RenderAdmin(w http.ResponseWriter, r *http.Request, status int, title string, t *template.Template, data any) {
	a, ok := siteext.AdminFrom(r.Context())
	if !ok {
		logger(r).Error("site.RenderAdmin: 사이트 관리자 화면 밖에서 불렀다", "path", r.URL.Path)
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "content", data); err != nil {
		logger(r).Error("사이트 관리자 템플릿", "path", r.URL.Path, "err", err)
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	// 사이트 템플릿을 html/template 으로 실행한 결과다 — 값은 그 실행에서 이스케이프됐다. 본문 자리(HTML 글 맥락)에
	// 그대로 넣는다. 사이트 코드는 사이트 저장소가 컴파일해 넣은 것이라 남이 올린 파일이 아니다 (D42 지배 원칙 1).
	a.Render(w, r, status, title, template.HTML(buf.String()))
}

// Done ends an admin POST that worked: the notice shows once on the screen at to, then 303 (D19 C11).
// to 는 관리자 트리 안의 주소다(`/admin/…`). 밖이면 보내지 않고 500 이다 — 폼 값을 그대로 넘긴 열린 리다이렉트를 막는다.
func Done(w http.ResponseWriter, r *http.Request, to, notice string) {
	a, ok := siteext.AdminFrom(r.Context())
	if !ok || !insideAdmin(to) {
		logger(r).Error("site.Done: 관리자 트리 밖으로 보내려 했다", "to", to)
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	a.Notify(to, notice)
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// insideAdmin reports whether to stays in the admin tree. `.`·`..` 마디는 받지 않는다 — http.Redirect 와 브라우저가
// 정리하면 `/admin/../logout` 은 `/logout` 이다. 마디는 디코딩한 경로로 본다(`%2e%2e` 도 브라우저에게는 `..` 다).
func insideAdmin(to string) bool {
	u, err := url.Parse(to)
	if err != nil || u.Scheme != "" || u.Host != "" || !strings.HasPrefix(to, "/admin/") {
		return false
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// Reauth reports whether the administrator has re-entered the password within 15 minutes, or did so in
// this form's `password` field — 맞으면 창을 연다 (D15 5.3-1). 돈·자격증명을 다루는 POST 가 처음에 부르고, false 면
// 403 으로 폼을 다시 그린다. 폼에는 ReauthField 를 넣는다 — 칸이 없으면 15분 뒤에는 그 동작을 할 수 없다.
func Reauth(r *http.Request) bool {
	a, ok := siteext.AdminFrom(r.Context())
	if !ok {
		return false
	}
	return !a.NeedsReauth() || a.ConfirmReauth(r.PostFormValue("password"))
}

// ReauthField is the core's password field for a guarded form, or "" while the window is open.
// 템플릿 데이터에 실어 `{{.ReauthField}}` 로 폼 안에 그린다.
func ReauthField(r *http.Request) template.HTML {
	a, ok := siteext.AdminFrom(r.Context())
	if !ok || !a.NeedsReauth() {
		return ""
	}
	return a.ReauthField
}

// OpLog writes one line to the op log (A-601) as the administrator on r. action 은 "site.이름" 이다.
// 기록에 실패해도 요청은 그대로 간다 — 일은 이미 했다. 실패는 오류 로그로 남는다.
func OpLog(r *http.Request, action, target, summary string) {
	a, ok := siteext.AdminFrom(r.Context())
	switch {
	case !ok:
		logger(r).Error("site.OpLog: 사이트 관리자 화면 밖에서 불렀다", "action", action)
	case !siteext.ValidKey(action):
		logger(r).Error("site.OpLog: 동작은 site. 다음에 소문자로 시작하는 마디 하나다", "action", action)
	default:
		if err := a.Record(action, target, summary); err != nil {
			logger(r).Error("작업 로그 기록 실패", "action", action, "target", target, "err", err)
		}
	}
}

// EndSessions ends every app token of subject — 인부가 그만둘 때 사이트 관리자 화면에서 부른다. Lookup 이 그 번호를
// 더 허락하지 않아도 이미 받은 토큰은 30일 동안 살아 있으므로 둘 다 한다. 관리자 화면 밖이면 오류다.
func EndSessions(ctx context.Context, subject string) error {
	a, ok := siteext.AdminFrom(ctx)
	switch {
	case !ok:
		return errors.New("site.EndSessions: 사이트 관리자 화면 밖에서 불렀다")
	case subject == "":
		return errors.New("site.EndSessions: 대상이 비었다")
	}
	return a.EndSessions(ctx, subject)
}

func logger(r *http.Request) *slog.Logger {
	if c, ok := siteext.CallerFrom(r.Context()); ok && c.Log != nil {
		return c.Log
	}
	return slog.Default()
}
