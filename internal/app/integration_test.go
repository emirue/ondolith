package app

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

const dsnEnv = "ONDOLITH_TEST_DSN"

// New() runs migrations through a *sql.DB built from the shared pool and then
// closes that *sql.DB while the pool keeps serving the site. pgx documents
// that closing the wrapper leaves the pool open, but the whole operating tree
// depends on it: if a pgx upgrade ever changed that, the server would come up
// and lose its database on the first request, with nothing in the build to
// catch it.
//
// This pins the behaviour at the version we depend on.
func TestClosingSQLWrapperLeavesPoolUsable(t *testing.T) {
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s 미설정 — 통합 테스트를 건너뜁니다 (make test-integration)", dsnEnv)
	}
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("풀 생성 실패: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("테스트 DB 응답 없음: %v", err)
	}

	db := stdlib.OpenDBFromPool(pool)
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("래퍼로 핑 실패: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("래퍼 닫기 실패: %v", err)
	}

	// The point of the test: the pool must still work.
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("*sql.DB 를 닫았더니 풀이 죽었다 — 부팅 직후 DB 연결이 끊긴다: %v", err)
	}
	var one int
	if err := pool.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("풀로 쿼리 실패: one=%d err=%v", one, err)
	}
}

// The counterpart, so the test above cannot pass by accident: closing the pool
// itself really does break it. Without this, a Ping that never fails would
// make the assertion above meaningless.
func TestClosingPoolDoesBreakIt(t *testing.T) {
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s 미설정 — 통합 테스트를 건너뜁니다 (make test-integration)", dsnEnv)
	}
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("풀 생성 실패: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("테스트 DB 응답 없음: %v", err)
	}

	pool.Close()

	if err := pool.Ping(ctx); err == nil {
		t.Fatal("닫힌 풀이 핑에 성공했다 — 이 테스트가 아무것도 구분하지 못한다")
	}
}
