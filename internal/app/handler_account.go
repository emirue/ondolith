package app

import (
	"errors"
	"net/http"
	"time"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/content"
)

// accountDeps covers signup, verification and the three self-service screens.
type accountDeps struct {
	loginDeps
	mailer *auth.Mailer
	// verifyRequired mirrors the auth.email_verification_required setting
	// (FR-214). Sites that cannot send mail — an intranet — turn it off.
	verifyRequired func() bool
	baseURL        string
}

// signupForm is the whole of what P-103 accepts.
//
// Mass-assignment defence is structural: the struct has three fields, so a
// posted `role`, `is_admin` or `is_active` has nowhere to land. Binding a map
// or the user row directly is how a signup form becomes a privilege escalation
// (D19 P-103).
type signupForm struct {
	Email       string
	Password    string
	DisplayName string
}

func readSignup(r *http.Request) signupForm {
	return signupForm{
		Email:       content.NormalizeEmail(r.PostFormValue("email")),
		Password:    r.PostFormValue("password"),
		DisplayName: r.PostFormValue("display_name"),
	}
}

// P-103 POST.
func (d *accountDeps) signup(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	f := readSignup(r)

	if !d.limiter.Allow("signup:ip:"+clientIP(r), d.limits.SignupPerIP) {
		w.Header().Set("Retry-After", "3600")
		d.render(w, r, "auth/signup.html", http.StatusTooManyRequests,
			map[string]any{"Error": "가입 시도가 너무 잦습니다."})
		return
	}

	email, err := content.ValidateEmail(f.Email)
	if err != nil {
		d.render(w, r, "auth/signup.html", http.StatusBadRequest,
			map[string]any{"Error": "이메일 형식이 올바르지 않습니다.", "Form": f})
		return
	}
	if err := content.ValidatePassword(f.Password); err != nil {
		d.render(w, r, "auth/signup.html", http.StatusBadRequest,
			map[string]any{"Error": "비밀번호는 10자 이상이어야 합니다.", "Form": f})
		return
	}

	hash, err := auth.HashPassword(f.Password)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}

	userID, err := d.store.CreateUser(ctx, email, hash, f.DisplayName)
	if errors.Is(err, auth.ErrEmailTaken) {
		// FR-210: the answer must not reveal that this address has an account.
		// "이미 가입된 이메일입니다" is a membership oracle — anyone can test a
		// list of addresses against it. The same page is shown as for success,
		// and the existing account is told by mail that someone tried.
		d.mailer.SendAsync(email, "가입 시도 알림",
			"이 주소로 가입을 시도한 요청이 있었습니다. 본인이 아니라면 무시하세요.")
		d.renderSignupAccepted(w, r, email)
		return
	}
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}

	if !d.verifyRequired() {
		// FR-214 off: the account is usable immediately, and logging them in
		// here is what makes "sign up" mean what it says.
		if err := d.sm.RenewToken(ctx); err != nil {
			http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
			return
		}
		d.sm.Put(ctx, sessUserID, userID)
		// The account was created a moment ago, so its cutoff is the freshest
		// one in the table — this is the exact spot where a process clock
		// running behind the database logs the new user straight back out.
		authAt, err := d.store.Now(ctx)
		if err != nil {
			http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
			return
		}
		stampAuthAt(d.sm, ctx, authAt)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	d.sendVerification(ctx, userID, email)
	d.renderSignupAccepted(w, r, email)
}

// renderSignupAccepted is the one response both a real signup and a duplicate
// get. Keeping it in a function is what stops the two paths from drifting into
// distinguishable answers.
func (d *accountDeps) renderSignupAccepted(w http.ResponseWriter, r *http.Request, email string) {
	d.render(w, r, "auth/signup-sent.html", http.StatusOK, map[string]any{"Email": email})
}

func (d *accountDeps) sendVerification(ctx contextLike, userID, email string) {
	raw, err := d.store.IssueToken(ctx, auth.KindEmailVerify, userID)
	if err != nil {
		return // logged by the caller's error path; the user can request again
	}
	// The token is a path segment, not a query parameter. D11 registers P-112
	// at /verify/{token}, and the two forms disagreed for a while: this mail
	// sent /verify?t=…, which the route does not match, so every verification
	// link in production answered 404. The integration test did not catch it
	// because it built its own registry with /verify — a fixture that differed
	// from the tree (.ai/MISTAKES.md M14).
	d.mailer.SendAsync(email, "이메일 인증",
		"아래 링크로 인증을 완료하세요:\n"+d.baseURL+"/verify/"+raw)
}

// contextLike keeps the signature honest without importing context here twice.
type contextLike = interface {
	Deadline() (time.Time, bool)
	Done() <-chan struct{}
	Err() error
	Value(any) any
}

