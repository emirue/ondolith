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

// FR-704 / D15 5.2: two administrators deleting the last two superuser holders
// at the same moment must not both succeed.
//
// Deletion and deactivation reach the same end state — a site with nobody who
// can let anyone back in — so the lock has to cover both. One pair races too
// rarely to be evidence; 30 rounds fail on the first one when the lock is gone.
func TestConcurrentDeletionCannotEmptyTheSuperuserRole(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()

	for round := range 30 {
		if _, err := pool.Exec(ctx, `DELETE FROM user_roles`); err != nil {
			t.Fatal(err)
		}
		a, err := s.CreateUser(ctx, fmt.Sprintf("a%d@example.com", round), "h", "A")
		if err != nil {
			t.Fatal(err)
		}
		b, err := s.CreateUser(ctx, fmt.Sprintf("b%d@example.com", round), "h", "B")
		if err != nil {
			t.Fatal(err)
		}
		grantRole(t, pool, a, "admin")
		grantRole(t, pool, b, "admin")

		start := make(chan struct{})
		errs := make(chan error, 2)
		for _, pair := range [][2]string{{a, b}, {b, a}} {
			go func(victim string) {
				<-start
				errs <- s.DeleteUser(ctx, victim)
			}(pair[0])
		}
		close(start)
		var failures int
		for range 2 {
			if err := <-errs; err != nil {
				if !errors.Is(err, ErrLastSuperuser) {
					t.Fatalf("round %d: 예상 밖 오류: %v", round, err)
				}
				failures++
			}
		}

		var left int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM users u
			JOIN user_roles ur ON ur.user_id = u.id
			JOIN roles r ON r.id = ur.role_id
			WHERE r.is_superuser AND u.is_active`).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left == 0 {
			t.Fatalf("round %d: 두 요청이 모두 성공해 관리자가 0명이 됐다 (거부 %d건)", round, failures)
		}
	}
}

// D15 2.4: 게시판 스코프 권한은 게시판 단위로 판정된다. 게시판 A 에만 부여된
// 권한으로 게시판 B 에 접근하면 거부된다 — 그러지 않으면 게시판 하나에 글쓰기를
// 준 것이 사이트 전체에 준 것이 된다.
func TestScopedPermissionsAreJudgedPerBoard(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()

	var boardA, boardB string
	if err := pool.QueryRow(ctx,
		`INSERT INTO boards (slug, name) VALUES ('a','게시판 A') RETURNING id`).Scan(&boardA); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO boards (slug, name) VALUES ('b','게시판 B') RETURNING id`).Scan(&boardB); err != nil {
		t.Fatal(err)
	}

	uid, err := s.CreateUser(ctx, "u@example.com", "h", "사용자")
	if err != nil {
		t.Fatal(err)
	}
	// member 는 암묵 역할이라 user_roles 행 없이도 유효하다 (D15 2.3).
	if _, err := pool.Exec(ctx, `
		INSERT INTO role_permissions (role_id, permission_id, board_id)
		SELECT r.id, p.id, $1 FROM roles r, permissions p
		WHERE r.key = 'member' AND p.key = 'post.write'`, boardA); err != nil {
		t.Fatal(err)
	}

	perms, err := s.LoadPermissions(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if !perms.CanOn("post.write", BoardID(boardA)) {
		t.Error("부여한 게시판에서 거부됐다")
	}
	if perms.CanOn("post.write", BoardID(boardB)) {
		t.Error("게시판 A 의 부여로 게시판 B 에 글을 쓴다")
	}
	// 스코프 부여는 전역이 아니다. 전역으로 읽히면 게시판이 없는 화면까지 뚫린다.
	if perms.Can("post.write") {
		t.Error("스코프 부여가 전역 권한으로 읽혔다")
	}
}

// 전역 부여는 모든 게시판에서 참이다. 스코프를 도입하면서 전역이 좁아지면
// Phase 1 화면들이 조용히 닫힌다.
func TestGlobalGrantsStillAnswerForEveryBoard(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	var board string
	if err := pool.QueryRow(ctx,
		`INSERT INTO boards (slug, name) VALUES ('a','게시판 A') RETURNING id`).Scan(&board); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser(ctx, "op@example.com", "h", "운영자")
	if err != nil {
		t.Fatal(err)
	}
	grantRole(t, pool, uid, "operator")

	perms, err := s.LoadPermissions(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	// operator 는 post.read 를 전역으로 갖는다 (D15 2.5 의 ●).
	if !perms.Can("post.read") {
		t.Error("전역 부여가 사라졌다")
	}
	if !perms.CanOn("post.read", BoardID(board)) {
		t.Error("전역 부여가 특정 게시판에서 거부됐다")
	}
	// 그리고 여전히 Phase 1 권한을 갖는다.
	if !perms.Can("admin.access") {
		t.Error("Phase 1 전역 권한이 사라졌다")
	}
}

// 익명도 스코프 부여를 받는다 — 공개 게시판 프리셋이 그렇게 만든다.
func TestAnonymousGetsScopedGrantsToo(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	var board string
	if err := pool.QueryRow(ctx,
		`INSERT INTO boards (slug, name) VALUES ('a','게시판 A') RETURNING id`).Scan(&board); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO role_permissions (role_id, permission_id, board_id)
		SELECT r.id, p.id, $1 FROM roles r, permissions p
		WHERE r.key = 'anonymous' AND p.key = 'post.read'`, board); err != nil {
		t.Fatal(err)
	}

	perms, err := s.LoadAnonymousPermissions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !perms.CanOn("post.read", BoardID(board)) {
		t.Error("익명이 공개 게시판을 못 읽는다")
	}
	if perms.CanOn("post.read", BoardID("00000000-0000-0000-0000-000000000000")) {
		t.Error("익명의 스코프 부여가 다른 게시판에도 먹는다")
	}
}

// **주문 이력이 있는 계정은 지울 수 없다** (FR-212, D30 3-1).
//
// DeleteUser 는 이것을 데이터베이스에 맡기고 23503 을 ErrUserInUse 로 옮긴다 —
// 주석에도 "orders are RESTRICT" 라고 적혀 있었다. 그런데 00012 는 그 외래키를
// `ON DELETE SET NULL` 로 만들었다. 그래서 삭제는 **거부되지 않고** 주문의
// 주인만 조용히 사라졌다: FR-212 가 막으려던 상태(주문 주체 없음)를 정확히
// 만들어 낸 것이다. 코드·문서·스키마 셋 중 스키마만 달랐고 확인하는 테스트가
// 없었다. 00018 이 RESTRICT 로 돌린다.
func TestDeletingAUserWithOrdersIsRefused(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "buyer@example.com")

	if _, err := pool.Exec(ctx, `
		INSERT INTO orders (order_no, user_id, status, total_amount,
		                    receiver_name, receiver_phone, postcode, address1,
		                    orderer_email, orderer_phone)
		VALUES ('ORD-0001', $1, '결제대기', 1000, '받는이', '01000000000', '00000', '주소',
		        'buyer@example.com', '01000000000')`,
		u); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteUser(ctx, u); !errors.Is(err, ErrUserInUse) {
		t.Fatalf("삭제가 거부되지 않았다 (err=%v) — 주문의 주체가 사라진다", err)
	}
	// 거부가 주문을 건드리지 않았는지도 본다. SET NULL 이면 에러 없이
	// user_id 만 비워지므로, 「에러가 났다」만으로는 구별되지 않는다.
	var owner *string
	if err := pool.QueryRow(ctx,
		`SELECT user_id::text FROM orders WHERE order_no = 'ORD-0001'`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner == nil || *owner != u {
		t.Errorf("주문의 주인이 %v 로 바뀌었다 — SET NULL 이 그대로다", owner)
	}
}
