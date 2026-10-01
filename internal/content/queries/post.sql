-- name: CountPosts :one
SELECT count(*) FROM posts p
WHERE p.board_id = $1 AND p.status = 'published';

-- name: CountPostsSearch :one
SELECT count(*) FROM posts p
WHERE p.board_id = $1 AND p.status = 'published'
  AND p.search_vector @@ to_tsquery('simple', sqlc.arg('search'));

-- name: CountSearchPosts :one
SELECT count(*) FROM posts p
WHERE p.board_id = ANY(sqlc.arg('readable')::uuid[]) AND p.status = 'published'
  AND (NOT p.is_secret OR p.board_id = ANY(sqlc.arg('secret_in')::uuid[]) OR p.author_id = sqlc.narg('viewer_id'))
  AND p.search_vector @@ to_tsquery('simple', sqlc.arg('search'));

-- name: SitemapPosts :many
SELECT id, board_id, updated_at FROM (
    SELECT id, board_id, updated_at,
           row_number() OVER (PARTITION BY board_id ORDER BY created_at DESC, id DESC) AS rn
    FROM posts
    WHERE board_id = ANY(sqlc.arg('board_ids')::uuid[]) AND status = 'published' AND NOT is_secret
) t
WHERE rn <= sqlc.arg('per_board')::int
ORDER BY board_id, rn;

-- name: CreatePost :one
INSERT INTO posts (board_id, author_id, title, body, custom_fields, is_secret)
VALUES ($1, $2, $3, $4, $5, $6) RETURNING id;

-- name: UpdatePost :execrows
UPDATE posts SET title = $2, body = $3, custom_fields = $4, is_secret = $5,
       updated_at = now()
WHERE id = $1;

-- name: SetPostFlags :execrows
UPDATE posts SET is_pinned = $2, status = $3, updated_at = now() WHERE id = $1;

-- name: DeletePost :execrows
DELETE FROM posts WHERE id = $1;

-- name: BumpViewCount :exec
UPDATE posts SET view_count = view_count + 1 WHERE id = $1;

-- name: Comments :many
SELECT c.id, c.post_id, coalesce(c.parent_id::text, '')::text AS parent_id,
       coalesce(c.author_id::text, '')::text AS author_id, coalesce(u.display_name, '')::text AS author_name,
       c.body, coalesce(c.deleted_at, 'epoch'::timestamptz)::timestamptz AS deleted_at, c.created_at
FROM comments c
LEFT JOIN users u ON u.id = c.author_id
WHERE c.post_id = $1
ORDER BY coalesce(c.parent_id, c.id), c.created_at, c.id;

-- name: ModerateComments :many
SELECT c.id, c.post_id, coalesce(c.parent_id::text, '')::text AS parent_id,
       coalesce(c.author_id::text, '')::text AS author_id, coalesce(u.display_name, '')::text AS author_name,
       c.body, coalesce(c.deleted_at, 'epoch'::timestamptz)::timestamptz AS deleted_at, c.created_at
FROM comments c
JOIN posts p ON p.id = c.post_id
LEFT JOIN users u ON u.id = c.author_id
WHERE p.board_id = $1
ORDER BY c.created_at DESC, c.id DESC
LIMIT $2;

-- name: CommentByID :one
SELECT id, post_id, coalesce(parent_id::text, '')::text AS parent_id, coalesce(author_id::text, '')::text AS author_id,
       body, coalesce(deleted_at, 'epoch'::timestamptz)::timestamptz AS deleted_at, created_at
FROM comments WHERE id = $1;

-- name: CreateComment :one
INSERT INTO comments (post_id, parent_id, author_id, body)
VALUES ($1, $2, $3, $4) RETURNING id;

-- name: DeleteComment :execrows
DELETE FROM comments WHERE id = $1;

-- name: TombstoneComment :execrows
UPDATE comments SET body = '', deleted_at = now(), updated_at = now()
WHERE id = $1 AND deleted_at IS NULL;

-- name: UpdateComment :execrows
UPDATE comments SET body = $2, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL;
