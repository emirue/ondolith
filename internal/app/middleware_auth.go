package app

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/alexedwards/scs/v2"

	"github.com/emirue/ondolith/internal/auth"
)

// Session keys. Kept here so that no handler invents its own spelling.
const (
	sessUserID = "user_id"
	sessAuthAt = "auth_at"
	sessReauth = "reauth_at"
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
func withActor(sm *scs.SessionManager, store *auth.Store) func(http.Handler) http.Handler {
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
					http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
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
				http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
				return
			}

			next.ServeHTTP(w, r.WithContext(withActorValue(ctx, a)))
		})
	}
}
