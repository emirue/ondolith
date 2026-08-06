// Package app builds the operating-mode route tree — everything the site
// serves once it is installed.
package app

import (
	"context"
	"embed"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/alexedwards/scs/pgxstore"
	"github.com/alexedwards/scs/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/emirue/ondolith/internal/admin"
	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/commerce"
	"github.com/emirue/ondolith/internal/config"
	"github.com/emirue/ondolith/internal/content"
	"github.com/emirue/ondolith/internal/migrations"
	"github.com/emirue/ondolith/internal/theme"
)

//go:embed templates/*.html
var templatesFS embed.FS

// sessionLifetime is how long a session survives without activity.
const sessionLifetime = 12 * time.Hour

// siteView is everything a theme template may learn about the site.
//
// It exists so that config.Config never reaches a template. That struct holds
// DatabaseURL — the DSN, password included — and themes are third-party files
// swapped at runtime (FR-302, FR-303). A single `{{.DatabaseURL}}` added to a
// theme for debugging would publish the database credential to every visitor.
//
// Widen this deliberately, one field at a time. docs/17-theme-contract.md
// defines what belongs here.
type siteView struct {
	Name string
}

func newSiteView(cfg *config.Config) siteView {
	return siteView{Name: cfg.SiteName}
}

// newSessionManager applies the session cookie hardening NFR-204 requires.
//
// Split out of New() because New() needs a live database: leaving these three
// flags inline would put a security requirement behind an integration test,
// and it would pass silently on any machine without PostgreSQL
// (.ai/MISTAKES.md M4).
func newSessionManager(store scs.Store, secureCookies bool) *scs.SessionManager {
	s := scs.New()
	s.Store = store
	s.Lifetime = sessionLifetime
	s.Cookie.HttpOnly = true
	s.Cookie.SameSite = http.SameSiteLaxMode
	s.Cookie.Secure = secureCookies
	return s
}

// withMiddleware wraps the route tree in the three layers the architecture
// allows, outermost first: CSRF, then session load/save, then the routes
// (docs/20-architecture.md). Split out for the same reason as above — the CSRF
// layer is NFR-205 and must be testable without a database.
func withMiddleware(h http.Handler, sessions *scs.SessionManager) http.Handler {
	h = sessions.LoadAndSave(h)
	return http.NewCrossOriginProtection().Handler(h)
}

