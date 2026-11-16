package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/apikeys"
	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/siteext"
)

// testSiteAPI is the gate with one good key ("ondo_good", 이름 flutter) and these routes.
func testSiteAPI(on *bool, routes ...siteext.Route) http.Handler {
	db := &sql.DB{} // 처리기에 그대로 건네지는지만 본다 — 열지 않는다
	return (&siteAPI{
		routes:  routes,
		enabled: func(context.Context) (bool, error) { return *on, nil },
		lookup: func(_ context.Context, raw string) (apikeys.Key, bool, error) {
			if raw == "ondo_good" {
				return apikeys.Key{ID: "k1", Name: "flutter"}, true, nil
			}
			return apikeys.Key{}, false, nil
		},
		limiter: auth.NewLimiter(),
		db:      db,
		log:     slog.New(slog.DiscardHandler),
	}).handler()
}

func call(h http.Handler, method, path, key, body string) (int, string, http.Header) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String(), rec.Header()
}

// **/api/site/ 의 문지기** (D42 5절): 켜짐 → 앱 키 → 키별 상한 → 등록된 주소 → 본문 상한 → 처리기(패닉은 500). 오류는
// 언제나 JSON `{"error": …}` 하나다. 처리기는 코어가 확인을 마친 값(어느 앱인지·DB)을 받는다.
func TestSiteAPIGate(t *testing.T) {
	on := true
	var seen siteext.Caller
	var readErr error
	h := testSiteAPI(&on,
		siteext.Route{Method: "GET", Path: "/v1/notes", Handler: func(w http.ResponseWriter, r *http.Request) {
			seen, _ = siteext.CallerFrom(r.Context())
			siteext.WriteJSON(w, http.StatusOK, map[string]string{"ok": "1"})
		}},
		// 읽기 오류를 스스로 413 으로 바꾸지 않는다 — 그러면 코어의 길이 검사가 없어도 413 이 나와 시험이 물지 않는다.
		siteext.Route{Method: "POST", Path: "/v1/notes", Handler: func(w http.ResponseWriter, r *http.Request) {
			_, readErr = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
		}},
		siteext.Route{Method: "GET", Path: "/v1/boom", Handler: func(http.ResponseWriter, *http.Request) { panic("사이트 코드의 실수") }},
	)

	for _, tc := range []struct {
		name         string
		method, path string
		key, body    string
		want         int
	}{
		{"성공", "GET", "/api/site/v1/notes", "ondo_good", "", 200},
		{"키 없음", "GET", "/api/site/v1/notes", "", "", 401},
		{"틀린 키", "GET", "/api/site/v1/notes", "ondo_bad", "", 401},
		{"등록되지 않은 주소 — 키가 있어야 404 를 안다", "GET", "/api/site/v1/nope", "ondo_good", "", 404},
		{"등록되지 않은 주소 — 키가 없으면 401", "GET", "/api/site/v1/nope", "", "", 401},
		{"메서드가 다르면 없는 주소", "DELETE", "/api/site/v1/notes", "ondo_good", "", 404},
		{"본문 1MB 까지", "POST", "/api/site/v1/notes", "ondo_good", strings.Repeat("a", 1<<20), 201},
		{"본문이 1MB 를 넘음", "POST", "/api/site/v1/notes", "ondo_good", strings.Repeat("a", 1<<20+1), 413},
		{"처리기 패닉", "GET", "/api/site/v1/boom", "ondo_good", "", 500},
	} {
		code, body, hdr := call(h, tc.method, tc.path, tc.key, tc.body)
		if code != tc.want {
			t.Errorf("%s: HTTP %d, want %d (%s)", tc.name, code, tc.want, body)
			continue
		}
		if code >= 400 {
			var e map[string]string
			if err := json.Unmarshal([]byte(body), &e); err != nil || e["error"] == "" ||
				!strings.HasPrefix(hdr.Get("Content-Type"), "application/json") {
				t.Errorf("%s: 오류가 JSON 하나의 모양이 아니다: %q %q", tc.name, hdr.Get("Content-Type"), body)
			}
		}
	}
	// 길이를 밝히지 않은 본문(chunked)은 문지기가 미리 볼 수 없다 — 처리기가 읽다가 1MB 에서 끊긴다.
	req := httptest.NewRequest("POST", "/api/site/v1/notes", io.NopCloser(strings.NewReader(strings.Repeat("a", 1<<20+1))))
	req.ContentLength = -1
	req.Header.Set("Authorization", "Bearer ondo_good")
	h.ServeHTTP(httptest.NewRecorder(), req)
	var tooBig *http.MaxBytesError
	if !errors.As(readErr, &tooBig) {
		t.Errorf("길이를 밝히지 않은 큰 본문을 끝까지 읽었다: %v", readErr)
	}
	if seen.App != "flutter" || seen.DB == nil || seen.Log == nil {
		t.Errorf("처리기가 받은 값 = %+v", seen)
	}

	// 꺼져 있으면 키가 맞아도 아무것도 없다.
	on = false
	if code, _, _ := call(h, "GET", "/api/site/v1/notes", "ondo_good", ""); code != 404 {
		t.Errorf("꺼짐: HTTP %d, want 404", code)
	}
}

