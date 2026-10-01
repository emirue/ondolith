-- name: CountAttachments :one
SELECT count(*) FROM attachments WHERE post_id = $1;

-- name: CreateAttachment :one
INSERT INTO attachments (post_id, stored_path, original_name, mime_type, byte_size)
VALUES ($1, $2, $3, $4, $5) RETURNING id, created_at;

-- name: Attachments :many
SELECT id, post_id, stored_path, original_name, mime_type, byte_size, created_at
FROM attachments WHERE post_id = $1 ORDER BY created_at, id;

-- name: AttachmentByID :one
SELECT id, post_id, stored_path, original_name, mime_type, byte_size, created_at
FROM attachments WHERE id = $1;

-- name: DeleteAttachment :execrows
DELETE FROM attachments WHERE id = $1;

-- name: AttachmentPaths :many
SELECT stored_path FROM attachments WHERE post_id = $1;

-- name: BoardAttachments :many
SELECT a.id, a.post_id, a.stored_path, a.original_name, a.mime_type, a.byte_size, a.created_at
FROM attachments a
JOIN posts p ON p.id = a.post_id
WHERE p.board_id = $1
ORDER BY a.created_at DESC, a.id DESC
LIMIT $2;
