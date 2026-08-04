package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/emirue/ondolith/internal/migrations"
)

const dsnEnv = "ONDOLITH_TEST_DSN"

func testStore(t *testing.T) (*Store, *pgxpool.Pool) {
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
	return NewStore(pool), pool
}

func mkUser(t *testing.T, s *Store, email string) string {
	t.Helper()
	id, err := s.CreateUser(context.Background(), email, "hash", email)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func grantRole(t *testing.T, pool *pgxpool.Pool, userID, roleKey string) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE key = $2`,
		userID, roleKey)
	if err != nil {
		t.Fatal(err)
	}
}

// countingTracer counts the queries the driver actually sends. Server-side
// counters (pg_stat_database) are updated asynchronously and count
// transactions, so they answered 0 for a query that had just run — a counter
// that reports zero is worse than none, because the assertion still passes when
// the number is meaningless.
type countingTracer struct{ n atomic.Int64 }

func (c *countingTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.n.Add(1)
	return ctx
}
func (c *countingTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// NFR-105 / D15 4.3: the whole permission set in one query. Two queries here
// would make "judging every menu entry is free" false, and that claim is why a
// private board can be hidden from the menu at all.
func TestLoadPermissionsUsesOneQuery(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "op@example.com")
	grantRole(t, pool, u, "operator")

	// A second pool, traced, pointed at the same database.
	cfg, err := pgxpool.ParseConfig(os.Getenv(dsnEnv))
	if err != nil {
		t.Fatal(err)
	}
	tr := &countingTracer{}
	cfg.ConnConfig.Tracer = tr
	traced, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer traced.Close()

	p, err := NewStore(traced).LoadPermissions(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Can("settings.update") {
		t.Error("operator 인데 settings.update 가 없다")
	}
	if n := tr.n.Load(); n != 1 {
		t.Errorf("쿼리 %d회, want 1회 (NFR-105)", n)
	}
}

// D15 4.3-1: revoking a role must bite on the very next request. If this ever
// needs a logout to take effect, the cache moved somewhere it must not be.
func TestPermissionChangeIsImmediate(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "e@example.com")
	grantRole(t, pool, u, "editor")

	p, err := s.LoadPermissions(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Can("page.create") {
		t.Fatal("editor 인데 page.create 가 없다")
	}

	if _, err := pool.Exec(ctx, `DELETE FROM user_roles WHERE user_id = $1`, u); err != nil {
		t.Fatal(err)
	}
	p2, err := s.LoadPermissions(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Can("page.create") {
		t.Error("역할을 회수했는데 다음 조회에 남아 있다")
	}
}

func TestSuperuserFlagComesFromTheRole(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "a@example.com")
	grantRole(t, pool, u, "admin")

	p, err := s.LoadPermissions(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Superuser {
		t.Fatal("admin 역할인데 superuser 가 아니다")
	}
	// A superuser passes permissions that were never seeded — that is the
	// point of bypassing the table (D15 1.3).
	if !p.Can("permission.that.does.not.exist") {
		t.Error("superuser 가 전건 통과하지 않는다")
	}
}

// An installation may grant to `anonymous` (D15 2.5), so the anonymous path is
// a query, not an empty set assumed in a handler.
func TestAnonymousPermissionsAreLoaded(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()

	p, err := s.LoadAnonymousPermissions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p.Can("page.view") {
		t.Fatal("시드 상태의 anonymous 가 page.view 를 갖는다")
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO role_permissions (role_id, permission_id)
		SELECT r.id, p.id FROM roles r, permissions p
		WHERE r.key = 'anonymous' AND p.key = 'page.view'`); err != nil {
		t.Fatal(err)
	}
	p2, err := s.LoadAnonymousPermissions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !p2.Can("page.view") {
		t.Error("anonymous 에 부여했는데 반영되지 않는다")
	}
}

func TestCreateUserRejectsDuplicateEmail(t *testing.T) {
	s, _ := testStore(t)
	mkUser(t, s, "dup@example.com")
	if _, err := s.CreateUser(context.Background(), "dup@example.com", "h", "d"); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("중복 이메일 err = %v, want ErrEmailTaken", err)
	}
}

