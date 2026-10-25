package app

import (
	"net/http"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/content"
)

// P-104 GET — ask for the address.
func (d *accountDeps) resetRequestForm(w http.ResponseWriter, r *http.Request) {
	if !d.mailOK() {
		d.renderNoMail(w, r)
		return
	}
	d.render(w, r, "auth/password-reset-request.html", http.StatusOK, nil)
}

// renderNoMail tells the visitor reset cannot work here. **메일을 보낼 수 없는 사이트의
// 성질이지 계정의 성질이 아니다** — 어느 주소에나 같은 답이라 FR-207 의 열거가 되지
// 않는다. 「보냈습니다」라고 답하면 오지 않을 메일을 기다리게 한다(D19 P-104).
// 일시적인 발송 실패는 여기 오지 않는다 — 그것은 여전히 성공과 같은 응답이다.
func (d *accountDeps) renderNoMail(w http.ResponseWriter, r *http.Request) {
	// 문구는 Error 로도 싣는다 — 이 화면을 덮어쓴 테마는 NoMail 을 모르지만 Error 는 그린다.
	d.render(w, r, "auth/password-reset-request.html", http.StatusOK, map[string]any{"NoMail": true,
		"Error": "이 사이트는 아직 메일을 보낼 수 없어 비밀번호 재설정을 쓸 수 없습니다. 사이트 관리자에게 문의해 주세요."})
}

// P-104 POST — send the link.
//
// Every path below that is not a malformed address ends in the same page with
// the same status. That is the whole point of the screen's design (FR-207):
// "이 주소로 계정이 있습니까" is exactly the question a reset form must not
// answer, and it answers it by any difference at all — wording, status, or a
// redirect target.
func (d *accountDeps) resetRequest(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	if !d.mailOK() {
		d.renderNoMail(w, r)
		return
	}

	if !d.limiter.Allow("reset:ip:"+clientIP(r), d.limits.PasswordResetIP) {
		w.Header().Set("Retry-After", "3600")
		d.render(w, r, "auth/password-reset-request.html", http.StatusTooManyRequests,
			map[string]any{"Error": "재설정 요청이 너무 잦습니다. 잠시 후 다시 시도하세요."})
		return
	}

	// The format check is the one thing that may answer differently, because
	// "not an email address" is a property of what was typed, not of who has an
	// account (D19 P-104: 400).
	email, err := content.ValidateEmail(content.NormalizeEmail(r.PostFormValue("email")))
	if err != nil {
		d.render(w, r, "auth/password-reset-request.html", http.StatusBadRequest,
			map[string]any{"Error": "이메일 형식이 올바르지 않습니다."})
		return
	}

	// 스팸 판정도 계정이 아니라 제출의 성질이다 — 계정 유무와 무관하게 같은 답이다.
	if code, msg := d.spam.check(r); code != 0 {
		d.render(w, r, "auth/password-reset-request.html", code, map[string]any{"Error": msg})
		return
	}

	// FindActiveUserByEmail filters on is_active, so a disabled account takes
	// the same silent path as a missing one.
	user, _, err := d.store.FindActiveUserByEmail(ctx, email)
	if err == nil && user != nil {
		if raw, terr := d.store.IssueResetToken(ctx, user.ID); terr == nil {
			d.mailer.SendAsync(email, "비밀번호 재설정", resetMailBody(d.baseURL+"/password/reset/"+raw))
		}
	}
	// Mail failures land here too: SendAsync reports to the log, and D19 puts
	// SMTP trouble in the "성공과 같은 응답" row — a failing relay must not become
	// a way to enumerate addresses. (A-205 가 비어 있는 사이트는 위의 renderNoMail 이
	// 먼저 답한다 — 어느 주소에나 같은 답이다.)
	d.render(w, r, "auth/password-reset-sent.html", http.StatusOK, nil)
}

// newPasswordPath 는 P-105 의 폼 주소다. **토큰이 없다.** 화면을 그리는 주소에 토큰이
// 있으면 그 화면에 붙은 스크립트가 주소째 밖으로 보낸다 — GA4 의 page_location 이
// 그렇고, Referrer-Policy 는 Referer 만 막는다 (D60 「비밀이 든 주소」).
const newPasswordPath = "/password/new"

// resetCookie 는 메일 링크에서 옮겨 온 재설정 토큰이다. **세션이 아니라 쿠키다** —
// 세션은 DB(pgxstore)에 있고, 토큰 원문은 DB에 두지 않는다 (D19 P-105). 쿠키는
// 폼 주소에만 실리고(Path) 스크립트가 읽지 못한다(HttpOnly).
const resetCookie = "reset_token"

func (d *accountDeps) setResetCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: resetCookie, Value: value, Path: newPasswordPath,
		MaxAge: maxAge, HttpOnly: true, Secure: d.sm.Cookie.Secure, SameSite: http.SameSiteLaxMode})
}

// P-105 GET /password/reset/{token} — the link in the mail.
//
// It renders nothing: the token moves into resetCookie and the browser goes to
// newPasswordPath. The token is not checked here either. Checking it would
// answer "is this link live?" to anyone who pastes one, and the answer is due
// at POST anyway; a dead link fails then, with the same sentence as every other
// failure.
func (d *accountDeps) resetLink(w http.ResponseWriter, r *http.Request) {
	d.setResetCookie(w, r.PathValue("token"), int(auth.PasswordResetTTL.Seconds()))
	http.Redirect(w, r, newPasswordPath, http.StatusSeeOther)
}

// P-105 GET /password/new — the new-password form.
func (d *accountDeps) resetForm(w http.ResponseWriter, r *http.Request) {
	d.render(w, r, "auth/password-reset.html", http.StatusOK, nil)
}

// P-105 POST — set the password.
func (d *accountDeps) resetPassword(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	// The token comes from resetCookie (resetLink). `email` and `user_id` are
	// not read: the token names the account, and a form field that could name a
	// different one turns a valid link of your own into anyone's password
	// (D19 P-105). A field error leaves the cookie for the retry.
	var token string
	if c, err := r.Cookie(resetCookie); err == nil {
		token = c.Value
	}
	pw := r.PostFormValue("password")

	fail := func(msg string, code int) {
		d.render(w, r, "auth/password-reset.html", code, map[string]any{"Error": msg})
	}
	if pw != r.PostFormValue("password_confirm") {
		fail("두 비밀번호가 일치하지 않습니다.", http.StatusBadRequest)
		return
	}
	if err := content.ValidatePassword(pw); err != nil {
		fail("비밀번호는 10자 이상이어야 합니다.", http.StatusBadRequest)
		return
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		plainServerError(d.log, w, r, err)
		return
	}

	// Wrong, expired, spent, and disabled-account are one sentence. Telling
	// them apart tells a guesser which of their attempts was close.
	if _, err := d.store.ResetPassword(ctx, token, hash); err != nil {
		fail("링크가 유효하지 않거나 만료되었습니다.", http.StatusBadRequest)
		return
	}
	d.setResetCookie(w, "", -1)

	// No session is opened. ResetPassword moved sessions_valid_from, so every
	// session that existed is dead; handing this browser a fresh one would
	// re-open the very thing the reset was meant to close if the person doing
	// the reset is not the one who asked for it.
	http.Redirect(w, r, loginPath, http.StatusSeeOther)
}
