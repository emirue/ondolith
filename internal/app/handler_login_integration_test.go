package app

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/auth"
)

// loginFixture wires the login handler over a real database.
func loginFixture(t *testing.T) (*loginDeps, http.Handler, *auth.Store) {
	t.Helper()
	store, _, sm := authFixture(t)
	d := &loginDeps{
		sm: sm, store: store,
		limiter: auth.NewLimiter(),
		limits:  auth.DefaultLimits(),
		render: func(w http.ResponseWriter, _ *http.Request, _ string, code int, data any) {
			w.WriteHeader(code)
			if m, ok := data.(map[string]any); ok {
				if e, ok := m["Error"].(string); ok {
					_, _ = w.Write([]byte(e))
				}
			}
		},
	}
	mux := http.NewServeMux()
	NewRegistry().
		Add(Route{Screen: "P-101", Method: "POST", Pattern: "/login", Class: SC2, Handler: d.login}).
		Add(Route{Screen: "P-102", Method: "POST", Pattern: "/logout", Class: SC2, Handler: d.logout}).
		Mount(mux)
	return d, sm.LoadAndSave(withActor(sm, store, nil)(mux)), store
}

func mkAccount(t *testing.T, store *auth.Store, email, password string) string {
	t.Helper()
	h, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.CreateUser(context.Background(), email, h, email)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func postForm(h http.Handler, target string, form url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "192.0.2.5:1111"
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sessionCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == "session" {
			return c
		}
	}
	return nil
}

func TestLoginSucceedsAndRedirects(t *testing.T) {
	_, h, store := loginFixture(t)
	mkAccount(t, store, "a@example.com", "correct-horse-battery")

	rec := postForm(h, "/login", url.Values{
		"email":    {"A@Example.COM"}, // uppercase: normalisation happens server-side
		"password": {"correct-horse-battery"},
	}, nil)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("HTTP %d, want 303 (본문 %q)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want /", loc)
	}
	if sessionCookie(rec) == nil {
		t.Error("세션 쿠키가 발급되지 않았다")
	}
}

// FR-204 session fixation. Anyone who can set a cookie before login — a shared
// machine, XSS on a sibling host — otherwise holds a session that becomes
// authenticated the moment the victim signs in.
func TestLoginRenewsSessionToken(t *testing.T) {
	_, h, store := loginFixture(t)
	mkAccount(t, store, "a@example.com", "correct-horse-battery")

	// Arrive with a session (the anonymous middleware creates one).
	pre := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.RemoteAddr = "192.0.2.5:1111"
	h.ServeHTTP(pre, req)
	before := sessionCookie(pre)
	if before == nil {
		t.Skip("사전 세션 쿠키가 없다")
	}

	rec := postForm(h, "/login", url.Values{
		"email": {"a@example.com"}, "password": {"correct-horse-battery"},
	}, []*http.Cookie{before})
	after := sessionCookie(rec)
	if after == nil {
		t.Fatal("로그인 후 세션 쿠키가 없다")
	}
	if after.Value == before.Value {
		t.Error("세션 ID 가 재발급되지 않았다 — 세션 고정 공격이 성립한다 (FR-204)")
	}
}

// FR-201: "no such account", "wrong password" and "deactivated" must be
// indistinguishable. Three answers enumerate which addresses have accounts.
func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	_, _, store := loginFixture(t)
	_ = store

	cases := map[string]url.Values{
		"없는 계정":   {"email": {"nobody@example.com"}, "password": {"correct-horse-battery"}},
		"틀린 비밀번호": {"email": {"real@example.com"}, "password": {"wrong-password-here"}},
		"비활성 계정":  {"email": {"off@example.com"}, "password": {"correct-horse-battery"}},
	}
	var code int
	var body string
	for name, form := range cases {
		// A fresh fixture per case so the rate limiter does not interfere.
		_, hh, ss := loginFixture(t)
		mkAccount(t, ss, "real@example.com", "correct-horse-battery")
		mkAccount(t, ss, "off@example.com", "correct-horse-battery")
		if name == "비활성 계정" {
			if err := ss.SetActive(context.Background(), mustFind(t, ss, "off@example.com"), false); err != nil {
				t.Fatal(err)
			}
		}
		rec := postForm(hh, "/login", form, nil)
		if code == 0 {
			code, body = rec.Code, rec.Body.String()
			continue
		}
		if rec.Code != code {
			t.Errorf("%s: HTTP %d, 다른 경우는 %d — 응답이 구분된다", name, rec.Code, code)
		}
		if rec.Body.String() != body {
			t.Errorf("%s: 본문 %q, 다른 경우는 %q — 응답이 구분된다", name, rec.Body.String(), body)
		}
	}
}

