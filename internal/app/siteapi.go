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
	// siteAPIPerMinute is the per-key request ceiling (D42 5절). 앱 하나가 다른 앱과 사이트를 굶기지 않게.
	siteAPIPerMinute = 120
	// siteAPIPerIP bounds key lookups per client IP — 틀린 키로 두드려도 DB 조회가 끝없이 늘지 않게. 키별 상한은
	// 맞는 키에만 걸리므로 그 앞에 둔다. 한 현장의 휴대폰들이 통신사 NAT 하나로 나와도 넉넉하다.
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
}

func (s *siteAPI) handler() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range s.routes {
		mux.Handle(rt.Method+" "+siteext.APIPrefix+rt.Path, s.gate(rt.Handler))
	}
	// 등록되지 않은 주소와 메서드는 키를 확인한 뒤 404 다 — 키 없이 무엇이 있는지 알아내지 못하게. 이 패턴이
	// 모든 메서드를 받으므로 ServeMux 의 평문 405 도 나가지 않는다.
	mux.Handle(siteext.APIPrefix+"/", s.gate(nil))
	return mux
}

// gate runs D42 5절's checks in order; next == nil is an unregistered address.
func (s *siteAPI) gate(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		on, err := s.enabled(r.Context())
		if err != nil {
			s.fail(w, err)
			return
		}
		if !on {
			siteext.WriteError(w, http.StatusNotFound, "없는 주소입니다.")
			return
		}
		if !s.limiter.Allow("siteapi-ip:"+clientIP(r), auth.Limit{Burst: siteAPIPerIP, Window: time.Minute}) {
			tooMany(w)
			return
		}
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || raw == "" {
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
		if !s.limiter.Allow("siteapi:"+key.ID, auth.Limit{Burst: siteAPIPerMinute, Window: time.Minute}) {
			tooMany(w)
			return
		}
		if next == nil {
			siteext.WriteError(w, http.StatusNotFound, "없는 주소입니다.")
			return
		}
		if r.ContentLength > siteAPIMaxBody {
			siteext.WriteError(w, http.StatusRequestEntityTooLarge, "본문이 너무 큽니다.")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, siteAPIMaxBody) // 길이를 밝히지 않는 본문도 같은 선에서 끊는다
		log := s.log.With("app", key.Name)
		ctx := siteext.WithCaller(r.Context(), siteext.Caller{DB: s.db, App: key.Name, Log: log})
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
