package content

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// D41 3절: 설정형은 화면 키마다 한 벌(덮어쓰기), 모음형은 순서 있는 행.
func TestThemeSettingsUpsertAndBatchRead(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()

	got, err := s.ThemeSettings(ctx, "hero")
	if err != nil || len(got) != 0 {
		t.Fatalf("저장 전에는 빈 값: %v %v", got, err)
	}
	if err := s.SaveThemeSettings(ctx, "hero", map[string]any{"headline": "첫"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveThemeSettings(ctx, "hero", map[string]any{"headline": "둘", "count": 3.0}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ThemeSettings(ctx, "hero"); !reflect.DeepEqual(got, map[string]any{"headline": "둘", "count": 3.0}) {
		t.Errorf("덮어써야 한다: %#v", got)
	}

	if _, err := s.CreateThemeItem(ctx, "partners", 2, map[string]any{"name": "나"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateThemeItem(ctx, "partners", 1, map[string]any{"name": "가"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateThemeItem(ctx, "other", 0, map[string]any{"name": "다른 화면"}); err != nil {
		t.Fatal(err)
	}
	settings, items, err := s.ThemeContent(ctx, []string{"hero", "none"}, []string{"partners"})
	if err != nil {
		t.Fatal(err)
	}
	if len(settings) != 1 || settings["hero"]["headline"] != "둘" {
		t.Errorf("설정 묶음 읽기: %#v", settings)
	}
	rows := items["partners"]
	if len(rows) != 2 || rows[0]["name"] != "가" || rows[1]["name"] != "나" {
		t.Errorf("순서대로, 요청한 화면만: %#v", items)
	}
	if _, ok := items["other"]; ok {
		t.Error("요청하지 않은 화면의 행이 왔다")
	}
	// 선언이 없는 테마는 질의하지 않는다 — 빈 목록이면 빈 결과다.
	if s2, i2, err := s.ThemeContent(ctx, nil, nil); err != nil || len(s2) != 0 || len(i2) != 0 {
		t.Errorf("빈 요청: %v %v %v", s2, i2, err)
	}
}

func TestThemeItemsAreScopedToTheirScreen(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	id, err := s.CreateThemeItem(ctx, "partners", 0, map[string]any{"name": "가"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ThemeItem(ctx, "hero", id); !errors.Is(err, ErrNotFound) {
		t.Errorf("다른 화면의 키로 행을 읽었다: %v", err)
	}
	if err := s.UpdateThemeItem(ctx, "hero", id, 5, map[string]any{"name": "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("다른 화면의 키로 행을 고쳤다: %v", err)
	}
	if err := s.DeleteThemeItem(ctx, "hero", id); !errors.Is(err, ErrNotFound) {
		t.Errorf("다른 화면의 키로 행을 지웠다: %v", err)
	}
	if err := s.UpdateThemeItem(ctx, "partners", id, 5, map[string]any{"name": "나"}); err != nil {
		t.Fatal(err)
	}
	it, err := s.ThemeItem(ctx, "partners", id)
	if err != nil || it.SortOrder != 5 || it.Data["name"] != "나" {
		t.Errorf("수정 결과: %+v %v", it, err)
	}
	if n, err := s.NextThemeItemOrder(ctx, "partners"); err != nil || n != 6 {
		t.Errorf("다음 순서 = %d, want 6 (%v)", n, err)
	}
	if n, _ := s.NextThemeItemOrder(ctx, "empty"); n != 1 {
		t.Errorf("빈 화면의 다음 순서 = %d, want 1", n)
	}
	if err := s.DeleteThemeItem(ctx, "partners", id); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ThemeItems(ctx, "partners"); len(list) != 0 {
		t.Errorf("지운 행이 남았다: %v", list)
	}
}

func TestThemeItemLimit(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO theme_items (screen_key, data)
		SELECT 'partners', '{}' FROM generate_series(1, $1)`, MaxThemeItems-1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateThemeItem(ctx, "partners", 0, map[string]any{}); err != nil {
		t.Fatalf("%d번째 행은 들어가야 한다: %v", MaxThemeItems, err)
	}
	if _, err := s.CreateThemeItem(ctx, "partners", 0, map[string]any{}); !errors.Is(err, ErrThemeItemLimit) {
		t.Errorf("%d번째를 넘었는데 %v", MaxThemeItems, err)
	}
	if _, err := s.CreateThemeItem(ctx, "other", 0, map[string]any{}); err != nil {
		t.Errorf("상한은 화면마다다: %v", err)
	}
}
