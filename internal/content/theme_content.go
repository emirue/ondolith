package content

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/emirue/ondolith/internal/content/contentq"
)

// 테마 선언 관리자 화면의 값 (D41 3절). 화면은 테마 파일에 있고 여기는 화면 키와
// JSONB 값만 안다 — 어떤 칸이 있는지, 값이 맞는지는 internal/theme 이 판정한다.

// MaxThemeItems bounds one collection screen (D41 2절).
const MaxThemeItems = 200

// ErrThemeItemLimit means the collection already holds MaxThemeItems rows.
var ErrThemeItemLimit = errors.New("content: 행이 너무 많습니다")

// ThemeItem is one row of a collection screen.
type ThemeItem struct {
	ID        string
	SortOrder int
	Data      map[string]any
	CreatedAt time.Time
}

// ThemeContent reads every declared screen's values at once — 설정형 키 전부를 한 번,
// 모음형 키 전부를 한 번 (D41 7절). Empty key lists skip their query, so a theme
// that declares nothing costs nothing.
func (s *Store) ThemeContent(ctx context.Context, settingKeys, collectionKeys []string) (map[string]map[string]any, map[string][]map[string]any, error) {
	settings := map[string]map[string]any{}
	items := map[string][]map[string]any{}
	if len(settingKeys) > 0 {
		rows, err := s.q.ThemeSettingsFor(ctx, settingKeys)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range rows {
			data, err := decodeThemeData(r.Data)
			if err != nil {
				return nil, nil, err
			}
			settings[r.ScreenKey] = data
		}
	}
	if len(collectionKeys) > 0 {
		rows, err := s.q.ThemeItemsFor(ctx, collectionKeys)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range rows {
			data, err := decodeThemeData(r.Data)
			if err != nil {
				return nil, nil, err
			}
			items[r.ScreenKey] = append(items[r.ScreenKey], data)
		}
	}
	return settings, items, nil
}

// ThemeSettings is one settings screen's stored values, empty before the first save.
func (s *Store) ThemeSettings(ctx context.Context, key string) (map[string]any, error) {
	b, err := s.q.ThemeSetting(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeThemeData(b)
}

// SaveThemeSettings replaces one settings screen's values. 병합(선언 밖 칸 보존)은
// 호출자가 theme.Screen.Apply 로 이미 했다 — 여기는 그 결과를 통째로 쓴다.
func (s *Store) SaveThemeSettings(ctx context.Context, key string, data map[string]any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.q.SaveThemeSettings(ctx, contentq.SaveThemeSettingsParams{ScreenKey: key, Data: b})
}

// ThemeItems lists one collection's rows in display order.
func (s *Store) ThemeItems(ctx context.Context, key string) ([]ThemeItem, error) {
	rows, err := s.q.ThemeItems(ctx, key)
	if err != nil {
		return nil, err
	}
	out := make([]ThemeItem, 0, len(rows))
	for _, r := range rows {
		data, err := decodeThemeData(r.Data)
		if err != nil {
			return nil, err
		}
		out = append(out, ThemeItem{ID: r.ID, SortOrder: int(r.SortOrder), Data: data, CreatedAt: r.CreatedAt})
	}
	return out, nil
}

// ThemeItem reads one row of the named screen, or ErrNotFound — 다른 화면의 행도 없는 것이다.
func (s *Store) ThemeItem(ctx context.Context, key, id string) (ThemeItem, error) {
	r, err := s.q.ThemeItem(ctx, contentq.ThemeItemParams{ScreenKey: key, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return ThemeItem{}, ErrNotFound
	}
	if err != nil {
		return ThemeItem{}, err
	}
	data, err := decodeThemeData(r.Data)
	if err != nil {
		return ThemeItem{}, err
	}
	return ThemeItem{ID: r.ID, SortOrder: int(r.SortOrder), Data: data, CreatedAt: r.CreatedAt}, nil
}

// CreateThemeItem adds a row, or returns ErrThemeItemLimit at MaxThemeItems.
func (s *Store) CreateThemeItem(ctx context.Context, key string, sortOrder int, data map[string]any) (string, error) {
	b, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	id, err := s.q.CreateThemeItem(ctx, contentq.CreateThemeItemParams{
		ScreenKey: key, SortOrder: int32(sortOrder), Data: b, MaxItems: MaxThemeItems})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrThemeItemLimit
	}
	return id, err
}

// UpdateThemeItem rewrites one row of the named screen, or ErrNotFound.
func (s *Store) UpdateThemeItem(ctx context.Context, key, id string, sortOrder int, data map[string]any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	n, err := s.q.UpdateThemeItem(ctx, contentq.UpdateThemeItemParams{
		ScreenKey: key, ID: id, SortOrder: int32(sortOrder), Data: b})
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

// DeleteThemeItem removes one row of the named screen, or ErrNotFound. 사진 파일은
// 지우지 않는다 — 주소가 공개라 글 본문에 복사됐을 수 있다 (D41 1절).
func (s *Store) DeleteThemeItem(ctx context.Context, key, id string) error {
	n, err := s.q.DeleteThemeItem(ctx, contentq.DeleteThemeItemParams{ScreenKey: key, ID: id})
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

// NextThemeItemOrder is the order a new row starts with — 맨 뒤에 붙는다.
func (s *Store) NextThemeItemOrder(ctx context.Context, key string) (int, error) {
	n, err := s.q.NextThemeItemOrder(ctx, key)
	return int(n), err
}

func decodeThemeData(b []byte) (map[string]any, error) {
	out := map[string]any{}
	if len(b) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}
