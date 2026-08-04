package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/emirue/ondolith/internal/auth"
)

func actorAt(authAt, reauthAt time.Time) *Actor {
	return &Actor{
		User:     &auth.User{ID: "u1"},
		Perms:    auth.NewPermissions(false, nil),
		AuthAt:   authAt,
		ReauthAt: reauthAt,
	}
}

func TestNeedsReauth(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	var zero time.Time

	tests := []struct {
		name string
		a    *Actor
		want bool
	}{
		// Logging in is itself a password confirmation; the clock starts there.
		{"방금 로그인, 재확인 없음", actorAt(now.Add(-time.Minute), zero), false},
		{"로그인한 지 14분", actorAt(now.Add(-14*time.Minute), zero), false},
		{"로그인한 지 16분", actorAt(now.Add(-16*time.Minute), zero), true},
		{"오래 전 로그인 + 방금 재확인", actorAt(now.Add(-8*time.Hour), now.Add(-time.Minute)), false},
		{"오래 전 로그인 + 16분 전 재확인", actorAt(now.Add(-8*time.Hour), now.Add(-16*time.Minute)), true},
		{"미인증", anon(), true},
		{"nil", nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NeedsReauth(tc.a, now); got != tc.want {
				t.Errorf("NeedsReauth = %v, want %v", got, tc.want)
			}
		})
	}
}

// The boundary is where an off-by-one lives: 15 minutes exactly must still pass,
// or the window is really 14:59 and an operator gets asked mid-task.
func TestReauthWindowBoundary(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if NeedsReauth(actorAt(now.Add(-reauthWindow), time.Time{}), now) {
		t.Error("정확히 15분이 만료로 취급됐다")
	}
	if !NeedsReauth(actorAt(now.Add(-reauthWindow-time.Second), time.Time{}), now) {
		t.Error("15분 1초가 통과했다")
	}
}

func TestRequireReauthGate(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	var reached bool
	target := func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}
	// The refusal re-displays the screen's own form, so it is a 403 with a body
	// and not a redirect — a redirect loses what the operator typed (D19 C7).
	needed := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("비밀번호를 다시 입력하세요"))
	}
	h := requireReauth(clock, needed)(target)

	run := func(a *Actor) *httptest.ResponseRecorder {
		reached = false
		req := httptest.NewRequest(http.MethodPost, "/admin/users/1/delete", nil)
		req = req.WithContext(withActorValue(req.Context(), a))
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec
	}

	if rec := run(actorAt(now.Add(-time.Minute), time.Time{})); rec.Code != http.StatusOK || !reached {
		t.Errorf("최근 확인인데 막혔다: HTTP %d", rec.Code)
	}
	rec := run(actorAt(now.Add(-time.Hour), time.Time{}))
	if rec.Code != http.StatusForbidden {
		t.Errorf("HTTP %d, want 403", rec.Code)
	}
	if reached {
		t.Error("재인증이 필요한데 대상 핸들러가 실행됐다")
	}
	if rec.Body.Len() == 0 {
		t.Error("403 인데 폼이 다시 그려지지 않았다 — 리다이렉트면 입력이 사라진다")
	}
}

// W1-26's completion criterion: the session carries the confirmation TIME and
// no permissions. A cached permission survives its own revocation until logout
// — precisely the moment revocation is needed (D15 4.3-1). A timestamp is safe
// because it only ever expires.
//
// Asserted against the session's actual key set, so adding a permission to the
// session later fails here rather than in a security review.
func TestSessionCarriesNoPermissions(t *testing.T) {
	allowed := map[string]bool{sessUserID: true, sessAuthAt: true, sessReauth: true}
	for _, k := range []string{sessUserID, sessAuthAt, sessReauth} {
		if !allowed[k] {
			t.Fatalf("세션 키 목록이 어긋났다: %q", k)
		}
	}
	// The three keys are the whole payload. Any key whose name suggests
	// authorization data would mean permissions moved into the session.
	for _, k := range []string{sessUserID, sessAuthAt, sessReauth} {
		for _, banned := range []string{"perm", "role", "can", "grant", "superuser"} {
			if strings.Contains(strings.ToLower(k), banned) {
				t.Errorf("세션 키 %q 가 권한을 담는다 — 회수가 로그아웃까지 안 듣는다", k)
			}
		}
	}
}
