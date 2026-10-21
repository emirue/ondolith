package app

import (
	"context"
	"net/http"
	"time"
)

// reauthWindow is how long a password confirmation lasts (D15 5.3-1).
//
// The alternative — a short admin session — makes an administrator log in
// several times a day, which is phishing training. Asking at the dangerous
// moment costs one prompt and lands where it matters.
const reauthWindow = 15 * time.Minute

// NeedsReauth reports whether this Actor must confirm their password before the
// destructive action they are attempting.
//
// The session carries WHEN the password was last confirmed. It does not carry
// permissions, and it must not: a session-cached permission survives a
// revocation until logout, which is exactly the moment revocation is needed
// (D15 4.3-1). A timestamp is safe to cache because it only ever expires.
func NeedsReauth(a *Actor, now time.Time) bool {
	if a == nil || !a.IsAuthenticated() {
		return true
	}
	if a.ReauthAt.IsZero() {
		// Logging in IS a password confirmation; the clock starts there.
		return now.Sub(a.AuthAt) > reauthWindow
	}
	return now.Sub(a.ReauthAt) > reauthWindow
}

// confirmPassword is the password re-check of the public 「본인 계정의 통제권」
// screens — P-109, P-110, P-111 (D15 5.3-1).
//
// **계정당 5회/분** (D15 4.3-2) — 관리자 재인증(adminCaller.ConfirmReauth)과 같은
// 버킷이다. 없으면 열린 세션 하나로 로그인 제한을 비켜 비밀번호를 맞혀 본다.
func (d *loginDeps) confirmPassword(ctx context.Context, a *Actor, password string) bool {
	key := "reauth:" + a.User.ID
	if password == "" || !d.limiter.Allow(key, d.limits.ReauthAccount) {
		return false
	}
	if _, err := d.store.Authenticate(ctx, a.User.Email, password); err != nil {
		return false
	}
	// 맞히면 버킷을 비운다 — 한 번 틀린 정상 사용자의 남은 횟수가 줄어 있지 않게.
	d.limiter.Forget(key)
	return true
}

// requireReauth wraps a destructive handler.
//
// The refusal is 403 plus that screen's own form re-displayed, not a redirect
// (D15 5.3-1, D19 C7): a redirect loses everything the operator typed, and they
// will type it again with less care.
//
// There is no separate re-auth screen. The password field appears on the target
// screen's form — rendered only when the window has lapsed — so the operator
// never leaves the page they are working on.
func requireReauth(now func() time.Time, onNeeded http.HandlerFunc) func(http.HandlerFunc) http.HandlerFunc {
	if now == nil {
		now = time.Now
	}
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if NeedsReauth(ActorFrom(r.Context()), now()) {
				onNeeded(w, r)
				return
			}
			next(w, r)
		}
	}
}
