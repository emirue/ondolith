package app

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

// **일반 사이트(cms)는 부팅할 때 라우트 점검 경고를 내지 않는다** (FR-710). 일부러 등록하지 않은 쇼핑몰 화면과
// 그 권한을 한 줄씩 WARN 으로 찍어 부팅마다 58줄이 나왔고, 진짜 경고가 그 사이에 묻혔다 (사이트 보고 2026-11-14).
// 그 몫은 INFO 한 줄로 접는다. 쇼핑몰(shop)로 띄우면 빠진 화면이 없으므로 역시 경고가 없다.
func TestBootDoesNotWarnAboutWhatCmsLeavesOut(t *testing.T) {
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

	boot := func(siteType string) string {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO settings (key, value) VALUES ('site.type', $1)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, siteType); err != nil {
			t.Fatal(err)
		}
		var logs strings.Builder
		cfg := &config.Config{DatabaseURL: dsn, SiteName: "경고", UploadDir: t.TempDir()}
		_, cleanup, err := New(ctx, cfg, "1.0.0", slog.New(slog.NewTextHandler(&logs, nil)), nil)
		if err != nil {
			t.Fatalf("%s 기동 실패: %v", siteType, err)
		}
		cleanup()
		return logs.String()
	}

	for _, siteType := range []string{"cms", "shop"} {
		logs := boot(siteType)
		var warns []string
		for _, line := range strings.Split(logs, "\n") {
			if strings.Contains(line, "level=WARN") && strings.Contains(line, "라우트 자체 점검") {
				warns = append(warns, line)
			}
		}
		if len(warns) > 0 {
			t.Errorf("%s 부팅이 라우트 점검 경고 %d줄을 냈다. 첫 줄: %s", siteType, len(warns), warns[0])
		}
		folded := strings.Contains(logs, "쇼핑몰이 아니라 등록하지 않은")
		if folded != (siteType == "cms") {
			t.Errorf("%s 부팅의 요약 한 줄 = %v, want %v", siteType, folded, siteType == "cms")
		}
	}
}
