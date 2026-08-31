package migrations

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// The static tests in migrations_test.go read the SQL as text. They cannot tell
// whether PostgreSQL accepts it, whether a constraint actually rejects the row
// it claims to, or whether Down leaves anything behind. Those only answer to a
// real server.
//
//	make test-integration
//
// Skipped when ONDOLITH_TEST_DSN is unset so `make check` stays DB-free.
const dsnEnv = "ONDOLITH_TEST_DSN"

func testDB(t *testing.T) (*sql.DB, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s 미설정 — 통합 테스트를 건너뜁니다 (make test-integration)", dsnEnv)
	}
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("테스트 DB 연결 실패: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("테스트 DB 응답 없음: %v", err)
	}
	// Reset the whole schema, not a list of tables: the list goes stale with
	// every migration and dies on the first foreign key that outlives it.
	for _, stmt := range []string{"DROP SCHEMA public CASCADE", "CREATE SCHEMA public"} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { db.Close() })
	return db, pool
}

func tableNames(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT tablename FROM pg_tables WHERE schemaname = 'public'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func count(t *testing.T, pool *pgxpool.Pool, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// seededGrants reads the (role, permission) pairs out of 00003_rbac_seed.sql's
// VALUES list. Parsing the migration keeps this test honest about what the
// migration does rather than about what someone believed it did.
func seededGrants(t *testing.T) map[string][]string {
	t.Helper()
	// 시드 파일 목록을 적지 않고 **찾는다.** 목록을 적어 두었더니 Phase 3
	// 시드를 새 파일에 넣는 순간 파서가 그것을 못 보고, 늘어난 부여가 전부
	// "예상 밖" 으로 보고됐다 — 그때 기대값을 손으로 맞추는 것이 M9 다.
	block := regexp.MustCompile(`(?s)INSERT INTO role_permissions.*?FROM \(VALUES(.*?)\)\s*AS`)
	pair := regexp.MustCompile(`\(\s*'([a-z_]+)'\s*,\s*'([a-z][a-z0-9._]*)'\s*\)`)
	out := map[string][]string{}
	for _, name := range seedFiles(t) {
		sql, err := fs.ReadFile(FS, name)
		if err != nil {
			t.Fatal(err)
		}
		up, _, _ := strings.Cut(string(sql), "-- +goose Down")
		m := block.FindStringSubmatch(up)
		if m == nil {
			continue // 부여를 심지 않는 마이그레이션
		}
		for _, p := range pair.FindAllStringSubmatch(m[1], -1) {
			out[p[1]] = append(out[p[1]], p[2])
		}
	}
	if len(out) == 0 {
		t.Fatal("시드에서 부여를 한 건도 읽지 못했다")
	}
	for _, v := range out {
		sort.Strings(v)
	}
	return out
}

// seededPermissions counts what the seed files insert, so the expectation comes
// from the SQL rather than from a number typed here — the same chain
// seededGrants keeps for grants.
func seededPermissions(t *testing.T) (total, scoped int) {
	t.Helper()
	block := regexp.MustCompile(`(?s)INSERT INTO permissions \(key, description, is_scoped\) VALUES(.*?);`)
	row := regexp.MustCompile(`\(\s*'([a-z][a-z0-9._]*)'\s*,\s*'[^']*'\s*,\s*(true|false)\s*\)`)
	for _, name := range seedFiles(t) {
		sql, err := fs.ReadFile(FS, name)
		if err != nil {
			t.Fatal(err)
		}
		up, _, _ := strings.Cut(string(sql), "-- +goose Down")
		m := block.FindStringSubmatch(up)
		if m == nil {
			continue // 권한을 심지 않는 마이그레이션
		}
		found := row.FindAllStringSubmatch(m[1], -1)
		if len(found) == 0 {
			t.Fatalf("%s 에서 권한을 한 건도 읽지 못했다", name)
		}
		for _, r := range found {
			total++
			if r[2] == "true" {
				scoped++
			}
		}
	}
	if total == 0 {
		t.Fatal("어떤 마이그레이션에서도 권한을 읽지 못했다 — 검사가 헛돌았다")
	}
	return total, scoped
}

// FR-103, NFR-302: the whole shipped schema has to be something PostgreSQL
// actually accepts. Until Phase 1 only two tables had ever been executed; the
// remaining ten existed as prose in D30.
func TestAllMigrationsApply(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatalf("마이그레이션 적용 실패: %v", err)
	}

	// D30 §3-3 Phase 0 + Phase 1 + Phase 2 게시판.
	// goose_db_version is goose's own bookkeeping.
	want := []string{
		"attachments", "board_fields", "boards", "cart_items", "carts",
		"categories", "comments", "email_verification_tokens", "goose_db_version",
		"menus", "operation_logs", "order_agreements", "order_items", "orders",
		"pages", "password_reset_tokens", "payments", "permissions", "posts",
		"product_categories", "product_options", "product_variants", "products",
		"refund_items", "refunds", "return_items", "returns", "role_permissions",
		"roles", "sessions", "settings", "shipments", "social_accounts", "terms",
		"user_fields", "user_roles", "users", "webhook_events",
	}
	got := tableNames(t, pool)
	if len(got) != len(want) {
		t.Fatalf("테이블 %d개, want %d개\n got: %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("테이블 목록이 다르다: got %v, want %v", got, want)
			break
		}
	}
}

// NFR-303 / NFR-308: a Down that leaves debris is worse than no Down — the
// downgrade appears to succeed and the next Up fails on an object that should
// not exist. Nothing checked this before: the existing tests only grep for the
// `-- +goose Down` marker.
func TestEveryMigrationRollsBackCompletely(t *testing.T) {
	db, pool := testDB(t)
	ctx := context.Background()

	if err := Run(ctx, db); err != nil {
		t.Fatalf("마이그레이션 적용 실패: %v", err)
	}
	before := tableNames(t, pool)

	p, err := goose.NewProvider(goose.DialectPostgres, db, FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 0); err != nil {
		t.Fatalf("롤백 실패: %v", err)
	}

	// goose keeps its own version table; everything else must be gone.
	for _, name := range tableNames(t, pool) {
		if name != "goose_db_version" {
			t.Errorf("롤백 후에도 남아 있는 테이블: %s", name)
		}
	}
	// Indexes and constraints live inside tables, so an empty schema covers
	// them — but a leftover index on a dropped table would have failed above.
	if len(before) < 10 {
		t.Fatalf("롤백 전 테이블이 %d개뿐이다 — 이 테스트가 아무것도 검증하지 않았다", len(before))
	}

	// Up again from zero: the real reason NFR-303 exists is that an operator
	// downgrades and then upgrades. A Down that half-worked breaks here.
	if err := Run(ctx, db); err != nil {
		t.Fatalf("롤백 후 재적용 실패 — Down 이 불완전하다: %v", err)
	}
}

// D15 §3: exactly one superuser role, enforced by the database. The handler
// that also checks this is not the guarantee — a partial unique index is.
func TestOnlyOneSuperuserRoleIsAccepted(t *testing.T) {
	db, pool := testDB(t)
	ctx := context.Background()
	if err := Run(ctx, db); err != nil {
		t.Fatal(err)
	}

	_, err := pool.Exec(ctx,
		`INSERT INTO roles (key, name, is_superuser) VALUES ('root', '루트', true)`)
	if err == nil {
		t.Fatal("두 번째 superuser 역할이 삽입됐다 — roles_one_superuser_idx 가 없거나 안 문다")
	}

	// A non-superuser role with the same shape must still be insertable, or the
	// index is rejecting far more than it should.
	if _, err := pool.Exec(ctx,
		`INSERT INTO roles (key, name, is_superuser) VALUES ('root', '루트', false)`); err != nil {
		t.Fatalf("평범한 역할 삽입이 거부됐다: %v", err)
	}
}

// D30 §3-1: the delete rules are the difference between "the role is gone" and
// "every assignment silently vanished with it".
func TestForeignKeyDeleteRules(t *testing.T) {
	db, pool := testDB(t)
	ctx := context.Background()
	if err := Run(ctx, db); err != nil {
		t.Fatal(err)
	}

	var userID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash) VALUES ('a@example.com', 'x') RETURNING id`,
	).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE key = 'operator'`,
		userID); err != nil {
		t.Fatal(err)
	}

	// user_roles.role_id is RESTRICT: a role with holders cannot be deleted.
	// This is what A-403 returns 409 for.
	if _, err := pool.Exec(ctx, `DELETE FROM roles WHERE key = 'operator'`); err == nil {
		t.Error("부여된 사용자가 있는 역할이 삭제됐다 — RESTRICT 가 아니다")
	}

	// user_roles.user_id is CASCADE: deleting the account takes its grants.
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID); err != nil {
		t.Fatalf("사용자 삭제 실패: %v", err)
	}
	if n := count(t, pool, `SELECT count(*) FROM user_roles WHERE user_id = $1`, userID); n != 0 {
		t.Errorf("사용자를 지웠는데 user_roles 가 %d행 남았다 — CASCADE 가 아니다", n)
	}

	// role_permissions.permission_id is RESTRICT: the permission list is owned
	// by code (D15 P1), so a row disappearing under a grant must be refused.
	if _, err := pool.Exec(ctx,
		`DELETE FROM permissions WHERE key = 'page.view'`); err == nil {
		t.Error("부여에 쓰이는 권한이 삭제됐다 — RESTRICT 가 아니다")
	}
}

