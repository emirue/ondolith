// Command migrate-only applies internal/migrations to the DSN in
// ONDOLITH_TEST_DSN and exits. Nothing else — no server, no seed data.
//
// scripts/schema-sql.sh 가 docs/schema.sql 을 뽑기 전에 부른다. **제품이 쓰는
// migrations.Run 을 그대로 쓴다**: 덤프의 값어치는 "실제로 만들어지는 스키마"
// 라는 데 있고, 여기서 goose 를 따로 흉내 내면 그 값어치가 사라진다.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/emirue/ondolith/internal/migrations"
)

func main() {
	dsn := os.Getenv("ONDOLITH_TEST_DSN")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "migrate-only: ONDOLITH_TEST_DSN 이 없다")
		os.Exit(2)
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate-only:", err)
		os.Exit(1)
	}
	db := stdlib.OpenDB(*cfg)
	defer db.Close()
	if err := migrations.Run(context.Background(), db); err != nil {
		fmt.Fprintln(os.Stderr, "migrate-only:", err)
		os.Exit(1)
	}
}
