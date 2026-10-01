-- name: PublishedPageBySlug :one
SELECT id, slug, title, body, status, template, created_at, updated_at
FROM pages WHERE slug = $1 AND status = 'published';

-- name: PageBySlug :one
SELECT id, slug, title, body, status, template, created_at, updated_at
FROM pages WHERE slug = $1;

-- name: PageByID :one
SELECT id, slug, title, body, status, template, created_at, updated_at
FROM pages WHERE id = $1;

-- name: Pages :many
SELECT id, slug, title, body, status, template, created_at, updated_at
FROM pages ORDER BY updated_at DESC, id;

-- name: PublishedPages :many
SELECT id, slug, title, body, status, template, created_at, updated_at
FROM pages WHERE status = 'published' ORDER BY slug;

-- name: CreatePage :one
INSERT INTO pages (slug, title, body, template)
VALUES ($1, $2, $3, $4) RETURNING id;

-- name: UpdatePage :execrows
UPDATE pages SET slug = $2, title = $3, body = $4, template = $5, updated_at = now()
WHERE id = $1;

-- name: PageStatus :one
SELECT status FROM pages WHERE id = $1;

-- name: SetPageStatus :execrows
UPDATE pages SET status = sqlc.arg('to'), updated_at = now()
WHERE id = $1 AND status = sqlc.arg('from');

-- name: DeletePage :execrows
DELETE FROM pages WHERE id = $1;

-- name: Settings :many
SELECT key, value FROM settings WHERE key = ANY(sqlc.arg('keys')::text[]);

-- name: PutSetting :exec
INSERT INTO settings (key, value) VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();

-- name: LegacySecretSettings :many
SELECT key, value FROM settings
WHERE (key IN ('pg.secret_key', 'mail.smtp_password') OR key LIKE 'social.%.client_secret')
  AND value <> '' AND value NOT LIKE 'enc:v1:%';

-- name: MenuItems :many
SELECT id, coalesce(parent_id::text, '')::text AS parent_id, title, url, sort_order
FROM menus
ORDER BY parent_id NULLS FIRST, sort_order, id;

-- name: CreateMenuItem :one
INSERT INTO menus (title, url, parent_id, sort_order)
VALUES ($1, $2, nullif(sqlc.arg('parent_id')::text, '')::uuid, $3) RETURNING id;

-- name: UpdateMenuItem :execrows
UPDATE menus SET title = $2, url = $3, parent_id = nullif(sqlc.arg('parent_id')::text, '')::uuid, sort_order = $4,
       updated_at = now()
WHERE id = $1;

-- name: DeleteMenuItem :execrows
DELETE FROM menus WHERE id = $1;
