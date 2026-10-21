package app

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/emirue/ondolith/internal/auth"
)

const resetPrefix = "https://example.com/password/reset/"

// openResetLink 는 메일의 링크를 사람이 누른 대로 연다. 토큰이 든 주소는 화면을
// 그리지 않고 토큰 없는 폼 주소로 보낸다 — 토큰은 그 폼 주소에만 실리는 HttpOnly
// 쿠키로 옮겨진다. 그 쿠키를 돌려준다.
func openResetLink(t *testing.T, h http.Handler, path string) []*http.Cookie {
	t.Helper()
	rec := doGet(h, path)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != newPasswordPath {
		t.Fatalf("%s → HTTP %d Location %q, want 303 %s (%q)",
			path, rec.Code, rec.Header().Get("Location"), newPasswordPath, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name != resetCookie {
			continue
		}
		if !c.HttpOnly || c.Path != newPasswordPath {
			t.Fatalf("재설정 쿠키 HttpOnly=%v Path=%q — 스크립트가 읽거나 다른 주소에 실린다", c.HttpOnly, c.Path)
		}
		return []*http.Cookie{c}
	}
	t.Fatal("재설정 쿠키가 없다 — 토큰을 옮길 곳이 없다")
	return nil
}

// **재설정 링크는 토큰이 든 주소에서 화면을 그리지 않는다** (D60 「비밀이 든 주소」).
//
// 화면에 붙은 스크립트는 주소를 통째로 밖으로 보낸다 — GA4 의 page_location 이
// 그렇다. `Referrer-Policy` 는 Referer 만 막는다. A-201 에서 GA4 ID 를 바꿀 수 있는
// 운영자는 그렇게 최고 관리자의 재설정 토큰을 받아 볼 수 있었다 (2026-10-21 점검).
func TestResetLinkLeavesTheTokenURLBeforeRendering(t *testing.T) {
	_, h, store, sender := accountFixture(t, true)
	mkAccount(t, store, "leak@example.com", "correct-horse-battery")
	postForm(h, "/password/reset", url.Values{"email": {"leak@example.com"}}, nil)
	path := mailedPath(t, sender, "leak@example.com", resetPrefix)

	c := openResetLink(t, h, path)

	// 폼도 실패한 제출도 토큰 없는 주소에서 그려진다.
	req := httptest.NewRequest(http.MethodGet, newPasswordPath, nil)
	req.AddCookie(c[0])
	form := httptest.NewRecorder()
	h.ServeHTTP(form, req)
	if form.Code != http.StatusOK {
		t.Fatalf("폼 HTTP %d (%q)", form.Code, form.Body.String())
	}
	if rec := postForm(h, newPasswordPath, url.Values{
		"password": {"long-enough-passphrase"}, "password_confirm": {"different-passphrase"},
	}, c); rec.Code != http.StatusBadRequest {
		t.Fatalf("불일치 HTTP %d, want 400", rec.Code)
	}
	if rec := postForm(h, newPasswordPath, url.Values{
		"password": {"brand-new-passphrase"}, "password_confirm": {"brand-new-passphrase"},
	}, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("재설정 HTTP %d (%q)", rec.Code, rec.Body.String())
	}
	if _, err := store.Authenticate(t.Context(), "leak@example.com", "brand-new-passphrase"); err != nil {
		t.Errorf("새 비밀번호로 로그인되지 않는다: %v", err)
	}

	// 링크 없이 폼만 제출하면 토큰이 없다 — 같은 한 문장의 400.
	if rec := postForm(h, newPasswordPath, url.Values{
		"password": {"no-token-passphrase"}, "password_confirm": {"no-token-passphrase"},
	}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("토큰 없는 제출 HTTP %d, want 400", rec.Code)
	}
}

// FR-207: the answer must be the same whether or not the address has an
// account. Anything that differs — status, body, redirect — turns the form
// into a membership oracle for a list of addresses.
func TestResetRequestDoesNotRevealWhetherAccountExists(t *testing.T) {
	_, h, store, _ := accountFixture(t, true)
	mkAccount(t, store, "known@example.com", "correct-horse-battery")

	known := postForm(h, "/password/reset", url.Values{"email": {"known@example.com"}}, nil)
	unknown := postForm(h, "/password/reset", url.Values{"email": {"nobody@example.com"}}, nil)

	if known.Code != unknown.Code {
		t.Errorf("가입 %d, 미가입 %d — 상태코드로 계정 존재가 샌다", known.Code, unknown.Code)
	}
	if known.Body.String() != unknown.Body.String() {
		t.Errorf("가입 %q, 미가입 %q — 본문으로 계정 존재가 샌다",
			known.Body.String(), unknown.Body.String())
	}
}

// A disabled account takes the silent path too: "이 계정은 정지되었습니다" is
// the same oracle wearing a different sentence.
func TestResetRequestSaysNothingAboutDisabledAccounts(t *testing.T) {
	_, h, store, sender := accountFixture(t, true)
	id := mkAccount(t, store, "off@example.com", "correct-horse-battery")
	if err := store.SetActive(t.Context(), id, false); err != nil {
		t.Fatal(err)
	}

	rec := postForm(h, "/password/reset", url.Values{"email": {"off@example.com"}}, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("HTTP %d (%q)", rec.Code, rec.Body.String())
	}
	for _, m := range sender.snapshot() {
		if m.to == "off@example.com" {
			t.Error("비활성 계정에 재설정 링크가 발송됐다")
		}
	}
}

// D15 4.3-2: three per IP per hour.
func TestResetRequestIsRateLimitedPerIP(t *testing.T) {
	_, h, _, _ := accountFixture(t, true)

	for i := 0; i < 3; i++ {
		if rec := postForm(h, "/password/reset", url.Values{"email": {"a@example.com"}}, nil); rec.Code != http.StatusOK {
			t.Fatalf("%d번째 요청이 HTTP %d", i+1, rec.Code)
		}
	}
	rec := postForm(h, "/password/reset", url.Values{"email": {"a@example.com"}}, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("4번째 요청이 HTTP %d, want 429 — IP당 3회/시간이 걸리지 않았다", rec.Code)
	}
}

// The whole flow, over the link the mail actually sent.
func TestResetLinkFromMailSetsThePassword(t *testing.T) {
	_, h, store, sender := accountFixture(t, true)
	mkAccount(t, store, "r@example.com", "correct-horse-battery")

	postForm(h, "/password/reset", url.Values{"email": {"r@example.com"}}, nil)
	path := mailedPath(t, sender, "r@example.com", resetPrefix)

	rec := postForm(h, newPasswordPath, url.Values{
		"password": {"brand-new-passphrase"}, "password_confirm": {"brand-new-passphrase"},
	}, openResetLink(t, h, path))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("재설정 HTTP %d (%q)", rec.Code, rec.Body.String())
	}

	if _, err := store.Authenticate(t.Context(), "r@example.com", "brand-new-passphrase"); err != nil {
		t.Errorf("새 비밀번호로 로그인되지 않는다: %v", err)
	}
	if _, err := store.Authenticate(t.Context(), "r@example.com", "correct-horse-battery"); err == nil {
		t.Error("옛 비밀번호가 아직 통한다")
	}

	// Single-use: the same link a second time cannot set another password.
	again := postForm(h, newPasswordPath, url.Values{
		"password": {"third-passphrase-here"}, "password_confirm": {"third-passphrase-here"},
	}, openResetLink(t, h, path))
	if again.Code != http.StatusBadRequest {
		t.Errorf("두 번째 사용이 HTTP %d, want 400", again.Code)
	}
	if _, err := store.Authenticate(t.Context(), "r@example.com", "third-passphrase-here"); err == nil {
		t.Error("소모된 링크로 비밀번호가 또 바뀌었다")
	}
}

