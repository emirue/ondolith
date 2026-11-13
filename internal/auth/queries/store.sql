-- name: LoadPermissions :one
-- R9: 모든 요청이 갖는 역할(anonymous·member)에서는 방문자 권한(visitor)만 효력이 있다 — 거기 들어간
-- 관리 권한은 가입한 누구에게나 간다. 부여 화면이 막기 전에 들어간 것까지 여기서 버린다.
WITH effective AS (
    SELECT r.id, r.key, r.is_superuser
    FROM roles r
    WHERE r.key IN ('anonymous', 'member')
       OR r.id IN (SELECT ur.role_id FROM user_roles ur WHERE ur.user_id = sqlc.arg('user_id'))
)
SELECT
    coalesce(bool_or(e.is_superuser), false)::bool AS superuser,
    coalesce(
        array_agg(p.key || ' ' || coalesce(rp.board_id::text, '') || ' ' || coalesce(rp.theme_screen, ''))
            FILTER (WHERE p.key IS NOT NULL),
        '{}'
    )::text[] AS perms
FROM effective e
LEFT JOIN role_permissions rp ON rp.role_id = e.id
LEFT JOIN permissions p       ON p.id = rp.permission_id
    AND (e.key NOT IN ('anonymous', 'member') OR p.key = ANY(sqlc.arg('visitor')::text[]));

-- name: LoadRolePermissions :one
-- 한 역할(anonymous·member)이 가진 권한 — 사용자가 아니라 역할을 묻는다.
SELECT coalesce(
    array_agg(p.key || ' ' || coalesce(rp.board_id::text, '') || ' ' || coalesce(rp.theme_screen, ''))
        FILTER (WHERE p.key IS NOT NULL),
    '{}')::text[] AS perms
FROM roles r
LEFT JOIN role_permissions rp ON rp.role_id = r.id
LEFT JOIN permissions p       ON p.id = rp.permission_id
    AND (r.key NOT IN ('anonymous', 'member') OR p.key = ANY(sqlc.arg('visitor')::text[]))
WHERE r.key = sqlc.arg('role_key');

-- name: FindActiveUserByEmail :one
SELECT id, email, display_name, is_active, sessions_valid_from,
       email_verified_at, password_hash
FROM users
WHERE email = $1 AND is_active;

-- name: FindUserByID :one
SELECT id, email, display_name, is_active, sessions_valid_from, email_verified_at
FROM users WHERE id = $1;

-- 쪽을 먼저 고르고(users_created_idx), 역할은 그 쪽의 회원에게만 모은다. 조인하고
-- GROUP BY 한 뒤 자르면 회원 전부의 역할을 모으고 나서 한 쪽만 남긴다.
-- name: ListUsers :many
SELECT u.id, u.email, u.display_name, u.is_active,
       (u.email_verified_at IS NOT NULL)::bool AS verified,
       coalesce((SELECT array_agg(r.key ORDER BY r.key)
                 FROM user_roles ur JOIN roles r ON r.id = ur.role_id
                 WHERE ur.user_id = u.id), '{}')::text[] AS roles,
       u.custom_fields
FROM (
    SELECT uu.id, uu.email, uu.display_name, uu.is_active, uu.email_verified_at, uu.custom_fields, uu.created_at
    FROM users uu
    -- A-401 검색: 이메일·표시 이름의 일부. 패턴 글자는 호출자가 이스케이프했다(\).
    WHERE sqlc.arg('pattern')::text = ''
       OR uu.email ILIKE '%' || sqlc.arg('pattern')::text || '%' ESCAPE '\'
       OR uu.display_name ILIKE '%' || sqlc.arg('pattern')::text || '%' ESCAPE '\'
    ORDER BY uu.created_at DESC, uu.id
    LIMIT sqlc.arg('limit')::int OFFSET sqlc.arg('offset')::int
) u
ORDER BY u.created_at DESC, u.id;

-- name: CreateUser :one
INSERT INTO users (email, password_hash, display_name)
VALUES ($1, $2, $3) RETURNING id;

-- name: DBNow :one
SELECT now()::timestamptz AS now;

-- 기준 시각(sessions_valid_from)은 **뒤로 가지 않는다** — 옮기는 쓰기 다섯 곳 모두
-- GREATEST(now(), 이전 값 + 1µs). now() 는 트랜잭션 시작 시각이라, 먼저 BEGIN 한 회수가 그 사이
-- 커밋된 비밀번호 변경보다 앞선 값을 써서 그 뒤에 찍힌 세션을 살려 두었다 (2026-10-21 6차 점검).
-- 잠금을 기다린 UPDATE 는 최신 행으로 다시 계산되므로 늘 엄격히 커진다.
-- name: InvalidateSessions :exec
UPDATE users SET sessions_valid_from = GREATEST(now(), sessions_valid_from + interval '1 microsecond'), updated_at = now() WHERE id = $1;

-- name: RevokeCredentials :exec
UPDATE users SET password_hash = '', sessions_valid_from = GREATEST(now(), sessions_valid_from + interval '1 microsecond'), updated_at = now() WHERE id = $1;

