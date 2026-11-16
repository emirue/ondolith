// Package sms sends text messages for the site binary's phone login ([D42](../../docs/42-site-extension.md) 9절).
package sms

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Endpoint is SENS's API host. 호스트는 고정이다 — 관리자가 넣는 것은 키·서비스 ID·발신번호뿐이라 저장된 키를
// 남의 서버로 보낼 길이 없다.
const Endpoint = "https://sens.apigw.ntruss.com"

// SENS sends through Naver Cloud SENS (공식 문서 sens-sms-send·common-ncpapi, 2026-11-16 확인).
type SENS struct {
	AccessKey, SecretKey, ServiceID, From string
	// Endpoint, Client, Now are for tests; zero values are the real host, a 10-second client and the clock.
	Endpoint string
	Client   *http.Client
	Now      func() time.Time
}

var (
	// 서비스 ID 는 경로에 들어간다 — `/`·`?`·`#`·`.` 를 받으면 다른 API 를 부르게 된다.
	serviceID = regexp.MustCompile(`^[A-Za-z0-9:_-]+$`)
	digits    = regexp.MustCompile(`^[0-9]{3,16}$`)
)

// ValidServiceID reports whether id can go into the API path — A-215 가 저장 전에, Send 가 보내기 전에 본다.
func ValidServiceID(id string) bool { return serviceID.MatchString(id) }

// ValidNumber reports whether n is digits only (3~16), the shape SENS takes for from and to.
func ValidNumber(n string) bool { return digits.MatchString(n) }

// ErrNotConfigured is a SENS with a field missing.
var ErrNotConfigured = errors.New("sms: SENS 설정이 비어 있다 — 액세스 키·시크릿 키·서비스 ID·발신번호")

// Send asks SENS to deliver text to one number. 접수(202)까지만 본다 — 실제 도착은 비동기다.
func (s SENS) Send(ctx context.Context, to, text string) error {
	if s.AccessKey == "" || s.SecretKey == "" || s.ServiceID == "" || s.From == "" {
		return ErrNotConfigured
	}
	if !ValidServiceID(s.ServiceID) {
		return fmt.Errorf("sms: 서비스 ID 의 모양이 아니다 (ncp:sms:kr:…:…)")
	}
	if !ValidNumber(s.From) || !ValidNumber(to) {
		return fmt.Errorf("sms: 발신·수신 번호는 숫자만이다")
	}
	body, err := json.Marshal(map[string]any{"type": "SMS", "from": s.From, "content": text,
		"messages": []map[string]string{{"to": to}}})
	if err != nil {
		return err
	}
	endpoint, client, now := s.Endpoint, s.Client, s.Now
	if endpoint == "" {
		endpoint = Endpoint
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	if now == nil {
		now = time.Now
	}
	path := "/sms/v2/services/" + s.ServiceID + "/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	ts := strconv.FormatInt(now().UnixMilli(), 10)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("x-ncp-apigw-timestamp", ts)
	req.Header.Set("x-ncp-iam-access-key", s.AccessKey)
	// 서명하는 경로는 실제로 나가는 경로여야 한다 — 서비스 ID 의 `:` 는 경로에서 인코딩되지 않는다.
	req.Header.Set("x-ncp-apigw-signature-v2", signature(s.SecretKey, http.MethodPost, req.URL.RequestURI(), ts, s.AccessKey))
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sms: SENS 에 닿지 못했다: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
	var out struct {
		StatusCode string `json:"statusCode"`
	}
	_ = json.Unmarshal(b, &out)
	if res.StatusCode != http.StatusAccepted || out.StatusCode != "202" {
		return fmt.Errorf("sms: SENS 가 접수하지 않았다 — HTTP %d %s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// signature is x-ncp-apigw-signature-v2: Base64(HMAC-SHA256(secret, "METHOD URI\nTIMESTAMP\nACCESSKEY")).
func signature(secret, method, uri, ts, accessKey string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(method + " " + uri + "\n" + ts + "\n" + accessKey))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// smsBytes is SENS's SMS limit, counted in EUC-KR.
const smsBytes = 90

// EUCKRLen counts s the way an EUC-KR SMS does: ASCII 1 byte, anything else 2.
func EUCKRLen(s string) int {
	n := 0
	for _, r := range s {
		if r < 0x80 {
			n++
		} else {
			n += 2
		}
	}
	return n
}

// LoginText is the login code message: "[사이트 이름] 인증번호 123456 (5분)". 90바이트를 넘으면 사이트 이름만 줄인다.
func LoginText(site, code string) string {
	tail := "인증번호 " + code + " (5분)"
	site = strings.TrimSpace(site)
	if site == "" {
		return tail
	}
	room := smsBytes - EUCKRLen(tail) - len("[] ")
	name := []rune(site)
	for EUCKRLen(string(name)) > room && len(name) > 0 {
		name = name[:len(name)-1]
	}
	if len(name) == 0 {
		return tail
	}
	return "[" + string(name) + "] " + tail
}

// Mask hides the middle of a phone number for logs: 010****5678.
func Mask(phone string) string {
	if len(phone) < 8 {
		return strings.Repeat("*", len(phone))
	}
	return phone[:3] + strings.Repeat("*", len(phone)-7) + phone[len(phone)-4:]
}
