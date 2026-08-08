-- +goose Up

-- **회원 프로필 항목을 운영자가 정의한다** (FR-215).
--
-- 지금 회원이 가진 것은 이메일·이름·비밀번호뿐이다. 사이트마다 더 받아야 할
-- 것이 다르고(생년월일·소속·연락처·약관 외 동의…), 그것을 코드에 적으면
-- 사이트마다 다른 바이너리가 된다.
--
-- **게시판 커스텀 필드와 같은 구조다** (DEC-3.9). 항목을 늘릴 때마다 컬럼을
-- 붙이면 그누보드가 여분 필드를 10개로 고정한 것과 같은 벽에 부딪히고, 그
-- 벽은 ALTER TABLE 없이는 못 넘는다. 정의는 행으로, 값은 JSONB 로 둔다 —
-- 그래서 **개수 제한이 없다.**
--
-- board_fields 와 나란한 표를 따로 두는 이유: 하나로 합치면
-- `(scope, scope_id)` 같은 다형 키가 생기고 FK 를 걸 수 없게 된다. 두 표는
-- 모양만 같을 뿐 참조 대상이 다르다.
CREATE TABLE user_fields (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    key          text        NOT NULL UNIQUE
                             CHECK (key ~ '^[a-z][a-z0-9_]*$' AND length(key) <= 32),
    label        text        NOT NULL CHECK (length(label) BETWEEN 1 AND 100),
    -- board_fields 와 **같은 목록**이다. 갈라지면 partials/field.html 이
    -- 한쪽 타입을 그리지 못한다.
    field_type   text        NOT NULL CHECK (field_type IN
                             ('text','textarea','number','select','checkbox','multiselect','date','url')),
    is_required  boolean     NOT NULL DEFAULT false,
    -- 관리자 사용자 목록(A-401)에 열로 보일지.
    show_in_list boolean     NOT NULL DEFAULT false,
    options      jsonb       NOT NULL DEFAULT '[]',
    sort_order   integer     NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- 값. 정의를 지워도 여기 남은 값은 지우지 않는다 (D14 3절 규칙 4) — 항목을
-- 잘못 지운 운영자가 사람들이 적어 낸 것까지 잃지 않도록.
ALTER TABLE users ADD COLUMN custom_fields jsonb NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE users DROP COLUMN custom_fields;
DROP TABLE user_fields;
