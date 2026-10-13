-- name: CountPosts :one
SELECT count(*) FROM posts p
WHERE p.board_id = $1 AND p.status = 'published';

-- name: CountPostsSearch :one
-- **읽을 수 없는 비밀글은 검색에서 빠진다.** 목록은 비밀글 제목을 누구에게나
-- 보이므로, 검색이 본문을 보고 비밀글을 남기면 「이 낱말로 그 글이 걸렸다」가
-- 곧 본문 한 낱말이다. ListPostsSearch 와 같은 술어여야 합계가 쪽과 맞는다.
-- **상한(cap)까지만 센다.** 흔한 낱말은 맞는 글이 수십만이고, 다 세면 표를 끝까지 읽는다(100만 건에서
-- 960ms). cap 에서 멈추면 51ms 다 — 화면은 「10,000건 이상」이라 쓴다.
SELECT count(*) FROM (
    SELECT 1 FROM posts p
    WHERE p.board_id = sqlc.arg('board_id') AND p.status = 'published'
      AND (NOT p.is_secret OR sqlc.arg('can_secret')::bool OR p.author_id = sqlc.narg('viewer_id'))
      AND p.search_vector @@ to_tsquery('simple', sqlc.arg('search'))
    LIMIT sqlc.arg('cap')::int
) m;

-- name: CountSearchPosts :one
-- 상한까지만 센다 (CountPostsSearch 와 같은 이유).
SELECT count(*) FROM (
    SELECT 1 FROM posts p
    WHERE p.board_id = ANY(sqlc.arg('readable')::uuid[]) AND p.status = 'published'
      AND (NOT p.is_secret OR p.board_id = ANY(sqlc.arg('secret_in')::uuid[]) OR p.author_id = sqlc.narg('viewer_id'))
      AND p.search_vector @@ to_tsquery('simple', sqlc.arg('search'))
    LIMIT sqlc.arg('cap')::int
) m;

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
-- 카드 사진은 실제로 있는 사진일 때만 붙는다 — 본문 HTML 은 브라우저가 보낸 것이라 없는
-- id 를 실을 수 있고, 그대로 넣으면 외래 키 위반이 500 이 된다.
INSERT INTO posts (board_id, author_id, title, body, body_html, custom_fields, is_secret, cover_media_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, (SELECT m.id FROM media m WHERE m.id = sqlc.narg('cover_media_id')))
RETURNING id;

-- name: UpdatePost :execrows
UPDATE posts SET title = $2, body = $3, body_html = $4, custom_fields = $5, is_secret = $6,
       cover_media_id = (SELECT m.id FROM media m WHERE m.id = sqlc.narg('cover_media_id')),
       updated_at = now()
WHERE posts.id = $1;

-- name: SetPostFlags :execrows
UPDATE posts SET is_pinned = $2, status = $3, updated_at = now() WHERE id = $1;

-- name: DeletePost :execrows
DELETE FROM posts WHERE id = $1;

-- name: AddViewCounts :exec
-- 모아 둔 조회수를 한 문장으로 더한다 (content.viewBuffer). 그 사이 지워진 글은 맞는 행이
-- 없어 건너뛴다. 두 배열은 같은 길이라 select 목록의 unnest 둘이 나란히 짝지어진다
-- (sqlc 는 인자 둘인 unnest 를 모른다).
UPDATE posts p SET view_count = p.view_count + v.n
FROM (SELECT unnest(sqlc.arg('ids')::uuid[]) AS id, unnest(sqlc.arg('counts')::int[]) AS n) v
WHERE p.id = v.id;

-- name: Comments :many
SELECT c.id, c.post_id, coalesce(c.parent_id::text, '')::text AS parent_id,
       coalesce(c.author_id::text, '')::text AS author_id, coalesce(u.display_name, '')::text AS author_name,
       c.body, coalesce(c.deleted_at, 'epoch'::timestamptz)::timestamptz AS deleted_at, c.created_at
FROM comments c
LEFT JOIN users u ON u.id = c.author_id
-- 스레드는 그 첫 댓글의 시각 순이다. 묶는 열(coalesce(parent_id, id))만으로 정렬하면
-- 무작위 uuid 순이 된다. 스레드 안에서는 첫 댓글, 그다음 답이 시각 순 (대댓글은 1단계).
LEFT JOIN comments root ON root.id = c.parent_id
WHERE c.post_id = $1
ORDER BY coalesce(root.created_at, c.created_at), coalesce(c.parent_id, c.id),
         c.parent_id IS NOT NULL, c.created_at, c.id;

