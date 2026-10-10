package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// TurnstileVerifyURL is Cloudflare's siteverify endpoint (2026-10-08 공식 문서
// developers.cloudflare.com/turnstile/get-started/server-side-validation 확인).
const TurnstileVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// TurnstileField is the form field the widget adds to the form it sits in.
const TurnstileField = "cf-turnstile-response"

// ErrTurnstileRejected means the visitor's token did not pass: missing, expired,
// already used, or forged. Any other error from Verify is ours — a wrong secret,
// Cloudflare unreachable — and belongs in the log, not in front of the visitor.
var ErrTurnstileRejected = errors.New("turnstile: 확인을 통과하지 못했다")

// Turnstile verifies widget tokens server-side (FR-216). URL is a field so the
// test can point it at a local server; production uses TurnstileVerifyURL.
type Turnstile struct {
	Client *http.Client
	URL    string
}

// ourFault are the siteverify codes that describe our request, not the visitor.
var ourFault = []string{"missing-input-secret", "invalid-input-secret", "bad-request", "internal-error"}

// Verify asks siteverify about one token. A token is valid for 300 seconds and
// once only — the widget refreshes it by itself (refresh-expired=auto, 기본값).
func (t Turnstile) Verify(ctx context.Context, secret, token, remoteIP string) error {
	// 문서의 토큰 상한이 2048자다. 빈 토큰은 위젯이 없는 폼(봇이 고른 길)이다.
	if token == "" || len(token) > 2048 {
		return ErrTurnstileRejected
	}
	form := url.Values{"secret": {secret}, "response": {token}}
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.URL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("turnstile: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.Client.Do(req)
	if err != nil {
		return fmt.Errorf("turnstile: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Success bool     `json:"success"`
		Codes   []string `json:"error-codes"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return fmt.Errorf("turnstile: HTTP %d: %w", resp.StatusCode, err)
	}
	if out.Success {
		return nil
	}
	for _, c := range out.Codes {
		if slices.Contains(ourFault, c) {
			return fmt.Errorf("turnstile: %v", out.Codes)
		}
	}
	return ErrTurnstileRejected
}
