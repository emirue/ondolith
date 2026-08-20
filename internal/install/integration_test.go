package install

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/emirue/ondolith/internal/config"
)

// These exercise the wizard against a real PostgreSQL, because the parts worth
// checking — goose applying the embedded migrations, the admin insert, the
// duplicate guard — are exactly the parts a fake would not reproduce.
//
//	make test-integration
//
// Skipped when ONDOLITH_TEST_DSN is unset so `make check` stays DB-free.
const dsnEnv = "ONDOLITH_TEST_DSN"

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s 미설정 — 통합 테스트를 건너뜁니다 (make test-integration)", dsnEnv)
	}
	return dsn
}

// freshSchema drops everything the wizard would create so each test starts from
// the empty database a real operator is told to provide.
func freshSchema(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("테스트 DB 연결 실패: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("테스트 DB 응답 없음: %v", err)
	}
	// Drop the schema rather than a list of tables. Enumerating them meant this
	// helper had to be edited with every migration, and it broke the moment
	// Phase 1 added a table referencing users: DROP TABLE users failed on the
	// dependency instead of resetting anything. "The empty database a real
	// operator provides" is the whole schema, so reset the whole schema.
	for _, stmt := range []string{
		"DROP SCHEMA public CASCADE",
		"CREATE SCHEMA public",
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return pool
}

func installForm(dsn string) url.Values {
	u, _ := url.Parse(dsn)
	pw, _ := u.User.Password()
	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = "5432"
	}
	sslmode := u.Query().Get("sslmode")
	if sslmode == "" {
		sslmode = "disable"
	}
	return url.Values{
		"db_host":                {host},
		"db_port":                {port},
		"db_user":                {u.User.Username()},
		"db_password":            {pw},
		"db_name":                {strings.TrimPrefix(u.Path, "/")},
		"db_sslmode":             {sslmode},
		"site_name":              {"온돌 통합 테스트"},
		"admin_email":            {"Admin@Example.COM"},
		"admin_name":             {"온돌 운영자"},
		"admin_password":         {"correct-horse-battery"},
		"admin_password_confirm": {"correct-horse-battery"},
	}
}

type wizard struct {
	handler    http.Handler
	configPath string
	installed  *config.Config
}

