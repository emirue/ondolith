package app

import (
	"errors"
	"net/http"
	"time"

	"github.com/alexedwards/scs/v2"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/content"
)

// loginDeps is what the login screens need. Passing them in keeps the handler
// testable without booting the whole tree.
type loginDeps struct {
	sm      *scs.SessionManager
	store   *auth.Store
	limiter *auth.Limiter
	limits  auth.Limits
	render  func(w http.ResponseWriter, r *http.Request, name string, code int, data any)
	// social 은 A-206 에서 **켠** 프로바이더다. 화면이 버튼을 그리는 데 쓴다 —
	// 활성 프로바이더가 0이면 버튼도 없다 (FR-709). nil 이면 소셜이 없는
	// 배포다.
	social func() []auth.SocialConfig
}

// socialButtons is what the login screen draws. 켠 것이 없으면 빈 목록이다.
func (d *loginDeps) socialButtons() []auth.SocialConfig {
	if d.social == nil {
		return nil
	}
	return d.social()
}

// P-101 GET: the form.
func (d *loginDeps) loginForm(w http.ResponseWriter, r *http.Request) {
	if ActorFrom(r.Context()).IsAuthenticated() {
		http.Redirect(w, r, auth.SafeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}
	d.render(w, r, "auth/login.html", http.StatusOK, map[string]any{
		"Next": auth.SafeNext(r.URL.Query().Get("next")), "Social": d.socialButtons(),
	})
}

// P-102 POST: end the session.
//
// Destroy, not "clear the user id": scs removes the row, so the old token is
// dead server-side (FR-203). Blanking a key would leave a valid session that a
// stolen cookie can still ride.
func (d *loginDeps) logout(w http.ResponseWriter, r *http.Request) {
	if err := d.sm.Destroy(r.Context()); err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// P-101 POST: authenticate.
func (d *loginDeps) login(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	email := content.NormalizeEmail(r.PostFormValue("email"))
	password := r.PostFormValue("password")
	next := auth.SafeNext(r.PostFormValue("next"))

	// Two buckets: per IP stops a spray across many accounts, per account stops
	// a focused guess from a botnet. Either alone leaves the other open
	// (D15 4.3-2).
	ip := clientIP(r)
	if !d.limiter.Allow("login:ip:"+ip, d.limits.LoginPerIP) ||
		(email != "" && !d.limiter.Allow("login:acct:"+email, d.limits.LoginPerAccount)) {
		w.Header().Set("Retry-After", "60")
		d.render(w, r, "auth/login.html", http.StatusTooManyRequests, map[string]any{
			"Error": "시도가 너무 잦습니다. 잠시 후 다시 시도하세요.",
			"Next":  next,
		})
		return
	}

	// The password length floor is NOT applied here: it governs setting a
	// password, and applying it at login would lock out every account created
	// before the rule.
	u, err := d.store.Authenticate(ctx, email, password)
	if errors.Is(err, auth.ErrBadCredentials) {
		// One message for "no account", "wrong password" and "deactivated".
		// Three answers would enumerate which addresses have accounts here.
		d.render(w, r, "auth/login.html", http.StatusBadRequest, map[string]any{
			"Error": "이메일 또는 비밀번호가 올바르지 않습니다.",
			"Email": email,
			"Next":  next,
		})
		return
	}
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}

	// FR-204 session fixation: the token the visitor arrived with must not be
	// the token they leave authenticated with. Without this, anyone who can set
	// a cookie before login (a shared machine, an XSS on a sibling host) holds
	// a session that becomes authenticated when the victim signs in.
	if err := d.sm.RenewToken(ctx); err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.sm.Put(ctx, sessUserID, u.ID)
	putTime(d.sm, ctx, sessAuthAt, time.Now())

	// Successful login clears the failure counters: someone who mistyped four
	// times must not stay throttled afterwards.
	d.limiter.Forget("login:acct:" + email)
	d.limiter.Forget("login:ip:" + ip)

	http.Redirect(w, r, next, http.StatusSeeOther)
}
