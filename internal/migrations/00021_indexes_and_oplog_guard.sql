-- +goose Up

-- 홈(P-201)과 대시보드(A-101)의 「최근 글」: 읽을 수 있는 게시판 전부를 합쳐
-- created_at 순으로 몇 건. 게시판별 인덱스(posts_board_list_idx)로는 게시판마다
-- 훑어 합친 뒤 전부 정렬해야 한다 — 글 수에 비례한다. 이 인덱스를 거꾸로 걸으면
-- 여섯 건에서 멈춘다.
CREATE INDEX posts_recent_idx ON posts (created_at DESC, id DESC) WHERE status = 'published';

-- P-505·A-505·A-510 의 배송 목록은 kind 조건 없이 order_id 로 찾는다. 있던 두
-- 인덱스는 둘 다 부분 인덱스라 그 조건에 쓰이지 않았다 — 주문이 늘수록 순차 탐색.
CREATE INDEX shipments_order_idx ON shipments (order_id, shipped_at DESC);

-- A-508 대사의 기간 조회.
CREATE INDEX payments_created_idx ON payments (created_at DESC);

-- 작업 로그 append-only 의 구멍 (D15 7절). 00010 의 UPDATE 트리거는 WHEN 절로
-- 「actor_user_id 를 NULL 로 바꾸는 UPDATE」만 통과시키는데(사용자 삭제의
-- SET NULL 을 위해), 그 UPDATE 가 **다른 컬럼도 함께** 바꾸는 것을 막지 않았다:
--   UPDATE operation_logs SET actor_user_id = NULL, summary = '…' WHERE id = …
-- 는 행마다 한 번씩 통과했다 — 기록과 주체를 한 문장으로 지운다. 이 트리거는
-- actor_user_id 를 뺀 나머지 컬럼 중 하나라도 SET 에 있으면 무조건 거부한다.
-- 외래키의 SET NULL 은 actor_user_id 만 건드리므로 그대로 통한다.
CREATE TRIGGER operation_logs_no_update_cols
    BEFORE UPDATE OF id, actor_email, action, target_type, target_id, summary, ip, created_at
    ON operation_logs
    FOR EACH ROW EXECUTE FUNCTION operation_logs_append_only();

-- +goose Down

DROP TRIGGER operation_logs_no_update_cols ON operation_logs;
DROP INDEX payments_created_idx;
DROP INDEX shipments_order_idx;
DROP INDEX posts_recent_idx;