func newWizard(t *testing.T) *wizard {
	t.Helper()
	w := &wizard{configPath: filepath.Join(t.TempDir(), "ondolith.json")}
	h, err := New(w.configPath, slog.New(slog.DiscardHandler), func(c *config.Config) error {
		w.installed = c
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	w.handler = h
	return w
}

func (w *wizard) post(form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/install", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	w.handler.ServeHTTP(rec, req)
	return rec
}

// FR-102 ~ FR-106: one form submission provisions the database, creates the
// administrator, writes the config and hands control to the operating tree.
func TestInstallProvisionsDatabase(t *testing.T) {
	dsn := testDSN(t)
	pool := freshSchema(t, dsn)
	ctx := context.Background()

	w := newWizard(t)
	rec := w.post(installForm(dsn))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("HTTP %d, want 303. 본문: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Errorf("Location = %q, want /", got)
	}

	// FR-103: the embedded migrations ran.
	var version int64
	if err := pool.QueryRow(ctx, "SELECT max(version_id) FROM goose_db_version").Scan(&version); err != nil {
		t.Fatalf("goose 버전 조회 실패 — 마이그레이션이 실행되지 않았다: %v", err)
	}
	if version < 1 {
		t.Errorf("goose 버전 = %d, want >= 1", version)
	}

	// FR-104: administrator exists, email lower-cased, password only as bcrypt.
	//
	// `is_admin` 은 보지 않는다 — 00006 이 지웠다 (W2-01). 관리자인지는 아래
	// 역할 단언이 정한다. 그 단언이 이 테스트의 본체이고, 불리언은 처음부터
	// 그것을 대신하지 못했다.
	var email, hash string
	err := pool.QueryRow(ctx,
		"SELECT email, password_hash FROM users").Scan(&email, &hash)
	if err != nil {
		t.Fatalf("관리자 계정 조회 실패: %v", err)
	}
	if email != "admin@example.com" {
		t.Errorf("email = %q, want admin@example.com (소문자화)", email)
	}

	// NFR-212. **폼의 칸 이름과 핸들러가 읽는 이름은 다른 것이다.** 한쪽만
	// 고치면 운영자가 적은 이름은 조용히 버려지고 기본값이 저장되는데, 화면은
	// 아무 말도 하지 않는다. 여기까지 와야 배선이 확인된다.
	var display string
	if err := pool.QueryRow(ctx,
		"SELECT display_name FROM users WHERE email = 'admin@example.com'").Scan(&display); err != nil {
		t.Fatalf("표시 이름 조회 실패: %v", err)
	}
	if display != "온돌 운영자" {
		t.Errorf("display_name = %q, want 온돌 운영자 — 폼의 admin_name 이 닿지 않았다", display)
	}
	if strings.Contains(display, "@") {
		t.Errorf("표시 이름에 이메일이 들어갔다: %q — 글·댓글 작성자 줄에 그대로 나간다", display)
	}

	// **FR-104 는 "관리자 계정" 이지 "is_admin 이 켜진 행" 이 아니다.**
	//
	// 권한은 역할이 정한다 (D15). 불리언만 보던 이 테스트는 통과하는데 새로
	// 설치한 사이트의 유일한 관리자가 `/admin` 에서 「권한이 없습니다」를 보고
	// 있었다 — 00003 의 백필은 마이그레이션 시점에 한 번 돌고, 그때 users 는
	// 아직 비어 있다. 관리자 화면에 못 들어가면 그 사이트는 손댈 방법이 없다.
	var roles []string
	rows, err := pool.Query(ctx, `
		SELECT r.key FROM users u
		JOIN user_roles ur ON ur.user_id = u.id
		JOIN roles r ON r.id = ur.role_id
		WHERE u.email = 'admin@example.com'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		roles = append(roles, k)
	}
	rows.Close()
	if !slices.Contains(roles, "admin") {
		t.Errorf("관리자 역할이 없다 (역할: %v) — 설치 직후 /admin 에 들어갈 수 없다", roles)
	}

	// 그 역할이 실제로 superuser 여야 의미가 있다. 이름만 'admin' 이고 권한이
	// 없으면 위 단언은 통과하면서 아무것도 보장하지 않는다.
	var superuser bool
	if err := pool.QueryRow(ctx,
		`SELECT r.is_superuser FROM roles r WHERE r.key = 'admin'`).Scan(&superuser); err != nil {
		t.Fatal(err)
	}
	if !superuser {
		t.Error("admin 역할이 superuser 가 아니다 — 역할을 붙여도 관리자가 되지 않는다")
	}
	if strings.Contains(hash, "correct-horse-battery") {
		t.Fatal("비밀번호가 평문으로 저장됐다")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("correct-horse-battery")); err != nil {
		t.Errorf("bcrypt 해시가 아니거나 비밀번호와 맞지 않는다: %v", err)
	}

	// FR-105: the config is written, owner-only, and loads back.
	fi, err := os.Stat(w.configPath)
	if err != nil {
		t.Fatalf("설정 파일이 없다: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("설정 파일 권한 = %04o, want 0600", perm)
	}
	cfg, err := config.Load(w.configPath)
	if err != nil {
		t.Fatalf("설정 파일을 다시 읽을 수 없다: %v", err)
	}
	if cfg.SiteName != "온돌 통합 테스트" {
		t.Errorf("SiteName = %q", cfg.SiteName)
	}

	// FR-106: the operating tree was handed the same config.
	if w.installed == nil {
		t.Fatal("onInstalled 이 호출되지 않았다 — 운영 모드로 전환되지 않는다")
	}
	if w.installed.DatabaseURL != cfg.DatabaseURL {
		t.Error("운영 트리에 넘어간 설정이 저장된 설정과 다르다")
	}
}

// FR-108: pointing the wizard at a database that already holds an account must
// not silently take it over.
func TestInstallRefusesDatabaseThatAlreadyHasAdmin(t *testing.T) {
	dsn := testDSN(t)
	freshSchema(t, dsn)

	if rec := newWizard(t).post(installForm(dsn)); rec.Code != http.StatusSeeOther {
		t.Fatalf("첫 설치 HTTP %d, want 303", rec.Code)
	}

	second := newWizard(t)
	rec := second.post(installForm(dsn))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("두 번째 설치 HTTP %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "이미") {
		t.Errorf("거부 사유가 화면에 없다: %s", rec.Body.String())
	}
	if second.installed != nil {
		t.Error("거부됐는데 운영 모드로 전환됐다")
	}
	if _, err := os.Stat(second.configPath); err == nil {
		t.Error("거부됐는데 설정 파일이 쓰였다")
	}
}

// NFR-302: migrations are idempotent, which is what makes "replace the binary
// and restart" safe.
func TestMigrationsAreRerunnable(t *testing.T) {
	dsn := testDSN(t)
	pool := freshSchema(t, dsn)
	ctx := context.Background()

	if rec := newWizard(t).post(installForm(dsn)); rec.Code != http.StatusSeeOther {
		t.Fatalf("설치 HTTP %d, want 303", rec.Code)
	}
	var before int64
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM goose_db_version").Scan(&before); err != nil {
		t.Fatal(err)
	}

	// A second wizard against the same database re-runs migrations before it
	// hits the duplicate-admin guard. Nothing new must be applied.
	newWizard(t).post(installForm(dsn))

	var after int64
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM goose_db_version").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("goose 적용 건수 %d → %d, 재실행에서 늘어나면 안 된다", before, after)
	}
}
