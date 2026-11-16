// Package migrations holds Ondolith's schema migrations, embedded into the
// binary so that installing never requires a separate CLI step.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

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

// SiteVersionTable is where a site binary's migrations record themselves (D42 4절 3). 코어의
// goose_db_version 과 나누지 않으면 사이트의 00001 과 코어의 00001 이 서로를 「이미 했다」고 본다.
const SiteVersionTable = "site_goose_db_version"

// RunSite applies a site's migrations after the core's. fsys holds the .sql files at its root;
// nil means the binary has no site extension.
func RunSite(ctx context.Context, db *sql.DB, fsys fs.FS) error {
	if fsys == nil {
		return nil
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, fsys, goose.WithTableName(SiteVersionTable))
	if err != nil {
		return fmt.Errorf("사이트 마이그레이션: %w", err)
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("사이트 마이그레이션: %w", err)
	}
	return nil
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
