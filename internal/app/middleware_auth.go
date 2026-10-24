package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/alexedwards/scs/v2"

	"github.com/emirue/ondolith/internal/auth"
)

// Session keys. Kept here so that no handler invents its own spelling.
const (
	sessUserID = "user_id"
	sessAuthAt = "auth_at"
	sessReauth = "reauth_at"
	// sessCartKey 는 비회원 장바구니의 열쇠다. 쿠키에 직접 두면 값이
	// 클라이언트 것이 되고, 남의 키를 적어 넣는 것만으로 남의 장바구니를
	// 연다 (SC-3).
	sessCartKey = "cart_key"
)

// Actor is who the request is. Assembled once, in the middleware, and read from
// the context by handlers.
//
// WHAT THIS MIDDLEWARE MUST NOT DO (D15 4.2): resource-level judgement. It does
// not parse a board slug, it does not look at path segments to decide what is
// being accessed, and it does not answer "may this person edit THAT post". The
// middleware knows the caller; the handler knows the target. A middleware that
// starts parsing paths drifts out of step with the routes the moment one is
// added, and the drift is silent.
type Actor struct {
	// User is nil for an anonymous request. Anonymous is not an error — it is
	// most of the traffic.
	User *auth.User
	// Perms is the caller's whole permission set, loaded once (D15 4.3-1).
	Perms *auth.Permissions
	// AuthAt is when this session authenticated, used by the re-auth gate.
	AuthAt time.Time
	// ReauthAt is the last password re-confirmation (D15 5.3-1).
	ReauthAt time.Time
}

// IsAuthenticated reports whether a user is attached.
func (a *Actor) IsAuthenticated() bool { return a != nil && a.User != nil }

// Can is the display/permission shortcut handlers use.
func (a *Actor) Can(perm string) bool {
	if a == nil {
		return false
	}
	return a.Perms.Can(perm)
}

// CanOn is the board-scoped form.
func (a *Actor) CanOn(perm string, board auth.BoardID) bool {
	if a == nil {
		return false
	}
	return a.Perms.CanOn(perm, board)
}

// Times go into the session as Unix nanoseconds, not as time.Time.
//
// scs gob-encodes session data, and an unregistered type makes the commit fail
// — which surfaces as HTTP 500 on login, at runtime, with nothing at compile
// time to warn anyone. `gob.Register(time.Time{})` fixes it, but it is a global
// registration somebody can forget in the next place a session manager is
// built. An int64 has nothing to forget.
func putTime(sm *scs.SessionManager, ctx context.Context, key string, t time.Time) {
	sm.Put(ctx, key, t.UnixNano())
}

// stampAuthAt is the only writer of auth_at.
//
// It takes an [auth.DBTime] — a value only the database can produce — because
// withActor compares auth_at against sessions_valid_from, and that column is
// written by the database's clock. See auth.DBTime for what goes wrong when the
// two sides come off different clocks. Nothing else may write this key; the
// package's own test enforces that, and the type enforces the value.
func stampAuthAt(sm *scs.SessionManager, ctx context.Context, at auth.DBTime) {
	sm.Put(ctx, sessAuthAt, at.Time().UnixNano())
}

