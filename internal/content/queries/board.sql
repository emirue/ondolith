-- name: CreateBoard :one
INSERT INTO boards (slug, name, skin, allow_attachments, allow_comments, allow_secret, per_page)
VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id;

-- name: GrantBoardPreset :execrows
INSERT INTO role_permissions (role_id, permission_id, board_id)
SELECT r.id, p.id, sqlc.arg('board_id')::uuid FROM roles r, permissions p
WHERE r.key = sqlc.arg('role') AND p.key = sqlc.arg('permission') AND p.is_scoped;

-- name: Boards :many
SELECT id, slug, name, skin, allow_attachments, allow_comments, allow_secret, per_page, created_at, updated_at
FROM boards ORDER BY name, id;

-- name: BoardBySlug :one
SELECT id, slug, name, skin, allow_attachments, allow_comments, allow_secret, per_page, created_at, updated_at
FROM boards WHERE slug = $1;

-- name: BoardByID :one
SELECT id, slug, name, skin, allow_attachments, allow_comments, allow_secret, per_page, created_at, updated_at
FROM boards WHERE id = $1;

-- name: BoardByPost :one
SELECT b.id, b.slug, b.name, b.skin, b.allow_attachments, b.allow_comments, b.allow_secret, b.per_page, b.created_at, b.updated_at
FROM boards b JOIN posts p ON p.board_id = b.id
WHERE p.id = $1;

-- name: UpdateBoard :execrows
UPDATE boards SET name = $2, skin = $3, allow_attachments = $4,
       allow_comments = $5, allow_secret = $6, per_page = $7, updated_at = now()
WHERE id = $1;

-- name: CountBoardPosts :one
SELECT count(*) FROM posts WHERE board_id = $1;

-- name: DeleteBoard :execrows
DELETE FROM boards WHERE id = $1;

-- name: BoardFields :many
SELECT key, label, field_type, is_required, show_in_list, options, sort_order
FROM board_fields WHERE board_id = $1 ORDER BY sort_order, key;

-- name: SaveBoardField :exec
INSERT INTO board_fields (board_id, key, label, field_type, is_required, show_in_list, options, sort_order)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (board_id, key) DO UPDATE SET
    label = EXCLUDED.label, field_type = EXCLUDED.field_type,
    is_required = EXCLUDED.is_required, show_in_list = EXCLUDED.show_in_list,
    options = EXCLUDED.options, sort_order = EXCLUDED.sort_order, updated_at = now();

-- name: DeleteBoardField :execrows
DELETE FROM board_fields WHERE board_id = $1 AND key = $2;
