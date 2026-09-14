-- +goose Up

-- 환불 실행(A-507 의 「PG 호출 → 확정」)이 돌려받은 응답 원문. 사후 대조의 근거다
-- (D15 SC-6 7항). 카드 필드는 넣기 전에 가린다 (DEC-3.7).
ALTER TABLE refunds ADD COLUMN pg_response jsonb;

-- 게시판 목록의 두 정렬(조회순·제목순)에 인덱스가 없었다. 정렬 키는 공개 URL 인자라
-- 크롤러가 밟고, OFFSET 과 만나면 게시판 글 전부를 정렬한다. 기본 방향만 잡는다 —
-- 고정 글이 앞이라 방향이 섞인 정렬은 인덱스 하나로 되지 않는다.
CREATE INDEX posts_board_views_idx ON posts (board_id, is_pinned DESC, view_count DESC, id DESC);
CREATE INDEX posts_board_title_idx ON posts (board_id, is_pinned DESC, title ASC, id ASC);

-- +goose Down

DROP INDEX posts_board_title_idx;
DROP INDEX posts_board_views_idx;
ALTER TABLE refunds DROP COLUMN pg_response;