// D30: the CHECK constraints are fail-closed backstops, not decoration. The
// menus one in particular has to reject protocol-relative URLs, which is the
// case a handler that only tests for a leading "/" lets through.
func TestCheckConstraintsRejectBadValues(t *testing.T) {
	db, pool := testDB(t)
	ctx := context.Background()
	if err := Run(ctx, db); err != nil {
		t.Fatal(err)
	}

	rejected := []struct {
		name string
		q    string
	}{
		{"menus: 프로토콜 상대 URL", `INSERT INTO menus (title, url) VALUES ('x', '//evil.com')`},
		{"menus: javascript 스킴", `INSERT INTO menus (title, url) VALUES ('x', 'javascript:alert(1)')`},
		{"menus: data 스킴", `INSERT INTO menus (title, url) VALUES ('x', 'data:text/html,x')`},
		{"pages: 허용목록 밖 상태", `INSERT INTO pages (slug, title, body, status) VALUES ('s', 't', 'b', 'archived')`},
		{"roles: 점이 들어간 key", `INSERT INTO roles (key, name) VALUES ('a.b', 'x')`},
		{"permissions: 점 없는 key", `INSERT INTO permissions (key, description) VALUES ('nodot', 'x')`},
		{"permissions: 3단 key", `INSERT INTO permissions (key, description) VALUES ('a.b.c', 'x')`},
	}
	for _, c := range rejected {
		if _, err := pool.Exec(ctx, c.q); err == nil {
			t.Errorf("거부되어야 하는데 통과했다 — %s", c.name)
		}
	}

	accepted := []struct {
		name string
		q    string
	}{
		{"menus: 내부 경로", `INSERT INTO menus (title, url) VALUES ('x', '/about')`},
		{"menus: 루트", `INSERT INTO menus (title, url) VALUES ('x', '/')`},
		{"menus: https 외부", `INSERT INTO menus (title, url) VALUES ('x', 'https://example.com/a')`},
		{"pages: draft", `INSERT INTO pages (slug, title, body) VALUES ('s1', 't', 'b')`},
		{"pages: published", `INSERT INTO pages (slug, title, body, status) VALUES ('s2', 't', 'b', 'published')`},
		{"permissions: 밑줄 있는 동작", `INSERT INTO permissions (key, description) VALUES ('user.reset_password2', 'x')`},
	}
	for _, c := range accepted {
		if _, err := pool.Exec(ctx, c.q); err != nil {
			t.Errorf("허용되어야 하는데 거부됐다 — %s: %v", c.name, err)
		}
	}
}

// The uniques on social_accounts are not interchangeable: one stops a provider
// account from attaching to two of ours, the other is what makes P-111's
// single-row delete predicate true.
func TestSocialAccountUniques(t *testing.T) {
	db, pool := testDB(t)
	ctx := context.Background()
	if err := Run(ctx, db); err != nil {
		t.Fatal(err)
	}

	var u1, u2 string
	for i, dst := range []*string{&u1, &u2} {
		if err := pool.QueryRow(ctx,
			`INSERT INTO users (email, password_hash) VALUES ($1, 'x') RETURNING id`,
			[]string{"a@example.com", "b@example.com"}[i],
		).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO social_accounts (user_id, provider, provider_uid) VALUES ($1,'kakao','K1')`,
		u1); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO social_accounts (user_id, provider, provider_uid) VALUES ($1,'kakao','K1')`,
		u2); err == nil {
		t.Error("같은 소셜 계정이 우리 계정 둘에 붙었다 — (provider, provider_uid) 유니크가 없다")
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO social_accounts (user_id, provider, provider_uid) VALUES ($1,'kakao','K2')`,
		u1); err == nil {
		t.Error("한 계정에 같은 프로바이더가 둘 붙었다 — P-111 의 해제 술어가 두 행을 지우게 된다")
	}
}

// W1-04: the seed is a hand-copy of D15 §2.5 into SQL, and a hand-copy drifts.
// This is the only thing that ties the two together.
func TestSeedMatchesAccessControlDoc(t *testing.T) {
	db, pool := testDB(t)
	ctx := context.Background()
	if err := Run(ctx, db); err != nil {
		t.Fatal(err)
	}

	// D15 §1.1. anonymous and member are not assignable: they are granted
	// implicitly, so a user_roles row pointing at them would contradict that.
	wantRoles := map[string]struct{ builtin, superuser, assignable bool }{
		"anonymous": {true, false, false},
		"member":    {true, false, false},
		"editor":    {true, false, true},
		"operator":  {true, false, true},
		"admin":     {true, true, true},
	}
	if n := count(t, pool, `SELECT count(*) FROM roles`); n != int64(len(wantRoles)) {
		t.Errorf("roles %d행, want %d행", n, len(wantRoles))
	}
	for key, want := range wantRoles {
		var got struct{ builtin, superuser, assignable bool }
		err := pool.QueryRow(ctx,
			`SELECT is_builtin, is_superuser, is_assignable FROM roles WHERE key = $1`, key,
		).Scan(&got.builtin, &got.superuser, &got.assignable)
		if err != nil {
			t.Errorf("역할 %s 가 없다: %v", key, err)
			continue
		}
		if got != want {
			t.Errorf("역할 %s: got %+v, want %+v", key, got, want)
		}
	}

	// 권한은 Phase 마다 심는다. 37개를 한 번에 심으면 D15 4.4 의 "어떤 라우트도
	// 쓰지 않는 권한" 경고가 매 부팅 여러 건을 뱉어 검사 자체가 무시된다.
	// 기대값은 시드 SQL 에서 읽는다 — 여기에 숫자를 적으면 세 번째 사본이다.
	wantPerms, wantScoped := seededPermissions(t)
	if n := count(t, pool, `SELECT count(*) FROM permissions`); n != int64(wantPerms) {
		t.Errorf("permissions %d행, want %d행", n, wantPerms)
	}
	if n := count(t, pool, `SELECT count(*) FROM permissions WHERE is_scoped`); n != int64(wantScoped) {
		t.Errorf("스코프 권한 %d행, want %d행 (D15 2.4 의 6개)", n, wantScoped)
	}

	// The expected grants are read out of the seed SQL itself, not written here
	// a second time. checkdocs.sh already compares that SQL against D15 §2.5, so
	// this closes the chain document → SQL → database with no hand-copy in it.
	// A literal here would be exactly the third copy M9/M11/M12 are about, and
	// D81's own risk table asks for parsing rather than restating.
	wantGrants := seededGrants(t)
	rows, err := pool.Query(ctx, `
		SELECT r.key, p.key
		FROM role_permissions rp
		JOIN roles r       ON r.id = rp.role_id
		JOIN permissions p ON p.id = rp.permission_id
		ORDER BY r.key, p.key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	gotGrants := map[string][]string{}
	for rows.Next() {
		var role, perm string
		if err := rows.Scan(&role, &perm); err != nil {
			t.Fatal(err)
		}
		gotGrants[role] = append(gotGrants[role], perm)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	for role, want := range wantGrants {
		got := gotGrants[role]
		if len(got) != len(want) {
			t.Errorf("%s 부여 %d건, want %d건\n got: %v\nwant: %v", role, len(got), len(want), got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s 부여가 D15 2.5 와 다르다\n got: %v\nwant: %v", role, got, want)
				break
			}
		}
	}
	for role := range gotGrants {
		if _, expected := wantGrants[role]; !expected {
			t.Errorf("D15 2.5 에 없는 역할에 부여가 들어갔다: %s", role)
		}
	}
}

// D15 §3 / D30 two-release rule: release N grants the admin role to whoever
// already had is_admin. Skipping this backfill upgrades an existing site into
// one whose only administrator has no role — locked out of its own admin tree.
func TestIsAdminIsCarriedIntoUserRoles(t *testing.T) {
	db, pool := testDB(t)
	ctx := context.Background()

	p, err := goose.NewProvider(goose.DialectPostgres, db, FS)
	if err != nil {
		t.Fatal(err)
	}
	// Stop before the seed so the account exists when the backfill runs — this
	// is the state a Phase 0 installation upgrades from.
	if _, err := p.UpTo(ctx, 2); err != nil {
		t.Fatalf("00002 까지 적용 실패: %v", err)
	}

	var adminID, plainID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, is_admin) VALUES ('admin@example.com','x',true) RETURNING id`,
	).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, is_admin) VALUES ('plain@example.com','x',false) RETURNING id`,
	).Scan(&plainID); err != nil {
		t.Fatal(err)
	}

	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("시드 적용 실패: %v", err)
	}

	n := count(t, pool, `
		SELECT count(*) FROM user_roles ur
		JOIN roles r ON r.id = ur.role_id
		WHERE ur.user_id = $1 AND r.key = 'admin'`, adminID)
	if n != 1 {
		t.Errorf("is_admin 계정의 admin 역할이 %d행 — want 1행. 업그레이드하면 관리자가 자기 사이트에서 잠긴다", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM user_roles WHERE user_id = $1`, plainID); n != 0 {
		t.Errorf("is_admin 이 아닌 계정에 역할이 %d행 부여됐다", n)
	}

	// **컬럼은 사라졌고, 백필은 그 전에 돌았다** (00003 → 00020, W2-01).
	//
	// 위 두 단언이 이 순서를 지킨다: 전부 적용한 뒤에도 admin 역할이 남아
	// 있다는 것은 백필이 삭제보다 먼저 돌았다는 뜻이다. 00020 을 00003 보다
	// 앞 번호로 옮기면 백필이 없는 컬럼을 읽어 마이그레이션이 깨지고, 그
	// 실패가 여기서 잡힌다.
	//
	// D30 의 두 릴리즈 규칙은 v0.1.0(릴리즈 N)으로 충족됐다 — 옛 스키마에서
	// 올라오는 사이트는 00003 을 지나며 역할을 받는다.
	if n := count(t, pool, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'users' AND column_name = 'is_admin'`); n != 0 {
		t.Errorf("is_admin 컬럼이 %d 개 남아 있다 — 00020 이 지웠어야 한다 (W2-01)", n)
	}
}

