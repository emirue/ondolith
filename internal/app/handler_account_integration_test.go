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

// waitMail polls until some recorded mail satisfies want, and returns it.
//
// **한 번 찍어 보고 판단하면 안 된다.** 발송은 고루틴이므로 요청이 끝난 직후의
// 스냅숏에는 아직 아무것도 없을 수 있다 — 한가한 기계에서는 통과하고 바쁜
// 기계에서는 실패하는, 이유를 알 수 없는 실패가 된다. 고정 시간 sleep 도 같은
// 문제를 옮기기만 한다: 짧으면 깜빡이고 길면 매 테스트가 느려진다.
//
// 없으면 (capturedMail{}, false) 다. 「오지 않아야 한다」를 확인하는 쪽은 이
// 함수를 쓰지 않는다 — 그 경우 기다림은 실패를 늦출 뿐이다.
func waitMail(t *testing.T, s *recordingSender, want func(capturedMail) bool) (capturedMail, bool) {
	t.Helper()
	for range 500 {
		for _, m := range s.snapshot() {
			if want(m) {
				return m, true
			}
		}
		time.Sleep(time.Millisecond)
	}
	return capturedMail{}, false
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
					// 접수 화면의 문구를 가르는 값. 본문 비교(FR-210)에 함께 걸린다.
					if nv, ok := mm["NoVerify"].(bool); ok && nv {
						_, _ = w.Write([]byte("|noverify"))
					}
					if nm, ok := mm["NoMail"].(bool); ok && nm {
						_, _ = w.Write([]byte("|nomail"))
					}
				}
			},
		},
		mailer:         m,
		verifyRequired: func() bool { return verifyRequired },
		signupClosed:   func() bool { return false },
		baseURL:        "https://example.com",
	}
	// 운영(app.go)과 같이 로그인 문에 미인증 판정을 단다 (FR-214).
	d.loginDeps.unverified = d.refuseUnverified
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
	buildTree(nil, &d.loginDeps, d, nil, nil, nil, nil, false,
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

	if _, ok := waitMail(t, sender, func(m capturedMail) bool {
		return m.to == "taken@example.com" && strings.Contains(m.subject, "가입 시도")
	}); !ok {
		t.Error("기존 계정 소유자에게 알림이 가지 않았다")
	}
}

