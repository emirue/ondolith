package app

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/emirue/ondolith/internal/auth"
)

// adminPrefix is the one path segment the tree gate knows about. That is the
// whole extent of path knowledge allowed in middleware (D15 4.2) — a prefix,
// not a parse.
const adminPrefix = "/admin"

// loginPath is where an unauthenticated caller is sent (P-101).
const loginPath = "/login"

// withTreeGate guards the admin tree (D15 4.1 [4]).
//
// Unauthenticated goes to the login screen carrying `next`, because sending a
// 403 to someone who simply is not logged in yet makes the product look broken.
// Authenticated but without admin.access is a 403: they are logged in, and the
// answer is no.
//
// This gate is about ENTERING the tree. Every screen inside still judges its
// own resource — the middleware saw a prefix, not a target (D15 4.2).
func withTreeGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isUnder(r.URL.Path, adminPrefix) {
			next.ServeHTTP(w, r)
			return
		}
		a := ActorFrom(r.Context())
		if !a.IsAuthenticated() {
			to := loginPath + "?next=" + url.QueryEscape(r.URL.RequestURI())
			http.Redirect(w, r, to, http.StatusSeeOther)
			return
		}
		if !a.Can("admin.access") {
			http.Error(w, "권한이 없습니다.", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isUnder reports whether p is the prefix itself or a path below it. A plain
// strings.HasPrefix would also match "/administrator".
func isUnder(p, prefix string) bool {
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

// withAdminRateLimit caps admin-tree traffic per IP (D15 4.3-2: 60/min).
//
// Only the admin tree: the public site is cached and hit by crawlers, and a
// per-IP cap there would throttle a search engine off the site. The webhook
// path is deliberately never rate limited — a 429 makes the PG retry, and that
// retry is the storm (D50).
func withAdminRateLimit(l *auth.Limiter, lim auth.Limit) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isUnder(r.URL.Path, adminPrefix) {
				next.ServeHTTP(w, r)
				return
			}
			if !l.Allow("admin:ip:"+clientIP(r), lim) {
				w.Header().Set("Retry-After", "60")
				http.Error(w, "요청이 너무 잦습니다. 잠시 후 다시 시도하세요.", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// clientIP is the remote address, port stripped.
//
// X-Forwarded-For is NOT consulted. Anyone can send that header, so trusting it
// lets a caller pick their own rate-limit bucket and the limit stops existing.
// Behind a proxy the operator terminates TLS and forwards, and the proxy's own
// limits apply; wiring a trusted-proxy list is a separate decision with its own
// failure mode.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