// D19 P-105's most dangerous field. The account is named by the token; a form
// field that could name another one is a complete account takeover.
func TestResetIgnoresEmailInTheForm(t *testing.T) {
	_, h, store, sender := accountFixture(t, true)
	mkAccount(t, store, "mine@example.com", "correct-horse-battery")
	mkAccount(t, store, "victim@example.com", "victim-original-secret")

	postForm(h, "/password/reset", url.Values{"email": {"mine@example.com"}}, nil)
	path := mailedPath(t, sender, "mine@example.com", resetPrefix)

	postForm(h, newPasswordPath, url.Values{
		"password": {"attacker-chosen-value"}, "password_confirm": {"attacker-chosen-value"},
		"email": {"victim@example.com"}, "user_id": {"victim@example.com"},
	}, openResetLink(t, h, path))

	if _, err := store.Authenticate(t.Context(), "victim@example.com", "attacker-chosen-value"); err == nil {
		t.Fatal("폼의 email 로 남의 비밀번호가 바뀌었다")
	}
	if _, err := store.Authenticate(t.Context(), "victim@example.com", "victim-original-secret"); err != nil {
		t.Errorf("피해 계정의 비밀번호가 손상됐다: %v", err)
	}
	if _, err := store.Authenticate(t.Context(), "mine@example.com", "attacker-chosen-value"); err != nil {
		t.Errorf("토큰 주인의 비밀번호가 바뀌지 않았다: %v", err)
	}
}