// P-112 GET /verify/{token} — consume the token.
func (d *accountDeps) verify(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, already, err := d.store.ConsumeVerifyToken(ctx, r.PathValue("token"))
	switch {
	case already:
		// D19 P-112: a second visit to a spent token succeeds quietly. Mail
		// clients prefetch links, so the token is often burned before the
		// person clicks it, and 400 would show them a failure for a
		// verification that had in fact worked.
		d.render(w, r, "auth/verify.html", http.StatusOK, nil)
		return
	case err != nil:
		// Wrong and expired are one answer. This screen tells "spent" apart
		// on purpose (above) and nothing else.
		d.render(w, r, "auth/verify.html", http.StatusBadRequest,
			map[string]any{"Error": "링크가 올바르지 않거나 만료되었습니다."})
		return
	}
	if err := d.store.MarkEmailVerified(ctx, userID); err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.render(w, r, "auth/verify.html", http.StatusOK, nil)
}

// signupForm's GET. Already-authenticated callers are sent on rather than shown
// a form that would create a second account under a session they already have.
func (d *accountDeps) signupForm(w http.ResponseWriter, r *http.Request) {
	if ActorFrom(r.Context()).IsAuthenticated() {
		http.Redirect(w, r, "/me", http.StatusSeeOther)
		return
	}
	d.render(w, r, "auth/signup.html", http.StatusOK, nil)
}

// P-108 GET — own profile. The subject is the session, so there is no id.
func (d *accountDeps) profileForm(w http.ResponseWriter, r *http.Request) {
	a := ActorFrom(r.Context())
	if !a.IsAuthenticated() {
		http.Redirect(w, r, loginPath, http.StatusSeeOther)
		return
	}
	d.render(w, r, "account/profile.html", http.StatusOK, map[string]any{
		"DisplayName": a.User.DisplayName, "Email": a.User.Email,
	})
}

// P-109 GET — the password form.
func (d *accountDeps) passwordForm(w http.ResponseWriter, r *http.Request) {
	if !ActorFrom(r.Context()).IsAuthenticated() {
		http.Redirect(w, r, loginPath, http.StatusSeeOther)
		return
	}
	d.render(w, r, "account/password.html", http.StatusOK, nil)
}

// profileForm is P-108's whole surface. `role`, `is_active` and `is_admin` are
// absent by construction — the escalation they would allow cannot be typed.
type profileForm struct{ DisplayName string }

// P-108 POST — edit own profile.
func (d *accountDeps) updateProfile(w http.ResponseWriter, r *http.Request) {
	a := ActorFrom(r.Context())
	if !a.IsAuthenticated() {
		http.Redirect(w, r, loginPath, http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	name := r.PostFormValue("display_name")
	if name == "" {
		d.render(w, r, "account/profile.html", http.StatusBadRequest,
			map[string]any{"Error": "표시 이름을 입력하세요."})
		return
	}
	// Ownership is the session's user id, never a form field: SC-3 says the
	// subject comes from the session, so there is no id to tamper with.
	if err := d.store.UpdateDisplayName(r.Context(), a.User.ID, name); err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/me", http.StatusSeeOther)
}

// P-109 POST — change own password.
func (d *accountDeps) changePassword(w http.ResponseWriter, r *http.Request) {
	a := ActorFrom(r.Context())
	if !a.IsAuthenticated() {
		http.Redirect(w, r, loginPath, http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	current := r.PostFormValue("current_password")
	next := r.PostFormValue("new_password")

	// The current password is required even though the session is already
	// authenticated: a session left open on a shared machine must not be enough
	// to take the account (D15 5.3-1).
	if _, err := d.store.Authenticate(ctx, a.User.Email, current); err != nil {
		d.render(w, r, "account/password.html", http.StatusBadRequest,
			map[string]any{"Error": "현재 비밀번호가 올바르지 않습니다."})
		return
	}
	if err := content.ValidatePassword(next); err != nil {
		d.render(w, r, "account/password.html", http.StatusBadRequest,
			map[string]any{"Error": "새 비밀번호는 10자 이상이어야 합니다."})
		return
	}
	hash, err := auth.HashPassword(next)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	// SetPassword also moves sessions_valid_from, ending every OTHER session,
	// and hands back the cutoff it wrote.
	cutoff, err := d.store.SetPassword(ctx, a.User.ID, hash)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	// ...including this one, so it is renewed and re-stamped: the person who
	// just proved they know the password stays logged in, everyone else does
	// not.
	if err := d.sm.RenewToken(ctx); err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.sm.Put(ctx, sessUserID, a.User.ID)
	// Stamp with the cutoff the database wrote, not with time.Now(): comparing
	// the process clock against the database clock logs this user out of the
	// session they just proved they own.
	stampAuthAt(d.sm, ctx, cutoff)
	// reauth_at 은 프로세스 시계(adminCaller.now)와만 비교되고 창이 분 단위라
	// 시계 차이가 무의미하다 — auth_at 과 달리 DBTime 을 요구하지 않는다.
	putTime(d.sm, ctx, sessReauth, cutoff.Time())
	http.Redirect(w, r, "/me", http.StatusSeeOther)
}