func mustFind(t *testing.T, store *auth.Store, email string) string {
	t.Helper()
	u, _, err := store.FindActiveUserByEmail(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// FR-203: the row is gone, so a stolen cookie cannot ride the old session.
func TestLogoutDestroysSession(t *testing.T) {
	_, h, store := loginFixture(t)
	mkAccount(t, store, "a@example.com", "correct-horse-battery")

	in := postForm(h, "/login", url.Values{
		"email": {"a@example.com"}, "password": {"correct-horse-battery"},
	}, nil)
	c := sessionCookie(in)
	if c == nil {
		t.Fatal("로그인 후 쿠키가 없다")
	}

	postForm(h, "/logout", nil, []*http.Cookie{c})

	// The pre-logout cookie must no longer name a live session. scs issues a
	// fresh token when the old one is unknown, which is what proves the row is
	// gone rather than merely blanked.
	probe := httptest.NewRequest(http.MethodPost, "/logout", nil)
	probe.RemoteAddr = "192.0.2.5:1111"
	probe.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, probe)
	if again := sessionCookie(rec); again != nil && again.Value == c.Value {
		t.Error("로그아웃 후에도 같은 세션 토큰이 살아 있다 (FR-203)")
	}
}

// The rejected value is ignored, not an error (D19 P-101).
func TestLoginNextStaysOnSite(t *testing.T) {

	for _, next := range []string{"//evil.com", "https://evil.com/x", "/\\evil.com"} {
		_, hh, ss := loginFixture(t)
		mkAccount(t, ss, "a@example.com", "correct-horse-battery")
		rec := postForm(hh, "/login", url.Values{
			"email": {"a@example.com"}, "password": {"correct-horse-battery"}, "next": {next},
		}, nil)
		if loc := rec.Header().Get("Location"); loc != "/" {
			t.Errorf("next=%q → Location %q, want /", next, loc)
		}
	}
	// A legitimate internal path is honoured.
	_, hh, ss := loginFixture(t)
	mkAccount(t, ss, "a@example.com", "correct-horse-battery")
	rec := postForm(hh, "/login", url.Values{
		"email": {"a@example.com"}, "password": {"correct-horse-battery"}, "next": {"/admin/pages"},
	}, nil)
	if loc := rec.Header().Get("Location"); loc != "/admin/pages" {
		t.Errorf("정상 next 가 무시됐다: %q", loc)
	}
}

func TestLoginRateLimited(t *testing.T) {
	d, h, store := loginFixture(t)
	mkAccount(t, store, "a@example.com", "correct-horse-battery")
	d.limits.LoginPerIP = auth.Limit{Burst: 2, Window: 60_000_000_000}

	for i := 0; i < 2; i++ {
		postForm(h, "/login", url.Values{"email": {"a@example.com"}, "password": {"wrong"}}, nil)
	}
	rec := postForm(h, "/login", url.Values{"email": {"a@example.com"}, "password": {"wrong"}}, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("HTTP %d, want 429", rec.Code)
	}
}

// **로그인에 성공해도 IP 버킷은 그대로다.**
//
// 앞 판은 성공한 로그인 뒤 IP 버킷까지 지웠다. 그러면 자기 계정 하나만 있으면
// 몇 번 틀릴 때마다 한 번씩 로그인해 IP 제한을 계속 새로 시작할 수 있다 — IP
// 제한이 막으려는 것이 여러 계정에 걸친 비밀번호 대입인데(D15 4.3-2) 그것이
// 무력해진다. 오타 낸 사람을 풀어 주는 것은 계정 버킷을 지우는 것으로 충분하다.
func TestSuccessfulLoginDoesNotResetTheIPBucket(t *testing.T) {
	d, h, store := loginFixture(t)
	mkAccount(t, store, "me@example.com", "correct-horse-battery")
	d.limits.LoginPerIP = auth.Limit{Burst: 3, Window: 60_000_000_000}

	// 남의 계정 둘을 틀리고(2), 자기 계정으로 성공(3)
	postForm(h, "/login", url.Values{"email": {"v1@example.com"}, "password": {"guess"}}, nil)
	postForm(h, "/login", url.Values{"email": {"v2@example.com"}, "password": {"guess"}}, nil)
	if rec := postForm(h, "/login", url.Values{"email": {"me@example.com"},
		"password": {"correct-horse-battery"}}, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("자기 계정 로그인 = HTTP %d", rec.Code)
	}
	rec := postForm(h, "/login", url.Values{"email": {"v3@example.com"}, "password": {"guess"}}, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("IP 한도를 넘긴 네 번째 시도 = HTTP %d, want 429 — 성공한 로그인이 IP 버킷을 지웠다", rec.Code)
	}
}

// **회수가 커밋되기 전에 로그인한 세션도 회수 뒤에는 죽는다** (D15 5.4).
//
// A-402 의 회수는 트랜잭션이고 기준 시각은 그 시작 시각(now())이다. 커밋 전에는 다른 연결이 옛
// 해시를 읽으므로, 그 사이의 로그인은 성공한다. 로그인이 「지금」을 auth_at 으로 찍으면 그 값이
// 기준 시각보다 뒤라서 회수 뒤에도 살아남았다 (2026-10-21 5차 점검). 로그인은 해시와 같은 문장에서
// 읽은 기준 시각 자체를 찍는다 — 회수가 그 값을 옮기면 세션이 끊긴다.
func TestLoginDuringAnUncommittedRevokeDoesNotSurviveIt(t *testing.T) {
	_, h, store, _ := accountFixture(t, false)
	id := mkAccount(t, store, "race@example.com", "correct-horse-battery")
	pool, err := pgxpool.New(context.Background(), os.Getenv(dsnEnv))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// 회수를 시작하고 아직 커밋하지 않는다.
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(context.Background(),
		`UPDATE users SET password_hash = '', sessions_valid_from = now() WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	login := postForm(h, "/login", url.Values{"email": {"race@example.com"}, "password": {"correct-horse-battery"}}, nil)
	c := sessionCookie(login)
	if c == nil {
		t.Fatalf("회수가 커밋되기 전의 로그인 = HTTP %d — 이 시험의 전제(옛 해시가 보인다)가 깨졌다", login.Code)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Error("회수가 커밋된 뒤에도 그 전에 로그인한 세션이 살아 있다")
	}
}

func rememberedCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == rememberCookie {
			return c
		}
	}
	return nil
}

// **「아이디 저장」은 로그인 화면 전용 쿠키에 이메일만 둔다** (FR-218). 비밀번호·세션과 상관없고 인증에 쓰지
// 않는다 — 로그인 화면의 이메일 칸을 채울 뿐이다. HttpOnly(스크립트가 못 읽는다)·Path=/login(다른 주소로
// 가지 않는다)·SameSite=Lax·30일. 성공한 로그인만 남긴다 — 실패한 입력을 남기면 남이 친 주소가 그 브라우저의
// 다음 로그인 화면에 뜬다. 체크하지 않고 로그인하면 지운다.
func TestRememberEmailCookie(t *testing.T) {
	d, h, store := loginFixture(t)
	mkAccount(t, store, "a@example.com", "correct-horse-battery")

	if c := rememberedCookie(postForm(h, "/login", url.Values{"email": {"a@example.com"},
		"password": {"wrong-password"}, "remember_email": {"1"}}, nil)); c != nil {
		t.Errorf("실패한 로그인이 쿠키를 남겼다: %v", c)
	}
	rec := postForm(h, "/login", url.Values{"email": {"A@Example.com"},
		"password": {"correct-horse-battery"}, "remember_email": {"1"}}, nil)
	c := rememberedCookie(rec)
	if c == nil {
		t.Fatalf("아이디 저장 쿠키가 없다 (HTTP %d)", rec.Code)
	}
	if !c.HttpOnly || c.Path != "/login" || c.SameSite != http.SameSiteLaxMode || c.MaxAge != 30*24*60*60 || c.Secure {
		t.Errorf("쿠키 속성 %+v — HttpOnly·Path=/login·Lax·30일·(시험 설정은 Secure 끔)", c)
	}
	if got := rememberedEmail(c.Value); got != "a@example.com" {
		t.Errorf("쿠키의 이메일 = %q, want a@example.com (정규화된 값)", got)
	}

	// 로그인 화면이 칸을 채우고 체크를 켠다.
	var data map[string]any
	d.render = func(w http.ResponseWriter, _ *http.Request, _ string, code int, v any) {
		w.WriteHeader(code)
		data, _ = v.(map[string]any)
	}
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	req.AddCookie(c)
	d.sm.LoadAndSave(http.HandlerFunc(d.loginForm)).ServeHTTP(httptest.NewRecorder(), req)
	if data["Email"] != "a@example.com" || data["RememberEmail"] != true {
		t.Errorf("로그인 화면 = 이메일 %v · 체크 %v, want a@example.com · true", data["Email"], data["RememberEmail"])
	}

	// 체크하지 않고 로그인하면 지운다.
	rec = postForm(h, "/login", url.Values{"email": {"a@example.com"}, "password": {"correct-horse-battery"}},
		[]*http.Cookie{c})
	if gone := rememberedCookie(rec); gone == nil || gone.MaxAge >= 0 {
		t.Errorf("체크를 끄고 로그인했는데 쿠키를 지우지 않았다: %v", gone)
	}
}

// **손댄 쿠키로는 칸을 채우지 않는다** (FR-218). 값은 이메일 모양일 때만 쓴다 — 템플릿이 이스케이프하더라도
// 남이 심은 값(형제 호스트의 쿠키 등)으로 로그인 칸을 채우게 두지 않는다.
func TestRememberedEmailIgnoresAForgedCookie(t *testing.T) {
	for _, v := range []string{
		"", "not base64!!", rememberValue(`<script>alert(1)</script>`),
		rememberValue("no-at-sign"), rememberValue(strings.Repeat("a", 250) + "@example.com"),
	} {
		if got := rememberedEmail(v); got != "" {
			t.Errorf("rememberedEmail(%q) = %q, want 빈 값", v, got)
		}
	}
	if got := rememberedEmail(rememberValue(" A@Example.com ")); got != "a@example.com" {
		t.Errorf("정상 값 = %q, want a@example.com", got)
	}
}
