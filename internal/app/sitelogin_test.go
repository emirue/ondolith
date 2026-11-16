package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/emirue/ondolith/internal/apikeys"
	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/siteext"
	"github.com/emirue/ondolith/internal/sms"
)

// fakeLogin is applogin.Store in memory: one code per phone, tokens by value.
type fakeLogin struct {
	codes    map[string]string
	sessions map[string]string
	issued   int
}

func (f *fakeLogin) IssueCode(_ context.Context, phone string) (string, error) {
	f.issued++
	f.codes[phone] = "123456"
	return "123456", nil
}
func (f *fakeLogin) VerifyCode(_ context.Context, phone, code string) (bool, error) {
	ok := f.codes[phone] != "" && f.codes[phone] == code
	if ok {
		delete(f.codes, phone)
	}
	return ok, nil
}
func (f *fakeLogin) StartSession(_ context.Context, subject string) (string, time.Time, error) {
	tok := "tok-" + subject
	f.sessions[tok] = subject
	return tok, time.Date(2026, 12, 17, 0, 0, 0, 0, time.UTC), nil
}
func (f *fakeLogin) LookupSession(_ context.Context, token string) (string, bool, error) {
	s, ok := f.sessions[token]
	return s, ok, nil
}
func (f *fakeLogin) EndSession(_ context.Context, token string) error {
	delete(f.sessions, token)
	return nil
}

type sentSMS struct{ to, text string }

// loginSite is the gate with phone login: 01011112222 is the worker w-1, every other number is refused.
func loginSite(t *testing.T, lookups *int, sent *[]sentSMS, sendErr error, failed *[]string, routes ...siteext.Route) (http.Handler, *fakeLogin) {
	t.Helper()
	h, store, _ := loginSiteWith(t, lookups, sent, sendErr, failed, nil, routes...)
	return h, store
}

// loginSiteWith is loginSite with the send step and the spawn under the test's control. spawn nil runs in place.
func loginSiteWith(t *testing.T, lookups *int, sent *[]sentSMS, sendErr error, failed *[]string,
	spawn func(func()), routes ...siteext.Route) (http.Handler, *fakeLogin, *siteAPI) {
	t.Helper()
	store := &fakeLogin{codes: map[string]string{}, sessions: map[string]string{}}
	if spawn == nil {
		spawn = func(f func()) { f() } // 시험은 발송을 기다린다 — 실제로는 응답 밖 고루틴이다
	}
	s := &siteAPI{
		routes:  routes,
		enabled: func(context.Context) (bool, error) { return true, nil },
		lookup: func(_ context.Context, raw string) (apikeys.Key, bool, error) {
			return apikeys.Key{ID: "k1", Name: "flutter"}, raw == "ondo_good", nil
		},
		limiter: auth.NewLimiter(),
		log:     slog.New(slog.DiscardHandler),
		// Lookup 은 그 앱 요청의 문맥으로 불려야 한다 — 사이트가 site.FromContext 로 DB 를 꺼낸다.
		phone: siteext.PhoneLogin{Lookup: func(ctx context.Context, phone string) (string, bool, error) {
			*lookups++
			if c, ok := siteext.CallerFrom(ctx); !ok || c.App != "flutter" {
				return "", false, errors.New("Lookup 의 문맥에 코어가 확인한 값이 없다")
			}
			return "w-1", phone == "01011112222", nil
		}},
		login:    store,
		siteName: func() string { return "주원산림" },
		send: func(_ context.Context, to, text string) error {
			*sent = append(*sent, sentSMS{to, text})
			return sendErr
		},
		sendFailed: func(phone string, err error) { *failed = append(*failed, phone+" "+err.Error()) },
		spawn:      spawn,
	}
	return s.handler(), store, s
}