// 키별 요청 상한 — 분당 120회. 넘으면 429 와 Retry-After. 다른 키는 따로 센다.
func TestSiteAPIRateLimitsPerKey(t *testing.T) {
	on := true
	h := testSiteAPI(&on, siteext.Route{Method: "GET", Path: "/v1/x", Handler: func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}})
	for i := range siteAPIPerMinute {
		if code, _, _ := call(h, "GET", "/api/site/v1/x", "ondo_good", ""); code != http.StatusNoContent {
			t.Fatalf("%d번째: HTTP %d", i+1, code)
		}
	}
	code, _, hdr := call(h, "GET", "/api/site/v1/x", "ondo_good", "")
	if code != http.StatusTooManyRequests || hdr.Get("Retry-After") == "" {
		t.Errorf("상한 넘음: HTTP %d, Retry-After %q", code, hdr.Get("Retry-After"))
	}
}

// 키를 찾다 DB 가 실패하면 500 이다 — 401 로 말하면 앱은 키가 틀린 줄 알고 사람을 부른다.
func TestSiteAPIStoreFailureIs500(t *testing.T) {
	h := (&siteAPI{
		routes:  []siteext.Route{{Method: "GET", Path: "/v1/x", Handler: func(http.ResponseWriter, *http.Request) {}}},
		enabled: func(context.Context) (bool, error) { return true, nil },
		lookup: func(context.Context, string) (apikeys.Key, bool, error) {
			return apikeys.Key{}, false, errors.New("DB 끊김")
		},
		limiter: auth.NewLimiter(), log: slog.New(slog.DiscardHandler),
	}).handler()
	if code, _, _ := call(h, "GET", "/api/site/v1/x", "ondo_good", ""); code != http.StatusInternalServerError {
		t.Errorf("HTTP %d, want 500", code)
	}
}

// **틀린 키로 두드려도 DB 조회가 끝없이 늘지 않는다.** 키별 상한은 맞는 키에만 걸린다 — 그 앞에서 IP 별로 분당
// siteAPIPerIP 회까지만 키를 찾는다. 한 현장의 인부 휴대폰들이 통신사 NAT 하나로 나와도 넉넉한 값이다.
func TestSiteAPILimitsLookupsPerIP(t *testing.T) {
	lookups := 0
	h := (&siteAPI{
		routes:  []siteext.Route{{Method: "GET", Path: "/v1/x", Handler: func(http.ResponseWriter, *http.Request) {}}},
		enabled: func(context.Context) (bool, error) { return true, nil },
		lookup: func(context.Context, string) (apikeys.Key, bool, error) {
			lookups++
			return apikeys.Key{}, false, nil
		},
		limiter: auth.NewLimiter(), log: slog.New(slog.DiscardHandler),
	}).handler()
	for range siteAPIPerIP {
		call(h, "GET", "/api/site/v1/x", "ondo_guess", "")
	}
	code, _, hdr := call(h, "GET", "/api/site/v1/x", "ondo_guess", "")
	if code != http.StatusTooManyRequests || hdr.Get("Retry-After") == "" || lookups != siteAPIPerIP {
		t.Errorf("HTTP %d, Retry-After %q, 키 조회 %d번 (want 429, %d번에서 멈춤)", code, hdr.Get("Retry-After"), lookups, siteAPIPerIP)
	}
}
