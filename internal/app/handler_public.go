package app

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/content"
	"github.com/emirue/ondolith/internal/theme"
)

// publicDeps serves the pages a visitor sees.
type publicDeps struct {
	content *content.Store
	// loader is a function, not a value: A-202 swaps the active theme while the
	// server runs and FR-303 says the next request uses it. Holding the loader
	// itself would mean "restart to change theme".
	loader func() *theme.Loader
	log    *slog.Logger
	site   func() theme.Site
	// dev decides how much an error page says (FR-306). In production it says
	// nothing; in development it names the cause.
	dev bool
}

// P-201 GET / — the home page.
func (d *publicDeps) home(w http.ResponseWriter, r *http.Request) {
	v := d.view(r, "홈", "")
	d.renderPage(w, r, "home.html", http.StatusOK, v)
}

// P-202 GET /{slug} — a published page.
//
// A draft is 404, not 403. Answering "forbidden" confirms the slug exists,
// which is the difference between a visitor bouncing off and someone learning
// that /pricing-2027 is being written (D15 SC-1 4항).
func (d *publicDeps) page(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	p, err := d.content.PublishedPageBySlug(r.Context(), slug)
	if errors.Is(err, content.ErrNotFound) {
		d.notFound(w, r)
		return
	}
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	// 본문 첫 줄이 설명이다. 페이지마다 다른 값이 나와야 검색 결과에서
	// 구분된다 (FR-511).
	v := d.view(r, p.Title, firstLine(p.Body))
	v.Data = p
	name := "page.html"
	if p.Template != "" {
		// FR-404: a page may name its own template, but only one the active
		// theme actually provides — otherwise a stored value picks an arbitrary
		// file path.
		if d.loader().HasBuiltin(p.Template) {
			name = p.Template
		}
	}
	d.renderPage(w, r, name, http.StatusOK, v)
}

// view assembles the common model (D17).
func (d *publicDeps) view(r *http.Request, title, desc string) theme.View {
	a := ActorFrom(r.Context())
	v := theme.NewView(d.site(), r.URL.Path)
	// FR-511: the screen fills what it knows, the site fills the rest. The
	// fallback is resolved HERE and not in the template — D17 규약 1 keeps
	// branching out of themes, and a theme that forgets the `if` would ship a
	// page with no description at all.
	v.Meta = theme.Meta{Title: title, Description: desc}
	if v.Meta.Description == "" {
		v.Meta.Description = v.Site.MetaDescription
	}
	if v.Meta.OGImage == "" {
		v.Meta.OGImage = v.Site.OGImage
	}
	if a.IsAuthenticated() {
		// Only what a template draws. Roles and permission rows stay out
		// (D17): a theme is third-party and a copied one would carry
		// assumptions about a permission model that differs per site.
		v.User = &theme.ViewUser{
			ID: a.User.ID, Email: a.User.Email, DisplayName: a.User.DisplayName,
		}
	}
	if items, err := d.content.MenuItems(r.Context()); err == nil {
		if tree, terr := content.BuildMenu(items, func(perm, board string) bool {
			return a.CanOn(perm, auth.BoardID(board))
		}); terr == nil {
			v.Menu = tree
		}
		// A broken menu (a cycle someone edited in) must not take the whole
		// site down: the page renders with no menu and the error is logged.
	}
	return v
}

func (d *publicDeps) renderPage(w http.ResponseWriter, r *http.Request, name string, code int, v theme.View) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if err := d.loader().Render(w, name, v); err != nil {
		// The status line is already sent, so this cannot become a 500 — log it
		// and let the truncated page speak for itself.
		d.log.Error("템플릿 렌더링 실패", "template", name, "err", err)
	}
}

// P-903 — not found.
// renderNamed is the render func the auth and account screens use: they build
// a plain map, and the view model wraps it so the theme still gets .Site, .Menu
// and .User (D17 뷰 모델 규약).
func (d *publicDeps) renderNamed(w http.ResponseWriter, r *http.Request, name string, code int, data any) {
	v := d.view(r, "", "")
	v.Data = data
	d.renderPage(w, r, name, code, v)
}

func (d *publicDeps) notFound(w http.ResponseWriter, r *http.Request) {
	v := d.view(r, "찾을 수 없습니다", "")
	d.renderPage(w, r, "error.html", http.StatusNotFound, v)
}

// P-904 — server error.
//
// NFR-210: the visitor gets a sentence. The stack, the query and the path go to
// the log, where the operator can read them and an attacker cannot. In dev the
// cause is shown, because the only audience is the person who caused it.
func (d *publicDeps) serverError(w http.ResponseWriter, r *http.Request, err error) {
	d.log.Error("요청 처리 실패", "path", r.URL.Path, "err", err)
	v := d.view(r, "일시적인 오류", "")
	if d.dev {
		v.Data = map[string]string{"Detail": err.Error()}
	}
	d.renderPage(w, r, "error.html", http.StatusInternalServerError, v)
}
