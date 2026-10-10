//go:build pgdump

// pgdump 빌드 태그 뒤에 둔다. 기본 빌드에 들어가면 `make test-integration` 이 DB 서버(18)와 같은
// 버전의 pg_dump·pg_restore 를 요구하게 되고, 없는 환경에서는 SKIP 이 생긴다 — 그 SKIP 을 이
// 저장소의 게이트가 거부한다 (scripts/check-testrun.sh). toss_live_test.go 와 같은 이유다.
//
// 실행: make test-backup (ONDOLITH_TEST_DSN, PATH 의 pg_dump·pg_restore)

package app

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emirue/ondolith/internal/backup"
	"github.com/emirue/ondolith/internal/config"
	"github.com/emirue/ondolith/internal/content"
)

// **백업 덤프로 빈 DB 에 복원한 사이트가 같은 글을 보인다** (FR-713, D80 Phase 6 완료 기준).
//
// 진짜 pg_dump·pg_restore 가 PATH 에 있어야 하고, 그 버전이 DB 서버보다 낮으면 실패한다 —
// 그래서 pgdump 태그 뒤에 두고 make test-backup 으로 돈다 (D70 「백업」).
func TestBackupRestoresIntoAnEmptyDatabase(t *testing.T) {
	srv, pool := liveSite(t)
	ctx := context.Background()
	mkBoard(t, pool, "free", content.PresetPublic)
	mkUser(t, pool, "m@example.com")
	c := client()
	login(t, srv.URL, "m@example.com", c)
	resp := httpPost(t, c, srv.URL+"/board/free/write", url.Values{"title": {"복원될 글"}, "body": {"백업 본문"}})
	resp.Body.Close()
	postPath := resp.Header.Get("Location")

	dsn := os.Getenv(dsnEnv)
	var dump bytes.Buffer
	if err := backup.Dump(dsn)(ctx, &dump); err != nil {
		t.Fatalf("pg_dump: %v", err)
	}
	_, pw := backup.Target(dsn)
	restore := func(stdin io.Reader, args ...string) string {
		t.Helper()
		cmd := exec.Command("pg_restore", args...)
		cmd.Env = append(os.Environ(), "PGPASSWORD="+pw)
		cmd.Stdin = stdin
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("pg_restore %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	if list := restore(bytes.NewReader(dump.Bytes()), "--list"); !strings.Contains(list, "TABLE DATA public posts") {
		t.Fatalf("pg_restore --list 에 글 표 데이터가 없다:\n%s", list)
	}

	// 덤프는 기본 public 스키마를 만들지 않는다 — 복원 대상은 새로 만든 빈 DB 여야 한다(D70).
	u, _ := url.Parse(dsn)
	name := strings.TrimPrefix(u.Path, "/") + "_restore"
	if _, err := pool.Exec(ctx, `DROP DATABASE IF EXISTS `+name); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE DATABASE `+name); err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	restored := u.String()
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`) })
	db, _ := backup.Target(restored)
	restore(bytes.NewReader(dump.Bytes()), "--no-owner", "--exit-on-error", "--dbname="+db)

	h, cleanup, err := New(ctx, &config.Config{DatabaseURL: restored, SiteName: "복원", UploadDir: t.TempDir()},
		"1.0.0", slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil {
		t.Fatalf("복원한 DB 로 기동 실패: %v", err)
	}
	t.Cleanup(cleanup)
	back := httptest.NewServer(h)
	t.Cleanup(back.Close)
	if code, body := mustGet(t, client(), back.URL+postPath); code != http.StatusOK || !strings.Contains(body, "복원될 글") {
		t.Errorf("복원한 사이트의 %s = HTTP %d, 글이 보이나 %v", postPath, code, strings.Contains(body, "복원될 글"))
	}
	var sessions int
	rp, err := pgxpool.New(ctx, restored)
	if err != nil {
		t.Fatal(err)
	}
	defer rp.Close()
	if err := rp.QueryRow(ctx, `SELECT count(*) FROM sessions`).Scan(&sessions); err != nil || sessions != 0 {
		t.Errorf("복원한 DB 에 로그인 세션이 %d개 (%v) — 백업에 세션이 실렸다", sessions, err)
	}
}
