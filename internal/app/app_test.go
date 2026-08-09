package app

import (
	"bytes"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2/memstore"

	"github.com/emirue/ondolith/internal/config"
)

// NFR-204. Each flag is asserted on its own so that flipping any single one
// fails a named test rather than a lump assertion.
func TestSessionCookieHardening(t *testing.T) {
	s := newSessionManager(memstore.New(), true)

	if !s.Cookie.HttpOnly {
		t.Error("HttpOnly = false — 스크립트가 세션 쿠키를 읽을 수 있다")
	}
	if s.Cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", s.Cookie.SameSite)
	}
	if s.Lifetime != sessionLifetime {
		t.Errorf("Lifetime = %v, want %v", s.Lifetime, sessionLifetime)
	}
}

// The Secure flag follows the config rather than being hardcoded: a site
// served over plain HTTP would drop every cookie if it were always true, and a
// TLS site would leak the session over HTTP if it were always false.
func TestSessionSecureFlagFollowsConfig(t *testing.T) {
	for _, secure := range []bool{true, false} {
		s := newSessionManager(memstore.New(), secure)
		if s.Cookie.Secure != secure {
			t.Errorf("secureCookies=%v → Cookie.Secure=%v", secure, s.Cookie.Secure)
		}
	}
}

func TestSessionManagerUsesGivenStore(t *testing.T) {
	store := memstore.New()
	if s := newSessionManager(store, true); s.Store != store {
		t.Error("전달한 스토어가 세션 매니저에 설정되지 않았다")
	}
}

// The theme is a third-party file swapped at runtime (FR-302, FR-303). If the
// view model carries the DSN, one `{{.DatabaseURL}}` added to a theme publishes
// the database password to every visitor.
func TestSiteViewCarriesNoSecret(t *testing.T) {
	const secret = "s3cr3t-db-password"
	cfg := &config.Config{
		DatabaseURL: "postgres://postgres:" + secret + "@127.0.0.1:5432/ondolith",
		SiteName:    "온돌 사이트",
	}

	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := tmpl.ExecuteTemplate(&out, "home.html", newSiteView(cfg)); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(out.String(), secret) {
		t.Fatal("렌더링 결과에 DB 비밀번호가 들어 있다")
	}
	if strings.Contains(out.String(), "postgres://") {
		t.Fatal("렌더링 결과에 DSN 이 들어 있다")
	}
	if !strings.Contains(out.String(), cfg.SiteName) {
		t.Error("사이트 이름이 렌더링되지 않았다 — 뷰 모델이 너무 좁다")
	}
}

// Widening the view model must be a deliberate act. Adding a field fails this
// test, which is the point: the next field could be a credential.
func TestSiteViewFieldsAreAllowlisted(t *testing.T) {
	allowed := map[string]bool{"Name": true}

	typ := reflect.TypeOf(siteView{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if !allowed[name] {
			t.Errorf("뷰 모델에 허용되지 않은 필드가 있다: %s "+
				"— docs/17-theme-contract.md 를 보고 의도한 것이면 이 목록에 추가할 것", name)
		}
		delete(allowed, name)
	}
	for name := range allowed {
		t.Errorf("뷰 모델에서 사라진 필드: %s", name)
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "reached")
	})
}

func serve(t *testing.T, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	h := withMiddleware(okHandler(), newSessionManager(memstore.New(), false))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// NFR-205. CrossOriginProtection is header-based, so a cross-site POST must be
// refused before it reaches any handler.
func TestCrossOriginPostIsRejected(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")

	rec := serve(t, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("교차 출처 POST 가 통과했다 (HTTP %d, 본문 %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got == "reached" {
		t.Error("핸들러까지 도달했다 — CSRF 미들웨어가 걸려 있지 않다")
	}
}

func TestSameOriginPostIsAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")

	if rec := serve(t, req); rec.Body.String() != "reached" {
		t.Errorf("동일 출처 POST 가 막혔다: HTTP %d", rec.Code)
	}
}

// Safe methods pass through by design, which is exactly why D15 P5 forbids
// changing state on them. This test records that the pass-through is real.
func TestSafeMethodPassesRegardlessOfOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")

	if rec := serve(t, req); rec.Body.String() != "reached" {
		t.Errorf("GET 이 막혔다: HTTP %d — 안전 메서드는 통과해야 한다", rec.Code)
	}
}

func TestSessionMiddlewareIsInChain(t *testing.T) {
	sessions := newSessionManager(memstore.New(), false)
	h := withMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessions.Put(r.Context(), "k", "v")
		if got := sessions.GetString(r.Context(), "k"); got != "v" {
			t.Errorf("세션 값 = %q, want v", got)
		}
	}), sessions)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	// LoadAndSave writes the session cookie on the way out.
	if len(rec.Result().Cookies()) == 0 {
		t.Error("세션 쿠키가 설정되지 않았다 — LoadAndSave 가 체인에 없다")
	}
}

func TestSessionLifetimeIsNotZero(t *testing.T) {
	if sessionLifetime <= 0 {
		t.Fatalf("sessionLifetime = %v", time.Duration(sessionLifetime))
	}
}

// NFR-211. **CSRF 는 클릭재킹을 막지 못한다.**
//
// `CrossOriginProtection` 은 요청의 `Sec-Fetch-Site` 를 본다. 공격자가 이 사이트를
// iframe 에 넣고 그 위에 투명한 미끼를 얹으면, 사용자가 누르는 것은 **이 사이트
// 안의 진짜 버튼**이다 — 그 요청은 프레임 자신에게서 나오므로 same-origin 이고
// CSRF 검사를 그대로 통과한다. 막는 것은 프레임에 들어가지 않는 것뿐이다.
//
// 세 헤더를 함께 본다. 하나만 두면 나머지가 조용히 빠진 채로 배포된다.
func TestSecurityHeadersAreSet(t *testing.T) {
	rec := serve(t, httptest.NewRequest(http.MethodGet, "/", nil))

	for _, c := range []struct{ header, want string }{
		// 프레임 금지. 클릭재킹의 유일한 방어다.
		{"X-Frame-Options", "DENY"},
		// 콘텐츠 타입을 브라우저가 다시 추측하지 않는다.
		{"X-Content-Type-Options", "nosniff"},
	} {
		if got := rec.Header().Get(c.header); got != c.want {
			t.Errorf("%s = %q, want %q", c.header, got, c.want)
		}
	}

	// 재설정 토큰은 **주소에** 있다 (`/password/reset/<토큰>`). Referer 를 그대로
	// 흘리면 그 화면의 바깥 링크·이미지 하나가 토큰을 남의 서버에 넘긴다.
	if got := rec.Header().Get("Referrer-Policy"); got == "" {
		t.Error("Referrer-Policy 가 없다 — 재설정 토큰이 Referer 로 샌다")
	}

	// CSP 는 XSS 가 하나 뚫렸을 때의 두 번째 문이다. frame-ancestors 는
	// X-Frame-Options 의 현대판이라 함께 둔다.
	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("Content-Security-Policy 가 없다")
	}
	for _, want := range []string{"frame-ancestors", "object-src", "base-uri"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP 에 %s 가 없다: %q", want, csp)
		}
	}
}
