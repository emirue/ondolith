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
//
// forbid 가 403 을 그린다 — 사이트 레이아웃 안의 오류 화면이다. 평문(http.Error)이면
// 회원은 24바이트 흰 화면을 봤다. nil 이면 평문이다(시험 조립).
func withTreeGate(next http.Handler, forbid http.HandlerFunc) http.Handler {
	if forbid == nil {
		forbid = func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "권한이 없습니다.", http.StatusForbidden)
		}
	}
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
			forbid(w, r)
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
// X-Forwarded-For is NOT consulted here. Anyone can send that header, so trusting
// it lets a caller pick their own rate-limit bucket and the limit stops existing.
// Behind a proxy on the same machine, withProxyClientIP has already replaced
// RemoteAddr with the address that proxy vouched for — and only for connections
// that came from it (config.ProxyIPHeader, D72).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// withProxyClientIP puts the client's address in RemoteAddr when a reverse proxy
// on this machine forwarded the request (config.ProxyIPHeader, D72).
//
// **루프백에서 온 연결에서만 헤더를 읽는다.** 그 연결은 같은 서버의 프록시뿐이고,
// 프록시는 헤더를 덮어써 넘긴다(nginx `proxy_set_header X-Real-IP $remote_addr`).
// 밖에서 곧장 들어온 요청이 같은 헤더를 보내면 무시한다 — 믿으면 요청자가 자기
// 속도 제한 버킷을 고른다. 값이 IP 하나가 아니면(목록 등) 역시 무시한다.
//
// RemoteAddr 를 고치므로 clientIP·작업 로그·웹훅 로그가 모두 같은 값을 본다.
func withProxyClientIP(header string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if header == "" {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ip := proxiedIP(r, header); ip != nil {
				r = r.WithContext(r.Context()) // 얕은 복사 — 호출자의 요청을 바꾸지 않는다
				r.RemoteAddr = net.JoinHostPort(ip.String(), "0")
			}
			next.ServeHTTP(w, r)
		})
	}
}

func proxiedIP(r *http.Request, header string) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return nil
	}
	if peer := net.ParseIP(host); peer == nil || !peer.IsLoopback() {
		return nil
	}
	return net.ParseIP(strings.TrimSpace(r.Header.Get(header)))
}