// D30 Phase 2's constraints are the ones a handler bug would otherwise turn
// into bad rows. Each case is a thing the database has to refuse on its own.
func TestBoardSchemaConstraintsBite(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	var boardID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO boards (slug, name) VALUES ('free', '자유게시판') RETURNING id`).
		Scan(&boardID); err != nil {
		t.Fatal(err)
	}

	refused := map[string]string{
		"대문자 슬러그":     `INSERT INTO boards (slug, name) VALUES ('Free', 'x')`,
		"빈 슬러그":       `INSERT INTO boards (slug, name) VALUES ('', 'x')`,
		"슬러그 중복":      `INSERT INTO boards (slug, name) VALUES ('free', '다른 이름')`,
		"경로를 벗어나는 스킨": `INSERT INTO boards (slug, name, skin) VALUES ('b1','x','../etc')`,
		"페이지 크기 범위 밖": `INSERT INTO boards (slug, name, per_page) VALUES ('b2','x',0)`,
		"필드 키에 대문자": `INSERT INTO board_fields (board_id, key, label, field_type)
			VALUES ('` + boardID + `', 'Key', 'x', 'text')`,
		"알 수 없는 필드 타입": `INSERT INTO board_fields (board_id, key, label, field_type)
			VALUES ('` + boardID + `', 'k', 'x', 'richtext')`,
		"select 인데 선택지가 없음": `INSERT INTO board_fields (board_id, key, label, field_type)
			VALUES ('` + boardID + `', 'k', 'x', 'select')`,
		"text 인데 선택지가 있음": `INSERT INTO board_fields (board_id, key, label, field_type, options)
			VALUES ('` + boardID + `', 'k', 'x', 'text', '["a"]')`,
		"커스텀 필드가 객체가 아님": `INSERT INTO posts (board_id, title, body, custom_fields)
			VALUES ('` + boardID + `', 't', 'b', '[]')`,
		"알 수 없는 글 상태": `INSERT INTO posts (board_id, title, body, status)
			VALUES ('` + boardID + `', 't', 'b', 'deleted')`,
		"조회수 음수": `INSERT INTO posts (board_id, title, body, view_count)
			VALUES ('` + boardID + `', 't', 'b', -1)`,
		"제목이 빈 글": `INSERT INTO posts (board_id, title, body) VALUES ('` + boardID + `', '', 'b')`,
	}
	for name, q := range refused {
		if _, err := pool.Exec(ctx, q); err == nil {
			t.Errorf("%s: DB가 통과시켰다", name)
		}
	}

	// ...and the legitimate shapes go in.
	if _, err := pool.Exec(ctx,
		`INSERT INTO board_fields (board_id, key, label, field_type, options)
		 VALUES ($1, 'color', '색상', 'select', '["빨강","파랑"]')`, boardID); err != nil {
		t.Errorf("정상 select 필드가 거부됐다: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO board_fields (board_id, key, label, field_type)
		 VALUES ($1, 'memo', '메모', 'text')`, boardID); err != nil {
		t.Errorf("정상 text 필드가 거부됐다: %v", err)
	}
	// Same key on the same board is the collision (board_id, key) exists for.
	if _, err := pool.Exec(ctx,
		`INSERT INTO board_fields (board_id, key, label, field_type)
		 VALUES ($1, 'memo', '중복', 'text')`, boardID); err == nil {
		t.Error("같은 게시판에 같은 키가 두 번 들어갔다")
	}
}

