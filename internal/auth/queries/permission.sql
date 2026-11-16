-- name: PermissionKeys :many
SELECT key FROM permissions ORDER BY key;

-- name: PermissionNames :many
-- 역할 편집기(A-403)가 키 옆에 설명을 보인다 — 사이트 권한(D42 7절)은 키만으로는 무슨 화면인지 모른다.
SELECT key, description FROM permissions ORDER BY key;

-- name: EnsurePermission :exec
-- 사이트 권한을 넣는다 (D42 7절). 이름은 코드가 정하므로 바뀌었으면 덮어쓴다. 코드에서 빠진 권한은 지우지 않는다 —
-- 그 권한을 준 역할이 남아 있을 수 있고(role_permissions 는 RESTRICT), 부팅이 「쓰지 않는 권한」으로 알린다.
INSERT INTO permissions (key, description) VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE SET description = EXCLUDED.description, updated_at = now()
WHERE permissions.description IS DISTINCT FROM EXCLUDED.description;
