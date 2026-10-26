-- 테마 선언 관리자 화면의 값 (D41 3절, 00031).

-- name: ThemeSettingsFor :many
-- 공개 화면 하나가 활성 테마의 설정형 화면 전부를 한 번에 읽는다 (D41 7절).
SELECT screen_key, data FROM theme_settings WHERE screen_key = ANY(sqlc.arg('keys')::text[]);

-- name: ThemeItemsFor :many
SELECT screen_key, data FROM theme_items
WHERE screen_key = ANY(sqlc.arg('keys')::text[])
ORDER BY screen_key, sort_order, created_at, id;

-- name: ThemeSetting :one
SELECT data FROM theme_settings WHERE screen_key = $1;

-- name: SaveThemeSettings :exec
INSERT INTO theme_settings (screen_key, data) VALUES ($1, $2)
ON CONFLICT (screen_key) DO UPDATE SET data = EXCLUDED.data, updated_at = now();

-- name: ThemeItems :many
SELECT id, sort_order, data, created_at FROM theme_items
WHERE screen_key = $1 ORDER BY sort_order, created_at, id;

-- name: ThemeItem :one
-- 화면 키로도 거른다 — 다른 화면의 행 id 로 이 화면을 고칠 수 없다.
SELECT id, sort_order, data, created_at FROM theme_items WHERE screen_key = $1 AND id = $2;

-- name: CreateThemeItem :one
-- 상한을 같은 문장에서 센다. ponytail: 동시에 두 요청이 들어오면 한두 행 넘을 수
-- 있다 — 상한은 공개 화면의 그리기 비용을 묶는 안전선이지 정확한 수가 아니다.
-- 정확해야 하면 화면 키 advisory 잠금으로 바꾼다.
INSERT INTO theme_items (screen_key, sort_order, data)
SELECT sqlc.arg('screen_key'), sqlc.arg('sort_order'), sqlc.arg('data')
WHERE (SELECT count(*) FROM theme_items t WHERE t.screen_key = sqlc.arg('screen_key')) < sqlc.arg('max_items')::bigint
RETURNING id;

-- name: UpdateThemeItem :execrows
UPDATE theme_items SET sort_order = $3, data = $4, updated_at = now()
WHERE screen_key = $1 AND id = $2;

-- name: DeleteThemeItem :execrows
DELETE FROM theme_items WHERE screen_key = $1 AND id = $2;

-- name: NextThemeItemOrder :one
-- 새 행의 순서 칸 기본값 — 맨 뒤에 붙는다 (D41 5절).
SELECT (coalesce(max(sort_order), 0) + 1)::integer FROM theme_items WHERE screen_key = $1;
