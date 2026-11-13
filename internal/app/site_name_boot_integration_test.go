package app

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/emirue/ondolith/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

// **설치 마법사에 넣은 사이트 이름이 「사이트 설정」에 보인다.** 이름은 설정 파일(site_name)에만 있었고
// A-201 은 DB 의 site.name 을 읽어 칸이 비었다 — 그대로 사업자 정보만 넣어 저장하면 422 였다 (사이트 보고
// 2026-11-14). 부팅이 DB 가 비었을 때 한 번 옮긴다. 운영자가 이미 바꾼 이름은 건드리지 않는다.
func TestBootCopiesTheInstalledSiteNameIntoSettings(t *testing.T) {
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s 미설정", dsnEnv)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, s := range []string{"DROP SCHEMA public CASCADE", "CREATE SCHEMA public"} {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	migrateAndSeed(t, pool)

	boot := func() {
		t.Helper()
		cfg := &config.Config{DatabaseURL: dsn, SiteName: "설치 때 이름", UploadDir: t.TempDir()}
		_, cleanup, err := New(ctx, cfg, "1.0.0", slog.New(slog.DiscardHandler), nil)
		if err != nil {
			t.Fatalf("기동 실패: %v", err)
		}
		cleanup()
	}
	stored := func() string {
		t.Helper()
		var v string
		if err := pool.QueryRow(ctx, `SELECT coalesce((SELECT value FROM settings WHERE key = 'site.name'), '')`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	if stored() != "" {
		t.Fatal("전제가 깨졌다: 시드가 이미 site.name 을 넣었다")
	}
	boot()
	if got := stored(); got != "설치 때 이름" {
		t.Errorf("부팅 뒤 site.name = %q, want 설치 때 이름", got)
	}

	if _, err := pool.Exec(ctx, `UPDATE settings SET value = '운영자가 바꾼 이름' WHERE key = 'site.name'`); err != nil {
		t.Fatal(err)
	}
	boot()
	if got := stored(); got != "운영자가 바꾼 이름" {
		t.Errorf("부팅이 운영자가 바꾼 이름을 덮어썼다: %q", got)
	}
}
