package migrations

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

func sqlFiles(t *testing.T) []string {
	t.Helper()
	names, err := fs.Glob(FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("내장된 마이그레이션이 없다 — embed 지시문이 파일을 못 찾았다")
	}
	return names
}

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := fs.ReadFile(FS, name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// FR-103: migrations ship inside the binary. If embed silently matched nothing
// the install wizard would "succeed" against an empty schema.
func TestMigrationsAreEmbedded(t *testing.T) {
	for _, name := range sqlFiles(t) {
		if strings.TrimSpace(read(t, name)) == "" {
			t.Errorf("%s 가 비어 있다", name)
		}
	}
}

// NFR-303: every migration is reversible. Nothing else in the build checks
// this, and a missing Down is only discovered when a downgrade is attempted —
// which is the worst possible moment (NFR-308).
func TestEveryMigrationHasUpAndDown(t *testing.T) {
	for _, name := range sqlFiles(t) {
		body := read(t, name)
		if !strings.Contains(body, "-- +goose Up") {
			t.Errorf("%s: `-- +goose Up` 이 없다", name)
		}
		if !strings.Contains(body, "-- +goose Down") {
			t.Errorf("%s: `-- +goose Down` 이 없다 — 되돌릴 수 없는 마이그레이션은 "+
				"CHANGELOG 에 명시해야 하고, 그 전에 이 테스트를 고쳐야 한다 (NFR-303)", name)
		}
	}
}

// D30: filenames are NNNNN_name.sql and version numbers are never reused.
func TestMigrationFilenamesAreWellFormed(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9]{5}_[a-z0-9_]+\.sql$`)
	seen := map[string]string{}

	for _, name := range sqlFiles(t) {
		if !pattern.MatchString(name) {
			t.Errorf("%s: 파일명이 NNNNN_name.sql 형식이 아니다", name)
			continue
		}
		version := name[:5]
		if prev, dup := seen[version]; dup {
			t.Errorf("버전 번호 %s 가 중복된다: %s, %s", version, prev, name)
		}
		seen[version] = name
	}
}

// The session table is dictated by scs/pgxstore, not by us. If a migration
// renames or drops it, logins break at runtime rather than at build time.
func TestSessionsTableSurvivesInSchema(t *testing.T) {
	var all strings.Builder
	for _, name := range sqlFiles(t) {
		all.WriteString(read(t, name))
	}
	schema := all.String()

	up := schema
	if i := strings.Index(schema, "-- +goose Down"); i >= 0 {
		up = schema[:i]
	}
	for _, col := range []string{"CREATE TABLE sessions", "token", "data", "expiry"} {
		if !strings.Contains(up, col) {
			t.Errorf("Up 마이그레이션에 sessions 스키마 요소가 없다: %q", col)
		}
	}
}