// D30: the attachment path regex refuses `../` at the database, not only in the
// handler. A stored path is joined to the upload root, so a traversal that gets
// past the handler becomes a read anywhere on disk.
func TestAttachmentPathRefusesTraversal(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var boardID, postID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO boards (slug, name) VALUES ('free','자유') RETURNING id`).Scan(&boardID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO posts (board_id, title, body) VALUES ($1,'t','b') RETURNING id`,
		boardID).Scan(&postID); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"../../etc/passwd",
		"2026/08/../../../etc/passwd",
		"/etc/passwd",
		"2026/08/abc.php",                             // extension
		"2026/8/0189a1b2-c3d4-5e6f-7081-92a3b4c5d6e7", // unpadded month
		"2026/08/not-a-uuid",
	} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO attachments (post_id, stored_path, original_name, mime_type, byte_size)
			 VALUES ($1, $2, 'x.png', 'image/png', 1)`, postID, path); err == nil {
			t.Errorf("경로 %q 가 통과했다", path)
		}
	}
	good := "2026/08/0189a1b2-c3d4-5e6f-7081-92a3b4c5d6e7"
	if _, err := pool.Exec(ctx,
		`INSERT INTO attachments (post_id, stored_path, original_name, mime_type, byte_size)
		 VALUES ($1, $2, 'x.png', 'image/png', 1)`, postID, good); err != nil {
		t.Errorf("정상 경로가 거부됐다: %v", err)
	}
	// Two rows on one file: deleting either would take the other's bytes.
	if _, err := pool.Exec(ctx,
		`INSERT INTO attachments (post_id, stored_path, original_name, mime_type, byte_size)
		 VALUES ($1, $2, 'y.png', 'image/png', 1)`, postID, good); err == nil {
		t.Error("같은 저장 경로가 두 행에 들어갔다")
	}
}

// D30: a comment with replies cannot be deleted (NO ACTION), which is what
// forces the tombstone. Deleting the post takes both in one statement.
func TestCommentDeletionRulesAreEnforcedByTheDatabase(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var boardID, postID, parentID, childID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO boards (slug, name) VALUES ('free','자유') RETURNING id`).Scan(&boardID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO posts (board_id, title, body) VALUES ($1,'t','b') RETURNING id`,
		boardID).Scan(&postID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO comments (post_id, body) VALUES ($1,'부모') RETURNING id`,
		postID).Scan(&parentID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO comments (post_id, parent_id, body) VALUES ($1,$2,'자식') RETURNING id`,
		postID, parentID).Scan(&childID); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM comments WHERE id = $1`, parentID); err == nil {
		t.Error("자식이 있는 댓글이 물리 삭제됐다 — 툼스톤 규칙의 근거가 사라진다")
	}
	// The tombstone shape: emptied body, deleted_at set.
	if _, err := pool.Exec(ctx,
		`UPDATE comments SET body = '', deleted_at = now() WHERE id = $1`, parentID); err != nil {
		t.Errorf("툼스톤 전환이 거부됐다: %v", err)
	}
	// A tombstone that still has a body is the state the theme would render.
	if _, err := pool.Exec(ctx,
		`UPDATE comments SET body = '남아있음' WHERE id = $1`, parentID); err == nil {
		t.Error("삭제 표시된 댓글에 본문이 남았다")
	}
	// A child with no replies is a physical delete.
	if _, err := pool.Exec(ctx, `DELETE FROM comments WHERE id = $1`, childID); err != nil {
		t.Errorf("자식 없는 댓글의 물리 삭제가 거부됐다: %v", err)
	}
	// Deleting the post takes the rest in one statement.
	if _, err := pool.Exec(ctx, `DELETE FROM posts WHERE id = $1`, postID); err != nil {
		t.Errorf("글 삭제가 댓글 FK 에 걸렸다: %v", err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM comments`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("글을 지웠는데 댓글 %d행이 남았다", left)
	}
}

// D15 2.4: the same permission on two boards is two grants, and the same global
// grant twice is one. NULLS NOT DISTINCT is what makes the second half true —
// under the default rule two NULL board_ids never collide.
func TestScopedGrantUniquenessCountsNulls(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var a, b string
	if err := pool.QueryRow(ctx,
		`INSERT INTO boards (slug, name) VALUES ('free','자유') RETURNING id`).Scan(&a); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO boards (slug, name) VALUES ('notice','공지') RETURNING id`).Scan(&b); err != nil {
		t.Fatal(err)
	}
	const grant = `INSERT INTO role_permissions (role_id, permission_id, board_id)
		SELECT r.id, p.id, $1 FROM roles r, permissions p
		WHERE r.key = 'member' AND p.key = 'page.view'`

	if _, err := pool.Exec(ctx, grant, a); err != nil {
		t.Fatalf("게시판 A 부여 실패: %v", err)
	}
	if _, err := pool.Exec(ctx, grant, b); err != nil {
		t.Errorf("다른 게시판에 같은 권한을 못 준다: %v", err)
	}
	if _, err := pool.Exec(ctx, grant, a); err == nil {
		t.Error("같은 게시판에 같은 권한이 두 번 들어갔다")
	}
	// A global grant stores NULL. Two of them must collide even though SQL's
	// default rule says NULL <> NULL — that is what NULLS NOT DISTINCT buys,
	// and without it the same global grant lands as many times as it is sent.
	if _, err := pool.Exec(ctx, grant, nil); err != nil {
		t.Fatalf("전역 부여 실패: %v", err)
	}
	if _, err := pool.Exec(ctx, grant, nil); err == nil {
		t.Error("전역 부여가 중복으로 들어갔다 — NULLS NOT DISTINCT 가 없다")
	}
	// Dropping a board takes its grants with it.
	if _, err := pool.Exec(ctx, `DELETE FROM boards WHERE id = $1`, a); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM role_permissions WHERE board_id = $1`, a).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("게시판을 지웠는데 스코프 부여 %d행이 남았다", n)
	}
}

// FR-507: Korean title and body queries have to reach the index, not scan.
//
// 실측 (2026-08-05, PostgreSQL 18):
//
//	본문이 서로 다른 90,000행에서 `게시판:*` 질의가 Bitmap Index Scan on
//	posts_search_idx 로 계획된다. 70,000행에서는 여전히 Seq Scan 이다 —
//	교차점이 그 사이다.
//
// 두 가지가 이 숫자를 만든다. ① 접두 tsquery 의 기본 선택도는 2% 고정이라
// 추정 행수가 테이블과 함께 커진다. 즉 "행을 늘리면 언젠가 인덱스를 탄다"가
// 저절로 성립하지는 않는다. ② 모든 행이 같은 단어를 담으면 통계가 무너져
// 어떤 크기에서도 Seq Scan 이다 — 처음 이 테스트를 '제목 1', '본문 1' 같은
// 연번 텍스트로 짰을 때 150,000행에서도 인덱스를 타지 않았다. 실제 게시판은
// 글마다 본문이 다르므로 여기서도 다르게 만든다.
//
// 그래서 이 테스트는 인덱스를 "쓸 수 있다"(enable_seqscan=off)가 아니라
// "고른다"를 단언한다. 전자는 인덱스가 없어도 문법만 맞으면 통과한다.
func TestKoreanSearchUsesTheIndex(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var boardID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO boards (slug, name) VALUES ('free','자유') RETURNING id`).Scan(&boardID); err != nil {
		t.Fatal(err)
	}
	// 서로 다른 본문. 같은 단어를 반복하면 통계가 무너져 어떤 크기에서도
	// 인덱스를 타지 않는다 (위 ②).
	if _, err := pool.Exec(ctx, `
		INSERT INTO posts (board_id, title, body)
		SELECT $1, md5(g::text), md5((g*7)::text) || ' ' || md5((g*13)::text)
		FROM generate_series(1, 90000) g`, boardID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO posts (board_id, title, body) VALUES ($1, '공지 게시판 안내', '게시판을 새로 열었습니다')`,
		boardID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ANALYZE posts`); err != nil {
		t.Fatal(err)
	}

	// D30 이 측정한 것: 조사가 붙은 토큰 때문에 비접두 질의는 본문을 놓치고,
	// 접두 질의가 그것을 덮는다. 질의 모양을 "정리"하면 여기서 걸린다.
	var hits int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM posts WHERE search_vector @@ to_tsquery('simple', $1)`,
		"게시판:*").Scan(&hits); err != nil {
		t.Fatal(err)
	}
	if hits == 0 {
		t.Error("접두 질의가 한 건도 찾지 못했다 (FR-507)")
	}

	rows, err := pool.Query(ctx, `
		EXPLAIN SELECT id FROM posts WHERE search_vector @@ to_tsquery('simple', '게시판:*')`)
	if err != nil {
		t.Fatal(err)
	}
	var plan string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		plan += line + "\n"
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "posts_search_idx") {
		t.Errorf("전문검색이 인덱스를 타지 않는다 (FR-507):\n%s", plan)
	}
	if strings.Contains(plan, "Seq Scan on posts") {
		t.Errorf("순차 스캔으로 떨어졌다:\n%s", plan)
	}
}

// D30 Phase 3 상품 스키마의 제약. 각각 핸들러 버그가 나쁜 행으로 굳는 자리다.
func TestProductSchemaConstraintsBite(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	var productID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO products (slug, name, base_price) VALUES ('tee','티셔츠',12000) RETURNING id`).
		Scan(&productID); err != nil {
		t.Fatal(err)
	}

	refused := map[string]string{
		"음수 가격":       `INSERT INTO products (slug,name,base_price) VALUES ('p1','x',-1)`,
		"대문자 슬러그":     `INSERT INTO products (slug,name,base_price) VALUES ('Tee','x',100)`,
		"슬러그 중복":      `INSERT INTO products (slug,name,base_price) VALUES ('tee','다른 이름',100)`,
		"빈 이름":        `INSERT INTO products (slug,name,base_price) VALUES ('p2','',100)`,
		"옵션 값이 배열 아님": `INSERT INTO product_options (product_id,name,values) VALUES ('` + productID + `','색상','"빨강"')`,
		"옵션 값이 비었음":   `INSERT INTO product_options (product_id,name,values) VALUES ('` + productID + `','색상','[]')`,
		"조합이 객체 아님":   `INSERT INTO product_variants (product_id,option_values) VALUES ('` + productID + `','[]')`,
		"음수 재고":       `INSERT INTO product_variants (product_id,option_values,stock) VALUES ('` + productID + `','{"색상":"빨강"}',-1)`,
		"자기 자신이 부모": `INSERT INTO categories (id,name,slug) VALUES ('00000000-0000-4000-8000-000000000001','x','c1')
		                   ; UPDATE categories SET parent_id = id WHERE slug='c1'`,
	}
	for name, q := range refused {
		if _, err := pool.Exec(ctx, q); err == nil {
			t.Errorf("%s: DB 가 통과시켰다", name)
		}
	}

	// 기본값이 fail-closed 다 — 옵션·재고를 넣기 전에 팔리지 않는다.
	var visible bool
	if err := pool.QueryRow(ctx,
		`SELECT is_visible FROM products WHERE id = $1`, productID).Scan(&visible); err != nil {
		t.Fatal(err)
	}
	if visible {
		t.Error("상품이 기본으로 노출된다 — 옵션·재고 전에 팔린다")
	}

	// 음수 price_delta 는 허용된다. 낮은 등급 옵션이 기본가보다 싼 것이 정상이고,
	// 금지하면 기본가를 최저 조합에 맞추는 우회가 생겨 표시 가격이 거짓이 된다.
	if _, err := pool.Exec(ctx,
		`INSERT INTO product_variants (product_id,option_values,price_delta)
		 VALUES ($1,'{"크기":"S"}',-2000)`, productID); err != nil {
		t.Errorf("음수 price_delta 가 거부됐다: %v", err)
	}

	// 같은 조합은 한 번만.
	if _, err := pool.Exec(ctx,
		`INSERT INTO product_variants (product_id,option_values) VALUES ($1,'{"크기":"S"}')`,
		productID); err == nil {
		t.Error("같은 옵션 조합이 두 번 들어갔다")
	}

	// SKU 는 있을 때만 유일하다 — 없는 조합이 여럿이어도 된다.
	for range 2 {
		if _, err := pool.Exec(ctx,
			`INSERT INTO product_variants (product_id,option_values)
			 VALUES ($1, jsonb_build_object('크기', md5(random()::text)))`, productID); err != nil {
			t.Errorf("SKU 없는 조합이 거부됐다: %v", err)
		}
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO product_variants (product_id,option_values,sku) VALUES ($1,'{"크기":"L"}','SKU-1')`,
		productID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO product_variants (product_id,option_values,sku) VALUES ($1,'{"크기":"XL"}','SKU-1')`,
		productID); err == nil {
		t.Error("같은 SKU 가 두 조합에 들어갔다")
	}
}

