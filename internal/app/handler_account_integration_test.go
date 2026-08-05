package app

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emirue/ondolith/internal/auth"
)

type capturedMail struct{ to, subject, body string }

// The mailer sends from a goroutine, so the recorder is shared across
// goroutines and needs a lock. Without it `-race` fails the suite — correctly:
// the test, not the production code, was the racy part.
type recordingSender struct {
	mu   sync.Mutex
	sent []capturedMail
}

func (s *recordingSender) Send(_ context.Context, to, subject, body string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, capturedMail{to, subject, body})
	return nil
}

// snapshot copies under the lock so callers iterate their own slice.
func (s *recordingSender) snapshot() []capturedMail {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]capturedMail(nil), s.sent...)
}

func accountFixture(t *testing.T, verifyRequired bool) (*accountDeps, http.Handler, *auth.Store, *recordingSender) {
	t.Helper()
	store, _, sm := authFixture(t)
	sender := &recordingSender{}
	m := auth.NewMailer(sender, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	d := &accountDeps{
		loginDeps: loginDeps{
			sm: sm, store: store,
			limiter: auth.NewLimiter(), limits: auth.DefaultLimits(),
			render: func(w http.ResponseWriter, _ *http.Request, name string, code int, data any) {
				w.WriteHeader(code)
				_, _ = w.Write([]byte(name))
				if mm, ok := data.(map[string]any); ok {
					if e, ok := mm["Error"].(string); ok {
						_, _ = w.Write([]byte("|" + e))
					}
				}
			},
		},
		mailer:         m,
		verifyRequired: func() bool { return verifyRequired },
		baseURL:        "https://example.com",
	}
	// The production tree, not a hand-written one.
	//
	// This fixture used to list its own patterns, and one of them — /verify
	// against the tree's /verify/{token} — did not exist in production. Every
	// test here passed while every verification mail in production 404ed
	// (.ai/MISTAKES.md M14). The other dependencies are nil because buildTree
	// only takes method values; the routes they own are never requested from
	// this fixture.
	// The static handler must not be nil — ServeMux.HandleFunc panics on one,
	// and the panic made every route in the tree unreachable.
	mux := http.NewServeMux()
	buildTree(nil, &d.loginDeps, d, nil, nil, nil, false,
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }).Mount(mux)
	return d, sm.LoadAndSave(withActor(sm, store)(mux)), store, sender
}

// FR-210: the response must not reveal that an address already has an account.
// "이미 가입된 이메일입니다" is a membership oracle — anyone can test a list.
func TestSignupDoesNotRevealExistingAccount(t *testing.T) {
	_, h, store, _ := accountFixture(t, true)
	mkAccount(t, store, "taken@example.com", "correct-horse-battery")

	fresh := postForm(h, "/signup", url.Values{
		"email": {"new@example.com"}, "password": {"correct-horse-battery"}, "display_name": {"새 사람"},
	}, nil)
	dup := postForm(h, "/signup", url.Values{
		"email": {"taken@example.com"}, "password": {"correct-horse-battery"}, "display_name": {"침입자"},
	}, nil)

	if fresh.Code != dup.Code {
		t.Errorf("신규 %d, 중복 %d — 상태코드로 계정 존재가 새어나간다", fresh.Code, dup.Code)
	}
	if fresh.Body.String() != dup.Body.String() {
		t.Errorf("신규 %q, 중복 %q — 본문으로 계정 존재가 새어나간다",
			fresh.Body.String(), dup.Body.String())
	}
}

// The existing account is told by mail that somebody tried — the one place the
// information belongs, because only its owner reads it.
func TestSignupOnExistingAccountNotifiesTheOwner(t *testing.T) {
	_, h, store, sender := accountFixture(t, true)
	mkAccount(t, store, "taken@example.com", "correct-horse-battery")

	postForm(h, "/signup", url.Values{
		"email": {"taken@example.com"}, "password": {"correct-horse-battery"},
	}, nil)

	// The mailer is async; drain by polling the recorder a bounded number of
	// times rather than sleeping a fixed amount.
	found := false
	for i := 0; i < 200 && !found; i++ {
		for _, m := range sender.snapshot() {
			if m.to == "taken@example.com" && strings.Contains(m.subject, "가입 시도") {
				found = true
			}
		}
		if !found {
			time.Sleep(time.Millisecond)
		}
	}
	if !found {
		t.Error("기존 계정 소유자에게 알림이 가지 않았다")
	}
}

