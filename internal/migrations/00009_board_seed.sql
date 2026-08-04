-- +goose Up

-- Phase 2 권한과 그 전역 부여.
--
-- Phase 1 이 19개만 심은 이유(D15 4.4 의 "어떤 라우트도 쓰지 않는 권한" 경고가
-- 매 부팅 18건을 뱉으면 검사 자체가 무시된다)가 여기서도 그대로다 — Phase 2
-- 화면이 붙는 릴리즈에 Phase 2 권한을 심는다.
--
-- is_scoped 는 D15 2.4 의 6개에만 true 다. 스코프가 아닌 권한에 board_id 를
-- 붙이려는 시도는 부여 핸들러가 막는다 (DB 로는 표현할 수 없다).
INSERT INTO permissions (key, description, is_scoped) VALUES
    ('theme.upload',     '테마 zip 업로드',                          false),
    ('board.view',       '게시판 정의·필드 스키마 조회',             false),
    ('board.manage',     '게시판 생성·수정·삭제, 필드 스키마 편집',  false),
    ('post.read',        '글 목록·본문 열람',                        true),
    ('post.write',       '글 작성',                                  true),
    ('post.moderate',    '남의 글 수정·삭제·이동·고정',              true),
    ('post.read_secret', '비밀글 열람',                              true),
    ('comment.write',    '댓글·대댓글 작성',                         true),
    ('comment.moderate', '남의 댓글 수정·삭제',                      true),
    ('log.view',         '작업 로그 열람',                           false);

-- D15 2.5 의 `●` 만 여기서 넣는다.
--
-- `◐` 는 게시판 스코프 부여이고 게시판을 만들 때 프리셋이 넣는다 (2.4). 전역
-- 으로 심으면 프리셋이 무의미해진다 — 모든 게시판이 이미 공개인 상태에서
-- "비공개" 를 골라도 아무것도 달라지지 않는다.
--
-- theme.upload 은 어떤 내장 역할에도 없다 = admin 만 가진다. admin 은
-- is_superuser 라 부여 표를 보지 않는다 (D15 1.2 — 코드 업로드는 최소
-- 인원이어야 한다).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM (VALUES
    ('editor',   'board.view'),
    ('editor',   'post.read'),
    ('editor',   'post.write'),
    ('editor',   'post.moderate'),
    ('editor',   'post.read_secret'),
    ('editor',   'comment.write'),
    ('editor',   'comment.moderate'),
    ('operator', 'board.view'),
    ('operator', 'board.manage'),
    ('operator', 'log.view'),
    ('operator', 'post.read'),
    ('operator', 'post.write'),
    ('operator', 'post.moderate'),
    ('operator', 'post.read_secret'),
    ('operator', 'comment.write'),
    ('operator', 'comment.moderate')
) AS m(role_key, permission_key)
JOIN roles       r ON r.key = m.role_key
JOIN permissions p ON p.key = m.permission_key;

-- +goose Down

DELETE FROM role_permissions rp USING permissions p
WHERE rp.permission_id = p.id AND p.key IN (
    'theme.upload','board.view','board.manage','post.read','post.write',
    'post.moderate','post.read_secret','comment.write','comment.moderate','log.view');

DELETE FROM permissions WHERE key IN (
    'theme.upload','board.view','board.manage','post.read','post.write',
    'post.moderate','post.read_secret','comment.write','comment.moderate','log.view');