func apiPost(h http.Handler, path, body string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer ondo_good")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// **문자 로그인 한 바퀴** (D42 8절): 번호로 인증번호를 받고, 그것으로 토큰을 받고, 토큰으로 site.Person 라우트를
// 부른다 — 처리기는 그 사람(Subject)을 안다. 로그아웃한 토큰은 401.
func TestPhoneLoginRoundTrip(t *testing.T) {
	var lookups int
	var sent []sentSMS
	var failed []string
	var subject string
	h, _ := loginSite(t, &lookups, &sent, nil, &failed, siteext.Route{Method: "GET", Path: "/v1/me", Auth: siteext.AuthPerson,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			c, _ := siteext.CallerFrom(r.Context())
			subject = c.Subject
			w.WriteHeader(http.StatusOK)
		}})

	if rec := apiPost(h, "/api/site/auth/code", `{"phone":"010-1111-2222"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("인증번호 요청 = %d %s", rec.Code, rec.Body)
	}
	if len(sent) != 1 || sent[0].to != "01011112222" || sent[0].text != "[주원산림] 인증번호 123456 (5분)" {
		t.Fatalf("보낸 문자 = %+v", sent)
	}
	rec := apiPost(h, "/api/site/auth/verify", `{"phone":"01011112222","code":"123456"}`)
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Token != "tok-w-1" || out.ExpiresAt.IsZero() {
		t.Fatalf("확인 = %d %s", rec.Code, rec.Body)
	}
	me := func(token string) int {
		req := httptest.NewRequest("GET", "/api/site/v1/me", nil)
		req.Header.Set("Authorization", "Bearer ondo_good")
		if token != "" {
			req.Header.Set("X-Ondolith-Session", token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := me(out.Token); code != 200 || subject != "w-1" {
		t.Fatalf("토큰으로 /v1/me = %d, Subject %q", code, subject)
	}
	subject = ""
	for name, tok := range map[string]string{"토큰 없음": "", "틀린 토큰": "tok-w-2"} {
		if code := me(tok); code != http.StatusUnauthorized || subject != "" {
			t.Errorf("%s = %d — 처리기를 부르지 않고 401", name, code)
		}
	}
	if rec := apiPost(h, "/api/site/auth/logout", "", "X-Ondolith-Session", out.Token); rec.Code != http.StatusNoContent {
		t.Errorf("로그아웃 = %d", rec.Code)
	}
	if code := me(out.Token); code != http.StatusUnauthorized {
		t.Errorf("로그아웃한 토큰 = %d, want 401", code)
	}
	if rec := apiPost(h, "/api/site/auth/verify", `{"phone":"01011112222","code":"123456"}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("쓴 인증번호로 다시 = %d, want 401", rec.Code)
	}
}