// FR-210 은 인증을 꺼도(FR-214 off) 그대로다.
//
// 예전에는 인증이 꺼져 있으면 새 가입만 곧바로 로그인시켰다(303 → 홈). 중복
// 주소는 로그인시킬 수 없으니 「확인 메일을 보냈습니다」(200)를 받았고, 그 차이로
// 아무 주소나 가입 여부를 알아낼 수 있었다. 쿠키 하나의 유무도 같은 신호다.
func TestSignupWithoutVerificationDoesNotRevealExistingAccount(t *testing.T) {
	_, h, store, _ := accountFixture(t, false)
	mkAccount(t, store, "taken@example.com", "correct-horse-battery")

	fresh := postForm(h, "/signup", url.Values{
		"email": {"new@example.com"}, "password": {"correct-horse-battery"}, "display_name": {"새 사람"},
	}, nil)
	dup := postForm(h, "/signup", url.Values{
		"email": {"taken@example.com"}, "password": {"correct-horse-battery"}, "display_name": {"침입자"},
	}, nil)

	if fresh.Code != dup.Code || fresh.Body.String() != dup.Body.String() {
		t.Errorf("신규 %d %q, 중복 %d %q — 응답으로 계정 존재가 새어나간다",
			fresh.Code, fresh.Body.String(), dup.Code, dup.Body.String())
	}
	if (sessionCookie(fresh) == nil) != (sessionCookie(dup) == nil) {
		t.Error("신규 가입에만 세션 쿠키가 붙는다 — 쿠키로 계정 존재가 새어나간다")
	}
	if !strings.Contains(fresh.Body.String(), "noverify") {
		t.Error("인증이 꺼졌다는 것을 접수 화면에 넘기지 않는다 — 보내지 않은 「확인 메일」을 말한다")
	}
	// 가입은 된 것이다: 그 계정으로 바로 로그인할 수 있어야 한다.
	if c := sessionCookie(postForm(h, "/login", url.Values{
		"email": {"new@example.com"}, "password": {"correct-horse-battery"},
	}, nil)); c == nil {
		t.Error("인증이 꺼져 있는데 새 계정으로 로그인되지 않는다")
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

// **A-201 에서 가입을 닫으면 운영 사이트가 따른다** (FR-217) — 머리글의 가입 링크가 사라지고,
// 폼과 제출이 403 이고, 계정이 생기지 않는다. 설정 하나를 P-103 과 테마가 같이 읽는 배선 시험이다.
func TestClosedSignupOnTheLiveSite(t *testing.T) {
	srv, pool := liveSite(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO settings (key, value) VALUES ('auth.signup_closed', '1')`); err != nil {
		t.Fatal(err)
	}
	anon := client()
	// 로그인 링크가 있어야 「머리글이 그려졌는데 가입 링크만 없다」이다 — 오류 화면도 가입 링크는 없다.
	if code, body := mustGet(t, anon, srv.URL+"/"); code != http.StatusOK || !strings.Contains(body, `href="/login"`) ||
		strings.Contains(body, `href="/signup"`) {
		t.Errorf("홈 = HTTP %d, 로그인 링크 %v, 가입 링크 %v — want 200, 있음, 없음", code,
			strings.Contains(body, `href="/login"`), strings.Contains(body, `href="/signup"`))
	}
	if code, body := mustGet(t, anon, srv.URL+"/signup"); code != http.StatusForbidden ||
		!strings.Contains(body, "회원가입을 받지 않습니다") {
		t.Errorf("GET /signup = HTTP %d, want 403 과 이유", code)
	}
	resp := httpPost(t, anon, srv.URL+"/signup", url.Values{
		"email": {"new@example.com"}, "password": {"correct-horse-battery"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST /signup = HTTP %d, want 403", resp.StatusCode)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE email = 'new@example.com'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("닫힌 가입으로 계정이 %d개 생겼다", n)
	}
}

// **인증을 켠 사이트에서 미인증 계정은 로그인되지 않는다** (FR-214).
//
// 인증 전 계정은 글·주문을 못 하는데 로그인은 됐다 — 「인증」이 반쪽이었다. 비밀번호가
// 맞으면 세션 대신 인증 메일을 다시 보낸다: 재발송(P-113)이 로그인을 전제로 했으므로
// 로그인을 막으면 그것이 재발송의 자리다. 인증을 끈 사이트는 그대로 로그인된다.
func TestUnverifiedAccountCannotLogInWhenVerificationIsRequired(t *testing.T) {
	_, h, store, sender := accountFixture(t, true)
	id := mkAccount(t, store, "u@example.com", "correct-horse-battery")
	login := url.Values{"email": {"u@example.com"}, "password": {"correct-horse-battery"}}

	rec := postForm(h, "/login", login, nil)
	if rec.Code != http.StatusForbidden || sessionCookie(rec) != nil {
		t.Fatalf("미인증 로그인 = HTTP %d·세션 %v, want 403·없음", rec.Code, sessionCookie(rec) != nil)
	}
	if !strings.Contains(rec.Body.String(), "인증") {
		t.Errorf("왜 막혔는지 말하지 않는다: %q", rec.Body.String())
	}
	if p := mailedPath(t, sender, "u@example.com", "https://example.com/verify/"); p == "" {
		t.Error("막으면서 인증 메일을 다시 보내지 않았다 — 인증할 길이 없다")
	}

	if err := store.MarkEmailVerified(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if c := sessionCookie(postForm(h, "/login", login, nil)); c == nil {
		t.Error("인증한 뒤에도 로그인되지 않는다 — 위 단언이 「늘 거부」였다")
	}

	// 인증을 끈 사이트(FR-214 off, 사내망)는 인증 없이 로그인된다.
	_, hOff, storeOff, _ := accountFixture(t, false)
	mkAccount(t, storeOff, "off@example.com", "correct-horse-battery")
	if c := sessionCookie(postForm(hOff, "/login", url.Values{
		"email": {"off@example.com"}, "password": {"correct-horse-battery"}}, nil)); c == nil {
		t.Error("인증을 끈 사이트에서 미인증 계정이 로그인되지 않는다")
	}
}

// 비밀번호 확인 칸이 있으면 서버도 대조한다 — 오타 난 비밀번호로 계정이 생기면 그 계정은
// 재설정 메일로만 열린다. 칸을 싣지 않는 요청(옛 폼·스크립트)은 예전과 같다.
func TestSignupChecksThePasswordConfirmation(t *testing.T) {
	_, h, store, _ := accountFixture(t, false)
	rec := postForm(h, "/signup", url.Values{"email": {"typo@example.com"},
		"password": {"correct horse battery"}, "password_confirm": {"correct horse batterz"}}, nil)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "서로 다릅니다") {
		t.Errorf("확인 불일치 = HTTP %d %q, want 422", rec.Code, rec.Body.String())
	}
	if _, _, err := store.FindActiveUserByEmail(context.Background(), "typo@example.com"); err == nil {
		t.Error("확인이 다른데 계정이 생겼다")
	}
	rec = postForm(h, "/signup", url.Values{"email": {"same@example.com"},
		"password": {"correct horse battery"}, "password_confirm": {"correct horse battery"}}, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("확인 일치 = HTTP %d, want 200", rec.Code)
	}
}

// 회원 화면의 탭 제목이 운영 트리에서 실제로 그려진다 — publicTitles 표만 있고 renderNamed 가
// 읽지 않으면 TestAccountScreensHaveTabTitles 는 초록이다.
func TestAccountTabTitleOnTheLiveSite(t *testing.T) {
	srv, pool := liveSite(t)
	if code, body := mustGet(t, client(), srv.URL+"/login"); code != http.StatusOK || !strings.Contains(body, "<title>로그인 · ") {
		t.Errorf("GET /login = HTTP %d, 탭 제목에 「로그인」이 없다", code)
	}
	// 같은 배선으로 내 정보를 본다: 탭 제목, 그리고 소셜 로그인이 꺼진 사이트에는 「소셜 계정 연결」이 없다.
	mkUser(t, pool, "m@example.com")
	member := client()
	login(t, srv.URL, "m@example.com", member)
	code, body := mustGet(t, member, srv.URL+"/me")
	if code != http.StatusOK || !strings.Contains(body, "<title>내 정보 · ") {
		t.Errorf("GET /me = HTTP %d, 탭 제목에 「내 정보」가 없다", code)
	}
	if strings.Contains(body, `href="/me/connections"`) {
		t.Error("소셜 로그인이 꺼진 사이트의 내 정보에 「소셜 계정 연결」이 있다")
	}
}
