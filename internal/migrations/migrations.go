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

// Status reports what A-602 shows: the migrations already applied, and how many
// are still waiting (NFR-302, NFR-303).
//
// It opens its own goose provider rather than keeping one around, because the
// screen is read rarely and a long-lived provider would hold a connection for
// the life of the process to answer a question nobody is asking.
func Status(ctx context.Context, db *sql.DB) (applied []string, pending int, err error) {
	p, err := goose.NewProvider(goose.DialectPostgres, db, FS)
	if err != nil {
		return nil, 0, err
	}
	st, err := p.Status(ctx)
	if err != nil {
		return nil, 0, err
	}
	for _, s := range st {
		if s.State == goose.StateApplied {
			applied = append(applied, s.Source.Path)
			continue
		}
		pending++
	}
	return applied, pending, nil
}
