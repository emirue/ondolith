package app

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/emirue/ondolith/internal/auth"
)

const resetPrefix = "https://example.com/password/reset/"

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

	if rec := doGet(h, path); rec.Code != http.StatusOK {
		t.Fatalf("메일이 보낸 %s 가 HTTP %d (%q)", path, rec.Code, rec.Body.String())
	}
	rec := postForm(h, path, url.Values{
		"password": {"brand-new-passphrase"}, "password_confirm": {"brand-new-passphrase"},
	}, nil)
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
	again := postForm(h, path, url.Values{
		"password": {"third-passphrase-here"}, "password_confirm": {"third-passphrase-here"},
	}, nil)
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

	postForm(h, path, url.Values{
		"password": {"attacker-chosen-value"}, "password_confirm": {"attacker-chosen-value"},
		"email": {"victim@example.com"}, "user_id": {"victim@example.com"},
	}, nil)

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

	rec := postForm(h, first, url.Values{
		"password": {"old-link-passphrase"}, "password_confirm": {"old-link-passphrase"},
	}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("옛 링크가 HTTP %d, want 400 — 무효화되지 않았다", rec.Code)
	}
	if _, err := store.Authenticate(t.Context(), "twice@example.com", "old-link-passphrase"); err == nil {
		t.Error("옛 링크로 비밀번호가 바뀌었다")
	}

	rec = postForm(h, second, url.Values{
		"password": {"new-link-passphrase"}, "password_confirm": {"new-link-passphrase"},
	}, nil)
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
	postForm(h, path, url.Values{
		"password": {"a-fresh-passphrase"}, "password_confirm": {"a-fresh-passphrase"},
	}, nil)

	after, err := store.FindUserByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !after.SessionsValidFrom.After(before.SessionsValidFrom) {
		t.Errorf("sessions_valid_from 이 %v 에서 움직이지 않았다 (%v)",
			before.SessionsValidFrom, after.SessionsValidFrom)
	}
}

// Confirmation mismatch and a short password re-show the form WITH the token —
// otherwise a typo costs the user their only link.
func TestResetFormKeepsTheTokenAfterAFieldError(t *testing.T) {
	_, h, store, sender := accountFixture(t, true)
	mkAccount(t, store, "typo@example.com", "correct-horse-battery")
	postForm(h, "/password/reset", url.Values{"email": {"typo@example.com"}}, nil)
	path := mailedPath(t, sender, "typo@example.com", resetPrefix)

	for _, bad := range []url.Values{
		{"password": {"long-enough-passphrase"}, "password_confirm": {"different-passphrase"}},
		{"password": {"short"}, "password_confirm": {"short"}},
	} {
		if rec := postForm(h, path, bad, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%v → HTTP %d, want 400", bad, rec.Code)
		}
	}
	// The link still works afterwards.
	rec := postForm(h, path, url.Values{
		"password": {"finally-a-good-one"}, "password_confirm": {"finally-a-good-one"},
	}, nil)
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
