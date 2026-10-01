package app

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 운영 풀은 매 실행마다 인자 값으로 계획한다. 기본값(auto)에서는 준비된 문장이 여섯
// 번째 실행부터 일반 계획으로 넘어갈 수 있고, `ORDER BY CASE WHEN $sort …` 질의는
// 거기서 인덱스 순서를 잃는다 — 게시판 목록이 0.36ms 에서 318ms 가 됐고 그 접속이
// 살아 있는 동안 계속 그랬다 (2026-10 실측).
//
// 설정 구조체가 아니라 **서버에 물어본다.** 키 이름을 틀리게 적으면 구조체에는
// 값이 있어도 접속이 거부되거나 무시된다.
func TestPoolPlansEveryExecutionWithItsParameters(t *testing.T) {
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s 미설정 — 통합 테스트를 건너뜁니다 (make test-integration)", dsnEnv)
	}
	ctx := context.Background()
	pcfg, err := poolConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var mode string
	if err := pool.QueryRow(ctx, `SHOW plan_cache_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "force_custom_plan" {
		t.Errorf("plan_cache_mode = %q, want force_custom_plan", mode)
	}
	if pcfg.MinConns != 1 {
		t.Errorf("MinConns = %d, want 1 — 유휴 뒤 첫 요청이 접속부터 연다", pcfg.MinConns)
	}
}
