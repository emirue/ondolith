package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// **siteverify 의 답을 세 갈래로 읽는다** (FR-216): 통과, 방문자 거부, 우리 쪽 고장.
// 비밀키가 틀린 것(invalid-input-secret)을 「자동 입력」 으로 읽으면 운영자는 모든 가입이
// 막힌 이유를 모른다 — 로그로 가야 할 오류다.
func TestTurnstileVerify(t *testing.T) {
	var got map[string]string
	answer := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Method != http.MethodPost {
			t.Errorf("요청 = %s %v", r.Method, err)
		}
		got = map[string]string{"secret": r.PostFormValue("secret"),
			"response": r.PostFormValue("response"), "remoteip": r.PostFormValue("remoteip")}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(answer))
	}))
	defer srv.Close()
	ts := Turnstile{Client: srv.Client(), URL: srv.URL}
	ctx := context.Background()

	answer = `{"success":true,"error-codes":[]}`
	if err := ts.Verify(ctx, "sec", "tok", "203.0.113.7"); err != nil {
		t.Fatalf("통과 = %v", err)
	}
	if got["secret"] != "sec" || got["response"] != "tok" || got["remoteip"] != "203.0.113.7" {
		t.Errorf("보낸 값 = %v", got)
	}

	answer = `{"success":false,"error-codes":["timeout-or-duplicate"]}`
	if err := ts.Verify(ctx, "sec", "tok", ""); !errors.Is(err, ErrTurnstileRejected) {
		t.Errorf("만료·재사용 토큰 = %v, want ErrTurnstileRejected", err)
	}

	answer = `{"success":false,"error-codes":["invalid-input-secret"]}`
	if err := ts.Verify(ctx, "sec", "tok", ""); err == nil || errors.Is(err, ErrTurnstileRejected) {
		t.Errorf("틀린 비밀키 = %v, want 방문자 거부가 아닌 오류", err)
	}

	answer = `<html>bad gateway</html>`
	if err := ts.Verify(ctx, "sec", "tok", ""); err == nil || errors.Is(err, ErrTurnstileRejected) {
		t.Errorf("JSON 이 아닌 답 = %v, want 방문자 거부가 아닌 오류", err)
	}

	// 토큰이 없거나 문서의 상한(2048자)을 넘으면 묻지 않고 거부한다.
	got = nil
	for _, tok := range []string{"", string(make([]byte, 2049))} {
		if err := ts.Verify(ctx, "sec", tok, ""); !errors.Is(err, ErrTurnstileRejected) {
			t.Errorf("토큰 %d자 = %v, want ErrTurnstileRejected", len(tok), err)
		}
	}
	if got != nil {
		t.Errorf("토큰이 없는데 siteverify 를 불렀다: %v", got)
	}
}
