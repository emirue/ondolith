package app

import (
	"bytes"
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/emirue/ondolith/internal/admin"
)

//go:embed templates/admin/*.html
var adminFS embed.FS

// adminRenderer draws the administrator screens.
//
// These do NOT go through the theme loader: D17 has no `A-###` templates, so an
// administrator screen is not part of the theme contract (D22 9절 7). A theme
// that could redraw the admin tree would be a third-party file deciding what
// the operator sees while granting permissions.
//
// Each screen is parsed together with the layout in its own template set,
// because every screen defines "content" and one set can only hold one of them.
type adminRenderer struct {
	site func() string
	log  *slog.Logger

	mu    sync.RWMutex
	cache map[string]*template.Template
}

func newAdminRenderer(site func() string, log *slog.Logger) *adminRenderer {
	return &adminRenderer{site: site, log: log, cache: map[string]*template.Template{}}
}

func (a *adminRenderer) lookup(name string) (*template.Template, error) {
	a.mu.RLock()
	t, ok := a.cache[name]
	a.mu.RUnlock()
	if ok {
		return t, nil
	}
	t, err := template.ParseFS(adminFS, "templates/admin/layout.html", "templates/"+name)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.cache[name] = t
	a.mu.Unlock()
	return t, nil
}

// adminScreenTitles names each screen for the heading and the tab.
var adminScreenTitles = map[string]string{
	"admin/dashboard.html":   "대시보드",
	"admin/settings.html":    "사이트 설정",
	"admin/mail.html":        "메일 설정",
	"admin/pages.html":       "페이지",
	"admin/page-edit.html":   "페이지 편집",
	"admin/menus.html":       "메뉴",
	"admin/users.html":       "사용자",
	"admin/user-detail.html": "사용자 상세",
	"admin/roles.html":       "역할·권한",
	"admin/themes.html":      "테마",
	"admin/system.html":      "시스템 정보",
}

// Render writes one administrator screen.
//
// The status code is written before the body, and a template failure after that
// point cannot change it — so the parse happens first and a broken template
// becomes a 500 instead of a half-drawn page under a 200.
func (a *adminRenderer) Render(w http.ResponseWriter, r *http.Request, name string, code int, data any) {
	t, err := a.lookup(name)
	if err != nil {
		a.log.Error("관리자 템플릿", "screen", name, "err", err)
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}

	actor := ActorFrom(r.Context())
	groups := admin.Nav(actor.Can)
	view := map[string]any{
		"Title":    adminScreenTitles[name],
		"SiteName": a.site(),
		"Nav":      groups,
		"Current":  admin.CurrentGroup(groups, r.URL.Path),
	}
	// The screen's own payload is merged in, never nested: the templates were
	// written against the map the handlers already build.
	if m, ok := data.(map[string]any); ok {
		for k, v := range m {
			view[k] = v
		}
	}

	// Rendered whole before the status line goes out: writing the code first
	// would leave a template error as a 200 with half a page.
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "admin/layout.html", view); err != nil {
		a.log.Error("관리자 렌더링", "screen", name, "err", err)
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if _, err := w.Write(buf.Bytes()); err != nil {
		a.log.Error("관리자 응답 쓰기", "screen", name, "err", err)
	}
}

// adminCaller adapts the request's Actor to what the admin package needs.
type adminCaller struct {
	a   *Actor
	now func() time.Time
}

func (c adminCaller) Can(perm string) bool { return c.a.Can(perm) }
func (c adminCaller) UserID() string {
	if c.a == nil || c.a.User == nil {
		return ""
	}
	return c.a.User.ID
}
func (c adminCaller) IsSuperuser() bool {
	return c.a != nil && c.a.Perms != nil && c.a.Perms.Superuser
}
func (c adminCaller) NeedsReauth() bool { return NeedsReauth(c.a, c.now()) }