// D30 3-1: 주문된 상품·조합은 물리 삭제되지 않는다. 그것이 소프트 삭제 컬럼을
// 두지 않는 근거이므로, RESTRICT 가 실제로 걸리는지 확인한다.
func TestOrderedProductsCannotBeDeleted(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	var productID, variantID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO products (slug,name,base_price) VALUES ('tee','티셔츠',12000) RETURNING id`).
		Scan(&productID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO product_variants (product_id,option_values) VALUES ($1,'{"크기":"S"}') RETURNING id`,
		productID).Scan(&variantID); err != nil {
		t.Fatal(err)
	}

	// 주문이 없으면 지워진다 — 상품 CASCADE 로 옵션·조합도 함께.
	if _, err := pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, productID); err != nil {
		t.Errorf("주문 없는 상품이 안 지워진다: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM product_variants`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("상품을 지웠는데 조합 %d행이 남았다", n)
	}
	// order_items 가 걸리는 쪽은 W3-04 에서 그 테이블이 생긴 뒤 확인한다.
}

// 소속 상품이 있는 카테고리와 하위가 있는 카테고리는 삭제를 거부한다.
func TestCategoryDeletionIsRestricted(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	var parent, child, productID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO categories (name,slug) VALUES ('의류','clothes') RETURNING id`).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO categories (parent_id,name,slug) VALUES ($1,'상의','tops') RETURNING id`,
		parent).Scan(&child); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO products (slug,name,base_price) VALUES ('tee','티셔츠',12000) RETURNING id`).
		Scan(&productID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO product_categories (product_id,category_id) VALUES ($1,$2)`,
		productID, child); err != nil {
		t.Fatal(err)
	}

	// 하위가 있는 카테고리는 지워지지 않는다.
	//
	// 상품이 붙지 않은 별도의 부모-자식 쌍으로 확인한다 — 아래 child 에는
	// 상품이 걸려 있어서, parent 를 CASCADE 로 바꿔도 product_categories 의
	// RESTRICT 가 대신 막는다. 그러면 이 단언은 부모 FK 를 검사하지 않는다.
	var p2, c2 string
	if err := pool.QueryRow(ctx,
		`INSERT INTO categories (name,slug) VALUES ('가전','appliance') RETURNING id`).Scan(&p2); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO categories (parent_id,name,slug) VALUES ($1,'주방','kitchen') RETURNING id`,
		p2).Scan(&c2); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM categories WHERE id = $1`, p2); err == nil {
		t.Error("하위가 있는 카테고리가 지워졌다")
	}
	var left int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM categories WHERE id = $1`, c2).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 1 {
		t.Error("부모를 지우려다 하위가 함께 사라졌다")
	}

	if _, err := pool.Exec(ctx, `DELETE FROM categories WHERE id = $1`, parent); err == nil {
		t.Error("하위가 있는 카테고리가 지워졌다 (상품이 붙은 경로)")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM categories WHERE id = $1`, child); err == nil {
		t.Error("소속 상품이 있는 카테고리가 지워졌다")
	}

	// 상품을 지우면 분류는 함께 간다 — 분류는 이력이 아니다.
	if _, err := pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, productID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM product_categories`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("상품을 지웠는데 분류 %d행이 남았다", n)
	}
	// 그러고 나면 카테고리는 지워진다.
	if _, err := pool.Exec(ctx, `DELETE FROM categories WHERE id = $1`, child); err != nil {
		t.Errorf("빈 카테고리가 안 지워진다: %v", err)
	}
}

// 같은 상품에 같은 카테고리를 두 번 붙일 수 없다 (PK).
func TestProductCategoryIsUniquePair(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var productID, categoryID string
	_ = pool.QueryRow(ctx,
		`INSERT INTO products (slug,name,base_price) VALUES ('tee','티셔츠',1) RETURNING id`).Scan(&productID)
	_ = pool.QueryRow(ctx,
		`INSERT INTO categories (name,slug) VALUES ('의류','clothes') RETURNING id`).Scan(&categoryID)

	if _, err := pool.Exec(ctx,
		`INSERT INTO product_categories (product_id,category_id) VALUES ($1,$2)`,
		productID, categoryID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO product_categories (product_id,category_id) VALUES ($1,$2)`,
		productID, categoryID); err == nil {
		t.Error("같은 (상품, 카테고리) 가 두 번 들어갔다")
	}
}

// seedOrderable makes a product, a variant and an order, returning their ids.
func seedOrderable(t *testing.T, pool *pgxpool.Pool) (productID, variantID, orderID string) {
	t.Helper()
	ctx := context.Background()
	if err := pool.QueryRow(ctx,
		`INSERT INTO products (slug,name,base_price) VALUES ('tee','티셔츠',12000) RETURNING id`).
		Scan(&productID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO product_variants (product_id,option_values,price_delta,stock)
		 VALUES ($1,'{"크기":"L"}',1000,5) RETURNING id`, productID).Scan(&variantID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO orders (order_no,total_amount,receiver_name,receiver_phone,postcode,
		                    address1,orderer_email,orderer_phone)
		VALUES ('20260805-ABCDEFGHJK',13000,'받는이','010-0000-0000','12345',
		        '서울시 어딘가','a@example.com','010-0000-0000') RETURNING id`).
		Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	return productID, variantID, orderID
}

// FR-612: 스냅샷만으로 주문서를 재발행한다. 이름이 바뀌거나 조합이 은퇴한 뒤에도
// 그때 산 것이 그대로 재현돼야 하므로, order_items 는 FK 조인으로 대체하지 않고
// 상품명·옵션 표기·단가를 복사한다.
func TestOrderItemsKeepSnapshotsAfterTheProductChanges(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	productID, variantID, orderID := seedOrderable(t, pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO order_items (order_id,product_id,variant_id,product_name,option_label,
		                         unit_price,quantity)
		VALUES ($1,$2,$3,'티셔츠','크기: L',13000,2)`, orderID, productID, variantID); err != nil {
		t.Fatal(err)
	}

	// 상품과 조합이 바뀌어도 주문서는 그대로다.
	if _, err := pool.Exec(ctx,
		`UPDATE products SET name = '이름이 바뀐 티셔츠', base_price = 99000 WHERE id = $1`,
		productID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE product_variants SET price_delta = 50000, is_visible = false WHERE id = $1`,
		variantID); err != nil {
		t.Fatal(err)
	}

	var name, label string
	var unit, line int
	if err := pool.QueryRow(ctx,
		`SELECT product_name, option_label, unit_price, line_amount FROM order_items`).
		Scan(&name, &label, &unit, &line); err != nil {
		t.Fatal(err)
	}
	if name != "티셔츠" || label != "크기: L" || unit != 13000 {
		t.Errorf("스냅샷이 현재 값으로 바뀌었다: %q / %q / %d", name, label, unit)
	}
	// 생성 컬럼이라 품목 금액이 단가·수량과 어긋날 수 없다.
	if line != 26000 {
		t.Errorf("line_amount = %d, want 26000", line)
	}
	if _, err := pool.Exec(ctx, `UPDATE order_items SET line_amount = 1`); err == nil {
		t.Error("생성 컬럼에 값을 써 넣을 수 있다")
	}
}