-- name: ModerateComments :many
SELECT c.id, c.post_id, coalesce(c.parent_id::text, '')::text AS parent_id,
       coalesce(c.author_id::text, '')::text AS author_id, coalesce(u.display_name, '')::text AS author_name,
       c.body, coalesce(c.deleted_at, 'epoch'::timestamptz)::timestamptz AS deleted_at, c.created_at
FROM comments c
JOIN posts p ON p.id = c.post_id
LEFT JOIN users u ON u.id = c.author_id
WHERE p.board_id = $1
ORDER BY c.created_at DESC, c.id DESC
LIMIT $2 OFFSET $3;

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
--
-- **ListPosts 의 안쪽은 id 만 고른다** (지연 조인, FR-508). 정렬 열과 거르는 조건이 공개
-- 목록 부분 인덱스(00029)에 다 있어서 안쪽은 인덱스만 읽는다 — OFFSET 이 건너뛰는 행마다
-- 표를 읽지 않는다. 열을 고르던 판은 5,000쪽에서 2.9초였다(100만 건, 00029 주석).
-- name: ListPosts :many
SELECT p.id, p.board_id, coalesce(p.author_id::text, '')::text AS author_id, coalesce(u.display_name, '')::text AS author_name,
       p.title, ''::text AS body, ''::text AS body_html, coalesce(p.cover_media_id::text, '')::text AS cover_media_id, p.custom_fields, p.status, p.is_pinned, p.is_secret,
       p.view_count, p.created_at, p.updated_at,
       (SELECT count(*) FROM comments c WHERE c.post_id = p.id)::bigint AS comment_count,
       EXISTS (SELECT 1 FROM attachments a WHERE a.post_id = p.id)::bool AS has_attachment
FROM (
    SELECT p.id
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
) k
JOIN posts p ON p.id = k.id
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

-- **검색 목록은 두 모양이다** (2026-10-13, 100만 건 실측). 개수를 먼저 세고(상한 SearchCountCap 까지) 그
-- 수로 고른다 — 계획기는 접두 tsquery 의 선택도를 맞히지 못해(D30) 드문 낱말에 정렬 인덱스를 걸어 게시판을
-- 끝까지 걷고(239ms), 흔한 낱말에 GIN 으로 맞는 글을 다 모아 정렬한다(957ms).
--   · 걷는 모양(ListPostsSearch·SearchPosts): 정렬 인덱스를 따라 걸으며 거르다 한 쪽이 차면 멈춘다. 맞는 글이
--     많을 때 빠르다 — 통합 검색 흔한 낱말 1쪽 957ms → 5ms (posts_pub_pinned_recent_idx, 00030).
--   · 모으는 모양(…Few): 맞는 글을 GIN 으로 먼저 모은 뒤(MATERIALIZED — 정렬 인덱스 쪽으로 다시 풀리지 않게)
--     그 안에서 정렬한다. 맞는 글이 적을 때 빠르다 — 게시판 드문 낱말 239ms → 9ms.
-- 둘 다 안쪽은 id 만 고르고 본문·열은 고른 행에서만 읽는다 — 정렬이 본문(최대 5만 자)을 들고 다니지 않는다.
-- name: ListPostsSearch :many
SELECT p.id, p.board_id, coalesce(p.author_id::text, '')::text AS author_id, coalesce(u.display_name, '')::text AS author_name,
       p.title, ''::text AS body, ''::text AS body_html, coalesce(p.cover_media_id::text, '')::text AS cover_media_id, p.custom_fields, p.status, p.is_pinned, p.is_secret,
       p.view_count, p.created_at, p.updated_at,
       (SELECT count(*) FROM comments c WHERE c.post_id = p.id)::bigint AS comment_count,
       EXISTS (SELECT 1 FROM attachments a WHERE a.post_id = p.id)::bool AS has_attachment
FROM (
    SELECT p.id
    FROM posts p
    WHERE p.board_id = sqlc.arg('board_id')
      AND p.status = 'published'
      -- CountPostsSearch 의 주석 참고. 검색에서만 거른다 — 검색어 없는 목록은 제목을 보인다.
      AND (NOT p.is_secret OR sqlc.arg('can_secret')::bool OR p.author_id = sqlc.narg('viewer_id'))
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
) k
JOIN posts p ON p.id = k.id
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

