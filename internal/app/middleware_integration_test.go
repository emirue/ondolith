package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/alexedwards/scs/v2/memstore"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/migrations"
)

func authFixture(t *testing.T) (*auth.Store, *pgxpool.Pool, *scs.SessionManager) {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s 미설정 — 통합 테스트를 건너뜁니다 (make test-integration)", dsnEnv)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, stmt := range []string{"DROP SCHEMA public CASCADE", "CREATE SCHEMA public"} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { db.Close() })
	if err := migrations.Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	sm := newSessionManager(memstore.New(), false)
	return auth.NewStore(pool), pool, sm
}

// loginStamp is the value production puts in auth_at — the database's clock
// (auth.Store.Now), not the process's.
//
// A test that stamps time.Now() instead re-creates the bug the handlers were
// fixed for: withActor compares auth_at against sessions_valid_from, which the
// database wrote, and on a machine whose database runs a couple of milliseconds
// ahead the session of a just-created account is destroyed on the very next
// request. That shows up as an unexplainable flake — the same test passes when
// enough other tests ran first to burn off the skew — so the fixtures use the
// same clock the handlers do.
func loginStamp(t *testing.T, store *auth.Store) time.Time {
	t.Helper()
	at, err := store.Now(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// runWithSession drives one request whose session was prepared by setup.
// The returned Actor is what the middleware built.
func runWithSession(t *testing.T, sm *scs.SessionManager, store *auth.Store,
	setup func(ctx context.Context)) (*Actor, int) {
	t.Helper()

	var seen *Actor
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = ActorFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	h := sm.LoadAndSave(withActor(sm, store)(inner))

	// First pass: populate the session and capture its cookie.
	rec := httptest.NewRecorder()
	prep := sm.LoadAndSave(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setup(r.Context())
	}))
	prep.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	out := httptest.NewRecorder()
	h.ServeHTTP(out, req)
	return seen, out.Code
}

func TestActorIsAnonymousWithoutSession(t *testing.T) {
	store, _, sm := authFixture(t)
	a, code := runWithSession(t, sm, store, func(ctx context.Context) {})
	if code != http.StatusOK {
		t.Fatalf("HTTP %d", code)
	}
	if a.IsAuthenticated() {
		t.Error("세션이 없는데 인증됐다")
	}
	if a.Perms == nil {
		t.Error("익명 권한 집합이 로드되지 않았다")
	}
}

func TestActorLoadsUserAndPermissions(t *testing.T) {
	store, pool, sm := authFixture(t)
	ctx := context.Background()
	id, err := store.CreateUser(ctx, "op@example.com", "h", "운영자")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE key='operator'`, id); err != nil {
		t.Fatal(err)
	}

	a, _ := runWithSession(t, sm, store, func(c context.Context) {
		sm.Put(c, sessUserID, id)
		putTime(sm, c, sessAuthAt, loginStamp(t, store))
	})
	if !a.IsAuthenticated() {
		t.Fatal("세션이 있는데 익명이다")
	}
	if !a.Can("admin.access") {
		t.Error("operator 인데 admin.access 가 없다")
	}
}

// A deactivated account must lose its session, not merely its permissions.
// Leaving the session alive means reactivating the account hands back a session
// that was supposed to be over.
func TestDeactivatedAccountLosesSession(t *testing.T) {
	store, pool, sm := authFixture(t)
	ctx := context.Background()
	id, err := store.CreateUser(ctx, "off@example.com", "h", "x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET is_active = false WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	a, _ := runWithSession(t, sm, store, func(c context.Context) {
		sm.Put(c, sessUserID, id)
		putTime(sm, c, sessAuthAt, loginStamp(t, store))
	})
	if a.IsAuthenticated() {
		t.Error("비활성 계정이 인증 상태로 남았다")
	}
}

// D15 5.4: this is what makes "log out everywhere" and a password change
// actually end the other sessions. Checked on every request, because that is
// the only moment we can reach them.
func TestSessionOlderThanCutoffIsRejected(t *testing.T) {
	store, _, sm := authFixture(t)
	ctx := context.Background()
	id, err := store.CreateUser(ctx, "old@example.com", "h", "x")
	if err != nil {
		t.Fatal(err)
	}

	// A session authenticated before the account's cutoff.
	old := time.Now().Add(-time.Hour)
	if err := store.InvalidateSessions(ctx, id); err != nil {
		t.Fatal(err)
	}
	a, _ := runWithSession(t, sm, store, func(c context.Context) {
		sm.Put(c, sessUserID, id)
		putTime(sm, c, sessAuthAt, old)
	})
	if a.IsAuthenticated() {
		t.Error("컷오프 이전 세션이 살아남았다 — 전체 로그아웃이 듣지 않는다")
	}

	// ...while a session issued after it survives.
	a2, _ := runWithSession(t, sm, store, func(c context.Context) {
		sm.Put(c, sessUserID, id)
		putTime(sm, c, sessAuthAt, loginStamp(t, store))
	})
	if !a2.IsAuthenticated() {
		t.Error("컷오프 이후 세션이 거부됐다")
	}
}

func TestSessionNamingMissingUserIsDestroyed(t *testing.T) {
	store, _, sm := authFixture(t)
	a, code := runWithSession(t, sm, store, func(c context.Context) {
		sm.Put(c, sessUserID, "00000000-0000-0000-0000-000000000000")
		putTime(sm, c, sessAuthAt, loginStamp(t, store))
	})
	if code != http.StatusOK {
		t.Fatalf("HTTP %d — 없는 사용자는 오류가 아니라 익명이어야 한다", code)
	}
	if a.IsAuthenticated() {
		t.Error("존재하지 않는 사용자로 인증됐다")
	}
}
