package app

import (
	"net/http"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/content"
)

// P-104 GET — ask for the address.
func (d *accountDeps) resetRequestForm(w http.ResponseWriter, r *http.Request) {
	d.render(w, r, "auth/password-reset-request.html", http.StatusOK, nil)
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

	// FindActiveUserByEmail filters on is_active, so a disabled account takes
	// the same silent path as a missing one.
	user, _, err := d.store.FindActiveUserByEmail(ctx, email)
	if err == nil && user != nil {
		if raw, terr := d.store.IssueResetToken(ctx, user.ID); terr == nil {
			d.mailer.SendAsync(email, "비밀번호 재설정",
				"아래 링크로 비밀번호를 재설정하세요. 30분 뒤 만료됩니다.\n"+
					d.baseURL+"/password/reset/"+raw)
		}
	}
	// Mail failures land here too: SendAsync reports to the log, and D19 puts
	// SMTP trouble in the "성공과 같은 응답" row — an unconfigured A-205 must not
	// become a way to enumerate addresses.
	d.render(w, r, "auth/password-reset-sent.html", http.StatusOK, nil)
}

// P-105 GET — the new-password form.
//
// The token is not checked here. Checking it would answer "is this link
// live?" to anyone who pastes one, and the answer is due at POST anyway; a
// dead link fails then, with the same sentence as every other failure.
func (d *accountDeps) resetForm(w http.ResponseWriter, r *http.Request) {
	d.render(w, r, "auth/password-reset.html", http.StatusOK,
		map[string]any{"Token": r.PathValue("token")})
}

// P-105 POST — set the password.
func (d *accountDeps) resetPassword(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	// The token comes from the path. `email` and `user_id` are not read: the
	// token names the account, and a form field that could name a different one
	// turns a valid link of your own into anyone's password (D19 P-105).
	token := r.PathValue("token")
	pw := r.PostFormValue("password")

	fail := func(msg string, code int) {
		d.render(w, r, "auth/password-reset.html", code,
			map[string]any{"Error": msg, "Token": token})
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
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}

	// Wrong, expired, spent, and disabled-account are one sentence. Telling
	// them apart tells a guesser which of their attempts was close.
	if _, err := d.store.ResetPassword(ctx, token, hash); err != nil {
		fail("링크가 유효하지 않거나 만료되었습니다.", http.StatusBadRequest)
		return
	}

	// No session is opened. ResetPassword moved sessions_valid_from, so every
	// session that existed is dead; handing this browser a fresh one would
	// re-open the very thing the reset was meant to close if the person doing
	// the reset is not the one who asked for it.
	http.Redirect(w, r, loginPath, http.StatusSeeOther)
}
