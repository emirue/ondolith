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

-- 게시판마다 posts_board_list_idx 를 앞에서부터 per_board 건만 읽는다. 창 함수
-- (row_number) 로 쓰면 읽을 수 있는 글 전부에 번호를 매긴 뒤 자른다.
-- **고정 글이 게시판 몫의 앞에 온다** — 인덱스의 순서(고정 → 최신)를 그대로 쓴다.
-- name: SitemapPosts :many
SELECT p.id, p.board_id, p.updated_at
FROM unnest(sqlc.arg('board_ids')::uuid[]) AS b(id)
CROSS JOIN LATERAL (
    SELECT id, board_id, updated_at, is_pinned, created_at FROM posts
    WHERE board_id = b.id AND status = 'published' AND NOT is_secret
    ORDER BY is_pinned DESC, created_at DESC, id DESC
    LIMIT sqlc.arg('per_board')::int
) p
ORDER BY p.board_id, p.is_pinned DESC, p.created_at DESC, p.id DESC;

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

-- **쪽을 먼저 고르고, 그 행에만 나머지를 붙인다** (ListPosts·ListPostsSearch·SearchPosts).
-- 안쪽 질의가 WHERE·ORDER BY·LIMIT 으로 한 쪽을 정하고, 바깥이 그 행들에 댓글 수·첨부
-- 여부·작성자를 계산한다. 한 층으로 쓰면 정렬이 인덱스를 못 타는 순간 걸러진 행
-- **전부**에 대해 댓글을 세고 나서 정렬한다. 바깥 ORDER BY 는 안쪽과 같은 사슬이다 —
-- 조인이 순서를 지켜 준다는 보장은 없다 (post_shape_test.go 가 둘이 같은지 본다).
-- name: ListPosts :many
SELECT p.id, p.board_id, coalesce(p.author_id::text, '')::text AS author_id, coalesce(u.display_name, '')::text AS author_name,
       p.title, ''::text AS body, p.custom_fields, p.status, p.is_pinned, p.is_secret,
       p.view_count, p.created_at, p.updated_at,
       (SELECT count(*) FROM comments c WHERE c.post_id = p.id)::bigint AS comment_count,
       EXISTS (SELECT 1 FROM attachments a WHERE a.post_id = p.id)::bool AS has_attachment
FROM (
    SELECT p.id, p.board_id, p.author_id, p.title, p.custom_fields, p.status, p.is_pinned, p.is_secret,
           p.view_count, p.created_at, p.updated_at
    FROM posts p
    WHERE p.board_id = $1
      AND p.status = 'published'
    ORDER BY p.is_pinned DESC,
             CASE WHEN sqlc.arg('sort')::text = 'created' AND sqlc.arg('desc')::bool THEN p.created_at END DESC,
             CASE WHEN sqlc.arg('sort')::text = 'created' AND NOT sqlc.arg('desc')::bool THEN p.created_at END ASC,
             CASE WHEN sqlc.arg('sort')::text = 'views' AND sqlc.arg('desc')::bool THEN p.view_count END DESC,
             CASE WHEN sqlc.arg('sort')::text = 'views' AND NOT sqlc.arg('desc')::bool THEN p.view_count END ASC,
             CASE WHEN sqlc.arg('sort')::text = 'title' AND sqlc.arg('desc')::bool THEN p.title END DESC,
             CASE WHEN sqlc.arg('sort')::text = 'title' AND NOT sqlc.arg('desc')::bool THEN p.title END ASC,
             CASE WHEN sqlc.arg('desc')::bool THEN p.id END DESC,
             CASE WHEN NOT sqlc.arg('desc')::bool THEN p.id END ASC
    LIMIT sqlc.arg('limit')::int OFFSET sqlc.arg('offset')::int
) p
LEFT JOIN users u ON u.id = p.author_id
ORDER BY p.is_pinned DESC,
         CASE WHEN sqlc.arg('sort')::text = 'created' AND sqlc.arg('desc')::bool THEN p.created_at END DESC,
         CASE WHEN sqlc.arg('sort')::text = 'created' AND NOT sqlc.arg('desc')::bool THEN p.created_at END ASC,
         CASE WHEN sqlc.arg('sort')::text = 'views' AND sqlc.arg('desc')::bool THEN p.view_count END DESC,
         CASE WHEN sqlc.arg('sort')::text = 'views' AND NOT sqlc.arg('desc')::bool THEN p.view_count END ASC,
         CASE WHEN sqlc.arg('sort')::text = 'title' AND sqlc.arg('desc')::bool THEN p.title END DESC,
         CASE WHEN sqlc.arg('sort')::text = 'title' AND NOT sqlc.arg('desc')::bool THEN p.title END ASC,
         CASE WHEN sqlc.arg('desc')::bool THEN p.id END DESC,
         CASE WHEN NOT sqlc.arg('desc')::bool THEN p.id END ASC;

