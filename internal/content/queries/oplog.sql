-- name: RecordOpLog :exec
INSERT INTO operation_logs
    (actor_user_id, actor_email, action, target_type, target_id, summary, ip)
VALUES (sqlc.narg('actor_user_id'), $1, $2, $3, sqlc.narg('target_id'), $4, sqlc.narg('ip')::inet);

-- name: RecentOpLog :many
SELECT id, actor_email, action, target_type, coalesce(target_id, '') AS target_id,
       summary, coalesce(host(ip), '')::text AS ip, created_at
FROM operation_logs
-- 기간 조회 (D19 0.6): 기록 시각 기준, `since ≤ 시각 < until`. NULL 이면 열린 끝이다.
WHERE (sqlc.narg('since')::timestamptz IS NULL OR created_at >= sqlc.narg('since'))
  AND (sqlc.narg('until')::timestamptz IS NULL OR created_at < sqlc.narg('until'))
ORDER BY created_at DESC, id DESC LIMIT sqlc.arg('limit')::int OFFSET sqlc.arg('offset')::int;

-- name: CountOpLog :one
SELECT count(*) FROM operation_logs
WHERE (sqlc.narg('since')::timestamptz IS NULL OR created_at >= sqlc.narg('since'))
  AND (sqlc.narg('until')::timestamptz IS NULL OR created_at < sqlc.narg('until'));
