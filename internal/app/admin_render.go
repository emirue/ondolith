package app

import (
	"bytes"
	"context"
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/alexedwards/scs/v2"

	"github.com/emirue/ondolith/internal/admin"
	"github.com/emirue/ondolith/internal/auth"
)

//go:embed templates/admin/*.html templates/admin/admin.css
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
	// shop mirrors FR-710: cms 모드에서는 커머스 메뉴가 없다.
	shop bool
	// css is the design system, inlined rather than served at its own path.
	// A route would need a D11 screen id and a boot-check entry for an asset
	// that only authenticated administrators ever fetch; one <style> block is
	// smaller than that whole apparatus.
	css template.CSS
	// theme selects one of the handoff's five directions (1a~1e). Empty means
	// 1a, the default the stylesheet ships with.
	theme string

	mu    sync.RWMutex
	cache map[string]*template.Template
}

func newAdminRenderer(site func() string, theme string, shop bool, log *slog.Logger) (*adminRenderer, error) {
	b, err := adminFS.ReadFile("templates/admin/admin.css")
	if err != nil {
		return nil, err
	}
	// The stylesheet is ours, from the embedded FS — not user input. It is
	// marked CSS so html/template inlines it instead of escaping every brace.
	return &adminRenderer{
		shop:  shop,
		site:  site,
		log:   log,
		css:   template.CSS(b),
		theme: theme,
		cache: map[string]*template.Template{},
	}, nil
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
	"admin/dashboard.html":        "대시보드",
	"admin/settings.html":         "사이트 설정",
	"admin/mail.html":             "메일 설정",
	"admin/pages.html":            "페이지",
	"admin/page-edit.html":        "페이지 편집",
	"admin/menus.html":            "메뉴",
	"admin/users.html":            "사용자",
	"admin/user-detail.html":      "사용자 상세",
	"admin/roles.html":            "역할·권한",
	"admin/themes.html":           "테마",
	"admin/system.html":           "시스템 정보",
	"admin/boards.html":           "게시판",
	"admin/board-edit.html":       "게시판 설정",
	"admin/board-fields.html":     "커스텀 필드",
	"admin/posts.html":            "글 관리",
	"admin/comments.html":         "댓글 관리",
	"admin/attachments.html":      "첨부 관리",
	"admin/oplog.html":            "작업 로그",
	"admin/theme-upload.html":     "테마 업로드",
	"admin/products.html":         "상품",
	"admin/categories.html":       "카테고리",
	"admin/orders.html":           "주문",
	"admin/order.html":            "주문 상세",
	"admin/shipping.html":         "송장 입력",
	"admin/refund.html":           "취소·환불 처리",
	"admin/returns.html":          "반품·교환 처리",
	"admin/policy.html":           "커머스 정책",
	"admin/product-edit.html":     "상품 편집",
	"admin/product-variants.html": "옵션·재고",
	"admin/terms.html":            "약관 관리",
	"admin/business.html":         "사업자 정보",
	"admin/reconcile.html":        "결제 대사",
	"admin/webhooks.html":         "웹훅 수신 이력",
	"admin/scan-receive.html":     "스캔 입고",
	"admin/stocktake.html":        "재고 실사",
	"admin/pick.html":             "출고 피킹 대조",
	"admin/scan-lookup.html":      "스캔 조회",
	"admin/qr-labels.html":        "QR 라벨",
	"admin/payment.html":          "결제 설정",
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
	groups := admin.Nav(actor.Can, a.shop)
	view := map[string]any{
		"Title":      adminScreenTitles[name],
		"SiteName":   a.site(),
		"Nav":        groups,
		"Current":    admin.CurrentGroup(groups, r.URL.Path),
		"CSS":        a.css,
		"AdminTheme": a.theme,
		"UserName":   actorName(actor),
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

// actorName is what the header greets. Only the display name — the header is
// on every admin screen, and an email there is one screenshot from a leak.
func actorName(a *Actor) string {
	if a == nil || a.User == nil {
		return ""
	}
	return a.User.DisplayName
}

// adminCaller adapts the request's Actor to what the admin package needs.
type adminCaller struct {
	a   *Actor
	now func() time.Time
	// ctx·auth·sm 은 ConfirmReauth 만 쓴다. 재인증은 비밀번호를 실제로 대조
	// 하고 세션에 시각을 찍는 일이라, Actor 스냅샷만으로는 할 수 없다.
	ctx  context.Context
	auth *auth.Store
	sm   *scs.SessionManager
	// limiter·limit 은 D15 4.3-2 의 「재인증 계정당 5회/분」이다. 세션이 이미
	// 있으므로 IP 가 아니라 계정 기준이다 — IP 로 걸면 훔친 세션 하나로
	// 관리자 트리 상한(60회/분)까지 비밀번호를 시도하는 오라클이 된다.
	limiter *auth.Limiter
	limit   auth.Limit
}

func (c adminCaller) Can(perm string) bool { return c.a.Can(perm) }
func (c adminCaller) CanOn(perm string, board auth.BoardID) bool {
	return c.a.CanOn(perm, board)
}
func (c adminCaller) Email() string {
	if c.a == nil || c.a.User == nil {
		return ""
	}
	return c.a.User.Email
}
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

// ConfirmReauth verifies the password typed into the destructive screen's own
// form and re-stamps the window (D15 5.3-1).
//
// **이 구현이 없으면 재인증 안내는 장식이다.** `reauth_at` 을 쓰는 곳이 비밀번호
// 변경 하나뿐이면, 로그인 후 15분이 지난 운영자는 폼의 비밀번호 칸을 채워도
// 계속 403 을 받는다 — 로그아웃했다 들어오기 전에는 환불이 불가능해진다.
func (c adminCaller) ConfirmReauth(password string) bool {
	if password == "" || c.a == nil || c.a.User == nil || c.auth == nil || c.sm == nil {
		return false
	}
	// **계정당 5회/분** (D15 4.3-2). 로그인과 같은 비밀번호 대조이므로 같은
	// 제한을 받아야 한다 — 없으면 세션 안에서 로그인 제한을 우회한다.
	if c.limiter == nil || !c.limiter.Allow("reauth:"+c.a.User.ID, c.limit) {
		return false
	}
	if _, err := c.auth.Authenticate(c.ctx, c.a.User.Email, password); err != nil {
		return false
	}
	// 성공하면 버킷을 비운다. 정상 사용자가 한 번 틀린 뒤 맞혔을 때 다음
	// 재인증까지 남은 횟수가 줄어 있으면, 제한이 공격자가 아니라 그 사람을 문다.
	c.limiter.Forget("reauth:" + c.a.User.ID)
	putTime(c.sm, c.ctx, sessReauth, c.now())
	return true
}
