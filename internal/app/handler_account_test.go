package app

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// **가입을 닫으면 폼도 제출도 403 이다** (FR-217). 제출 쪽이 실제 방어다 — 링크를 숨겨도
// 주소를 아는 봇은 바로 POST 한다.
func TestClosedSignupRefusesFormAndSubmission(t *testing.T) {
	d := &accountDeps{
		loginDeps: loginDeps{render: func(w http.ResponseWriter, _ *http.Request, name string, code int, _ any) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(name))
		}},
		signupClosed: func() bool { return true },
	}
	form := url.Values{"email": {"a@example.com"}, "password": {"correct-horse-battery"}}
	post := httptest.NewRequest(http.MethodPost, "/signup", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for name, tc := range map[string]struct {
		h http.HandlerFunc
		r *http.Request
	}{
		"GET":  {d.signupForm, httptest.NewRequest(http.MethodGet, "/signup", nil)},
		"POST": {d.signup, post},
	} {
		rec := httptest.NewRecorder()
		tc.h(rec, tc.r)
		if rec.Code != http.StatusForbidden || rec.Body.String() != "auth/signup.html" {
			t.Errorf("%s: HTTP %d %q, want 403 auth/signup.html", name, rec.Code, rec.Body.String())
		}
	}
}
