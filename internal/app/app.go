// Package app builds the operating-mode route tree — everything the site
// serves once it is installed.
package app

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/alexedwards/scs/pgxstore"
	"github.com/alexedwards/scs/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/emirue/ondolith/internal/admin"
	"github.com/emirue/ondolith/internal/auth"
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
		return s
	}
	dev := setting("site.dev_mode")["site.dev_mode"] != ""
	themeDir := func() string { return setting("theme.active")["theme.active"] }

	// The loader is swapped, not mutated: A-202 activates a theme on a running
	// server and FR-303 says the next request uses it. A pointer swap means a
	// request already rendering finishes against the loader it started with,
	// instead of having its template set change underneath it.
	var loaderRef atomic.Pointer[theme.Loader]
	newLoader := func(dir string) *theme.Loader {
		return theme.New(theme.Builtin(), dir, dev, theme.FuncMap(theme.Deps{}))
	}
	loaderRef.Store(newLoader(themeDir()))
	loader := loaderRef.Load
	limiter := auth.NewLimiter()
	limits := auth.DefaultLimits()

	pub := &publicDeps{content: contentStore, loader: loader, log: log, site: site, dev: dev}
	lg := &loginDeps{sm: sessions, store: authStore, limiter: limiter, limits: limits,
		render: pub.renderNamed}
	mailer := auth.NewMailer(settingsSender{settings: setting, log: log}, log)
	acc := &accountDeps{loginDeps: *lg, mailer: mailer, baseURL: "",
		verifyRequired: func() bool {
			return setting("auth.email_verification_required")["auth.email_verification_required"] != ""
		}}

	adminUI := newAdminRenderer(func() string { return site().Name }, log)
	ad := &admin.Deps{
		Content: contentStore,
		Auth:    authStore,
		Caller: func(r *http.Request) admin.Caller {
			return adminCaller{a: ActorFrom(r.Context()), now: time.Now}
		},
		Render:     adminUI.Render,
		Version:    version,
		Migrations: func(c context.Context) ([]string, int, error) { return migrations.Status(c, db) },
		ValidateTheme: func(name string) (string, error) {
			return theme.ValidateThemeDir(name, version)
		},
		OnThemeChange: func(name string) { loaderRef.Store(newLoader(name)) },
		SendReset: func(email, token string) {
			mailer.SendAsync(email, "비밀번호 재설정", "아래 링크로 재설정하세요:\n/password/reset/"+token)
		},
	}

	registry := buildTree(pub, lg, acc, ad, func(w http.ResponseWriter, r *http.Request) {
		loader().StaticHandler("/static/").ServeHTTP(w, r)
	})

	perms, err := authStore.PermissionKeys(ctx)
	if err != nil {
		return fail(fmt.Errorf("app: 권한 목록: %w", err))
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

	cleanup := func() {
		sessionStore.StopCleanup()
		_ = db.Close()
		pool.Close()
	}
	return h, cleanup, nil
}