func getTime(sm *scs.SessionManager, ctx context.Context, key string) time.Time {
	n := sm.GetInt64(ctx, key)
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

type actorKey struct{}

// withActorValue attaches a: exported to the package's own tests so the gate
// can be exercised without a database behind it.
func withActorValue(ctx context.Context, a *Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFrom returns the request's Actor, never nil. A handler that reaches for
// permissions before the middleware ran gets an empty set rather than a panic —
// and an empty set refuses everything, which is the safe direction.
func ActorFrom(ctx context.Context) *Actor {
	a, _ := ctx.Value(actorKey{}).(*Actor)
	if a == nil {
		return &Actor{Perms: auth.NewPermissions(false, nil)}
	}
	return a
}

// withActor builds the Actor for every request (D15 4.1 [3]).
//
// Three refusals live here, and each one exists because the alternative leaves
// a way in:
//
//   - a session naming a user who no longer exists → session destroyed
//   - a deactivated account → session destroyed, not merely "no permissions".
//     Leaving the session alive would let a reactivation hand back a session
//     that was supposed to be over.
//   - a session older than the account's sessions_valid_from → destroyed. This
//     is what makes "log out everywhere" and "password changed" actually end
//     the other sessions (D15 5.4), and it is checked on every request because
//     that is the only moment we can.
func withActor(sm *scs.SessionManager, store *auth.Store, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			a := &Actor{}

			userID := sm.GetString(ctx, sessUserID)
			if userID != "" {
				u, err := store.FindUserByID(ctx, userID)
				switch {
				case errors.Is(err, auth.ErrNoUser):
					sm.Destroy(ctx) //nolint:errcheck // destroying a stale session cannot fail usefully
				case err != nil:
					plainServerError(log, w, r, err)
					return
				case !u.IsActive:
					sm.Destroy(ctx) //nolint:errcheck
				default:
					authAt := getTime(sm, ctx, sessAuthAt)
					if authAt.Before(u.SessionsValidFrom) {
						// Issued before the cutoff: this is the session that
						// "log out everywhere" was aimed at.
						sm.Destroy(ctx) //nolint:errcheck
					} else {
						a.User = u
						a.AuthAt = authAt
						a.ReauthAt = getTime(sm, ctx, sessReauth)
					}
				}
			}

			var err error
			if a.User != nil {
				a.Perms, err = store.LoadPermissions(ctx, a.User.ID)
			} else {
				a.Perms, err = store.LoadAnonymousPermissions(ctx)
			}
			if err != nil {
				plainServerError(log, w, r, err)
				return
			}

			next.ServeHTTP(w, r.WithContext(withActorValue(ctx, a)))
		})
	}
}

// plainServerError answers a failure the visitor cannot fix with the plain line
// the login screens have always used, and puts the cause in the log (NFR-210).
// 저장소가 죽으면 모든 요청이 여기(withActor)서 500 이 된다 — 운영자가 장애를 알
// 곳은 로그뿐이다. 로그인·계정 화면도 같은 줄을 쓴다.
func plainServerError(log *slog.Logger, w http.ResponseWriter, r *http.Request, err error) {
	logFailure(log, r, err)
	http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
}

// logFailure is the one log line a 500 leaves: the cause, not the screen's
// sentence (D22 3절). 공개 화면(P-904)·로그인 화면·세션 확인이 같은 줄을 쓴다.
//
// 손님이 연결을 끊어 취소된 요청은 남기지 않는다 — 장애가 아니고, 남기면 연결을
// 끊는 것만으로 누구나 Error 를 찍을 수 있다.
func logFailure(log *slog.Logger, r *http.Request, err error) {
	if log == nil || errors.Is(err, context.Canceled) {
		return
	}
	log.Error("요청 처리 실패", "path", logPath(r), "err", err)
}

// tokenPaths are the GET links whose last segment is a credential — the reset
// and verification mails (P-105·P-112). TestFailureLogHidesTokens 가 라우트 표의
// `{token}` 과 대조한다.
var tokenPaths = []string{"/password/reset/", "/verify/"}

// logPath is the request path with a credential segment replaced by `{token}`
// (D15 7절). 쓰지 않은 재설정 토큰은 그 자체가 로그인이다 — 로그를 읽을 수 있는
// 누구에게나 계정이 간다.
func logPath(r *http.Request) string {
	if r.Method == http.MethodGet {
		for _, p := range tokenPaths {
			if rest, ok := strings.CutPrefix(r.URL.Path, p); ok && rest != "" && !strings.Contains(rest, "/") {
				return p + "{token}"
			}
		}
	}
	return r.URL.Path
}
