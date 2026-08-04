// Package app builds the operating-mode route tree — everything the site
// serves once it is installed.
package app

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/alexedwards/scs/pgxstore"
	"github.com/alexedwards/scs/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/emirue/ondolith/internal/config"
	"github.com/emirue/ondolith/internal/migrations"
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

// New brings up the operating tree: connection pool, session store, and
// routes. The returned func releases the pool and must be called on shutdown.
//
// Pending migrations are applied here rather than only at install time, so
// that upgrading is "replace the binary and restart".
func New(ctx context.Context, cfg *config.Config, log *slog.Logger) (http.Handler, func(), error) {
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, fmt.Errorf("app: pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("app: 데이터베이스에 연결할 수 없습니다: %w", err)
	}

	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	if err := migrations.Run(ctx, db); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("app: 마이그레이션: %w", err)
	}

	store := pgxstore.New(pool)
	sessions := newSessionManager(store, cfg.SecureCookies)

	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		pool.Close()
		return nil, nil, err
	}

	site := newSiteView(cfg)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.ExecuteTemplate(w, "home.html", site); err != nil {
			log.Error("렌더링 실패", "err", err)
		}
	})

	h := withMiddleware(mux, sessions)

	cleanup := func() {
		store.StopCleanup()
		pool.Close()
	}
	return h, cleanup, nil
}