-- name: ListPostsSearch :many
SELECT p.id, p.board_id, coalesce(p.author_id::text, '')::text AS author_id, coalesce(u.display_name, '')::text AS author_name,
       p.title, ''::text AS body, p.custom_fields, p.status, p.is_pinned, p.is_secret,
       p.view_count, p.created_at, p.updated_at,
       (SELECT count(*) FROM comments c WHERE c.post_id = p.id)::bigint AS comment_count,
       EXISTS (SELECT 1 FROM attachments a WHERE a.post_id = p.id)::bool AS has_attachment
FROM (
    SELECT p.id, p.board_id, p.author_id, p.title, p.custom_fields, p.status, p.is_pinned, p.is_secret,
           p.view_count, p.created_at, p.updated_at
    FROM posts p
    WHERE p.board_id = $1
      AND p.status = 'published'
      AND p.search_vector @@ to_tsquery('simple', sqlc.arg('search'))
    ORDER BY p.is_pinned DESC,
             CASE WHEN sqlc.arg('sort')::text = 'created' AND sqlc.arg('desc')::bool THEN p.created_at END DESC,
             CASE WHEN sqlc.arg('sort')::text = 'created' AND NOT sqlc.arg('desc')::bool THEN p.created_at END ASC,
             CASE WHEN sqlc.arg('sort')::text = 'views' AND sqlc.arg('desc')::bool THEN p.view_count END DESC,
             CASE WHEN sqlc.arg('sort')::text = 'views' AND NOT sqlc.arg('desc')::bool THEN p.view_count END ASC,
             CASE WHEN sqlc.arg('sort')::text = 'title' AND sqlc.arg('desc')::bool THEN p.title END DESC,
             CASE WHEN sqlc.arg('sort')::text = 'title' AND NOT sqlc.arg('desc')::bool THEN p.title END ASC,
             CASE WHEN sqlc.arg('desc')::bool THEN p.id END DESC,
             CASE WHEN NOT sqlc.arg('desc')::bool THEN p.id END ASC
    LIMIT sqlc.arg('limit')::int OFFSET sqlc.arg('offset')::int
) p
LEFT JOIN users u ON u.id = p.author_id
ORDER BY p.is_pinned DESC,
         CASE WHEN sqlc.arg('sort')::text = 'created' AND sqlc.arg('desc')::bool THEN p.created_at END DESC,
         CASE WHEN sqlc.arg('sort')::text = 'created' AND NOT sqlc.arg('desc')::bool THEN p.created_at END ASC,
         CASE WHEN sqlc.arg('sort')::text = 'views' AND sqlc.arg('desc')::bool THEN p.view_count END DESC,
         CASE WHEN sqlc.arg('sort')::text = 'views' AND NOT sqlc.arg('desc')::bool THEN p.view_count END ASC,
         CASE WHEN sqlc.arg('sort')::text = 'title' AND sqlc.arg('desc')::bool THEN p.title END DESC,
         CASE WHEN sqlc.arg('sort')::text = 'title' AND NOT sqlc.arg('desc')::bool THEN p.title END ASC,
         CASE WHEN sqlc.arg('desc')::bool THEN p.id END DESC,
         CASE WHEN NOT sqlc.arg('desc')::bool THEN p.id END ASC;

-- name: SearchPosts :many
SELECT p.id, p.board_id, coalesce(p.author_id::text, '')::text AS author_id, coalesce(u.display_name, '')::text AS author_name,
       p.title, p.body, p.custom_fields, p.status, p.is_pinned, p.is_secret,
       p.view_count, p.created_at, p.updated_at,
       (SELECT count(*) FROM comments c WHERE c.post_id = p.id)::bigint AS comment_count,
       EXISTS (SELECT 1 FROM attachments a WHERE a.post_id = p.id)::bool AS has_attachment
