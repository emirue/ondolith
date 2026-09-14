package content

import (
	"context"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/secretbox"
)

func sealer(t *testing.T) *secretbox.Box {
	t.Helper()
	k, err := secretbox.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := secretbox.New(k)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// 자격증명은 DB 에 평문으로 앉지 않는다 (D60 「저장된 자격증명」). 읽는 쪽은
// 평문을 받고, 시크릿이 아닌 키는 그대로다.
func TestSecretSettingsAreSealedAtRest(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	s.UseSealer(sealer(t))

	if err := s.PutSettings(ctx, map[string]string{
		"pg.secret_key": "test_sk_abc123", "pg.client_key": "test_ck_xyz", "site.name": "온돌",
		"social.google.client_secret": "goog-secret", "mail.smtp_password": "",
	}); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = 'pg.secret_key'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !secretbox.IsSealed(raw) || strings.Contains(raw, "test_sk_abc123") {
		t.Fatalf("pg.secret_key 가 DB 에 평문이다: %q", raw)
	}
	if err := pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = 'social.google.client_secret'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !secretbox.IsSealed(raw) {
		t.Errorf("소셜 client_secret 이 봉인되지 않았다: %q", raw)
	}
	if err := pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = 'pg.client_key'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != "test_ck_xyz" {
		t.Errorf("시크릿이 아닌 키를 봉인했다: %q", raw)
	}
	if err := pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = 'mail.smtp_password'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != "" {
		t.Errorf("빈 시크릿이 빈 값으로 남지 않았다: %q — 「설정 없음」 비교가 깨진다", raw)
	}

	kv, err := s.Settings(ctx, "pg.secret_key", "pg.client_key", "social.google.client_secret")
	if err != nil {
		t.Fatal(err)
	}
	if kv["pg.secret_key"] != "test_sk_abc123" || kv["social.google.client_secret"] != "goog-secret" || kv["pg.client_key"] != "test_ck_xyz" {
		t.Errorf("읽은 값 %v", kv)
	}

	// 다른 키로 부팅한 것처럼: 열리지 않으면 평문인 척하지 않고 오류다.
	s.UseSealer(sealer(t))
	if _, err := s.Settings(ctx, "pg.secret_key"); err == nil {
		t.Error("다른 키로 열리지 않는 값이 오류 없이 돌아왔다")
	}
}

// 봉인 전에 저장된 평문(v0.1.0·v0.2.0)은 부팅이 봉인한다.
func TestSealLegacySecretsUpgradesPlaintextRows(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO settings (key, value) VALUES
		('pg.secret_key', 'test_sk_plain'), ('mail.smtp_password', 'pw'),
		('social.kakao.client_secret', 'ks'), ('site.name', '온돌'), ('mail.smtp_host', 'smtp.example')`); err != nil {
		t.Fatal(err)
	}
	s.UseSealer(sealer(t))
	n, err := s.SealLegacySecrets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("봉인한 행 %d, want 3", n)
	}
	var plain int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM settings
		WHERE key IN ('pg.secret_key','mail.smtp_password','social.kakao.client_secret')
		  AND value NOT LIKE 'enc:v1:%'`).Scan(&plain); err != nil {
		t.Fatal(err)
	}
	if plain != 0 {
		t.Errorf("평문 시크릿 %d행이 남았다", plain)
	}
	var host string
	if err := pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = 'mail.smtp_host'`).Scan(&host); err != nil {
		t.Fatal(err)
	}
	if host != "smtp.example" {
		t.Errorf("시크릿이 아닌 값을 건드렸다: %q", host)
	}
	kv, err := s.Settings(ctx, "pg.secret_key", "social.kakao.client_secret")
	if err != nil || kv["pg.secret_key"] != "test_sk_plain" || kv["social.kakao.client_secret"] != "ks" {
		t.Errorf("승격 뒤 읽기 = %v, %v", kv, err)
	}
	// 두 번 돌려도 할 일이 없다.
	if n, _ := s.SealLegacySecrets(ctx); n != 0 {
		t.Errorf("두 번째 승격이 %d행을 다시 썼다", n)
	}
}
