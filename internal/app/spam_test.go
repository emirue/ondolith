package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/auth"
)

// **공개 폼 스팸 방지의 갈래** (FR-216). 숨은 칸은 늘 본다. Turnstile 은 사이트 키와
// 비밀키가 **둘 다** 있을 때만 켜진다 — 사이트 키 없이 비밀키만 있으면 위젯이 그려지지
// 않아 모든 제출이 거부된다. 확인 서버 고장은 방문자 거부(422)가 아니라 바깥 시스템 실패 502 다(D19 0.3).
func TestSpamGuard(t *testing.T) {
	post := func(form url.Values) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/signup", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		return r
	}
	var verified int
	verifyWith := func(err error) func(context.Context, string, string, string) error {
		return func(context.Context, string, string, string) error { verified++; return err }
	}
	keys := func(site, secret string) func() (string, string) {
		return func() (string, string) { return site, secret }
	}
	for _, tc := range []struct {
		name   string
		g      spamGuard
		form   url.Values
		want   int
		called bool
	}{
		{"꺼져 있으면 토큰 없이 통과", spamGuard{keys: keys("", "")}, url.Values{}, 0, false},
		{"숨은 칸을 채우면 거부", spamGuard{keys: keys("", "")}, url.Values{honeypotField: {"http://spam"}}, 422, false},
		{"비밀키만 있으면 꺼진 것", spamGuard{keys: keys("", "s"), verify: verifyWith(nil)}, url.Values{}, 0, false},
		{"켜져 있고 통과", spamGuard{keys: keys("k", "s"), verify: verifyWith(nil)},
			url.Values{auth.TurnstileField: {"tok"}}, 0, true},
		{"켜져 있고 거부", spamGuard{keys: keys("k", "s"), verify: verifyWith(auth.ErrTurnstileRejected)},
			url.Values{auth.TurnstileField: {"tok"}}, 422, true},
		{"확인 서버 고장", spamGuard{keys: keys("k", "s"), verify: verifyWith(errors.New("dial tcp: timeout"))},
			url.Values{auth.TurnstileField: {"tok"}}, 502, true},
	} {
		verified = 0
		code, msg := tc.g.check(post(tc.form))
		if code != tc.want || (code != 0) != (msg != "") {
			t.Errorf("%s: (%d, %q), want %d", tc.name, code, msg, tc.want)
		}
		if (verified > 0) != tc.called {
			t.Errorf("%s: siteverify 호출 %d번", tc.name, verified)
		}
	}
}