-- name: LockSuperuserHolders :many
SELECT u.id
FROM users u
JOIN user_roles ur ON ur.user_id = u.id
JOIN roles r       ON r.id = ur.role_id
WHERE r.is_superuser AND u.is_active
FOR UPDATE;

-- name: ActivateUser :exec
UPDATE users SET is_active = true, updated_at = now() WHERE id = $1;

-- name: DeactivateUser :exec
-- 세션 기준 시각도 옮긴다 — 그대로 두면 비활성 동안 쓰이지 않은 세션이 재활성화와 함께
-- 다시 통한다(미들웨어는 다음 요청에서야 비활성 세션을 지운다).
UPDATE users SET is_active = false, sessions_valid_from = GREATEST(now(), sessions_valid_from + interval '1 microsecond'), updated_at = now() WHERE id = $1;

-- name: DeleteUser :execrows
DELETE FROM users WHERE id = $1;

-- name: UpdateProfile :execrows
UPDATE users SET display_name = $2, custom_fields = $3, updated_at = now()
WHERE id = $1;

-- name: CustomFields :one
SELECT custom_fields FROM users WHERE id = $1;

-- name: HoldsSuperuser :one
SELECT EXISTS (
    SELECT 1 FROM user_roles ur
    JOIN roles r ON r.id = ur.role_id
    WHERE ur.user_id = $1 AND r.is_superuser
) AS holds;

-- DISTINCT: 게시판 범위 권한은 게시판마다 한 행이라, 없으면 같은 키가 게시판 수만큼
-- 나와 「N개 권한」이 부풀려진다.
-- name: Roles :many
SELECT r.key, r.name, r.is_superuser, r.is_assignable,
       coalesce(array_agg(DISTINCT p.key) FILTER (WHERE p.key IS NOT NULL), '{}')::text[] AS permissions
FROM roles r
LEFT JOIN role_permissions rp ON rp.role_id = r.id
LEFT JOIN permissions p       ON p.id = rp.permission_id
GROUP BY r.id, r.key, r.name, r.is_superuser, r.is_assignable
ORDER BY r.key;

-- name: RoleByKey :one
SELECT r.key, r.is_superuser,
       coalesce(array_agg(DISTINCT p.key) FILTER (WHERE p.key IS NOT NULL), '{}')::text[] AS permissions
FROM roles r
LEFT JOIN role_permissions rp ON rp.role_id = r.id
LEFT JOIN permissions p       ON p.id = rp.permission_id
WHERE r.key = $1
GROUP BY r.id, r.key, r.is_superuser;

-- name: PermissionIsScoped :one
SELECT is_scoped FROM permissions WHERE key = $1;

-- name: GrantPermission :exec
INSERT INTO role_permissions (role_id, permission_id, board_id, theme_screen)
SELECT r.id, p.id, NULLIF(sqlc.arg('board')::text, '')::uuid, NULLIF(sqlc.arg('screen')::text, '')
FROM roles r, permissions p
WHERE r.key = sqlc.arg('role_key') AND p.key = sqlc.arg('perm_key')
ON CONFLICT ON CONSTRAINT role_permissions_uniq DO NOTHING;

-- name: AssignRole :exec
INSERT INTO user_roles (user_id, role_id)
SELECT sqlc.arg('user_id')::uuid, r.id FROM roles r WHERE r.key = sqlc.arg('role_key') AND r.is_assignable
ON CONFLICT ON CONSTRAINT user_roles_uniq DO NOTHING;

-- name: RevokePermission :execrows
-- A-404 빼기: 그 부여 한 행만. 게시판·테마 화면 범위까지 같아야 같은 부여다 (빈 값 = 범위 없음).
DELETE FROM role_permissions rp
USING roles r, permissions p
WHERE rp.role_id = r.id AND rp.permission_id = p.id
  AND r.key = sqlc.arg('role_key') AND p.key = sqlc.arg('perm_key')
  AND rp.board_id IS NOT DISTINCT FROM NULLIF(sqlc.arg('board')::text, '')::uuid
  AND rp.theme_screen IS NOT DISTINCT FROM NULLIF(sqlc.arg('screen')::text, '');

-- name: UnassignRole :execrows
DELETE FROM user_roles ur
USING roles r
WHERE ur.role_id = r.id AND ur.user_id = sqlc.arg('user_id')::uuid AND r.key = sqlc.arg('role_key');

-- name: UserRoleKeys :many
SELECT r.key FROM user_roles ur JOIN roles r ON r.id = ur.role_id
WHERE ur.user_id = sqlc.arg('user_id')::uuid
ORDER BY r.key;

-- name: RoleGrants :many
-- A-403 이 범위까지 보여야 뺄 것을 고른다 — Roles 는 키만 모은다.
SELECT r.key AS role_key, p.key AS perm_key,
       coalesce(rp.board_id::text, '')::text AS board_id, coalesce(rp.theme_screen, '')::text AS theme_screen
FROM role_permissions rp
JOIN roles r       ON r.id = rp.role_id
JOIN permissions p ON p.id = rp.permission_id
ORDER BY r.key, p.key, board_id, theme_screen;

-- name: BoardsWithGrants :many
SELECT DISTINCT board_id::text AS board_id FROM role_permissions WHERE board_id IS NOT NULL;
