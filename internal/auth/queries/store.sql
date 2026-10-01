-- name: LoadPermissions :one
WITH effective AS (
    SELECT r.id, r.is_superuser
    FROM roles r
    WHERE r.key IN ('anonymous', 'member')
       OR r.id IN (SELECT ur.role_id FROM user_roles ur WHERE ur.user_id = $1)
)
SELECT
    coalesce(bool_or(e.is_superuser), false)::bool AS superuser,
    coalesce(
        array_agg(p.key || ' ' || coalesce(rp.board_id::text, ''))
            FILTER (WHERE p.key IS NOT NULL),
        '{}'
    )::text[] AS perms
FROM effective e
LEFT JOIN role_permissions rp ON rp.role_id = e.id
LEFT JOIN permissions p       ON p.id = rp.permission_id;

-- name: LoadAnonymousPermissions :one
SELECT coalesce(
    array_agg(p.key || ' ' || coalesce(rp.board_id::text, ''))
        FILTER (WHERE p.key IS NOT NULL),
    '{}')::text[] AS perms
FROM roles r
LEFT JOIN role_permissions rp ON rp.role_id = r.id
LEFT JOIN permissions p       ON p.id = rp.permission_id
WHERE r.key = 'anonymous';

-- name: FindActiveUserByEmail :one
SELECT id, email, display_name, is_active, sessions_valid_from,
       email_verified_at, password_hash
FROM users
WHERE email = $1 AND is_active;

-- name: FindUserByID :one
SELECT id, email, display_name, is_active, sessions_valid_from, email_verified_at
FROM users WHERE id = $1;

-- name: ListUsers :many
SELECT u.id, u.email, u.display_name, u.is_active,
       (u.email_verified_at IS NOT NULL)::bool AS verified,
       coalesce(array_agg(r.key ORDER BY r.key) FILTER (WHERE r.key IS NOT NULL), '{}')::text[] AS roles,
       u.custom_fields
FROM users u
LEFT JOIN user_roles ur ON ur.user_id = u.id
LEFT JOIN roles r ON r.id = ur.role_id
GROUP BY u.id
ORDER BY u.created_at DESC, u.id
LIMIT sqlc.arg('limit')::int OFFSET sqlc.arg('offset')::int;

-- name: CreateUser :one
INSERT INTO users (email, password_hash, display_name)
VALUES ($1, $2, $3) RETURNING id;

-- name: DBNow :one
SELECT now()::timestamptz AS now;

-- name: InvalidateSessions :exec
UPDATE users SET sessions_valid_from = now(), updated_at = now() WHERE id = $1;

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
UPDATE users SET is_active = false, updated_at = now() WHERE id = $1;

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
SELECT r.key, r.name, r.is_superuser,
       coalesce(array_agg(DISTINCT p.key) FILTER (WHERE p.key IS NOT NULL), '{}')::text[] AS permissions
FROM roles r
LEFT JOIN role_permissions rp ON rp.role_id = r.id
LEFT JOIN permissions p       ON p.id = rp.permission_id
GROUP BY r.id, r.key, r.name, r.is_superuser
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
INSERT INTO role_permissions (role_id, permission_id, board_id)
SELECT r.id, p.id, NULLIF(sqlc.arg('board')::text, '')::uuid FROM roles r, permissions p
WHERE r.key = sqlc.arg('role_key') AND p.key = sqlc.arg('perm_key')
ON CONFLICT ON CONSTRAINT role_permissions_uniq DO NOTHING;

-- name: AssignRole :exec
INSERT INTO user_roles (user_id, role_id)
SELECT sqlc.arg('user_id')::uuid, r.id FROM roles r WHERE r.key = sqlc.arg('role_key') AND r.is_assignable
ON CONFLICT ON CONSTRAINT user_roles_uniq DO NOTHING;

-- name: BoardsWithGrants :many
SELECT DISTINCT board_id::text AS board_id FROM role_permissions WHERE board_id IS NOT NULL;
