// Package migrations holds Ondolith's schema migrations, embedded into the
// binary so that installing never requires a separate CLI step.
package migrations

import (
	"context"
	"database/sql"
	"embed"

	"github.com/pressly/goose/v3"
)

//go:embed *.sql
var FS embed.FS

// Run applies all pending migrations.
func Run(ctx context.Context, db *sql.DB) error {
	p, err := goose.NewProvider(goose.DialectPostgres, db, FS)
	if err != nil {
		return err
	}
	_, err = p.Up(ctx)
	return err
}
