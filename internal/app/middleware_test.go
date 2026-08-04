package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/auth"
)

// actorHandler puts a fixed Actor on the request, standing in for withActor so
// the gate can be tested without a database.
func actorHandler(a *Actor, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		next.ServeHTTP(w, r.WithContext(withActorValue(ctx, a)))
	})
}

func ok(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }

func do(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = "192.0.2.10:1234"
	h.ServeHTTP(rec, req)
	return rec
}

func anon() *Actor { return &Actor{Perms: auth.NewPermissions(false, nil)} }
func admin() *Actor {
	return &Actor{
		User:  &auth.User{ID: "u1"},
		Perms: auth.NewPermissions(false, []auth.Grant{{Permission: "admin.access", Board: auth.Global}}),
	}
}
func plainUser() *Actor {
	return &Actor{User: &auth.User{ID: "u2"}, Perms: auth.NewPermissions(false, nil)}
}

func TestTreeGateRedirectsAnonymous(t *testing.T) {
	h := actorHandler(anon(), withTreeGate(http.HandlerFunc(ok)))
	rec := do(h, http.MethodGet, "/admin/pages")

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("HTTP %d, want 303", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, loginPath) {
		t.Errorf("Location = %q, want %s 로 이동", loc, loginPath)
	}
	// Without `next` the operator lands on the dashboard instead of where they
	// were going, and does it again on every deep link.
	if !strings.Contains(loc, "next=") || !strings.Contains(loc, "admin") {
		t.Errorf("next 가 실려 있지 않다: %q", loc)
	}
}

// Logged in but without admin.access is a refusal, not a redirect: sending them
// to the login screen they already passed is a loop.
func TestTreeGateForbidsWithoutPermission(t *testing.T) {
	h := actorHandler(plainUser(), withTreeGate(http.HandlerFunc(ok)))
	if rec := do(h, http.MethodGet, "/admin/pages"); rec.Code != http.StatusForbidden {
		t.Errorf("HTTP %d, want 403", rec.Code)
	}
}

func TestTreeGateAllowsAdmin(t *testing.T) {
	h := actorHandler(admin(), withTreeGate(http.HandlerFunc(ok)))
	if rec := do(h, http.MethodGet, "/admin/pages"); rec.Code != http.StatusOK {
		t.Errorf("HTTP %d, want 200", rec.Code)
	}
}

func TestTreeGateIgnoresPublicPaths(t *testing.T) {
	h := actorHandler(anon(), withTreeGate(http.HandlerFunc(ok)))
	for _, p := range []string{"/", "/about", "/board/free"} {
		if rec := do(h, http.MethodGet, p); rec.Code != http.StatusOK {
			t.Errorf("%s → HTTP %d, want 200", p, rec.Code)
		}
	}
}

// A plain HasPrefix would also gate /administrator and /adminfoo, which are
// public URLs a site may well use.
func TestTreeGateMatchesSegmentNotPrefix(t *testing.T) {
	h := actorHandler(anon(), withTreeGate(http.HandlerFunc(ok)))
	for _, p := range []string{"/administrator", "/adminfoo", "/admins"} {
		if rec := do(h, http.MethodGet, p); rec.Code != http.StatusOK {
			t.Errorf("%s 가 관리자 트리로 취급됐다: HTTP %d", p, rec.Code)
		}
	}
	if rec := do(h, http.MethodGet, "/admin"); rec.Code == http.StatusOK {
		t.Error("/admin 자체가 게이트를 통과했다")
	}
}

func TestAdminRateLimit(t *testing.T) {
	lim := auth.Limit{Burst: 3, Window: 60_000_000_000} // 3 per minute
	h := actorHandler(admin(),
		withAdminRateLimit(auth.NewLimiter(), lim)(http.HandlerFunc(ok)))

	for i := 0; i < 3; i++ {
		if rec := do(h, http.MethodGet, "/admin/x"); rec.Code != http.StatusOK {
			t.Fatalf("%d번째 요청 HTTP %d", i+1, rec.Code)
		}
	}
	rec := do(h, http.MethodGet, "/admin/x")
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("HTTP %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("Retry-After 가 없다")
	}
}

// The public site is crawled; a per-IP cap there would throttle a search engine
// off the site.
func TestAdminRateLimitDoesNotTouchPublicPaths(t *testing.T) {
	lim := auth.Limit{Burst: 1, Window: 60_000_000_000}
	h := actorHandler(anon(),
		withAdminRateLimit(auth.NewLimiter(), lim)(http.HandlerFunc(ok)))
	for i := 0; i < 10; i++ {
		if rec := do(h, http.MethodGet, "/"); rec.Code != http.StatusOK {
			t.Fatalf("공개 경로가 %d번째에 제한됐다: HTTP %d", i+1, rec.Code)
		}
	}
}

// X-Forwarded-For must not choose the bucket: anyone can send it, and then the
// limit does not exist.
func TestRateLimitBucketIgnoresForwardedFor(t *testing.T) {
	lim := auth.Limit{Burst: 2, Window: 60_000_000_000}
	h := actorHandler(admin(), withAdminRateLimit(auth.NewLimiter(), lim)(http.HandlerFunc(ok)))

	send := func(xff string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/admin/x", nil)
		req.RemoteAddr = "192.0.2.10:1234"
		req.Header.Set("X-Forwarded-For", xff)
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	send("10.0.0.1")
	send("10.0.0.2")
	// A third request with yet another spoofed address must still be refused.
	if code := send("10.0.0.3"); code != http.StatusTooManyRequests {
		t.Errorf("X-Forwarded-For 를 바꿔 제한을 우회했다: HTTP %d", code)
	}
}

// A handler that reaches for the Actor before the middleware ran must get an
// empty set, not a panic — and an empty set refuses.
func TestActorFromIsNeverNil(t *testing.T) {
	a := ActorFrom(httptest.NewRequest(http.MethodGet, "/", nil).Context())
	if a == nil {
		t.Fatal("nil Actor 가 반환됐다")
	}
	if a.IsAuthenticated() {
		t.Error("빈 컨텍스트인데 인증된 것으로 나온다")
	}
	if a.Can("admin.access") {
		t.Error("빈 Actor 가 권한을 통과시켰다")
	}
}
