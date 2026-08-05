-- +goose Up

-- Phase 1 권한과 그 전역 부여.
--
-- 이 줄이 형식이다. `make check` 가 시드 파일들에서 Phase 번호를 읽어 D15 2.2
-- 와 대조할 범위를 정한다 — 범위를 검사기에 적어 두면 다음 Phase 에서 낡고,
-- 시드가 심은 것에서 유도하면 "문서가 권한을 나중 Phase 로 옮겼다" 를 잡지
-- 못한다 (그 권한 자신이 범위를 넓혀 버린다).
--
-- Roles are data (D15 P2): an installation may add its own. These five are the
-- ones the code reasons about, so they ship as is_builtin.
--
-- anonymous and member are is_assignable = false. They are granted implicitly
-- to every request, so a user_roles row pointing at them would be a second,
-- contradictory source of the same fact.
INSERT INTO roles (key, name, description, is_builtin, is_superuser, is_assignable) VALUES
    ('anonymous', '익명',   '세션에 사용자가 없는 요청자. 모든 요청자의 바닥값',   true, false, false),
    ('member',    '회원',   '인증된 요청자 전원',                                  true, false, false),
    ('editor',    '편집자', '콘텐츠를 만진다. 돈과 설정은 못 만진다',              true, false, true),
    ('operator',  '운영자', '사이트를 굴린다. 게시판·상품·주문·환불·설정',         true, false, true),
    ('admin',     '관리자', '권한 체계를 바꿀 수 있는 유일한 역할',                true, true,  true);

-- Only the 19 permissions D15 §2.2 marks Phase 1.
--
-- Seeding all 37 would make D15 §4.4's "no route uses this permission" warning
-- fire 18 times on every boot, and a warning that always fires is a warning
-- nobody reads. The rest arrive with the phase that uses them.
INSERT INTO permissions (key, description, is_scoped) VALUES
    ('admin.access',        '관리자 트리 진입',                          false),
    ('settings.view',       '시스템 정보 조회 (버전·마이그레이션 상태·DB 연결)', false),
    ('settings.update',     '사이트 설정 변경',                          false),
    ('theme.view',          '테마 목록 조회',                            false),
    ('theme.activate',      '활성 테마 교체',                            false),
    ('menu.manage',         '메뉴 트리 편집',                            false),
    ('user.view',           '사용자 조회',                               false),
    ('user.create',         '사용자 생성',                               false),
    ('user.update',         '사용자 정보·활성 상태 변경',                false),
    ('user.delete',         '사용자 삭제',                               false),
    ('user.reset_password', '타인의 비밀번호 재설정 강제',               false),
    ('role.view',           '역할·권한 구성 조회',                       false),
    ('role.assign',         '사용자에게 역할 부여·회수',                 false),
    ('role.manage',         '역할 생성·삭제, 역할에 권한 부여',          false),
    ('page.view',           '페이지 목록·초안 열람',                     false),
    ('page.create',         '페이지 생성',                               false),
    ('page.update',         '페이지 수정',                               false),
    ('page.delete',         '페이지 삭제',                               false),
    ('page.publish',        '발행·발행취소',                             false);

-- D15 §2.5, restricted to the Phase 1 permissions above. admin is absent on
-- purpose: is_superuser bypasses the table, which is what keeps an upgrade
-- that adds a permission from needing a data migration per installation.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM (VALUES
    ('editor',   'admin.access'),
    ('editor',   'page.view'),
    ('editor',   'page.create'),
    ('editor',   'page.update'),
    ('editor',   'page.delete'),
    ('editor',   'page.publish'),
    ('operator', 'admin.access'),
    ('operator', 'settings.view'),
    ('operator', 'settings.update'),
    ('operator', 'theme.view'),
    ('operator', 'theme.activate'),
    ('operator', 'menu.manage'),
    ('operator', 'user.view'),
    ('operator', 'role.view'),
    ('operator', 'page.view'),
    ('operator', 'page.create'),
    ('operator', 'page.update'),
    ('operator', 'page.delete'),
    ('operator', 'page.publish')
) AS m(role_key, permission_key)
JOIN roles       r ON r.key = m.role_key
JOIN permissions p ON p.key = m.permission_key;

-- Carry the Phase 0 boolean over. Without this an existing installation
-- upgrades into a site whose only administrator has no role.
INSERT INTO user_roles (user_id, role_id)
SELECT u.id, r.id
FROM users u
CROSS JOIN roles r
WHERE u.is_admin AND r.key = 'admin';

-- +goose Down

-- Assignments to builtin roles go first: role_id is ON DELETE RESTRICT, so
-- leaving them would make this rollback fail on any installation that had
-- actually assigned somebody.
DELETE FROM user_roles WHERE role_id IN (SELECT id FROM roles WHERE is_builtin);
DELETE FROM role_permissions
 WHERE role_id       IN (SELECT id FROM roles       WHERE is_builtin)
    OR permission_id IN (SELECT id FROM permissions);
DELETE FROM permissions;
DELETE FROM roles WHERE is_builtin;