// D30 3-1: 주문 행과 주문된 상품·조합은 지워지지 않는다.
func TestOrderedRowsCannotBeDeleted(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	productID, variantID, orderID := seedOrderable(t, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO order_items (order_id,product_id,variant_id,product_name,unit_price,quantity)
		VALUES ($1,$2,$3,'티셔츠',13000,1)`, orderID, productID, variantID); err != nil {
		t.Fatal(err)
	}

	for name, q := range map[string]string{
		"주문": `DELETE FROM orders WHERE id = '` + orderID + `'`,
		"상품": `DELETE FROM products WHERE id = '` + productID + `'`,
		"조합": `DELETE FROM product_variants WHERE id = '` + variantID + `'`,
	} {
		if _, err := pool.Exec(ctx, q); err == nil {
			t.Errorf("주문된 %s 가 지워졌다", name)
		}
	}
}

// **주문 이력이 있는 계정은 지워지지 않는다** (FR-212, D30 3-1, D15 5.2,
// D19 A-402). 주문의 주체가 사라지면 정산과 분쟁 대응이 불가능해지므로, 막는
// 것은 애플리케이션이 아니라 데이터베이스다 — 본인 탈퇴(P-110)와 관리자
// 삭제(A-402) 양쪽에 한 번에 걸린다.
//
// 이 테스트는 **반대를 단언하고 있었다**: 「사용자를 지워도 주문은 남는다 …
// user_id 가 NULL 이 되지 않았다」. 00012 가 외래키를 `ON DELETE SET NULL` 로
// 만들었고 테스트가 그 동작을 고정하고 있었으므로, 문서 다섯 곳이 RESTRICT 를
// 말하는 동안 스키마와 테스트만 짝을 이뤄 반대편에 서 있었다. 요구사항이
// 위이므로(D90) 00018 이 스키마를 옮기고 이 테스트가 따라온다.
func TestDeletingAUserWithAnOrderIsRefused(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var uid string
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email,password_hash,display_name)
		 VALUES ('a@example.com','h','구매자') RETURNING id`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO orders (order_no,user_id,total_amount,receiver_name,receiver_phone,
		                    postcode,address1,orderer_email,orderer_phone)
		VALUES ('20260805-ABCDEFGHJK',$1,1000,'받는이','010','12345','주소','a@example.com','010')`,
		uid); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, uid); err == nil {
		t.Fatal("주문 이력이 있는 사용자가 지워졌다 — 주문의 주체가 사라진다")
	}
	// 거부가 주문을 건드리지 않았는지도 본다. SET NULL 이면 오류 없이 user_id
	// 만 비워지므로, 「오류가 났다」만으로는 두 동작이 구별되지 않는다.
	var email string
	var userID *string
	if err := pool.QueryRow(ctx,
		`SELECT orderer_email, user_id::text FROM orders`).Scan(&email, &userID); err != nil {
		t.Fatal(err)
	}
	if userID == nil || *userID != uid {
		t.Errorf("주문의 주인이 %v 로 바뀌었다", userID)
	}
	// 이메일 스냅샷은 그대로다. 회원이 탈퇴(비활성)해도 주문서를 보낼 곳이
	// 남아 있어야 하고, 비회원 조회(P-503)가 이 값을 쓴다.
	if email != "a@example.com" {
		t.Errorf("이메일 스냅샷이 사라졌다: %q", email)
	}
}

func TestOrderAndCartConstraintsBite(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	productID, variantID, orderID := seedOrderable(t, pool)
	_ = productID

	refused := map[string]string{
		"알 수 없는 주문 상태": `UPDATE orders SET status = '알수없음' WHERE id = '` + orderID + `'`,
		"음수 주문 금액":     `UPDATE orders SET total_amount = -1 WHERE id = '` + orderID + `'`,
		"연락처 없는 주문": `INSERT INTO orders (order_no,total_amount,receiver_name,receiver_phone,
		                      postcode,address1,orderer_email)
		               VALUES ('20260805-KKKKKKKKKK',1,'받는이','010','12345','주소','a@example.com')`,
		"주문번호 중복": `INSERT INTO orders (order_no,total_amount,receiver_name,receiver_phone,
		                    postcode,address1,orderer_email,orderer_phone)
		             VALUES ('20260805-ABCDEFGHJK',1,'받는이','010','12345','주소','a@example.com','010')`,
		"수량 0 인 장바구니 항목": `INSERT INTO carts (guest_key) VALUES ('` + strings.Repeat("g", 20) + `')
		                    ; INSERT INTO cart_items (cart_id,variant_id,quantity)
		                      SELECT id,'` + variantID + `',0 FROM carts`,
	}
	for name, q := range refused {
		if _, err := pool.Exec(ctx, q); err == nil {
			t.Errorf("%s: DB 가 통과시켰다", name)
		}
	}

	// 장바구니 주인은 정확히 하나다.
	// 주인이 둘인 장바구니를 시험하려면 실제 사용자가 있어야 한다 — 없으면
	// SELECT 가 0행이라 INSERT 가 아무것도 넣지 않고 오류도 나지 않는다.
	var ownerID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email,password_hash,display_name)
		 VALUES ('owner@example.com','h','주인') RETURNING id`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	for name, q := range map[string]string{
		"주인 없음": `INSERT INTO carts (user_id,guest_key) VALUES (NULL,NULL)`,
		"주인 둘":  `INSERT INTO carts (user_id,guest_key) VALUES ('` + ownerID + `','` + strings.Repeat("g", 20) + `')`,
	} {
		if _, err := pool.Exec(ctx, q); err == nil {
			t.Errorf("%s 인 장바구니가 만들어졌다", name)
		}
	}

	// 같은 조합은 한 행이다 (수량 합산).
	var cartID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO carts (guest_key) VALUES ($1) RETURNING id`, strings.Repeat("k", 20)).
		Scan(&cartID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO cart_items (cart_id,variant_id,quantity) VALUES ($1,$2,1)`,
		cartID, variantID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO cart_items (cart_id,variant_id,quantity) VALUES ($1,$2,1)`,
		cartID, variantID); err == nil {
		t.Error("같은 조합이 장바구니에 두 행으로 들어갔다")
	}

	// 조합이 사라지면 장바구니 항목도 간다 — 장바구니는 이력이 아니다.
	if _, err := pool.Exec(ctx, `DELETE FROM product_variants WHERE id = $1`, variantID); err != nil {
		t.Fatalf("주문 없는 조합이 안 지워진다: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cart_items`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("조합을 지웠는데 장바구니 항목 %d행이 남았다", n)
	}
}

// FR-619: 약관은 버전을 갖고, 배포된 버전은 수정하지 않는다. 소급 시행일은
// 거부한다 — 소급이 되면 "주문 시점에 유효했던 약관"이 나중에 바뀔 수 있다.
func TestTermsAreVersionedAndCannotBeBackdated(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	var termsID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO terms (kind,version,body,effective_at)
		VALUES ('이용약관','1.0','본문', now() + interval '1 day') RETURNING id`).
		Scan(&termsID); err != nil {
		t.Fatal(err)
	}
	// 같은 종류·버전은 한 번만 — 없으면 "어느 본문에 동의했는지"를 특정할 수 없다.
	if _, err := pool.Exec(ctx,
		`INSERT INTO terms (kind,version,body,effective_at)
		 VALUES ('이용약관','1.0','다른 본문', now())`); err == nil {
		t.Error("같은 종류·버전이 두 번 들어갔다")
	}
	// 소급 시행일은 거부.
	if _, err := pool.Exec(ctx,
		`INSERT INTO terms (kind,version,body,effective_at)
		 VALUES ('이용약관','2.0','본문', now() - interval '1 day')`); err == nil {
		t.Error("소급 시행일이 통과했다")
	}

	// 동의 이력이 가리키는 약관은 지워지지 않는다.
	_, _, orderID := seedOrderable(t, pool)
	if _, err := pool.Exec(ctx,
		`INSERT INTO order_agreements (order_id,terms_id) VALUES ($1,$2)`,
		orderID, termsID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM terms WHERE id = $1`, termsID); err == nil {
		t.Error("동의 이력이 있는 약관이 지워졌다")
	}
	// 같은 주문이 같은 약관에 두 번 동의할 수 없다.
	if _, err := pool.Exec(ctx,
		`INSERT INTO order_agreements (order_id,terms_id) VALUES ($1,$2)`,
		orderID, termsID); err == nil {
		t.Error("같은 (주문, 약관) 동의가 두 번 들어갔다")
	}
}

// seedPayable makes an order with one item and returns (orderID, orderItemID).
func seedPayable(t *testing.T, pool *pgxpool.Pool) (orderID, itemID string) {
	t.Helper()
	ctx := context.Background()
	productID, variantID, orderID := seedOrderable(t, pool)
	if err := pool.QueryRow(ctx, `
		INSERT INTO order_items (order_id,product_id,variant_id,product_name,option_label,
		                         unit_price,quantity)
		VALUES ($1,$2,$3,'티셔츠','크기: L',13000,2) RETURNING id`,
		orderID, productID, variantID).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	return orderID, itemID
}

// FR-608: 주문당 승인 1건. 동시 콜백 두 건이 이중 승인이 되지 않는 것을 DB 가 막는다.
//
// 부분 인덱스의 `AND status <> '실패'` 가 재결제 경로를 남긴다 — 그것 없이는 승인 API
// 가 한 번 실패한 주문이 영영 결제 불가가 되고, P-409 가 못박은 "주문은 결제대기에
// 머문다" 가 성립하지 않는다.
func TestOnePaymentPerOrderButFailedOnesDoNotBlockRetry(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	orderID, _ := seedPayable(t, pool)

	ins := func(status, key string) error {
		_, err := pool.Exec(ctx, `
			INSERT INTO payments (order_id,kind,status,pg,payment_key,approved_amount)
			VALUES ($1,'주문결제',$2,'toss',$3,26000)`, orderID, status, key)
		return err
	}

	if err := ins("실패", "k-fail-1"); err != nil {
		t.Fatal(err)
	}
	// 실패가 쌓여도 재시도를 막지 않는다.
	if err := ins("실패", "k-fail-2"); err != nil {
		t.Errorf("실패 행이 재결제를 막았다: %v", err)
	}
	if err := ins("대기", "k-live-1"); err != nil {
		t.Fatalf("재결제가 막혔다: %v", err)
	}
	// 살아 있는 건이 있으면 두 번째는 들어가지 못한다 — 동시 콜백 두 건 중 하나만
	// 산다.
	if err := ins("대기", "k-live-2"); err == nil {
		t.Error("주문 하나에 살아 있는 결제가 둘 생겼다 (FR-608)")
	}
	if err := ins("승인", "k-live-3"); err == nil {
		t.Error("대기 건이 있는데 승인 건이 또 들어갔다")
	}
}

