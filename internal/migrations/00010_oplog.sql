-- +goose Up

-- 작업 로그 (D15 7절). 이 표는 수정·삭제하지 않는다 — 아래 트리거가 강제한다.

CREATE TABLE operation_logs (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- 사용자가 지워져도 로그는 남는다 (D30 3-1: SET NULL).
    actor_user_id uuid        REFERENCES users(id) ON DELETE SET NULL,
    -- 그래서 이메일을 스냅샷으로 함께 둔다. '' 는 시스템 주체다.
    actor_email   text        NOT NULL DEFAULT '' CHECK (length(actor_email) <= 254),
    -- 값 집합을 열거형으로 고정하지 않는다. 단일 출처는 D15 7절 표이고 그
    -- 표는 Phase 3 까지 늘어난다 — CHECK 열거면 동작 하나 추가마다
    -- 마이그레이션이 필요하고, 값 집합을 좁히는 마이그레이션은 과거 행을
    -- 위반 상태로 만들어 NFR-303 과 부딪힌다. 형태만 고정한다.
    action        text        NOT NULL CHECK (action ~ '^[a-z][a-z0-9]*\.[a-z][a-z0-9_]*$'),
    target_type   text        NOT NULL CHECK (target_type ~ '^[a-z][a-z0-9_]*$'),
    -- uuid 가 아니다. 대상이 항상 uuid 행은 아니다 — 설정은 키, 테마는 이름,
    -- 메일 설정은 PK 가 없다. uuid 컬럼이면 D15 7절이 "반드시 기록"으로
    -- 지정한 그 항목들이 대상 없이 남는다.
    target_id     text        CHECK (target_id IS NULL OR length(target_id) <= 255),
    summary       text        NOT NULL DEFAULT '' CHECK (length(summary) <= 2000),
    ip            inet,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX operation_logs_created_at_idx ON operation_logs (created_at DESC);
CREATE INDEX operation_logs_actor_idx      ON operation_logs (actor_user_id);

-- +goose StatementBegin
CREATE FUNCTION operation_logs_append_only() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION '작업 로그는 수정·삭제할 수 없습니다 (D15 7절)';
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER operation_logs_no_delete BEFORE DELETE ON operation_logs
    FOR EACH ROW EXECUTE FUNCTION operation_logs_append_only();

-- 측정한 것: 단순한 BEFORE UPDATE 트리거는 **사용자 삭제를 통째로 막았다.**
-- PostgreSQL 은 ON DELETE SET NULL 을 내부 `UPDATE ONLY operation_logs SET
-- actor_user_id = NULL` 로 실행한다. UPDATE 전체를 막으면 3-1 이 정한 SET NULL
-- 이 실패하고 DELETE FROM users 가 에러를 낸다.
--
-- WHEN 절이 그 한 가지 UPDATE 만 통과시킨다: actor_user_id 를 NULL 로 바꾸는
-- UPDATE 이면서 원래 값이 NULL 이 아니었던 경우.
CREATE TRIGGER operation_logs_no_update BEFORE UPDATE ON operation_logs
    FOR EACH ROW WHEN (NEW.actor_user_id IS NOT NULL OR OLD.actor_user_id IS NULL)
    EXECUTE FUNCTION operation_logs_append_only();

-- +goose Down

DROP TABLE operation_logs;
DROP FUNCTION operation_logs_append_only();
