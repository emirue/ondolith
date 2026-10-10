package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/config"
)

// **A-604 가 백업 폴더(backup_dir)의 자동 백업을 보인다** (FR-715). 설정을 고른 적이
// 없으면 화면은 꺼짐·0시·7개를 보인다 — 스케줄러의 기본값(backupSchedule)과 같다.
func TestBackupScreenListsStoredBackups(t *testing.T) {
	dir := t.TempDir()
	srv, pool := liveSiteWith(t, func(c *config.Config) { c.BackupDir = dir })
	grantSuperuser(t, auth.NewStore(pool), mkUser(t, pool, "su@example.com"))
	if err := os.WriteFile(filepath.Join(dir, "auto-20261001-0300.zip"), make([]byte, 3<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	c := client()
	login(t, srv.URL, "su@example.com", c)
	code, body := mustGet(t, c, srv.URL+"/admin/backup")
	if code != http.StatusOK {
		t.Fatalf("A-604 = HTTP %d", code)
	}
	for _, want := range []string{
		`value="auto-20261001-0300.zip"`, "3.0 MB",
		`name="backup.hour" min="0" max="23" value="0"`, `name="backup.keep" min="1" max="30" value="7"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("A-604 에 %s 가 없다", want)
		}
	}
}

// 백업 폴더가 업로드 폴더 안이면 뜨지 않는다 — 다음 백업이 이전 백업을 싣는다 (FR-715).
func TestBootRefusesBackupDirInsideUploads(t *testing.T) {
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s 미설정 — 통합 테스트를 건너뜁니다 (make test-integration)", dsnEnv)
	}
	up := t.TempDir()
	_, cleanup, err := New(context.Background(),
		&config.Config{DatabaseURL: dsn, UploadDir: up, BackupDir: filepath.Join(up, "backups")},
		"1.0.0", slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err == nil {
		cleanup()
		t.Fatal("업로드 폴더 안의 백업 폴더로 기동했다")
	}
	if !strings.Contains(err.Error(), "backup_dir") {
		t.Errorf("오류가 원인을 말하지 않는다: %v", err)
	}
}