-- name: ListPostsSearchFew :many
WITH m AS MATERIALIZED (
    SELECT p.id, p.is_pinned, p.created_at, p.view_count, p.title
    FROM posts p
    WHERE p.board_id = sqlc.arg('board_id')
    AND p.status = 'published'
    -- CountPostsSearch 의 주석 참고. 검색에서만 거른다 — 검색어 없는 목록은 제목을 보인다.
    AND (NOT p.is_secret OR sqlc.arg('can_secret')::bool OR p.author_id = sqlc.narg('viewer_id'))
    AND p.search_vector @@ to_tsquery('simple', sqlc.arg('search'))
)
SELECT p.id, p.board_id, coalesce(p.author_id::text, '')::text AS author_id, coalesce(u.display_name, '')::text AS author_name,
       p.title, ''::text AS body, ''::text AS body_html, coalesce(p.cover_media_id::text, '')::text AS cover_media_id, p.custom_fields, p.status, p.is_pinned, p.is_secret,
       p.view_count, p.created_at, p.updated_at,
       (SELECT count(*) FROM comments c WHERE c.post_id = p.id)::bigint AS comment_count,
       EXISTS (SELECT 1 FROM attachments a WHERE a.post_id = p.id)::bool AS has_attachment
FROM (
    SELECT p.id
    FROM m p
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
) k
JOIN posts p ON p.id = k.id
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
       p.title, p.body, ''::text AS body_html, coalesce(p.cover_media_id::text, '')::text AS cover_media_id, p.custom_fields, p.status, p.is_pinned, p.is_secret,
       p.view_count, p.created_at, p.updated_at,
       (SELECT count(*) FROM comments c WHERE c.post_id = p.id)::bigint AS comment_count,
       EXISTS (SELECT 1 FROM attachments a WHERE a.post_id = p.id)::bool AS has_attachment
FROM (
    SELECT p.id
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
) k
JOIN posts p ON p.id = k.id
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

-- name: SearchPostsFew :many
WITH m AS MATERIALIZED (
    SELECT p.id, p.is_pinned, p.created_at, p.view_count, p.title
    FROM posts p
    WHERE p.board_id = ANY(sqlc.arg('readable')::uuid[])
    AND p.status = 'published'
    AND (NOT p.is_secret OR p.board_id = ANY(sqlc.arg('secret_in')::uuid[]) OR p.author_id = sqlc.narg('viewer_id'))
    AND p.search_vector @@ to_tsquery('simple', sqlc.arg('search'))
)
SELECT p.id, p.board_id, coalesce(p.author_id::text, '')::text AS author_id, coalesce(u.display_name, '')::text AS author_name,
       p.title, p.body, ''::text AS body_html, coalesce(p.cover_media_id::text, '')::text AS cover_media_id, p.custom_fields, p.status, p.is_pinned, p.is_secret,
       p.view_count, p.created_at, p.updated_at,
       (SELECT count(*) FROM comments c WHERE c.post_id = p.id)::bigint AS comment_count,
       EXISTS (SELECT 1 FROM attachments a WHERE a.post_id = p.id)::bool AS has_attachment
FROM (
    SELECT p.id
    FROM m p
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
) k
JOIN posts p ON p.id = k.id
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
       p.title, p.body, p.body_html, coalesce(p.cover_media_id::text, '')::text AS cover_media_id, p.custom_fields, p.status, p.is_pinned, p.is_secret,
       p.view_count, p.created_at, p.updated_at,
       (SELECT count(*) FROM comments c WHERE c.post_id = p.id)::bigint AS comment_count,
       EXISTS (SELECT 1 FROM attachments a WHERE a.post_id = p.id)::bool AS has_attachment
FROM posts p
LEFT JOIN users u ON u.id = p.author_id
WHERE p.id = $1 AND (sqlc.arg('can_secret')::bool OR NOT p.is_secret OR p.author_id = sqlc.narg('viewer_id'));

-- name: ModeratePosts :many
SELECT p.id, p.board_id, coalesce(p.author_id::text, '')::text AS author_id, coalesce(u.display_name, '')::text AS author_name,
       p.title, ''::text AS body, ''::text AS body_html, coalesce(p.cover_media_id::text, '')::text AS cover_media_id, p.custom_fields, p.status, p.is_pinned, p.is_secret,
       p.view_count, p.created_at, p.updated_at,
       (SELECT count(*) FROM comments c WHERE c.post_id = p.id)::bigint AS comment_count,
       EXISTS (SELECT 1 FROM attachments a WHERE a.post_id = p.id)::bool AS has_attachment
FROM posts p
LEFT JOIN users u ON u.id = p.author_id
WHERE p.board_id = $1
ORDER BY p.is_pinned DESC, p.created_at DESC, p.id DESC
LIMIT $2 OFFSET $3;

-- name: RecentPosts :many
SELECT p.id, p.board_id, coalesce(p.author_id::text, '')::text AS author_id, coalesce(u.display_name, '')::text AS author_name,
       p.title, ''::text AS body, ''::text AS body_html, coalesce(p.cover_media_id::text, '')::text AS cover_media_id, p.custom_fields, p.status, p.is_pinned, p.is_secret,
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
