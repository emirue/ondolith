package app

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/config"
	"github.com/emirue/ondolith/internal/secretbox"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 부팅이 (1) secret_key 가 없는 설정본에 키를 만들어 저장하고 (2) 평문으로
// 남아 있던 자격증명을 봉인한다 — v0.1.0·v0.2.0 에서 올라오는 사이트의 경로다.
func TestBootSealsLegacyPlaintextSecrets(t *testing.T) {
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
	if _, err := pool.Exec(ctx,
		`INSERT INTO settings (key, value) VALUES ('pg.secret_key', 'test_sk_legacy')`); err != nil {
		t.Fatal(err)
	}

	path := t.TempDir() + "/ondolith.json"
	cfg := &config.Config{DatabaseURL: dsn, SiteName: "테스트"}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SecretKey != "" {
		t.Fatal("전제가 깨졌다: 설정에 이미 키가 있다")
	}
	var logs strings.Builder
	_, cleanup, err := New(ctx, cfg, "1.0.0", slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatalf("기동 실패: %v", err)
	}
	cleanup()

	saved, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secretbox.New(saved.SecretKey); err != nil {
		t.Errorf("부팅이 설정 파일에 쓸 수 있는 키를 저장하지 않았다: %q (%v)", saved.SecretKey, err)
	}
	var raw string
	if err := pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = 'pg.secret_key'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !secretbox.IsSealed(raw) || strings.Contains(raw, "test_sk_legacy") {
		t.Errorf("부팅 뒤에도 pg.secret_key 가 평문이다: %q", raw)
	}
	if !strings.Contains(logs.String(), "봉인") {
		t.Errorf("봉인 사실이 로그에 없다: %s", logs.String())
	}
	_ = io.Discard
}