FROM (
    SELECT p.id, p.board_id, p.author_id, p.title, p.body, p.custom_fields, p.status, p.is_pinned, p.is_secret,
           p.view_count, p.created_at, p.updated_at
    FROM posts p
    WHERE p.board_id = ANY(sqlc.arg('readable')::uuid[])
      AND p.status = 'published'
      AND (NOT p.is_secret OR p.board_id = ANY(sqlc.arg('secret_in')::uuid[]) OR p.author_id = sqlc.narg('viewer_id'))
      AND p.search_vector @@ to_tsquery('simple', sqlc.arg('search'))
    ORDER BY p.is_pinned DESC,
             CASE WHEN sqlc.arg('sort')::text = 'created' AND sqlc.arg('desc')::bool THEN p.created_at END DESC,
             CASE WHEN sqlc.arg('sort')::text = 'created' AND NOT sqlc.arg('desc')::bool THEN p.created_at END ASC,
             CASE WHEN sqlc.arg('sort')::text = 'views' AND sqlc.arg('desc')::bool THEN p.view_count END DESC,
             CASE WHEN sqlc.arg('sort')::text = 'views' AND NOT sqlc.arg('desc')::bool THEN p.view_count END ASC,
             CASE WHEN sqlc.arg('sort')::text = 'title' AND sqlc.arg('desc')::bool THEN p.title END DESC,
             CASE WHEN sqlc.arg('sort')::text = 'title' AND NOT sqlc.arg('desc')::bool THEN p.title END ASC,
             CASE WHEN sqlc.arg('desc')::bool THEN p.id END DESC,
             CASE WHEN NOT sqlc.arg('desc')::bool THEN p.id END ASC
    LIMIT sqlc.arg('limit')::int OFFSET sqlc.arg('offset')::int
) p
LEFT JOIN users u ON u.id = p.author_id
ORDER BY p.is_pinned DESC,
         CASE WHEN sqlc.arg('sort')::text = 'created' AND sqlc.arg('desc')::bool THEN p.created_at END DESC,
         CASE WHEN sqlc.arg('sort')::text = 'created' AND NOT sqlc.arg('desc')::bool THEN p.created_at END ASC,
         CASE WHEN sqlc.arg('sort')::text = 'views' AND sqlc.arg('desc')::bool THEN p.view_count END DESC,
         CASE WHEN sqlc.arg('sort')::text = 'views' AND NOT sqlc.arg('desc')::bool THEN p.view_count END ASC,
         CASE WHEN sqlc.arg('sort')::text = 'title' AND sqlc.arg('desc')::bool THEN p.title END DESC,
         CASE WHEN sqlc.arg('sort')::text = 'title' AND NOT sqlc.arg('desc')::bool THEN p.title END ASC,
         CASE WHEN sqlc.arg('desc')::bool THEN p.id END DESC,
         CASE WHEN NOT sqlc.arg('desc')::bool THEN p.id END ASC;

-- name: PostByID :one
SELECT p.id, p.board_id, coalesce(p.author_id::text, '')::text AS author_id, coalesce(u.display_name, '')::text AS author_name,
       p.title, p.body, p.custom_fields, p.status, p.is_pinned, p.is_secret,
       p.view_count, p.created_at, p.updated_at,
       (SELECT count(*) FROM comments c WHERE c.post_id = p.id)::bigint AS comment_count,
       EXISTS (SELECT 1 FROM attachments a WHERE a.post_id = p.id)::bool AS has_attachment
FROM posts p
LEFT JOIN users u ON u.id = p.author_id
WHERE p.id = $1 AND (sqlc.arg('can_secret')::bool OR NOT p.is_secret OR p.author_id = sqlc.narg('viewer_id'));

-- name: ModeratePosts :many
SELECT p.id, p.board_id, coalesce(p.author_id::text, '')::text AS author_id, coalesce(u.display_name, '')::text AS author_name,
       p.title, ''::text AS body, p.custom_fields, p.status, p.is_pinned, p.is_secret,
       p.view_count, p.created_at, p.updated_at,
       (SELECT count(*) FROM comments c WHERE c.post_id = p.id)::bigint AS comment_count,
       EXISTS (SELECT 1 FROM attachments a WHERE a.post_id = p.id)::bool AS has_attachment
FROM posts p
LEFT JOIN users u ON u.id = p.author_id
WHERE p.board_id = $1
ORDER BY p.is_pinned DESC, p.created_at DESC, p.id DESC
LIMIT $2;

-- name: RecentPosts :many
SELECT p.id, p.board_id, coalesce(p.author_id::text, '')::text AS author_id, coalesce(u.display_name, '')::text AS author_name,
       p.title, ''::text AS body, p.custom_fields, p.status, p.is_pinned, p.is_secret,
       p.view_count, p.created_at, p.updated_at,
       (SELECT count(*) FROM comments c WHERE c.post_id = p.id)::bigint AS comment_count,
       EXISTS (SELECT 1 FROM attachments a WHERE a.post_id = p.id)::bool AS has_attachment
FROM posts p
LEFT JOIN users u ON u.id = p.author_id
WHERE p.board_id = ANY(sqlc.arg('readable')::uuid[])
  AND p.status = 'published'
  AND (NOT p.is_secret OR p.board_id = ANY(sqlc.arg('secret_in')::uuid[]) OR p.author_id = sqlc.narg('viewer_id'))
ORDER BY p.created_at DESC, p.id DESC
LIMIT $1;
