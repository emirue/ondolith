-- +goose Up

-- D41 테마 선언 관리자 화면. 테마가 admin.json 으로 선언한 화면의 값을 담는다.
--
-- **화면마다 테이블을 만들지 않는다** (DEC-3.9). 화면은 테마 파일에 있고 DB 에는
-- 화면 키와 JSONB 값만 있다 — 그래서 외래키가 없다. 값은 테마를 바꾸거나 선언에서
-- 화면을 빼도 지우지 않는다 (D41 지배 원칙 2). 같은 키를 다시 선언하면 돌아온다.
--
-- 화면 키 규칙은 게시판 커스텀 필드 키와 같다 — 템플릿이 `.Theme.<키>` 로 바로 쓴다.

-- 설정형: 화면 하나에 값 한 벌.
CREATE TABLE theme_settings (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    screen_key text        NOT NULL UNIQUE CHECK (screen_key ~ '^[a-z][a-z0-9_]{0,31}$'),
    data       jsonb       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(data) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- 모음형: 화면 하나에 행 여러 개. 순서는 숫자 칸이다 (메뉴 A-204 와 같다).
-- 화면당 200행 상한은 저장소가 센다 (D41 2절).
CREATE TABLE theme_items (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    screen_key text        NOT NULL CHECK (screen_key ~ '^[a-z][a-z0-9_]{0,31}$'),
    sort_order integer     NOT NULL DEFAULT 0,
    data       jsonb       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(data) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX theme_items_screen_sort_idx ON theme_items (screen_key, sort_order);

-- 화면 스코프 (D41 4절, D15 2.4). 권한 키를 화면마다 만들지 않고 부여 행에 화면 키를
-- 단다 — NULL 은 모든 화면이다. 「theme.edit_content 에만」은 다른 표를 읽어야 해서
-- CHECK 로 말할 수 없다: 부여 처리기와 이 시드가 auth.ValidateScreenGrant 를 따른다.
ALTER TABLE role_permissions
    ADD COLUMN theme_screen text CHECK (theme_screen ~ '^[a-z][a-z0-9_]{0,31}$');
ALTER TABLE role_permissions
    ADD CONSTRAINT role_permissions_one_scope CHECK (board_id IS NULL OR theme_screen IS NULL);

-- 유일성에 화면을 넣는다. 이름은 그대로 둔다 — 부여 질의가
-- `ON CONFLICT ON CONSTRAINT role_permissions_uniq` 로 이 이름을 쓴다.
-- 기존 행은 theme_screen 이 전부 NULL 이라 새 제약에서도 그대로 유일하다.
ALTER TABLE role_permissions DROP CONSTRAINT role_permissions_uniq;
ALTER TABLE role_permissions
    ADD CONSTRAINT role_permissions_uniq
    UNIQUE NULLS NOT DISTINCT (role_id, permission_id, board_id, theme_screen);

-- Phase 6 권한과 그 전역 부여.
--
-- is_scoped = false: 그 칸은 게시판 스코프를 뜻한다(D15 2.4). 화면 스코프는 위의
-- 불변식이 따로 정한다. operator 만 전역으로 갖는다 — editor 에는 운영자가 화면
-- 단위로 준다 (D15 2.5). admin 은 superuser 라 부여가 필요 없다.
INSERT INTO permissions (key, description, is_scoped) VALUES
    ('theme.edit_content', '테마 화면 편집', false);

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM (VALUES
    ('operator', 'theme.edit_content')
) AS g(role_key, perm_key)
JOIN roles r       ON r.key = g.role_key
JOIN permissions p ON p.key = g.perm_key;

-- +goose Down
-- **되돌리면 테마 화면에 입력한 값이 사라진다** (두 테이블을 지운다). 사진 파일은
-- media 에 남는다. 값을 지키려면 내리기 전에 백업한다 — 되돌리는 길은 백업 복원이다
-- (NFR-308, D41 「마이그레이션」).

-- 화면 스코프 부여는 옛 스키마로 표현할 수 없으므로 먼저 지운다. 남기면 옛 유일성
-- (role, permission, board)에서 같은 역할·권한의 화면 부여 여러 행이 서로 부딪힌다.
DELETE FROM role_permissions WHERE theme_screen IS NOT NULL;
ALTER TABLE role_permissions DROP CONSTRAINT role_permissions_uniq;
ALTER TABLE role_permissions
    ADD CONSTRAINT role_permissions_uniq
    UNIQUE NULLS NOT DISTINCT (role_id, permission_id, board_id);
ALTER TABLE role_permissions DROP CONSTRAINT role_permissions_one_scope;
ALTER TABLE role_permissions DROP COLUMN theme_screen;

DELETE FROM role_permissions
WHERE permission_id IN (SELECT id FROM permissions WHERE key = 'theme.edit_content');
DELETE FROM permissions WHERE key = 'theme.edit_content';

DROP TABLE theme_items;
DROP TABLE theme_settings;
