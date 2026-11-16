package app

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/emirue/ondolith/internal/apikeys"
	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/siteext"
)

const (
	// siteAPIPerMinute is the ceiling per (app key, client IP) (D42 5절). **키 하나에 걸지 않는다** — 키는 비밀이
	// 아니라서(앱을 풀면 나온다) 키 전체에 걸면 키를 꺼낸 한 사람이 그 앱의 모든 사용자를 429 로 막는다.
	siteAPIPerMinute = 120
	// siteAPIPerIP bounds every request per client IP, before anything reads the DB — 「켜짐」 확인도 DB 를 읽는다.
	// 틀린 키로 두드리거나 꺼진 문을 두드려도 DB 조회가 끝없이 늘지 않는다. 한 현장의 휴대폰들이 통신사 NAT 하나로
	// 나와도 넉넉하다.
	siteAPIPerIP = 600
	// siteAPIMaxBody bounds a request body. 앱이 보내는 것은 JSON 이다 — 파일은 이 길로 받지 않는다.
	siteAPIMaxBody = 1 << 20
)

// siteAPI serves /api/site/ outside the main tree, like the payment webhook (D15 SC-9):
// 세션·액터·CSRF 가 붙지 않는다. 앱은 쿠키가 없고, 교차 출처 보호는 Origin·Sec-Fetch-Site 없는 요청을 통과시키지만
// 그 우연에 기대지 않고 앱 키로 막는다.
type siteAPI struct {
	routes  []siteext.Route
	enabled func(context.Context) (bool, error)
	lookup  func(ctx context.Context, raw string) (apikeys.Key, bool, error)
	limiter *auth.Limiter
	db      *sql.DB
	log     *slog.Logger

	// 문자 인증 로그인 (D42 8절, sitelogin.go). phone.Lookup 이 nil 이면 /auth/ 주소가 없다.
	phone siteext.PhoneLogin
	login appLogin
	// siteName is the 「[사이트 이름]」 of the SMS.
	siteName func() string
	// send delivers one SMS — 사이트의 발송 함수나, A-215 설정을 그때 읽는 SENS.
	send func(ctx context.Context, to, text string) error
	// sendFailed records a send that did not go out (작업 로그). 응답은 이미 나갔다.
	sendFailed func(phone string, err error)
	// spawn runs the send outside the response; tests run it in place.
	spawn func(func())
}

func (s *siteAPI) handler() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range s.routes {
		mux.Handle(rt.Method+" "+siteext.APIPrefix+rt.Path, s.gate(rt.Handler, rt.Auth))
	}
	if s.phone.Lookup != nil {
		mux.Handle("POST "+siteext.APIPrefix+"/auth/code", s.gate(s.authCode, siteext.AuthApp))
		mux.Handle("POST "+siteext.APIPrefix+"/auth/verify", s.gate(s.authVerify, siteext.AuthApp))
		mux.Handle("POST "+siteext.APIPrefix+"/auth/logout", s.gate(s.authLogout, siteext.AuthApp))
	}
	// 등록되지 않은 주소와 메서드는 키를 확인한 뒤 404 다 — 키 없이 무엇이 있는지 알아내지 못하게. 이 패턴이
	// 모든 메서드를 받으므로 ServeMux 의 평문 405 도 나가지 않는다.
	mux.Handle(siteext.APIPrefix+"/", s.gate(nil, siteext.AuthApp))
	return mux
}