// D19 P-104: one live link at a time. Three impatient clicks otherwise leave
// three account takeovers alive for thirty minutes each.
func TestNewResetRequestKillsTheOlderLink(t *testing.T) {
	_, h, store, sender := accountFixture(t, true)
	mkAccount(t, store, "twice@example.com", "correct-horse-battery")

	postForm(h, "/password/reset", url.Values{"email": {"twice@example.com"}}, nil)
	first := mailedPath(t, sender, "twice@example.com", resetPrefix)
	postForm(h, "/password/reset", url.Values{"email": {"twice@example.com"}}, nil)

	var second string
	for i := 0; i < 500 && second == ""; i++ {
		for _, m := range sender.snapshot() {
			for _, f := range strings.Fields(m.body) {
				if !strings.HasPrefix(f, resetPrefix) {
					continue
				}
				if u, err := url.Parse(f); err == nil && u.Path != first {
					second = u.Path
				}
			}
		}
		if second == "" {
			time.Sleep(time.Millisecond)
		}
	}
	if second == "" {
		t.Fatal("두 번째 재설정 메일이 오지 않았다")
	}

	rec := postForm(h, newPasswordPath, url.Values{
		"password": {"old-link-passphrase"}, "password_confirm": {"old-link-passphrase"},
	}, openResetLink(t, h, first))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("옛 링크가 HTTP %d, want 400 — 무효화되지 않았다", rec.Code)
	}
	if _, err := store.Authenticate(t.Context(), "twice@example.com", "old-link-passphrase"); err == nil {
		t.Error("옛 링크로 비밀번호가 바뀌었다")
	}

	rec = postForm(h, newPasswordPath, url.Values{
		"password": {"new-link-passphrase"}, "password_confirm": {"new-link-passphrase"},
	}, openResetLink(t, h, second))
	if rec.Code != http.StatusSeeOther {
		t.Errorf("새 링크가 HTTP %d — 무효화가 새 토큰까지 삼켰다", rec.Code)
	}
}

// D15 5.4: a reset is what somebody does when they think the account is taken,
// so the sessions that existed before it must stop working.
func TestResetMovesTheSessionCutoff(t *testing.T) {
	_, h, store, sender := accountFixture(t, true)
	id := mkAccount(t, store, "sess@example.com", "correct-horse-battery")
	before, _, err := store.FindActiveUserByEmail(t.Context(), "sess@example.com")
	if err != nil {
		t.Fatal(err)
	}

	postForm(h, "/password/reset", url.Values{"email": {"sess@example.com"}}, nil)
	path := mailedPath(t, sender, "sess@example.com", resetPrefix)
	postForm(h, newPasswordPath, url.Values{
		"password": {"a-fresh-passphrase"}, "password_confirm": {"a-fresh-passphrase"},
	}, openResetLink(t, h, path))

	after, err := store.FindUserByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !after.SessionsValidFrom.After(before.SessionsValidFrom) {
		t.Errorf("sessions_valid_from 이 %v 에서 움직이지 않았다 (%v)",
			before.SessionsValidFrom, after.SessionsValidFrom)
	}
}

