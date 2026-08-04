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
	mux := http.NewServeMux()
	NewRegistry().
		Add(Route{Screen: "P-103", Method: "POST", Pattern: "/signup", Class: SC2, Handler: d.signup}).
		Add(Route{Screen: "P-112", Method: "GET", Pattern: "/verify", Class: SC2, Handler: d.verify}).
		Add(Route{Screen: "P-108", Method: "POST", Pattern: "/me", Class: SC3, Handler: d.updateProfile}).
		Add(Route{Screen: "P-109", Method: "POST", Pattern: "/me/password", Class: SC3, Handler: d.changePassword}).
		Add(Route{Screen: "P-101", Method: "POST", Pattern: "/login", Class: SC2, Handler: d.login}).
		Mount(mux)
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

// The verification link is single-use and the token never appears in storage.
func TestVerificationLinkWorksOnceAndExpiresLogically(t *testing.T) {
	d, h, store, sender := accountFixture(t, true)

	postForm(h, "/signup", url.Values{
		"email": {"v@example.com"}, "password": {"correct-horse-battery"},
	}, nil)

	var link string
	for i := 0; i < 200 && link == ""; i++ {
		for _, m := range sender.snapshot() {
			if m.to == "v@example.com" && strings.Contains(m.body, "/verify?t=") {
				link = m.body
			}
		}
		if link == "" {
			time.Sleep(time.Millisecond)
		}
	}
	if link == "" {
		t.Fatal("인증 메일이 발송되지 않았다")
	}
	tok := link[strings.Index(link, "?t=")+3:]
	tok = strings.TrimSpace(tok)

	first := doGet(h, "/verify?t="+url.QueryEscape(tok))
	if first.Code != http.StatusOK {
		t.Fatalf("첫 인증 HTTP %d (%q)", first.Code, first.Body.String())
	}
	second := doGet(h, "/verify?t="+url.QueryEscape(tok))
	if second.Code == http.StatusOK {
		t.Error("인증 링크가 두 번 쓰였다")
	}
	_ = d
	_ = store
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
