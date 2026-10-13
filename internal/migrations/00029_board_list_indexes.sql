-- +goose Up

-- FR-508: 게시판 목록(P-203)이 몇 쪽이든 인덱스만 읽고 끝나게 한다.
--
-- 목록은 안쪽에서 id 만 골라 쪽을 정하고 바깥에서 그 20행만 읽는다(지연 조인,
-- queries/post.sql ListPosts). 안쪽이 인덱스만 읽으려면 거르는 조건(status)까지 인덱스에
-- 있어야 한다 — 없으면 OFFSET 이 건너뛰는 행마다 표를 읽는다. 100만 건 실측(2026-10-13,
-- 50만 건 게시판, DB 메모리 512MB): 5,000쪽(OFFSET 99,980)이 2,919ms(표를 10만 번 읽음)
-- 에서 이 부분 인덱스로 13ms(Index Only Scan, Heap Fetches 0).
--
-- 공개 목록은 발행 글만 읽으므로 부분 인덱스다. 조회순·제목순의 전체 인덱스(00022·00023)는
-- 공개 목록만 썼으므로 부분 인덱스로 바꾼다. 관리 목록(A-307)은 숨긴 글도 보므로
-- posts_board_list_idx 는 그대로 둔다. posts_board_published_idx 는 「전체 N건」만 썼는데,
-- 그 수는 이제 프로세스가 기억해 두고(content.postCounts) 쓰기가 있을 때만 다시 센다 —
-- 다시 셀 때는 아래 작성일 인덱스만 읽어도 된다.
--
-- CONCURRENTLY 를 쓰지 않는다. 마이그레이션은 서비스를 멈춘 뒤 부팅에서 돌아(D70 업그레이드
-- 절차) 그 사이 쓰는 쪽이 없다 — CONCURRENTLY 는 표를 두 번 훑어 부팅만 길게 하고, 트랜잭션
-- 밖에서 돌아 중간에 실패하면 무효 인덱스가 남는다. 이 파일은 한 트랜잭션이라 실패하면 통째로
-- 되돌아간다. 100만 건에서 인덱스 하나에 약 1.4초다.
CREATE INDEX posts_board_pub_created_idx
    ON posts (board_id, is_pinned DESC, created_at DESC, id DESC) WHERE status = 'published';
CREATE INDEX posts_board_pub_views_idx
    ON posts (board_id, is_pinned DESC, view_count DESC, id DESC) WHERE status = 'published';
CREATE INDEX posts_board_pub_title_idx
    ON posts (board_id, is_pinned DESC, title ASC, id ASC) WHERE status = 'published';
CREATE INDEX posts_board_pub_title_desc_idx
    ON posts (board_id, is_pinned DESC, title DESC, id DESC) WHERE status = 'published';

DROP INDEX posts_board_views_idx;
DROP INDEX posts_board_title_idx;
DROP INDEX posts_board_title_desc_idx;
DROP INDEX posts_board_published_idx;

-- +goose Down
-- 되돌리면 깊은 쪽이 다시 표를 읽는다(100쪽 상한이 없는 판에서는 느려진다).
CREATE INDEX posts_board_published_idx ON posts (board_id) WHERE status = 'published';
CREATE INDEX posts_board_views_idx ON posts (board_id, is_pinned DESC, view_count DESC, id DESC);
CREATE INDEX posts_board_title_idx ON posts (board_id, is_pinned DESC, title ASC, id ASC);
CREATE INDEX posts_board_title_desc_idx ON posts (board_id, is_pinned DESC, title DESC, id DESC);

DROP INDEX posts_board_pub_created_idx;
DROP INDEX posts_board_pub_views_idx;
DROP INDEX posts_board_pub_title_idx;
DROP INDEX posts_board_pub_title_desc_idx;
