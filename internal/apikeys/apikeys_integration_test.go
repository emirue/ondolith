package apikeys

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

const dsnEnv = "ONDOLITH_TEST_DSN"

func store(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s 미설정 — 통합 테스트를 건너뜁니다 (make test-integration)", dsnEnv)
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
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { db.Close() })
	if err := migrations.Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	return New(pool), pool
}

// **앱 키는 한 번 보이고, 원문은 어디에도 남지 않는다** (D42 6절). 표에는 SHA-256 만 있고, 그 원문으로만 찾는다.
// 폐기한 키는 없는 키와 같다. 마지막 사용 시각이 화면에 보인다.
func TestIssuedKeyIsFoundOnlyByItsRawValue(t *testing.T) {
	s, pool := store(t)
	ctx := context.Background()

	raw, k, err := s.Issue(ctx, "flutter-android", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, Prefix) || len(raw) != len(Prefix)+43 || k.Name != "flutter-android" || k.ID == "" {
		t.Fatalf("Issue = %q, %+v", raw, k)
	}
	// 표 어디에도 원문이 없다 — 해시만 있다.
	var hashes, rawLeft int
	sum := sha256.Sum256([]byte(raw))
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM api_keys WHERE key_hash = $1`, sum[:]).Scan(&hashes); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM api_keys WHERE position($1 in name) > 0 OR position(convert_to($1, 'UTF8') in key_hash) > 0`, raw).Scan(&rawLeft); err != nil {
		t.Fatal(err)
	}
	if hashes != 1 || rawLeft != 0 {
		t.Errorf("해시로 찾은 행 %d(want 1), 원문이 남은 행 %d(want 0)", hashes, rawLeft)
	}

	got, ok, err := s.Lookup(ctx, raw)
	if err != nil || !ok || got.ID != k.ID || got.Name != "flutter-android" {
		t.Fatalf("Lookup(원문) = %+v, %v, %v", got, ok, err)
	}
	for _, wrong := range []string{"", raw + "x", raw[:len(raw)-1] + "A", "Bearer " + raw, strings.TrimPrefix(raw, Prefix)} {
		if _, ok, err := s.Lookup(ctx, wrong); ok || err != nil {
			t.Errorf("Lookup(%q) = %v, %v — 찾으면 안 된다", wrong, ok, err)
		}
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 1 || list[0].LastUsedAt == nil {
		t.Fatalf("List = %+v, %v — 쓴 키는 마지막 사용 시각이 있다", list, err)
	}

	name, err := s.Revoke(ctx, k.ID)
	if err != nil || name != "flutter-android" {
		t.Fatalf("Revoke = %q, %v", name, err)
	}
	if _, ok, _ := s.Lookup(ctx, raw); ok {
		t.Error("폐기한 키로 찾았다")
	}
	if _, err := s.Revoke(ctx, k.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("두 번 폐기 = %v, want ErrNotFound", err)
	}
	for _, id := range []string{"00000000-0000-4000-8000-000000000000", "아무거나", ""} {
		if _, err := s.Revoke(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Revoke(%q) = %v, want ErrNotFound", id, err)
		}
	}
	if list, _ := s.List(ctx); len(list) != 1 || list[0].RevokedAt == nil {
		t.Errorf("폐기한 키는 목록에 폐기 시각과 함께 남는다: %+v", list)
	}
}

// 이름은 1~60자다 — 화면이 줄 하나에 보인다. 앞뒤 공백은 이름이 아니다.
func TestKeyNameIsOneToSixtyCharacters(t *testing.T) {
	s, _ := store(t)
	for _, name := range []string{"", "   ", strings.Repeat("가", 61)} {
		if _, _, err := s.Issue(context.Background(), name, nil); !errors.Is(err, ErrName) {
			t.Errorf("Issue(%d자) = %v, want ErrName", len([]rune(name)), err)
		}
	}
	if _, k, err := s.Issue(context.Background(), "  "+strings.Repeat("가", 60)+"  ", nil); err != nil || k.Name != strings.Repeat("가", 60) {
		t.Errorf("60자 = %+v, %v", k, err)
	}
}