// FR-611: 환불 누적이 승인금액을 넘지 못한다. 넘으면 결제액보다 많은 돈이 나간다.
func TestRefundedAmountCannotExceedApproved(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	orderID, _ := seedPayable(t, pool)

	var payID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO payments (order_id,kind,status,pg,payment_key,approved_amount,approved_at)
		VALUES ($1,'주문결제','승인','toss','k1',26000,now()) RETURNING id`, orderID).
		Scan(&payID); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx,
		`UPDATE payments SET refunded_amount = 26000 WHERE id = $1`, payID); err != nil {
		t.Errorf("전액 환불이 막혔다: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE payments SET refunded_amount = 26001 WHERE id = $1`, payID); err == nil {
		t.Error("승인금액보다 많은 환불이 기록됐다 (FR-611)")
	}
	if _, err := pool.Exec(ctx,
		`UPDATE payments SET refunded_amount = -1 WHERE id = $1`, payID); err == nil {
		t.Error("음수 환불누적이 기록됐다")
	}
}

// 교환차액 행은 반드시 교환 건을 가리킨다. return_id 가 NULL 이면 "교환 건당 차액
// 1건" 부분 유니크가 통째로 우회된다 — 차액이 두 번 결제된다.
func TestExchangePaymentMustPointAtAReturn(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	orderID, _ := seedPayable(t, pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO payments (order_id,kind,status,pg,payment_key,approved_amount)
		VALUES ($1,'교환차액','대기','toss','k-x',3000)`, orderID); err == nil {
		t.Error("교환 건을 가리키지 않는 교환차액이 들어갔다")
	}
	// 반대 방향도 막힌다: 주문결제가 교환 건을 가리키면 위 유니크의 대상이 흐려진다.
	if _, err := pool.Exec(ctx, `
		INSERT INTO payments (order_id,return_id,kind,status,pg,payment_key,approved_amount)
		VALUES ($1,gen_random_uuid(),'주문결제','대기','toss','k-y',26000)`, orderID); err == nil {
		t.Error("주문결제가 교환 건을 가리켰다")
	}
}

// 같은 승인이 두 행으로 기록되면 A-508 대사가 무엇이 진짜인지 판정하지 못한다.
// 복합인 이유: 두 PG 가 같은 문자열을 발급해도 정상 이벤트여야 한다.
func TestPaymentKeyIsUniquePerPG(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	o1, _ := seedPayable(t, pool)
	var o2 string
	if err := pool.QueryRow(ctx, `
		INSERT INTO orders (order_no,total_amount,receiver_name,receiver_phone,postcode,
		                    address1,orderer_email,orderer_phone)
		VALUES ('20260805-QQQQQQQQQQ',13000,'받는이','010-0000-0000','12345',
		        '서울시 어딘가','b@example.com','010-0000-0000') RETURNING id`).
		Scan(&o2); err != nil {
		t.Fatal(err)
	}

	ins := func(order, pg, key string) error {
		_, err := pool.Exec(ctx, `
			INSERT INTO payments (order_id,kind,status,pg,payment_key,approved_amount)
			VALUES ($1,'주문결제','실패',$2,$3,26000)`, order, pg, key)
		return err
	}
	if err := ins(o1, "toss", "same-key"); err != nil {
		t.Fatal(err)
	}
	if err := ins(o2, "toss", "same-key"); err == nil {
		t.Error("같은 PG 의 같은 승인 키가 두 행으로 들어갔다")
	}
	// 다른 PG 의 같은 문자열은 다른 승인이다.
	if err := ins(o2, "kakao", "same-key"); err != nil {
		t.Errorf("다른 PG 의 같은 문자열이 중복으로 버려졌다: %v", err)
	}
}

// 새로고침 한 번이 이중 환불이 되지 않는다. 요청 키는 A-507 전용이 아니라 모든
// 경로에 NOT NULL 이다 — 화면마다 멱등 수단이 다르면 한쪽만 고쳐진다.
func TestRefundRequestKeyIsIdempotent(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	orderID, itemID := seedPayable(t, pool)
	var payID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO payments (order_id,kind,status,pg,payment_key,approved_amount,approved_at)
		VALUES ($1,'주문결제','승인','toss','k1',26000,now()) RETURNING id`, orderID).
		Scan(&payID); err != nil {
		t.Fatal(err)
	}

	ins := func(key string, amount int) error {
		_, err := pool.Exec(ctx, `
			INSERT INTO refunds (order_id,payment_id,status,requester,amount,request_key)
			VALUES ($1,$2,'요청','구매자',$3,$4)`, orderID, payID, amount, key)
		return err
	}
	if err := ins("req-1", 13000); err != nil {
		t.Fatal(err)
	}
	if err := ins("req-1", 13000); err == nil {
		t.Error("같은 요청 키로 환불이 두 번 들어갔다")
	}
	if err := ins("req-2", 13000); err != nil {
		t.Errorf("다른 요청이 막혔다: %v", err)
	}
	// 0원·음수 환불은 없다.
	if err := ins("req-3", 0); err == nil {
		t.Error("0원 환불이 들어갔다")
	}

	var refID string
	if err := pool.QueryRow(ctx,
		`SELECT id FROM refunds WHERE request_key = 'req-1'`).Scan(&refID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO refund_items (refund_id,order_item_id,quantity) VALUES ($1,$2,1)`,
		refID, itemID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO refund_items (refund_id,order_item_id,quantity) VALUES ($1,$2,1)`,
		refID, itemID); err == nil {
		t.Error("한 환불 건에 같은 품목이 두 행으로 들어갔다")
	}

	// 돈 기록은 지워지지 않는다 (D30 3-1 RESTRICT).
	if _, err := pool.Exec(ctx, `DELETE FROM payments WHERE id = $1`, payID); err == nil {
		t.Error("환불이 가리키는 결제 행이 지워졌다")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM orders WHERE id = $1`, orderID); err == nil {
		t.Error("결제가 달린 주문 행이 지워졌다")
	}
}

// FR-610: 같은 웹훅 이벤트가 두 번 반영되지 않는다. 없으면 같은 입금이 두 번 잡힌다.
func TestWebhookEventIsIdempotentPerPG(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	orderID, _ := seedPayable(t, pool)

	ins := func(pg, event string) error {
		_, err := pool.Exec(ctx,
			`INSERT INTO webhook_events (pg,event_id,order_id,payload) VALUES ($1,$2,$3,'{}')`,
			pg, event, orderID)
		return err
	}
	if err := ins("toss", "evt-1"); err != nil {
		t.Fatal(err)
	}
	if err := ins("toss", "evt-1"); err == nil {
		t.Error("같은 이벤트가 두 번 들어갔다 (FR-610)")
	}
	// 어댑터가 여럿이라는 것이 FR-605 의 전제다. 단일 컬럼 UNIQUE 였다면 여기서
	// 정상 이벤트가 중복으로 버려진다.
	if err := ins("kakao", "evt-1"); err != nil {
		t.Errorf("다른 PG 의 같은 이벤트 ID 가 버려졌다: %v", err)
	}
	// order_id 는 NULL 을 허용한다 — 주문을 특정하지 못한 이벤트도 받아 두고
	// A-603 이 사람에게 보인다.
	if _, err := pool.Exec(ctx,
		`INSERT INTO webhook_events (pg,event_id,payload) VALUES ('toss','evt-orphan','{}')`); err != nil {
		t.Errorf("주문 미상 이벤트가 거부됐다: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE webhook_events SET status = '알수없음'`); err == nil {
		t.Error("정의되지 않은 웹훅 상태가 들어갔다")
	}
}