// gate runs D42 5절's checks in order; next == nil is an unregistered address.
func (s *siteAPI) gate(next http.HandlerFunc, need siteext.Auth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if !s.limiter.Allow("siteapi-ip:"+ip, auth.Limit{Burst: siteAPIPerIP, Window: time.Minute}) {
			tooMany(w)
			return
		}
		on, err := s.enabled(r.Context())
		if err != nil {
			s.fail(w, err)
			return
		}
		if !on {
			siteext.WriteError(w, http.StatusNotFound, "없는 주소입니다.")
			return
		}
		raw, ok := bearer(r)
		if !ok {
			siteext.WriteError(w, http.StatusUnauthorized, "앱 키가 필요합니다.")
			return
		}
		key, found, err := s.lookup(r.Context(), raw)
		if err != nil {
			s.fail(w, err)
			return
		}
		if !found {
			siteext.WriteError(w, http.StatusUnauthorized, "앱 키가 맞지 않습니다.")
			return
		}
		if !s.limiter.Allow("siteapi:"+key.ID+"|"+ip, auth.Limit{Burst: siteAPIPerMinute, Window: time.Minute}) {
			tooMany(w)
			return
		}
		if next == nil {
			siteext.WriteError(w, http.StatusNotFound, "없는 주소입니다.")
			return
		}
		// 사람 인증 (D42 5절 4): 문자 로그인으로 받은 토큰. 없거나 지났거나 끊긴 토큰은 처리기를 부르지 않는다.
		var subject string
		if need == siteext.AuthPerson {
			sub, ok, err := s.login.LookupSession(r.Context(), r.Header.Get(sessionHeader))
			if err != nil {
				s.fail(w, err)
				return
			}
			if !ok {
				siteext.WriteError(w, http.StatusUnauthorized, "로그인이 필요합니다.")
				return
			}
			subject = sub
		}
		if r.ContentLength > siteAPIMaxBody {
			siteext.WriteError(w, http.StatusRequestEntityTooLarge, "본문이 너무 큽니다.")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, siteAPIMaxBody) // 길이를 밝히지 않는 본문도 같은 선에서 끊는다
		log := s.log.With("app", key.Name)
		ctx := siteext.WithCaller(r.Context(), siteext.Caller{DB: s.db, App: key.Name, Log: log, Subject: subject})
		rw := &wroteWriter{ResponseWriter: w}
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				log.Error("사이트 API 처리기 패닉", "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				if !rw.wrote {
					siteext.WriteError(w, http.StatusInternalServerError, "서버 오류입니다.")
				}
			}
		}()
		next(rw, r.WithContext(ctx))
	})
}

// bearer reads "Authorization: Bearer <key>". 인증 방식 이름은 대소문자를 가리지 않는다 (RFC 9110 11.1).
func bearer(r *http.Request) (string, bool) {
	scheme, raw, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	raw = strings.TrimSpace(raw)
	return raw, ok && strings.EqualFold(scheme, "Bearer") && raw != ""
}

func tooMany(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(int(time.Minute/time.Second)))
	siteext.WriteError(w, http.StatusTooManyRequests, "요청이 너무 많습니다. 잠시 후 다시 시도하세요.")
}

func (s *siteAPI) fail(w http.ResponseWriter, err error) {
	s.log.Error("사이트 API", "err", err)
	siteext.WriteError(w, http.StatusInternalServerError, "서버 오류입니다.")
}

// wroteWriter remembers whether the handler started its response, so a panic after that is not
// answered with a second status line.
type wroteWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *wroteWriter) WriteHeader(code int) {
	w.wrote = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *wroteWriter) Write(b []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the real writer (flush, deadlines).
func (w *wroteWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// siteAPILines is A-213's list of this binary's site API, "METHOD /api/site/… (인증 방식)". 문자 로그인이 있으면
// 코어의 /auth/ 주소도 적는다 — 앱 개발자가 보는 목록이다.
func siteAPILines(ext siteext.Extension) []string {
	var lines []string
	if ext.Phone.Lookup != nil {
		for _, op := range []string{"code", "verify", "logout"} {
			lines = append(lines, "POST "+siteext.APIPrefix+"/auth/"+op+" (문자 로그인 — 코어)")
		}
	}
	for _, rt := range ext.API {
		how := "앱 키"
		if rt.Auth == siteext.AuthPerson {
			how = "앱 키 + 로그인"
		}
		lines = append(lines, rt.Method+" "+siteext.APIPrefix+rt.Path+" ("+how+")")
	}
	return lines
}
