package migrations

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
)

// **사이트 마이그레이션은 코어 뒤에, 버전 표를 따로 써서 돈다** ([D42](../../docs/42-site-extension.md) 4절 3).
//
// 버전 표를 나누지 않으면 사이트의 00001 과 코어의 00001 이 같은 번호로 서로를 「이미 했다」고 본다. 사이트 것이
// 코어 표에 섞이지 않고, 다시 돌려도 한 번만 되며, 틀린 SQL 은 오류로 돌아와 부팅을 멈춘다.
func TestRunSiteKeepsItsOwnVersionTable(t *testing.T) {
	db, pool := testDB(t)
	ctx := context.Background()
	if err := Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	var coreBefore int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM goose_db_version`).Scan(&coreBefore); err != nil {
		t.Fatal(err)
	}
	site := fstest.MapFS{
		"00001_notes.sql": {Data: []byte("-- +goose Up\nCREATE TABLE site_notes (id bigserial PRIMARY KEY, body text NOT NULL);\n" +
			"-- +goose Down\nDROP TABLE site_notes;\n")},
		"00002_tag.sql": {Data: []byte("-- +goose Up\nALTER TABLE site_notes ADD COLUMN tag text;\n" +
			"-- +goose Down\nALTER TABLE site_notes DROP COLUMN tag;\n")},
	}
	for range 2 { // 두 번째는 할 것이 없어야 한다
		if err := RunSite(ctx, db, site); err != nil {
			t.Fatalf("RunSite: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO site_notes (body, tag) VALUES ('첫 메모', 'a')`); err != nil {
		t.Fatalf("사이트 표가 없거나 둘째 마이그레이션이 안 됐다: %v", err)
	}
	var siteRows, coreAfter int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM site_goose_db_version WHERE version_id > 0`).Scan(&siteRows); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM goose_db_version`).Scan(&coreAfter); err != nil {
		t.Fatal(err)
	}
	if siteRows != 2 || coreAfter != coreBefore {
		t.Errorf("사이트 버전 표 %d행(want 2), 코어 버전 표 %d → %d(바뀌면 안 된다)", siteRows, coreBefore, coreAfter)
	}

	broken := fstest.MapFS{"00003_broken.sql": {Data: []byte("-- +goose Up\nCREATE TABLE site_x (;\n")}}
	for k, v := range site {
		broken[k] = v
	}
	if err := RunSite(ctx, db, broken); err == nil || !strings.HasPrefix(err.Error(), "사이트 마이그레이션") {
		t.Errorf("틀린 SQL 이 지나갔다: %v", err)
	}
	if err := RunSite(ctx, db, nil); err != nil {
		t.Errorf("확장이 없는 실행 파일: %v", err)
	}
}