// 같은 품목에 처리 중인 반품·교환이 둘 이상 생기지 않는다. 생기면 같은 물건을
// 두 번 환불받는다.
//
// is_open 이 비정규화인 이유는 하나뿐이다: 부분 인덱스의 술어는 같은 테이블의
// 컬럼만 볼 수 있어 returns.status 를 참조할 수 없다.
func TestOneOpenReturnPerOrderItem(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	orderID, itemID := seedPayable(t, pool)

	mkReturn := func(no string) string {
		t.Helper()
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO returns (return_no,order_id,kind,status)
			VALUES ($1,$2,'반품','반품접수') RETURNING id`, no, orderID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	r1, r2 := mkReturn("R-0001"), mkReturn("R-0002")

	if _, err := pool.Exec(ctx,
		`INSERT INTO return_items (return_id,order_item_id,quantity) VALUES ($1,$2,1)`,
		r1, itemID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO return_items (return_id,order_item_id,quantity) VALUES ($1,$2,1)`,
		r2, itemID); err == nil {
		t.Fatal("같은 품목에 처리 중인 건이 둘 생겼다")
	}

	// 앞 건이 종결되면 다시 받을 수 있다 — 인덱스가 "처리 중" 만 겨냥한다.
	if _, err := pool.Exec(ctx,
		`UPDATE return_items SET is_open = false WHERE return_id = $1`, r1); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO return_items (return_id,order_item_id,quantity) VALUES ($1,$2,1)`,
		r2, itemID); err != nil {
		t.Errorf("종결된 건이 새 반품을 막았다: %v", err)
	}
}

// 동시 INSERT 두 건 중 하나만 성공한다. 애플리케이션이 먼저 SELECT 로 확인하는
// 방식은 두 트랜잭션이 같은 빈 결과를 보고 둘 다 통과한다.
func TestConcurrentReturnItemInsertsLeaveOneWinner(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	orderID, itemID := seedPayable(t, pool)

	ids := make([]string, 2)
	for i, no := range []string{"R-1001", "R-1002"} {
		if err := pool.QueryRow(ctx, `
			INSERT INTO returns (return_no,order_id,kind,status)
			VALUES ($1,$2,'반품','반품접수') RETURNING id`, no, orderID).Scan(&ids[i]); err != nil {
			t.Fatal(err)
		}
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, rid := range ids {
		go func(rid string) {
			<-start
			_, err := pool.Exec(ctx,
				`INSERT INTO return_items (return_id,order_item_id,quantity) VALUES ($1,$2,1)`,
				rid, itemID)
			errs <- err
		}(rid)
	}
	close(start)

	ok := 0
	for i := 0; i < 2; i++ {
		if <-errs == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Errorf("동시 두 건 중 %d 건이 성공했다, want 1", ok)
	}
}

// 수거 확인 시점을 넘겼는데 배송비 스냅샷이 없으면, 나중에 A-512 를 바꾸는 것만으로
// 과거 환불액이 달라진다.
func TestReturnRequiresShippingSnapshotAfterPickup(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	orderID, _ := seedPayable(t, pool)

	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO returns (return_no,order_id,kind,status)
		VALUES ('R-2001',$1,'반품','반품접수') RETURNING id`, orderID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	// 접수 단계에서는 아직 모르는 값이라 비어 있어도 된다.
	if _, err := pool.Exec(ctx, `UPDATE returns SET status = '반품수거' WHERE id = $1`, id); err == nil {
		t.Error("스냅샷 없이 수거로 넘어갔다")
	}
	if _, err := pool.Exec(ctx, `
		UPDATE returns SET status = '반품수거', fault = '구매자',
		       shipping_fee_policy = '차감', shipping_fee_amount = 3000
		WHERE id = $1`, id); err != nil {
		t.Errorf("스냅샷을 채웠는데 막혔다: %v", err)
	}
	// 하자 상품의 반품비를 구매자가 물지 않는다.
	if _, err := pool.Exec(ctx,
		`UPDATE returns SET fault = '판매자' WHERE id = $1`, id); err == nil {
		t.Error("판매자 귀책인데 배송비가 남아 있다")
	}
	if _, err := pool.Exec(ctx,
		`UPDATE returns SET fault = '판매자', shipping_fee_amount = 0 WHERE id = $1`, id); err != nil {
		t.Errorf("판매자 귀책 + 배송비 0 이 막혔다: %v", err)
	}
}

// 종류와 상태가 짝이 맞는다. 교환 전용 컬럼이 반품 건에 실리면 P-514 가 존재하지
// 않는 교환 건의 차액을 결제하려 든다.
func TestReturnKindAndStatusMustAgree(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	productID, variantID, orderID := seedOrderable(t, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO order_items (order_id,product_id,variant_id,product_name,option_label,
		                         unit_price,quantity)
		VALUES ($1,$2,$3,'티셔츠','크기: L',13000,2)`, orderID, productID, variantID); err != nil {
		t.Fatal(err)
	}

	ins := func(no, kind, status string, extra string, args ...any) error {
		q := `INSERT INTO returns (return_no,order_id,kind,status` + extra
		_, err := pool.Exec(ctx, q, append([]any{no, orderID, kind, status}, args...)...)
		return err
	}
	// 교환 상태를 단 반품 건.
	if err := ins("R-3001", "반품", "교환접수", `) VALUES ($1,$2,$3,$4)`); err == nil {
		t.Error("반품 건이 교환 상태를 달았다")
	}
	// 교환인데 새 조합이 없다.
	if err := ins("R-3002", "교환", "교환접수", `) VALUES ($1,$2,$3,$4)`); err == nil {
		t.Error("새 조합 없는 교환이 들어갔다")
	}
	// 반품인데 교환 전용 컬럼이 실렸다.
	if err := ins("R-3003", "반품", "반품접수",
		`,new_variant_id) VALUES ($1,$2,$3,$4,$5)`, variantID); err == nil {
		t.Error("반품 건에 교환 전용 컬럼이 실렸다")
	}
	// 차액을 받으러 가는데 받을 차액이 없다 — 0원 결제가 만들어진다.
	if err := ins("R-3004", "교환", "차액결제대기",
		`,new_variant_id,price_difference) VALUES ($1,$2,$3,$4,$5,0)`, variantID); err == nil {
		t.Error("차액 0 인데 차액결제대기로 들어갔다")
	}
	if err := ins("R-3005", "교환", "차액결제대기",
		`,new_variant_id,price_difference) VALUES ($1,$2,$3,$4,$5,3000)`, variantID); err != nil {
		t.Errorf("정상 교환 건이 막혔다: %v", err)
	}
}

// order_id 에 UNIQUE 를 걸면 D14 의 `교환발송 → 배송완료` 복귀 흐름이 성립하지
// 않는다. 부분 유니크 둘이 실제로 지키려던 불변식이다.
func TestShipmentUniquenessIsPerKindNotPerOrder(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	productID, variantID, orderID := seedOrderable(t, pool)
	_ = productID
	var retID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO returns (return_no,order_id,kind,status,new_variant_id)
		VALUES ('R-4001',$1,'교환','교환접수',$2) RETURNING id`, orderID, variantID).
		Scan(&retID); err != nil {
		t.Fatal(err)
	}

	ins := func(kind, tracking string, ret any) error {
		_, err := pool.Exec(ctx, `
			INSERT INTO shipments (order_id,return_id,kind,carrier,tracking_no,shipped_at)
			VALUES ($1,$2,$3,'cj',$4,now())`, orderID, ret, kind, tracking)
		return err
	}
	if err := ins("최초발송", "T-1", nil); err != nil {
		t.Fatal(err)
	}
	if err := ins("최초발송", "T-2", nil); err == nil {
		t.Error("최초발송이 두 건 생겼다")
	}
	// 같은 주문에 교환 재발송이 붙는 것은 정상이다 — 여기서 막히면 복귀 흐름이 죽는다.
	if err := ins("교환재발송", "T-3", retID); err != nil {
		t.Fatalf("교환 재발송이 막혔다: %v", err)
	}
	if err := ins("교환재발송", "T-4", retID); err == nil {
		t.Error("교환 건당 재발송이 두 건 생겼다")
	}
	// 종류와 return_id 가 짝이 맞는다.
	if err := ins("교환재발송", "T-5", nil); err == nil {
		t.Error("교환 건을 가리키지 않는 재발송이 들어갔다")
	}
	if err := ins("최초발송", "T-6", retID); err == nil {
		t.Error("최초발송이 교환 건을 가리켰다")
	}
}

// 돈 기록이 가리키는 반품 건은 지워지지 않는다. W3-05 에서 컬럼만 만들어 둔
// return_id 의 FK 가 여기서 걸린다.
func TestReturnIsRestrictedWhileMoneyPointsAtIt(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	productID, variantID, orderID := seedOrderable(t, pool)
	_ = productID
	var retID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO returns (return_no,order_id,kind,status,new_variant_id,price_difference)
		VALUES ('R-5001',$1,'교환','차액결제대기',$2,3000) RETURNING id`, orderID, variantID).
		Scan(&retID); err != nil {
		t.Fatal(err)
	}

	// 없는 반품 건을 가리키는 결제는 들어가지 못한다 — FK 가 걸리기 전에는
	// 아무 UUID 나 통과했다.
	if _, err := pool.Exec(ctx, `
		INSERT INTO payments (order_id,return_id,kind,status,pg,payment_key,approved_amount)
		VALUES ($1,gen_random_uuid(),'교환차액','대기','toss','k-ghost',3000)`, orderID); err == nil {
		t.Error("존재하지 않는 반품 건을 가리키는 교환차액이 들어갔다")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO payments (order_id,return_id,kind,status,pg,payment_key,approved_amount)
		VALUES ($1,$2,'교환차액','대기','toss','k-diff',3000)`, orderID, retID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM returns WHERE id = $1`, retID); err == nil {
		t.Error("결제가 가리키는 반품 건이 지워졌다")
	}
}

// seedFiles lists every embedded migration, so a new seed file is seen without
// anyone editing this test.
//
// 목록을 적어 두면 다음 Phase 의 시드가 조용히 빠지고, 그때 기대값을 손으로
// 맞추게 된다 (M9). 파일을 찾는 쪽이 짧고 틀릴 곳이 없다.
func seedFiles(t *testing.T) []string {
	t.Helper()
	entries, err := fs.ReadDir(FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			out = append(out, e.Name())
		}
	}
	if len(out) == 0 {
		t.Fatal("마이그레이션 파일을 하나도 찾지 못했다")
	}
	sort.Strings(out)
	return out
}
