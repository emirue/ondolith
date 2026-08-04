package install

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
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
	var email, hash string
	var isAdmin bool
	err := pool.QueryRow(ctx,
		"SELECT email, password_hash, is_admin FROM users").Scan(&email, &hash, &isAdmin)
	if err != nil {
		t.Fatalf("관리자 계정 조회 실패: %v", err)
	}
	if email != "admin@example.com" {
		t.Errorf("email = %q, want admin@example.com (소문자화)", email)
	}
	if !isAdmin {
		t.Error("is_admin = false, want true")
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