// Confirmation mismatch and a short password keep the token in the session —
// otherwise a typo costs the user their only link.
func TestResetFormKeepsTheTokenAfterAFieldError(t *testing.T) {
	_, h, store, sender := accountFixture(t, true)
	mkAccount(t, store, "typo@example.com", "correct-horse-battery")
	postForm(h, "/password/reset", url.Values{"email": {"typo@example.com"}}, nil)
	c := openResetLink(t, h, mailedPath(t, sender, "typo@example.com", resetPrefix))

	for _, bad := range []url.Values{
		{"password": {"long-enough-passphrase"}, "password_confirm": {"different-passphrase"}},
		{"password": {"short"}, "password_confirm": {"short"}},
	} {
		if rec := postForm(h, newPasswordPath, bad, c); rec.Code != http.StatusBadRequest {
			t.Errorf("%v → HTTP %d, want 400", bad, rec.Code)
		}
	}
	// The link still works afterwards.
	rec := postForm(h, newPasswordPath, url.Values{
		"password": {"finally-a-good-one"}, "password_confirm": {"finally-a-good-one"},
	}, c)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("오타 뒤 재시도가 HTTP %d — 실패가 토큰을 태웠다", rec.Code)
	}
}

// An inactive account gets the same 400 as a wrong token, and — because the
// consume and the update are one transaction — the token is not spent.
func TestResetOnDisabledAccountRollsBack(t *testing.T) {
	_, _, store, _ := accountFixture(t, true)
	id := mkAccount(t, store, "later@example.com", "correct-horse-battery")
	raw, err := store.IssueResetToken(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetActive(t.Context(), id, false); err != nil {
		t.Fatal(err)
	}

	if _, err := store.ResetPassword(t.Context(), raw, "$2a$10$notarealhashbutlongenoughvalue0000000000000000000000"); !errors.Is(err, auth.ErrTokenInvalid) {
		t.Fatalf("비활성 계정 = %v, want ErrTokenInvalid", err)
	}
	if err := store.SetActive(t.Context(), id, true); err != nil {
		t.Fatal(err)
	}
	// Rolled back, so the link survives the account being re-enabled.
	if _, err := store.ResetPassword(t.Context(), raw, mustHash(t, "revived-passphrase")); err != nil {
		t.Errorf("복구 뒤 링크가 죽어 있다: %v — 트랜잭션이 되돌려지지 않았다", err)
	}
}

func mustHash(t *testing.T, pw string) string {
	t.Helper()
	h, err := auth.HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// **메일을 보낼 수 없는 사이트는 재설정이 안 된다고 말한다** (FR-207, D19 P-104). SMTP 가
// 비어 있는데 「보냈습니다」라고 답해, 사용자는 오지 않을 메일을 기다렸다. 이 답은 주소와
// 무관하게 같다 — 사이트의 성질이지 계정의 성질이 아니라 열거가 되지 않는다.
func TestResetSaysSoWhenTheSiteCannotSendMail(t *testing.T) {
	d, h, store, sender := accountFixture(t, true)
	mkAccount(t, store, "known@example.com", "correct-horse-battery")
	d.mailReady = func() bool { return false }

	get := httptest.NewRecorder()
	h.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/password/reset", nil))
	form := get.Body.String()
	known := postForm(h, "/password/reset", url.Values{"email": {"known@example.com"}}, nil)
	unknown := postForm(h, "/password/reset", url.Values{"email": {"nobody@example.com"}}, nil)
	for name, rec := range map[string]string{"폼": form, "가입": known.Body.String(), "미가입": unknown.Body.String()} {
		if !strings.Contains(rec, "|nomail") {
			t.Errorf("%s: 메일을 보낼 수 없다는 안내가 없다 (%q)", name, rec)
		}
	}
	if known.Code != unknown.Code || known.Body.String() != unknown.Body.String() {
		t.Error("메일 미설정 답이 계정 유무로 갈린다")
	}
	// 「오지 않았다」는 기다려서 본다 — 발송은 고루틴이라 바로 찍으면 늘 비어 있다.
	if _, got := waitMail(t, sender, func(capturedMail) bool { return true }); got {
		t.Error("메일을 보낼 수 없는데 재설정 메일을 보내려 했다")
	}
}
