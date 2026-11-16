package app

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/emirue/ondolith/internal/applogin"
	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/siteext"
	"github.com/emirue/ondolith/internal/sms"
)

// sessionHeader carries the app token of a site.Person request (D42 8절).
const sessionHeader = "X-Ondolith-Session"

// 문자 상한 (D42 8절) — 문자는 돈이고 남의 번호로 폭탄을 보낼 수 있다. **Lookup 보다 먼저 건다** — 등록 여부와
// 관계없이 같은 순간에 막혀야 429 로도 등록 여부가 새지 않는다.
//
// ponytail: 상한기는 메모리다. 재시작·A-201 재조립 때 풀리고, 번호당 상한은 앱 키(비밀 아님)를 가진 누구나 남의
// 재로그인을 막을 수 있다(이미 받은 토큰은 그대로다). 비용이 문제되면 DB 로 센다.
var (
	smsPerPhone10m = auth.Limit{Burst: 3, Window: 10 * time.Minute}
	smsPerPhoneDay = auth.Limit{Burst: 10, Window: 24 * time.Hour}
	smsPerIPHour   = auth.Limit{Burst: 20, Window: time.Hour}
	// verifyPerIP bounds guesses across numbers from one place. 번호마다는 인증번호의 다섯 번이 막는다.
	verifyPerIP = auth.Limit{Burst: 30, Window: 10 * time.Minute}
	// smsSendTimeout bounds a send outside the response.
	smsSendTimeout = 30 * time.Second
)

// appLogin is applogin.Store — 문지기 시험이 메모리 가짜를 끼운다.
type appLogin interface {
	IssueCode(ctx context.Context, phone string) (string, error)
	VerifyCode(ctx context.Context, phone, code string) (bool, error)
	StartSession(ctx context.Context, subject string) (string, time.Time, error)
	LookupSession(ctx context.Context, token string) (string, bool, error)
	EndSession(ctx context.Context, token string) error
}

// authCode is POST /api/site/auth/code {"phone"} (D42 8절 1).
//
// **허락한 번호와 아닌 번호에 똑같이 답한다** — 상태·본문이 같고, 발송은 응답 밖이라 걸리는 시간도 같다. 발송이
// 실패해도 응답에 드러나지 않는다(그것도 등록 여부다). 실패는 작업 로그에 남는다.
func (s *siteAPI) authCode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Phone string `json:"phone"`
	}
	phone, ok := "", false
	if json.NewDecoder(r.Body).Decode(&in) == nil {
		phone, ok = applogin.NormalizePhone(in.Phone)
	}
	if !ok {
		siteext.WriteError(w, http.StatusBadRequest, "휴대폰 번호가 필요합니다.")
		return
	}
	if !s.limiter.Allow("sms-ip:"+clientIP(r), smsPerIPHour) ||
		!s.limiter.Allow("sms-10m:"+phone, smsPerPhone10m) || !s.limiter.Allow("sms-day:"+phone, smsPerPhoneDay) {
		w.Header().Set("Retry-After", "600")
		siteext.WriteError(w, http.StatusTooManyRequests, "인증번호 요청이 너무 많습니다. 잠시 후 다시 시도하세요.")
		return
	}
	_, allowed, err := s.phone.Lookup(r.Context(), phone)
	if err != nil {
		s.fail(w, err)
		return
	}
	if allowed {
		s.sendCode(r, phone)
	}
	siteext.WriteJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}

// sendCode makes the code and sends it outside the response. 저장이나 발송이 실패해도 응답은 같다 — 로그와 작업
// 로그에 남는다. 설정은 요청 안에서 읽는다.
func (s *siteAPI) sendCode(r *http.Request, phone string) {
	code, err := s.login.IssueCode(r.Context(), phone)
	if err != nil {
		s.log.Error("인증번호 저장", "to", sms.Mask(phone), "err", err)
		return
	}
	send, text := s.sender(r.Context()), sms.LoginText(s.siteName(), code)
	s.spawn(func() {
		ctx, cancel := context.WithTimeout(context.Background(), smsSendTimeout)
		defer cancel()
		if err := send(ctx, phone, text); err != nil {
			s.sendFailed(phone, err)
		}
	})
}

// authVerify is POST /api/site/auth/verify {"phone","code"} (D42 8절 2). 맞으면 앱 토큰을 준다.
// **실패는 이유가 무엇이든 같은 401 이다** — 틀림·지남·다섯 번·요청 없음·그만둔 사람(Lookup 거절).
func (s *siteAPI) authVerify(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Phone string `json:"phone"`
		Code  string `json:"code"`
	}
	phone, ok := "", false
	if json.NewDecoder(r.Body).Decode(&in) == nil {
		phone, ok = applogin.NormalizePhone(in.Phone)
	}
	if !ok {
		siteext.WriteError(w, http.StatusBadRequest, "휴대폰 번호가 필요합니다.")
		return
	}
	if !s.limiter.Allow("verify-ip:"+clientIP(r), verifyPerIP) {
		tooMany(w)
		return
	}
	refuse := func() {
		siteext.WriteError(w, http.StatusUnauthorized, "인증번호가 맞지 않거나 시간이 지났습니다.")
	}
	good, err := s.login.VerifyCode(r.Context(), phone, in.Code)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !good {
		refuse()
		return
	}
	// 인증번호를 보낸 뒤 그만뒀을 수 있다 — 대상도 이때 받는다.
	subject, allowed, err := s.phone.Lookup(r.Context(), phone)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !allowed {
		refuse()
		return
	}
	token, expires, err := s.login.StartSession(r.Context(), subject)
	if err != nil {
		s.fail(w, err)
		return
	}
	siteext.WriteJSON(w, http.StatusOK, map[string]any{"token": token, "expires_at": expires})
}

// authLogout is POST /api/site/auth/logout — 그 토큰을 끊는다. 없는 토큰도 204 다(이미 끊긴 것과 같다).
func (s *siteAPI) authLogout(w http.ResponseWriter, r *http.Request) {
	if tok := r.Header.Get(sessionHeader); tok != "" {
		if err := s.login.EndSession(r.Context(), tok); err != nil {
			s.fail(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
