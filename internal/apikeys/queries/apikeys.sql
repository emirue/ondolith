-- name: CreateAPIKey :one
INSERT INTO api_keys (name, key_hash, created_by)
VALUES (sqlc.arg('name'), sqlc.arg('key_hash'), sqlc.narg('created_by'))
RETURNING id, created_at;

-- name: ActiveAPIKeyByHash :one
-- 폐기된 키는 없는 키와 같다 — 401 이 둘을 구분하지 않는다.
SELECT id, name FROM api_keys WHERE key_hash = sqlc.arg('key_hash') AND revoked_at IS NULL;

-- name: TouchAPIKey :exec
-- 쓸 때마다 적으면 앱 요청 하나가 쓰기 하나다. 1분에 한 번만 고친다 — 화면은 분 단위로 보인다.
UPDATE api_keys SET last_used_at = now()
WHERE id = sqlc.arg('id') AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');

-- name: ListAPIKeys :many
SELECT id, name, created_at, last_used_at, revoked_at FROM api_keys ORDER BY created_at DESC, id;

-- name: RevokeAPIKey :one
UPDATE api_keys SET revoked_at = now() WHERE id = sqlc.arg('id') AND revoked_at IS NULL
RETURNING name;