// **등록되지 않은 번호에도 똑같이 답한다** (D42 11절). 상태·본문·머리글이 같고, 그 번호에는 인증번호를 만들지도
// 보내지도 않는다. 발송 실패도 응답에 드러나지 않는다. 응답 전에 하는 일이 같다는 것은 TestCodeRequestAnswersBeforeLookingUp 이 본다.
func TestCodeRequestLooksTheSameForUnknownNumbers(t *testing.T) {
	var lookups int
	var sent []sentSMS
	var failed []string
	h, store := loginSite(t, &lookups, &sent, errors.New("SENS 401"), &failed)
	known := apiPost(h, "/api/site/auth/code", `{"phone":"01011112222"}`)
	unknown := apiPost(h, "/api/site/auth/code", `{"phone":"01099998888"}`)
	if known.Code != unknown.Code || known.Body.String() != unknown.Body.String() || len(known.Header()) != len(unknown.Header()) {
		t.Fatalf("등록 %d %q %v / 미등록 %d %q %v — 같아야 한다", known.Code, known.Body, known.Header(),
			unknown.Code, unknown.Body, unknown.Header())
	}
	for k := range known.Header() {
		if known.Header().Get(k) != unknown.Header().Get(k) {
			t.Errorf("머리글 %s 가 다르다", k)
		}
	}
	if store.issued != 1 || len(sent) != 1 || sent[0].to != "01011112222" {
		t.Errorf("인증번호 %d개·문자 %+v — 등록된 번호에만", store.issued, sent)
	}
	// 발송이 실패해도 응답은 202 이고, 실패는 남는다(번호는 코어가 가린다).
	if known.Code != http.StatusAccepted || len(failed) != 1 || !strings.HasPrefix(failed[0], "01011112222 ") {
		t.Errorf("발송 실패: 응답 %d, 남긴 것 %v", known.Code, failed)
	}
	if rec := apiPost(h, "/api/site/auth/code", `{"phone":"02-123-4567"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("휴대폰 번호가 아님 = %d, want 400", rec.Code)
	}
	if rec := apiPost(h, "/api/site/auth/code", `not json`); rec.Code != http.StatusBadRequest {
		t.Errorf("JSON 이 아님 = %d, want 400", rec.Code)
	}
}

// **문자 상한** (D42 8절): 번호당 10분 3통, IP 당 시간 20통. 상한은 Lookup 보다 먼저다 — 등록 여부와 관계없이 같은
// 순간에 429 가 된다.
func TestCodeRequestLimits(t *testing.T) {
	var lookups int
	var sent []sentSMS
	var failed []string
	h, _ := loginSite(t, &lookups, &sent, nil, &failed)
	for _, phone := range []string{"01011112222", "01099998888"} {
		for i := 1; i <= 4; i++ {
			rec := apiPost(h, "/api/site/auth/code", `{"phone":"`+phone+`"}`)
			want := http.StatusAccepted
			if i == 4 {
				want = http.StatusTooManyRequests
			}
			if rec.Code != want {
				t.Errorf("%s %d번째 = %d, want %d", phone, i, rec.Code, want)
			}
		}
	}
	if lookups != 6 {
		t.Errorf("Lookup %d번, want 6 — 막힌 요청은 Lookup 전에 끝난다", lookups)
	}
	// IP 상한: 지금까지 이 IP 로 8번(막힌 2번 포함) 썼다 — 번호를 바꿔 가며 12번이 더 되고 그 뒤는 막힌다.
	var codes []int
	for i := 0; i < 14; i++ {
		phone := "0102000" + string(rune('0'+i/10)) + string(rune('0'+i%10)) + "00"
		codes = append(codes, apiPost(h, "/api/site/auth/code", `{"phone":"`+phone+`"}`).Code)
	}
	for i, c := range codes {
		want := http.StatusAccepted
		if i >= 12 {
			want = http.StatusTooManyRequests
		}
		if c != want {
			t.Errorf("IP 의 %d번째 요청 = %d, want %d (시간당 20통)", 9+i, c, want)
		}
	}
}

// **확인 실패는 이유가 무엇이든 같은 401 이다** — 틀린 번호·없는 요청·그만둔 사람(Lookup 거절)을 나누면 등록 여부가 샌다.
func TestVerifyFailuresAllLookTheSame(t *testing.T) {
	var lookups int
	var sent []sentSMS
	var failed []string
	h, store := loginSite(t, &lookups, &sent, nil, &failed)
	store.codes["01099998888"] = "123456" // 요청은 있었는데 지금은 Lookup 이 허락하지 않는 번호
	var bodies []string
	for name, body := range map[string]string{
		"틀린 인증번호":   `{"phone":"01011112222","code":"999999"}`,
		"요청한 적 없음":  `{"phone":"01033334444","code":"123456"}`,
		"Lookup 거절": `{"phone":"01099998888","code":"123456"}`,
	} {
		rec := apiPost(h, "/api/site/auth/verify", body)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401", name, rec.Code)
		}
		bodies = append(bodies, rec.Body.String())
	}
	if bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Errorf("실패 본문이 다르다: %q", bodies)
	}
	if len(store.sessions) != 0 {
		t.Errorf("실패했는데 토큰이 생겼다: %v", store.sessions)
	}
}

// 문자 로그인을 쓰지 않는 사이트에는 그 주소가 없다 — 키를 확인한 뒤 다른 없는 주소처럼 404.
func TestAuthAddressesNeedPhoneLogin(t *testing.T) {
	on := true
	h := testSiteAPI(&on)
	if code, _, _ := call(h, "POST", "/api/site/auth/code", "ondo_good", `{"phone":"01011112222"}`); code != http.StatusNotFound {
		t.Errorf("문자 로그인 없는 사이트의 /auth/code = %d, want 404", code)
	}
}

// **응답 전에는 상한만 본다** (D42 11절) — Lookup·인증번호 저장·설정 읽기·발송은 모두 응답 밖이다. 응답 전에 하는
// 일이 등록 여부에 따라 다르면 걸리는 시간으로 등록된 번호를 가려낼 수 있다. Lookup 이 실패해도 응답은 같은 202 다.
func TestCodeRequestAnswersBeforeLookingUp(t *testing.T) {
	var lookups int
	var sent []sentSMS
	var failed []string
	var queued []func()
	h, store, _ := loginSiteWith(t, &lookups, &sent, nil, &failed, func(f func()) { queued = append(queued, f) })
	rec := apiPost(h, "/api/site/auth/code", `{"phone":"01011112222"}`)
	if rec.Code != http.StatusAccepted || lookups != 0 || store.issued != 0 || len(sent) != 0 || len(queued) != 1 {
		t.Fatalf("응답 때: %d, Lookup %d번, 인증번호 %d개, 문자 %d통, 미룬 일 %d개 — 응답 전에는 아무것도 하지 않는다",
			rec.Code, lookups, store.issued, len(sent), len(queued))
	}
	queued[0]()
	if lookups != 1 || store.issued != 1 || len(sent) != 1 {
		t.Errorf("응답 뒤: Lookup %d번, 인증번호 %d개, 문자 %d통", lookups, store.issued, len(sent))
	}
}

// **응답 밖의 일은 사이트 코드가 패닉해도 서버를 죽이지 않는다** — 문지기가 사이트 처리기의 패닉을 막듯이. 실패로
// 남긴다. Lookup 이 오류를 내도 그렇다.
func TestCodeSendSurvivesSiteCode(t *testing.T) {
	var lookups int
	var sent []sentSMS
	var failed []string
	h, _, s := loginSiteWith(t, &lookups, &sent, nil, &failed, nil)
	s.send = func(context.Context, string, string) error { panic("사이트 발송 함수의 실수") }
	if rec := apiPost(h, "/api/site/auth/code", `{"phone":"01011112222"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("응답 = %d", rec.Code)
	}
	if len(failed) != 1 || !strings.Contains(failed[0], "패닉") {
		t.Errorf("발송 함수 패닉이 실패로 남지 않았다: %v", failed)
	}
	s.phone.Lookup = func(context.Context, string) (string, bool, error) { return "", false, errors.New("DB 끊김") }
	if rec := apiPost(h, "/api/site/auth/code", `{"phone":"01011112223"}`); rec.Code != http.StatusAccepted {
		t.Errorf("Lookup 오류 = %d, want 202 — 등록된 번호만 겪는 실패가 응답에 드러나면 안 된다", rec.Code)
	}
}

// 발송 오류 문구에 인증번호·번호 원문이 섞여도 작업 로그에는 남지 않는다 — 업체 SDK 가 요청을 오류에 담는 일이 있다.
func TestSendFailureHidesTheCodeAndNumber(t *testing.T) {
	var lookups int
	var sent []sentSMS
	var failed []string
	h, _, s := loginSiteWith(t, &lookups, &sent, nil, &failed, nil)
	s.send = func(_ context.Context, to, text string) error {
		return errors.New("업체 거절: to=" + to + " body=" + text)
	}
	apiPost(h, "/api/site/auth/code", `{"phone":"01011112222"}`)
	if len(failed) != 1 {
		t.Fatalf("실패 %v", failed)
	}
	msg := strings.TrimPrefix(failed[0], "01011112222 ")
	if strings.Contains(msg, "123456") || strings.Contains(msg, "01011112222") || !strings.Contains(msg, "010****2222") {
		t.Errorf("남긴 오류 = %q — 인증번호와 번호 원문이 없고 가린 번호가 있어야 한다", msg)
	}
}

// 확인도 IP 마다 센다 — 번호마다는 인증번호의 다섯 번이 막지만, 한 곳에서 여러 번호를 두드리는 것은 이것이 막는다.
func TestVerifyIsLimitedPerIP(t *testing.T) {
	var lookups int
	var sent []sentSMS
	var failed []string
	h, _ := loginSite(t, &lookups, &sent, nil, &failed)
	for i := 1; i <= 31; i++ {
		rec := apiPost(h, "/api/site/auth/verify", `{"phone":"01011112222","code":"000000"}`)
		want := http.StatusUnauthorized
		if i == 31 {
			want = http.StatusTooManyRequests
		}
		if rec.Code != want {
			t.Fatalf("%d번째 확인 = %d, want %d (10분 30번)", i, rec.Code, want)
		}
	}
}

// 보낼 설정이 비어 있는 것은 작업 로그에 남기지 않는다 — 부팅 경고와 A-215 가 말하고, 그 문장은 작업 로그가
// 거부하는 낱말(시크릿)을 담는다. 그 밖의 실패는 가린 번호로 남는다.
func TestSMSFailureEntry(t *testing.T) {
	if _, ok := smsFailureEntry("01011112222", sms.ErrNotConfigured); ok {
		t.Error("설정이 비어 있는 것을 작업 로그에 남기려 한다")
	}
	e, ok := smsFailureEntry("01011112222", errors.New("HTTP 401"))
	if !ok || e.Action != "sms.failed" || e.TargetID != "010****2222" || strings.Contains(e.Summary, "01011112222") ||
		!strings.Contains(e.Summary, "HTTP 401") {
		t.Errorf("작업 로그 = %+v", e)
	}
}

// SENS 설정을 읽지 못하면 보내지 않고 그 까닭을 말한다. 사이트 발송 함수가 있으면 설정을 읽지도 않는다.
func TestSMSSenderReadsSettingsAtSendTime(t *testing.T) {
	read := 0
	broken := func(context.Context, ...string) (map[string]string, error) {
		read++
		return nil, errors.New("DB 끊김")
	}
	if err := smsSender(nil, broken)(context.Background(), "01011112222", "x"); err == nil || !strings.Contains(err.Error(), "DB 끊김") {
		t.Errorf("설정 읽기 실패 = %v", err)
	}
	empty := func(context.Context, ...string) (map[string]string, error) { read++; return map[string]string{}, nil }
	if err := smsSender(nil, empty)(context.Background(), "01011112222", "x"); !errors.Is(err, sms.ErrNotConfigured) {
		t.Errorf("빈 설정 = %v, want ErrNotConfigured", err)
	}
	custom := func(context.Context, string, string) error { return nil }
	read = 0
	if err := smsSender(custom, broken)(context.Background(), "01011112222", "x"); err != nil || read != 0 {
		t.Errorf("사이트 발송 함수 = %v, 설정 읽기 %d번 — 읽지 않아야 한다", err, read)
	}
}