// New brings up the operating tree: connection pool, session store, and the
// route table. The returned func releases the pool and must be called on
// shutdown.
//
// Pending migrations are applied here rather than only at install time, so that
// upgrading is "replace the binary and restart".
//
// The boot self-check runs before a single route serves, and a failure returns
// an error rather than starting anyway (FR-110): a server that comes up in the
// wrong state has its wrong state discovered by a visitor.
func New(ctx context.Context, cfg *config.Config, version string, log *slog.Logger) (http.Handler, func(), error) {
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, fmt.Errorf("app: pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("app: 데이터베이스에 연결할 수 없습니다: %w", err)
	}

	db := stdlib.OpenDBFromPool(pool)
	if err := migrations.Run(ctx, db); err != nil {
		_ = db.Close()
		pool.Close()
		return nil, nil, fmt.Errorf("app: 마이그레이션: %w", err)
	}

	sessionStore := pgxstore.New(pool)
	sessions := newSessionManager(sessionStore, cfg.SecureCookies)
	fail := func(err error) (http.Handler, func(), error) {
		sessionStore.StopCleanup()
		_ = db.Close()
		pool.Close()
		return nil, nil, err
	}

	authStore := auth.NewStore(pool)
	contentStore := content.NewStore(pool)

	// Settings are read per request, not cached: A-201 changes them from the
	// running server and FR-303 says the next request reflects it.
	setting := func(keys ...string) map[string]string {
		kv, err := contentStore.Settings(ctx, keys...)
		if err != nil {
			log.Error("설정 조회", "err", err)
			return map[string]string{}
		}
		return kv
	}
	site := func() theme.Site {
		kv := setting("site.name", "site.meta_description", "site.og_image", "site.type")
		s := theme.Site{
			Name:            kv["site.name"],
			MetaDescription: kv["site.meta_description"],
			OGImage:         kv["site.og_image"],
			Type:            kv["site.type"],
		}
		if s.Name == "" {
			s.Name = cfg.SiteName
		}
		if s.Type == "" {
			s.Type = "cms"
		}
		// 사업자 정보는 shop 모드에서만 푸터에 나간다 (FR-711). cms 사이트는
		// 표시 의무가 없고, 비어 있는 여덟 줄을 그리면 그것이 곧 오류로 보인다.
		if s.Type == "shop" {
			raw := setting(commerce.BusinessKeys...)
			s.Business = map[string]string{}
			for _, k := range commerce.BusinessKeys {
				if v := raw[k]; v != "" {
					// **키가 아니라 항목 이름으로 넘긴다.** 테마는 우리 설정 키를
					// 몰라야 하고, `business.reg_no` 는 방문자가 읽을 말이 아니다.
					s.Business[commerce.BusinessLabels[k]] = v
				}
			}
			if len(s.Business) == 0 {
				s.Business = nil
			}
		}
		return s
	}
	dev := setting("site.dev_mode")["site.dev_mode"] != ""
	// A theme name is a directory under the theme root, never a path: the
	// setting comes from A-202 and an operator typing `../..` must not aim the
	// loader at the filesystem.
	themeDir := func() string { return themePath(cfg, setting("theme.active")["theme.active"]) }

	// The loader is swapped, not mutated: A-202 activates a theme on a running
	// server and FR-303 says the next request uses it. A pointer swap means a
	// request already rendering finishes against the loader it started with,
	// instead of having its template set change underneath it.
	var loaderRef atomic.Pointer[theme.Loader]
	loader := loaderRef.Load
	// asset() has to reach the loader that is current when the template runs,
	// and the loader owns the func map — so the closure reads the pointer
	// rather than capturing a loader that does not exist yet. Passing an empty
	// theme.Deps here is how every stylesheet URL came out as "" before.
	funcs := theme.FuncMap(theme.Deps{
		AssetURL: func(name string) string {
			if l := loader(); l != nil {
				return l.AssetURL(name)
			}
			return ""
		},
		URLFor: urlFor,
	})
	newLoader := func(dir string) *theme.Loader {
		return theme.New(theme.Builtin(), dir, dev, funcs)
	}
	loaderRef.Store(newLoader(themeDir()))
	limiter := auth.NewLimiter()
	limits := auth.DefaultLimits()

	pub := &publicDeps{content: contentStore, loader: loader, log: log, site: site, dev: dev,
		// P-907. 풀을 통째로 넘기지 않는다 — 헬스체크가 필요한 것은
		// "지금 DB 에 닿는가" 하나이고, 그보다 넓은 접근은 그 화면이 더
		// 많은 것을 말하게 되는 경로가 된다.
		ping: func(c context.Context) error { return pool.Ping(c) },
	}
	lg := &loginDeps{sm: sessions, store: authStore, limiter: limiter, limits: limits,
		render: pub.renderNamed}
	mailer := auth.NewMailer(settingsSender{settings: setting, log: log}, log)
	acc := &accountDeps{loginDeps: *lg, mailer: mailer, baseURL: "",
		verifyRequired: func() bool {
			return setting("auth.email_verification_required")["auth.email_verification_required"] != ""
		}}

	// FR-710 style: the direction is a setting, not a rebuild. The handoff's
	// five directions share one markup, so switching is a token swap.
	shopMode := site().Type == "shop"
	commerceStore := commerce.NewStore(pool)
	adminUI, err := newAdminRenderer(func() string { return site().Name },
		setting("admin.theme")["admin.theme"], shopMode, log)
	if err != nil {
		return fail(fmt.Errorf("app: 관리자 스타일: %w", err))
	}
	ad := &admin.Deps{
		Content: contentStore,
		Auth:    authStore,
		Caller: func(r *http.Request) admin.Caller {
			return adminCaller{a: ActorFrom(r.Context()), now: time.Now,
				ctx: r.Context(), auth: authStore, sm: sessions}
		},
		Render:      adminUI.Render,
		Commerce:    commerceStore,
		Attachments: contentStore.AttachmentsIn(cfg.Uploads()),
		OpLog:       contentStore.OpLog(),
		Logger:      log,
		Version:     version,
		Migrations:  func(c context.Context) ([]string, int, error) { return migrations.Status(c, db) },
		ValidateTheme: func(name string) (string, error) {
			// 이름을 테마 루트 아래 디렉터리로 푼다 — 로더가 쓰는 것과 같은
			// 방식이어야, 검증에 통과한 것과 실제로 그려지는 것이 같아진다.
			return theme.ValidateThemeDir(themePath(cfg, name), version)
		},
		// themePath 를 여기서도 거친다. 이름과 경로를 섞으면 세 곳(로더 초기화·
		// 검증·교체)이 서로 다른 것을 가리키고, 검증에 통과한 테마가 그려지지
		// 않는다 — 그 셋이 어긋난 상태를 통합 테스트가 잡았다.
		OnThemeChange: func(name string) { loaderRef.Store(newLoader(themePath(cfg, name))) },
		InstallTheme: func(name string, rd io.ReaderAt, size int64, replace bool) error {
			return theme.Install(cfg.Themes(), name, rd, size, replace)
		},
		SendReset: func(email, token string) {
			mailer.SendAsync(email, "비밀번호 재설정", "아래 링크로 재설정하세요:\n/password/reset/"+token)
		},
	}

	bd := &boardDeps{publicDeps: pub, sm: sessions, log: log,
		attachments: contentStore.AttachmentsIn(cfg.Uploads()), authStore: authStore}

	sh := &shopDeps{publicDeps: pub, sm: sessions, store: commerceStore, log: log,
		limiter: limiter, limits: limits,
		// 요청마다 읽는다. A-512 가 바꾸면 다음 요청부터 반영돼야 하고,
		// 부팅 때 붙잡아 두면 재시작 전까지 옛 값으로 계산한다.
		shipping: func() commerce.Shipping {
			kv := setting("shipping.flat_fee", "shipping.free_threshold")
			return commerce.Shipping{
				FlatFee:       atoiOr(kv["shipping.flat_fee"], 0),
				FreeThreshold: atoiOr(kv["shipping.free_threshold"], 0),
			}
		},
		// **A-209 가 정한다.** 하드코딩이면 관리자가 결제사를 골라도 웹훅
		// 경로와 payments.pg 가 옛 값으로 남는다. 미설정이면 토스다 —
		// 지금 등록된 어댑터가 그것 하나뿐이고, 빈 문자열은 라우트로도
		// 컬럼 값으로도 쓸 수 없다.
		pgName: func() string {
			if v := setting("pg.provider")["pg.provider"]; v != "" {
				return v
			}
			return "toss"
		},
		// 공개 키다. 시크릿은 어떤 경로로도 화면에 오지 않는다 (D19 P-407).
		pgClientKey: func() string { return setting("pg.client_key")["pg.client_key"] },
		gateway: func() commerce.Gateway {
			return commerce.NewToss(setting("pg.secret_key")["pg.secret_key"],
				"https://api.tosspayments.com", commerce.AuthWindow)
		}}

	// A-508 은 PG 에 실제 상태를 묻는다. sh 가 만들어진 뒤라야 하므로 여기서
	// 붙인다 — 조립 순서를 바꾸는 대신 한 줄을 옮긴다.
	ad.Gateway = sh.gateway

	static := func(w http.ResponseWriter, r *http.Request) {
		loader().StaticHandler("/static/").ServeHTTP(w, r)
	}
	// FR-710: 조립 시점에 정한다. site.type 이 shop 이 아니면 커머스 라우트는
	// 등록되지 않고, 등록되지 않은 것은 404 다.
	registry := buildTree(pub, lg, acc, bd, ad, sh, shopMode, static)

	perms, err := authStore.PermissionKeys(ctx)
	if err != nil {
		return fail(fmt.Errorf("app: 권한 목록: %w", err))
	}
	// The administrator menu is still a second list (internal/admin/shell.go)
	// rather than something derived from the route table, which is what W1-35's
	// criterion asked for. Until it is derived, this compares them: a menu entry
	// with no route is a link that 404s, and it looks exactly like a link the
	// caller lacks permission for — nobody reports it.
	for _, p := range admin.NavPaths(shopMode) {
		if !registry.HasPath(p) {
			res0 := "관리자 메뉴가 등록되지 않은 경로를 가리킨다: " + p
			return fail(fmt.Errorf("app: %s", res0))
		}
	}

	res := registry.Check(perms, screenInventory)
	for _, w := range res.Warnings {
		log.Warn("라우트 자체 점검", "경고", w)
	}
	if err := res.Err(); err != nil {
		return fail(err)
	}

	mux := http.NewServeMux()
	registry.Mount(mux)
	// D15 4.1's order, outermost first: [1] CSRF, [2] session, [3] actor load,
	// [4] tree gate + rate limit, [5] mux. The gate reads the Actor, so it has
	// to sit INSIDE the loader — outside it, every request looks anonymous and
	// the gate sends a logged-in administrator to the login form.
	h := withActor(sessions, authStore)(
		withAdminRateLimit(limiter, limits.AdminTreeIP)(withTreeGate(mux)))
	h = withMiddleware(h, sessions)

	// **P-905 는 본 트리 밖이다** (D15 SC-8 1항). 세션도 CSRF 도 액터도 붙지
	// 않는다 — PG 의 서버에는 줄 쿠키가 없고, `CrossOriginProtection` 이 이
	// 요청을 통과시키는 것은 브라우저 헤더가 없어서 생기는 우연이지 설계된
	// 보호가 아니다. 그 우연에 기대는 대신 아예 다른 문으로 받는다.
	//
	// `cms` 모드에서는 등록하지 않는다 — 커머스가 없으면 결제 웹훅도 없다.
	if shopMode {
		hooks := webhookMux(webhookDeps{store: commerceStore,
			gateway: sh.gateway, pgName: sh.pgName, log: log})
		main := h
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/webhooks/") {
				hooks.ServeHTTP(w, r)
				return
			}
			main.ServeHTTP(w, r)
		})
	}

	cleanup := func() {
		sessionStore.StopCleanup()
		_ = db.Close()
		pool.Close()
	}
	return h, cleanup, nil
}

// themePath resolves a theme NAME to its directory.
//
// A name is a directory under the theme root, never a path: the value comes
// from A-202 and an operator typing `../..` must not aim the loader at the
// filesystem. Empty means the built-in theme.
func themePath(cfg *config.Config, name string) string {
	if name == "" {
		return ""
	}
	return filepath.Join(cfg.Themes(), filepath.Base(name))
}

// atoiOr parses a setting that must be a number, falling back when it is not.
//
// 설정 값은 사람이 A-512 에서 입력한다. 숫자가 아니면 0 으로 두고 넘어가는
// 이유는, 여기서 오류를 내면 배송비 설정 오타 하나가 상점 전체를 500 으로
// 만들기 때문이다. 검증은 저장하는 화면의 몫이다.
func atoiOr(s string, fallback int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return n
}