// FR-214 off: the account is usable at once, and the visitor is logged in —
// which is what "sign up" means to the person who clicked it.
func TestSignupWithoutVerificationLogsInImmediately(t *testing.T) {
	_, h, _, _ := accountFixture(t, false)

	rec := postForm(h, "/signup", url.Values{
		"email": {"a@example.com"}, "password": {"correct-horse-battery"}, "display_name": {"홍길동"},
	}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("HTTP %d, want 303 (본문 %q)", rec.Code, rec.Body.String())
	}
	if sessionCookie(rec) == nil {
		t.Error("인증이 꺼져 있는데 로그인되지 않았다")
	}
}

// mailedPath waits for a message to `to` and returns the path of the first URL
// in it, taken from the body verbatim.
//
// Verbatim is the point. The earlier version of this test built the request
// itself ("/verify?t="+token), so it exercised a URL the mail never sent and
// the tree never served (.ai/MISTAKES.md M14). Whatever the mail says is what
// the person clicks.
func mailedPath(t *testing.T, sender *recordingSender, to, prefix string) string {
	t.Helper()
	for i := 0; i < 500; i++ {
		for _, m := range sender.snapshot() {
			if m.to != to {
				continue
			}
			for _, f := range strings.Fields(m.body) {
				if !strings.HasPrefix(f, prefix) {
					continue
				}
				// url.Parse, not TrimPrefix: trimming the prefix would eat the
				// last path segment along with it and hand back "/{token}",
				// which the mux then routes to the catch-all page handler.
				u, err := url.Parse(f)
				if err != nil {
					t.Fatalf("메일의 링크를 해석하지 못했다: %q", f)
				}
				return u.Path
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("%s 로 %s 링크가 담긴 메일이 오지 않았다", to, prefix)
	return ""
}

// The link in the verification mail is one the route table actually serves.
func TestVerificationLinkFromMailResolves(t *testing.T) {
	_, h, store, sender := accountFixture(t, true)

	postForm(h, "/signup", url.Values{
		"email": {"v@example.com"}, "password": {"correct-horse-battery"},
	}, nil)
	path := mailedPath(t, sender, "v@example.com", "https://example.com/verify/")

	first := doGet(h, path)
	if first.Code != http.StatusOK {
		t.Fatalf("메일이 보낸 %s 가 HTTP %d (%q)", path, first.Code, first.Body.String())
	}
	u, _, err := store.FindActiveUserByEmail(t.Context(), "v@example.com")
	if err != nil || u == nil {
		t.Fatalf("가입한 계정을 읽지 못했다: %v", err)
	}
	if u.EmailVerifiedAt == nil {
		t.Fatal("200 을 받았는데 email_verified_at 이 비어 있다 — 화면만 성공했다")
	}

	// D19 P-112: a second visit succeeds quietly. Mail clients prefetch, so
	// the token is usually spent before the human clicks.
	second := doGet(h, path)
	if second.Code != http.StatusOK {
		t.Errorf("재방문 HTTP %d — 프리페치가 사용자에게 실패로 보인다", second.Code)
	}
	if strings.Contains(second.Body.String(), "|") {
		t.Errorf("재방문에 오류 문구가 붙었다: %q", second.Body.String())
	}
}

func doGet(h http.Handler, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "192.0.2.5:1111"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// D19 P-108: the form has three fields and none of them is `role`. The defence
// is structural — there is nowhere for the value to land.
func TestProfileFormIgnoresPrivilegeFields(t *testing.T) {
	_, h, store, _ := accountFixture(t, false)
	mkAccount(t, store, "a@example.com", "correct-horse-battery")

	in := postForm(h, "/login", url.Values{
		"email": {"a@example.com"}, "password": {"correct-horse-battery"},
	}, nil)
	c := sessionCookie(in)
	if c == nil {
		t.Fatal("로그인 실패")
	}

	postForm(h, "/me", url.Values{
		"display_name": {"새 이름"},
		"is_admin":     {"true"},
		"role":         {"admin"},
		"is_active":    {"true"},
	}, []*http.Cookie{c})

	ctx := context.Background()
	u, _, err := store.FindActiveUserByEmail(ctx, "a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if u.DisplayName != "새 이름" {
		t.Errorf("표시 이름이 저장되지 않았다: %q", u.DisplayName)
	}
	p, err := store.LoadPermissions(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Superuser {
		t.Error("폼으로 superuser 가 됐다")
	}
	if p.Can("admin.access") {
		t.Error("폼으로 관리자 권한이 붙었다")
	}
}

// D15 5.4: changing the password ends every OTHER session while keeping the one
// that just proved it knows the password.
func TestPasswordChangeEndsOtherSessions(t *testing.T) {
	_, h, store, _ := accountFixture(t, false)
	mkAccount(t, store, "a@example.com", "correct-horse-battery")

	// Two sessions for the same account.
	s1 := sessionCookie(postForm(h, "/login", url.Values{
		"email": {"a@example.com"}, "password": {"correct-horse-battery"}}, nil))
	s2 := sessionCookie(postForm(h, "/login", url.Values{
		"email": {"a@example.com"}, "password": {"correct-horse-battery"}}, nil))
	if s1 == nil || s2 == nil {
		t.Fatal("세션 두 개를 만들지 못했다")
	}

	rec := postForm(h, "/me/password", url.Values{
		"current_password": {"correct-horse-battery"},
		"new_password":     {"a-completely-new-password"},
	}, []*http.Cookie{s2})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("비밀번호 변경 HTTP %d (%q)", rec.Code, rec.Body.String())
	}

	// The other session must be refused on its very next request.
	//
	// Both outcomes are 303, so the code alone cannot tell them apart: success
	// redirects to /me, a dead session redirects to /login. Asserting on the
	// status would pass whichever happened.
	probe := postForm(h, "/me", url.Values{"display_name": {"x"}}, []*http.Cookie{s1})
	if loc := probe.Header().Get("Location"); loc != loginPath {
		t.Errorf("다른 세션이 비밀번호 변경 후에도 살아 있다: Location %q (D15 5.4)", loc)
	}

	// ...and the session that changed the password keeps working — using the
	// cookie the response handed back. RenewToken issues a new token, so the
	// pre-change cookie is dead by design; a browser would already have
	// replaced it.
	fresh := sessionCookie(rec)
	if fresh == nil {
		t.Fatal("비밀번호 변경 응답에 새 세션 쿠키가 없다")
	}
	keep := postForm(h, "/me", url.Values{"display_name": {"유지"}}, []*http.Cookie{fresh})
	if loc := keep.Header().Get("Location"); loc != "/me" {
		t.Errorf("비밀번호를 바꾼 세션이 끊겼다: Location %q", loc)
	}
}

// Being logged in is not enough: a session left open on a shared machine must
// not be enough to take the account.
func TestPasswordChangeRequiresCurrentPassword(t *testing.T) {
	_, h, store, _ := accountFixture(t, false)
	mkAccount(t, store, "a@example.com", "correct-horse-battery")
	c := sessionCookie(postForm(h, "/login", url.Values{
		"email": {"a@example.com"}, "password": {"correct-horse-battery"}}, nil))

	rec := postForm(h, "/me/password", url.Values{
		"current_password": {"wrong-password-xx"},
		"new_password":     {"a-completely-new-password"},
	}, []*http.Cookie{c})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("HTTP %d, want 400", rec.Code)
	}
}
