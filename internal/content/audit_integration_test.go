package content

import (
	"context"
	"testing"
	"time"
)

// 메뉴 항목을 고쳐도 updated_at 이 만든 날에 머물렀다 — 다른 표의 UPDATE 는 전부
// 찍는다.
func TestUpdateMenuItemTouchesUpdatedAt(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	id, err := s.CreateMenuItem(ctx, MenuItem{Title: "회사", URL: "/about", Sort: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE menus SET updated_at = now() - interval '1 day' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateMenuItem(ctx, id, MenuItem{Title: "회사 소개", URL: "/about", Sort: 2}); err != nil {
		t.Fatal(err)
	}
	var age time.Duration
	if err := pool.QueryRow(ctx,
		`SELECT now() - updated_at FROM menus WHERE id = $1`, id).Scan(&age); err != nil {
		t.Fatal(err)
	}
	if age > time.Hour {
		t.Errorf("수정 뒤 updated_at 이 %v 전이다 — 갱신되지 않았다", age)
	}
}
