package app

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
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
	// log 는 500 의 원인을 받는다 (plainServerError). nil 이면 남기지 않는다.
	log *slog.Logger
	// social 은 A-206 에서 **켠** 프로바이더다. 화면이 버튼을 그리는 데 쓴다 —
	// 활성 프로바이더가 0이면 버튼도 없다 (FR-709). nil 이면 소셜이 없는
	// 배포다.
	social func() []auth.SocialConfig
	// unverified refuses a correctly authenticated but unverified account when
	// the site requires verification (FR-214), resending the verification mail
	// within the per-account limit. refused=false lets the login through; sent
	// reports whether a mail went out this time. nil 이면 검사하지 않는다.
	unverified func(ctx context.Context, u *auth.User) (refused, sent bool)
	// secureCookies is config.SecureCookies — 「아이디 저장」 쿠키도 세션 쿠키와 같은 Secure 를 단다.
	secureCookies bool
}

// 「아이디 저장」 (FR-218). 로그인 화면의 이메일 칸을 채우는 데만 쓴다 — 비밀번호·세션과 상관없고 인증에
// 쓰지 않는다. 쿠키는 HttpOnly(스크립트가 못 읽는다)·Path=/login(다른 주소로 가지 않는다)·SameSite=Lax 이고
// Secure 는 세션 쿠키를 따른다. 값은 base64 다 — 이메일에는 쿠키 값에 쓸 수 없는 글자가 올 수 있다.
const (
	rememberCookie = "remember_email"
	rememberFor    = 30 * 24 * time.Hour
)

// rememberValue is the cookie value for email.
func rememberValue(email string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(email))
}

// rememberedEmail reads a cookie value back, or "" — 이메일 모양이 아니면 버린다. 남이 심은 값(형제 호스트의
// 쿠키 등)으로 로그인 칸을 채우지 않는다.
func rememberedEmail(v string) string {
	b, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		return ""
	}
	email, err := content.ValidateEmail(string(b))
	if err != nil || len(email) > 254 {
		return ""
	}
	return email
}

// remember sets or clears the cookie after a successful login — 체크했으면 남기고 아니면 지운다.
func (d *loginDeps) remember(w http.ResponseWriter, email string, on bool) {
	c := &http.Cookie{Name: rememberCookie, Path: "/login", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: d.secureCookies}
	if on {
		c.Value, c.MaxAge = rememberValue(email), int(rememberFor/time.Second)
	} else {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
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
	data := map[string]any{
		"Next": auth.SafeNext(r.URL.Query().Get("next")), "Social": d.socialButtons(),
	}
	if c, err := r.Cookie(rememberCookie); err == nil {
		if email := rememberedEmail(c.Value); email != "" {
			data["Email"], data["RememberEmail"] = email, true
		}
	}
	// 소셜 콜백이 실패하면 안내를 세션에 담아 여기로 보낸다 (socialFail).
	if msg := d.sm.PopString(r.Context(), sessSocialError); msg != "" {
		data["Error"] = msg
	}
	d.render(w, r, "auth/login.html", http.StatusOK, data)
}

// P-102 POST: end the session.
//
// Destroy, not "clear the user id": scs removes the row, so the old token is
// dead server-side (FR-203). Blanking a key would leave a valid session that a
// stolen cookie can still ride.
func (d *loginDeps) logout(w http.ResponseWriter, r *http.Request) {
	if err := d.sm.Destroy(r.Context()); err != nil {
		plainServerError(d.log, w, r, err)
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
	rememberOn := r.PostFormValue("remember_email") != ""

	// Two buckets: per IP stops a spray across many accounts, per account stops
	// a focused guess from a botnet. Either alone leaves the other open
	// (D15 4.3-2).
	ip := clientIP(r)
	if !d.limiter.Allow("login:ip:"+ip, d.limits.LoginPerIP) ||
		(email != "" && !d.limiter.Allow("login:acct:"+email, d.limits.LoginPerAccount)) {
		w.Header().Set("Retry-After", "60")
		d.render(w, r, "auth/login.html", http.StatusTooManyRequests, map[string]any{
			"Error": "시도가 너무 잦습니다. 잠시 후 다시 시도하세요.",
			"Next":  next, "RememberEmail": rememberOn,
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
			"Next":  next, "RememberEmail": rememberOn,
		})
		return
	}
	if err != nil {
		plainServerError(d.log, w, r, err)
		return
	}
	// FR-214: 인증을 켠 사이트에서 미인증 계정에는 세션을 주지 않는다. 비밀번호가 맞을
	// 때만 여기에 오므로, 인증 메일을 다시 보내는 자리로 쓴다 — 재발송(P-113)은
	// 로그인을 전제로 했고 이제 그 로그인이 막힌다.
	if d.unverified != nil {
		if refused, sent := d.unverified(ctx, u); refused {
			msg := "이메일 인증이 끝나지 않았습니다. " + u.Email + " 로 인증 메일을 다시 보냈습니다. 메일의 링크를 누른 뒤 로그인하세요."
			if !sent {
				msg = "이메일 인증이 끝나지 않았습니다. 받은 인증 메일의 링크를 누른 뒤 로그인하세요. 인증 메일은 한 시간에 세 번까지 보냅니다."
			}
			d.render(w, r, "auth/login.html", http.StatusForbidden, map[string]any{
				"Error": msg, "Email": email, "Next": next, "RememberEmail": rememberOn})
			return
		}
	}

	// FR-204 session fixation: the token the visitor arrived with must not be
	// the token they leave authenticated with. Without this, anyone who can set
	// a cookie before login (a shared machine, an XSS on a sibling host) holds
	// a session that becomes authenticated when the victim signs in.
	if err := d.sm.RenewToken(ctx); err != nil {
		plainServerError(d.log, w, r, err)
		return
	}
	d.sm.Put(ctx, sessUserID, u.ID)
	// auth_at 은 해시와 같은 문장에서 읽은 기준 시각이다 (auth.User.Cutoff) — 그 뒤의 회수가
	// 커밋되면 이 세션이 끊긴다. 재인증 창(15분)은 로그인한 지금부터 잰다.
	stampAuthAt(d.sm, ctx, u.Cutoff())
	putTime(d.sm, ctx, sessReauth, time.Now())

	// Successful login clears the ACCOUNT counter: someone who mistyped four
	// times must not stay throttled afterwards. **IP 버킷은 지우지 않는다** — 지우면
	// 자기 계정으로 몇 번에 한 번 로그인해 IP 제한을 계속 새로 시작할 수 있고,
	// 그 제한이 막으려던 여러 계정 대입이 열린다 (D15 4.3-2).
	d.limiter.Forget("login:acct:" + email)

	// 성공한 로그인만 「아이디 저장」을 바꾼다 — 실패한 입력을 남기면 남이 친 주소가 다음 화면에 뜬다.
	d.remember(w, email, rememberOn)
	http.Redirect(w, r, next, http.StatusSeeOther)
}
