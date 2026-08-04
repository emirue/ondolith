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
	sql, err := fs.ReadFile(FS, "00003_rbac_seed.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(sql), "-- +goose Down")

	block := regexp.MustCompile(`(?s)INSERT INTO role_permissions.*?FROM \(VALUES(.*?)\)\s*AS`)
	m := block.FindStringSubmatch(up)
	if m == nil {
		t.Fatal("00003_rbac_seed.sql 에서 role_permissions VALUES 블록을 찾지 못했다 — 이 테스트가 아무것도 검증하지 않는다")
	}

	pair := regexp.MustCompile(`\(\s*'([a-z_]+)'\s*,\s*'([a-z][a-z0-9._]*)'\s*\)`)
	out := map[string][]string{}
	for _, p := range pair.FindAllStringSubmatch(m[1], -1) {
		out[p[1]] = append(out[p[1]], p[2])
	}
	if len(out) == 0 {
		t.Fatal("시드에서 부여를 한 건도 읽지 못했다")
	}
	for _, v := range out {
		sort.Strings(v)
	}
	return out
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
		"attachments", "board_fields", "boards", "comments",
		"email_verification_tokens", "goose_db_version", "menus", "pages",
		"password_reset_tokens", "permissions", "posts", "role_permissions",
		"roles", "sessions", "settings", "social_accounts", "user_roles", "users",
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

	// D30: Phase 1 seeds only the 19 permissions D15 §2.2 marks Phase 1.
	// Seeding all 37 would make D15 §4.4's unused-permission warning fire 18
	// times every boot, and a warning that always fires is not read.
	if n := count(t, pool, `SELECT count(*) FROM permissions`); n != 19 {
		t.Errorf("permissions %d행, want 19행 (Phase 1 분만)", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM permissions WHERE is_scoped`); n != 0 {
		t.Errorf("스코프 권한이 %d행 시딩됐다 — 대상 6개는 전부 Phase 2다", n)
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

	// The column stays until Phase 2 (W2-01). Dropping it here would leave no
	// downgrade path (D30 two-release rule, NFR-308).
	if n := count(t, pool, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'users' AND column_name = 'is_admin'`); n != 1 {
		t.Error("is_admin 컬럼이 사라졌다 — Phase 1 에서 지우면 다운그레이드 경로가 없다")
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