// The filter belongs in the WHERE clause. A caller who forgets a Go-side check
// would log in a deactivated account; a predicate that is not there cannot be
// forgotten.
func TestInactiveUserIsFilteredInSQL(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "off@example.com")

	if _, _, err := s.FindActiveUserByEmail(ctx, "off@example.com"); err != nil {
		t.Fatalf("활성 계정을 못 찾는다: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET is_active = false WHERE id = $1`, u); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.FindActiveUserByEmail(ctx, "off@example.com"); !errors.Is(err, ErrNoUser) {
		t.Errorf("비활성 계정이 인증 조회에 걸렸다: %v", err)
	}
	// ...but the session path still finds it, so the middleware can end the
	// session rather than treat it as a stale ID.
	if _, err := s.FindUserByID(ctx, u); err != nil {
		t.Errorf("세션 조회에서 비활성 계정을 못 찾는다: %v", err)
	}
}

func TestInvalidateSessionsMovesCutoff(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "c@example.com")

	before, err := s.FindUserByID(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InvalidateSessions(ctx, u); err != nil {
		t.Fatal(err)
	}
	after, err := s.FindUserByID(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if !after.SessionsValidFrom.After(before.SessionsValidFrom) {
		t.Error("sessions_valid_from 이 앞당겨지지 않았다")
	}
}

func TestLastSuperuserCannotBeDeactivated(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	a := mkUser(t, s, "admin1@example.com")
	grantRole(t, pool, a, "admin")

	if err := s.SetActive(ctx, a, false); !errors.Is(err, ErrLastSuperuser) {
		t.Fatalf("마지막 관리자가 비활성화됐다: %v", err)
	}
	// A second holder makes it allowed again.
	b := mkUser(t, s, "admin2@example.com")
	grantRole(t, pool, b, "admin")
	if err := s.SetActive(ctx, a, false); err != nil {
		t.Errorf("관리자가 둘인데 거부됐다: %v", err)
	}
}

// D15 5.2, and the reason SetActive holds a lock: two administrators switching
// each other off must not both read "2 remaining" and both proceed. Exactly one
// wins; the site is never left without anyone who can let people back in.
//
// Repeated because a single pair usually serialises by luck — the two
// goroutines finish one after the other and the race never happens. W1-14 asks
// for a test that FAILS when the lock is removed, and one attempt does not
// deliver that: with FOR UPDATE deleted this loop leaves 0 administrators
// within a handful of rounds, while the locked version is 1 every time.
func TestConcurrentDeactivationLeavesOneSuperuser(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()

	const rounds = 30
	for i := 0; i < rounds; i++ {
		a := mkUser(t, s, fmt.Sprintf("x%d@example.com", i))
		b := mkUser(t, s, fmt.Sprintf("y%d@example.com", i))
		grantRole(t, pool, a, "admin")
		grantRole(t, pool, b, "admin")

		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() { defer wg.Done(); <-start; errs[0] = s.SetActive(ctx, a, false) }()
		go func() { defer wg.Done(); <-start; errs[1] = s.SetActive(ctx, b, false) }()
		close(start) // release both as close to simultaneously as we can
		wg.Wait()

		var live int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM users u
			JOIN user_roles ur ON ur.user_id = u.id
			JOIN roles r ON r.id = ur.role_id
			WHERE r.is_superuser AND u.is_active`).Scan(&live); err != nil {
			t.Fatal(err)
		}
		if live != 1 {
			t.Fatalf("%d회차: 살아남은 관리자 %d명, want 1명 — 사이트가 잠겼다 (errs=%v)", i, live, errs)
		}

		ok := 0
		for _, e := range errs {
			if e == nil {
				ok++
			}
		}
		if ok != 1 {
			t.Fatalf("%d회차: 동시 비활성화 성공 %d건, want 1건 (errs=%v)", i, ok, errs)
		}

		// Reset for the next round.
		if _, err := pool.Exec(ctx, `DELETE FROM user_roles`); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM users`); err != nil {
			t.Fatal(err)
		}
	}
}
