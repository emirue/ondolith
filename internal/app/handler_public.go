package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/commerce"
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
	// products lists the newest visible products for the home page. nil 이면
	// 커머스가 꺼진 배포다 — 핸들러가 `if 커머스켜짐` 을 검사하는 대신 조립
	// 시점에 정해진다 (FR-710, tree.go 와 같은 규칙).
	products func(ctx context.Context, limit int) ([]commerce.Product, error)
	// ping answers P-907. 함수인 이유는 이 타입이 풀을 알 필요가 없기 때문이다
	// — 헬스체크가 필요한 것은 "지금 DB 에 닿는가" 하나다.
	ping func(context.Context) error
}

// homeItems is how many entries each home section shows.
//
// 한 화면에 담기는 만큼이다. 더 보려면 그 목록 화면으로 가면 되고, 홈이 목록
// 화면을 대신하려 들면 둘 다 어중간해진다.
const homeItems = 6

// P-201 GET / — the home page.
//
// **사이트 유형이 무엇을 보일지 정한다** (FR-710). `shop` 이면 신상품,
// `cms` 면 최근 글이다 — 한 바이너리가 두 종류의 사이트를 굴리므로 첫 화면이
// 그 성격을 말해야 한다.
//
// **데이터가 없으면 그 절을 아예 넘긴다.** 빈 상자를 남기면 설치 직후의 사이트가
// 고장 난 것처럼 보이고, 운영자가 지울 방법도 없다.
func (d *publicDeps) home(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a := ActorFrom(ctx)
	v := d.view(r, "홈", "")

	data := map[string]any{}
	// **유형을 여기서 다시 보지 않는다.** `products` 는 커머스일 때만 채워지므로
	// (app.go 조립부, FR-710) nil 여부가 곧 유형이다. 두 곳에서 판단하면 한쪽만
	// 고쳐진 채로 남고, 그 어긋남은 화면에서만 보인다 — 실제로 사이트 유형을
	// 여기 한 번 더 검사해 두었더니 그 조건을 지워도 아무 테스트가 안 물었다.
	// TestNoCommerceFlagInsideHandlers 가 이 규칙을 소스에서 강제한다.
	if d.products != nil {
		// 신상품. 보이는 것만 — 숨긴 상품이 첫 화면에 뜨면 숨긴 의미가 없다.
		items, err := d.products(ctx, homeItems)
		if err != nil {
			d.serverError(w, r, err)
			return
		}
		data["Products"] = items
	}

	// 최근 글은 두 유형 모두에서 쓸모가 있다 — 쇼핑몰도 공지를 쓴다.
	boards, err := d.content.Boards(ctx)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	// **권한 술어는 검색과 같은 것을 쓴다.** 홈이 자기 조건을 따로 쓰면 그 한
	// 줄이 어긋나는 날 비공개 게시판 글이 첫 화면에 뜬다 (D12 P-201).
	readable, secretIn := readableBoards(a, boards)
	posts, err := d.content.RecentPosts(ctx, readable, secretIn, actorID(a), homeItems)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	if len(posts) > 0 {
		// **글이 속한 게시판이 그 게시판으로 가는 링크가 된다.** 이것이
		// 없는 동안 새로 설치한 사이트에서는 게시판에 갈 방법이 메뉴를 직접
		// 만드는 것뿐이었다 — 홈에 글은 뜨는데 목록으로는 갈 수 없었다.
		type board struct{ slug, name string }
		of := map[string]board{}
		for _, b := range boards {
			of[b.ID] = board{b.Slug, b.Name}
		}
		type postView struct {
			content.Post
			BoardSlug string
			BoardName string
		}
		views := make([]postView, 0, len(posts))
		for _, p := range posts {
			views = append(views, postView{
				Post: p, BoardSlug: of[p.BoardID].slug, BoardName: of[p.BoardID].name})
		}
		data["Posts"] = views
	}

	v.Data = data
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
	// **Can 은 테마가 그릴지 말지를 정하는 데만 쓴다.** 역할도 권한 행도 넘기지
	// 않는다 (D17) — 여기 담기는 것은 테마가 실제로 묻는 열쇠뿐이다. 이것이
	// 비어 있는 동안 관리자는 사이트 어디에서도 /admin/ 으로 갈 수 없었다.
	for _, perm := range theme.CanKeys {
		v.Can[perm] = a.CanOn(perm, auth.Global)
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

// renderPartial writes a screen's block with no page chrome — for htmx swaps.
func (d *publicDeps) renderPartial(w http.ResponseWriter, r *http.Request, name string, v theme.View) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := d.loader().RenderPartial(w, name, v); err != nil {
		d.log.Error("조각 렌더링 실패", "template", name, "err", err)
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

// health is P-907 `/healthz`.
//
// **내부 구조를 노출하지 않는다** (NFR-210). 버전도, DB 이름도, 오류 원문도
// 나가지 않는다 — 이 경로는 공개이고, 로드밸런서가 읽는 두 글자 말고는 전부
// 공격자에게 주는 정보다. 자세한 진단은 A-602(관리자 화면)에 있다.
//
// **DB 연결을 실제로 확인한다.** 프로세스가 살아 있다는 사실만 보고 200 을
// 내면, DB 가 끊긴 인스턴스가 계속 트래픽을 받는다 — 헬스체크가 있으나 마나가
// 되는 정확히 그 상황이다.
func (d *publicDeps) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if d.ping == nil {
		// 배선이 빠졌다. 「모르겠다」를 「정상」으로 답하지 않는다.
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("unavailable\n"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
	defer cancel()
	if err := d.ping(ctx); err != nil {
		// 원인은 로그에만. 응답에 담으면 DSN·호스트명이 새어 나간다.
		if d.log != nil {
			d.log.Warn("헬스체크 실패", "err", err)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("unavailable\n"))
		return
	}
	_, _ = w.Write([]byte("ok\n"))
}

// healthTimeout bounds the probe. 무한정 기다리면 헬스체크 자체가 커넥션을
// 붙잡고, 로드밸런서는 타임아웃으로 읽어 같은 결론에 더 비싸게 도달한다.
const healthTimeout = 2 * time.Second
