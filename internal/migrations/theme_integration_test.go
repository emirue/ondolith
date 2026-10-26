package migrations

import (
	"context"
	"testing"
)

// D41 3·4절을 DB 가 막는지 — 화면 키 형식, data 가 객체인지, 화면 스코프의 모양과 유일성.
// 처리기가 거르는 것을 DB 도 거르면 처리기 하나를 빠뜨린 코드가 쓰레기를 남기지 못한다.
func TestThemeContentSchemaBites(t *testing.T) {
	db, pool := testDB(t)
	ctx := context.Background()
	if err := Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	refused := func(name, sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err == nil {
			t.Errorf("%s: 들어갔다", name)
		}
	}
	accepted := func(name, sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	accepted("설정 화면", `INSERT INTO theme_settings (screen_key, data) VALUES ('hero', '{"a": "b"}')`)
	refused("설정 화면 키 중복", `INSERT INTO theme_settings (screen_key, data) VALUES ('hero', '{}')`)
	refused("설정 화면 키 형식", `INSERT INTO theme_settings (screen_key, data) VALUES ('Hero', '{}')`)
	refused("설정 data 가 배열", `INSERT INTO theme_settings (screen_key, data) VALUES ('other', '[]')`)
	accepted("모음 행", `INSERT INTO theme_items (screen_key, sort_order, data) VALUES ('partners', 1, '{}')`)
	accepted("같은 화면의 둘째 행", `INSERT INTO theme_items (screen_key, sort_order, data) VALUES ('partners', 1, '{}')`)
	refused("모음 화면 키 형식", `INSERT INTO theme_items (screen_key, data) VALUES ('a-b', '{}')`)
	refused("모음 data 가 문자열", `INSERT INTO theme_items (screen_key, data) VALUES ('partners', '"x"')`)

	const grant = `INSERT INTO role_permissions (role_id, permission_id, board_id, theme_screen)
		SELECT r.id, p.id, $1, $2 FROM roles r, permissions p
		WHERE r.key = 'editor' AND p.key = 'theme.edit_content'`
	accepted("화면 부여", grant, nil, "hero")
	accepted("다른 화면 부여", grant, nil, "partners")
	accepted("전역 부여는 화면 부여와 함께 있을 수 있다", grant, nil, nil)
	refused("같은 화면 부여 두 번", grant, nil, "hero")
	refused("전역 부여 두 번 (NULLS NOT DISTINCT)", grant, nil, nil)
	refused("화면 키 형식", grant, nil, "Hero")

	var board string
	if err := pool.QueryRow(ctx, `INSERT INTO boards (slug, name) VALUES ('free', '자유') RETURNING id`).Scan(&board); err != nil {
		t.Fatal(err)
	}
	refused("게시판과 화면을 함께", grant, board, "hero")

	// 시드: 권한은 비스코프(게시판 단위가 아니다)이고 operator 가 전역으로 갖는다 (D15 2.5).
	var scoped bool
	if err := pool.QueryRow(ctx, `SELECT is_scoped FROM permissions WHERE key = 'theme.edit_content'`).Scan(&scoped); err != nil {
		t.Fatalf("theme.edit_content 가 심어지지 않았다: %v", err)
	}
	if scoped {
		t.Error("theme.edit_content 가 게시판 스코프 권한이다")
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM role_permissions rp
		JOIN roles r ON r.id = rp.role_id JOIN permissions p ON p.id = rp.permission_id
		WHERE r.key = 'operator' AND p.key = 'theme.edit_content'
		  AND rp.board_id IS NULL AND rp.theme_screen IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("operator 의 전역 부여 %d행, want 1", n)
	}
}
