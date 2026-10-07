-- name: CreateMedia :one
INSERT INTO media (uploader_id, stored_path, mime_type, thumb_mime, width, height, byte_size)
VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id, created_at;

-- name: MediaByID :one
SELECT id, coalesce(uploader_id::text, '')::text AS uploader_id, stored_path, mime_type, thumb_mime,
       width, height, byte_size, created_at
FROM media WHERE id = $1;
