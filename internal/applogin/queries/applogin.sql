-- name: PutCode :exec
-- 번호마다 마지막 하나 — 덮어쓰고 틀린 횟수를 0 으로. 5분은 D42 8절이다.
INSERT INTO app_login_codes (phone, code_hash, expires_at, attempts)
VALUES ($1, $2, now() + interval '5 minutes', 0)
ON CONFLICT (phone) DO UPDATE SET code_hash = EXCLUDED.code_hash, expires_at = EXCLUDED.expires_at, attempts = 0;

-- name: DeleteExpiredCodes :exec
DELETE FROM app_login_codes WHERE expires_at <= now();

-- name: SpendAttempt :one
-- **한 문장으로 센다.** 읽고 나서 올리면 동시에 보낸 추측이 다섯 번을 넘는다. 남은 횟수가 없거나 지났으면 행이 없다.
UPDATE app_login_codes SET attempts = attempts + 1
WHERE phone = $1 AND expires_at > now() AND attempts < sqlc.arg(max_attempts)::integer
RETURNING code_hash;

-- name: DeleteCode :exec
DELETE FROM app_login_codes WHERE phone = $1;

-- name: InsertSession :one
-- 30일은 D42 8절이다.
INSERT INTO app_sessions (token_hash, subject, expires_at)
VALUES ($1, $2, now() + interval '30 days')
RETURNING expires_at;

-- name: DeleteExpiredSessions :exec
DELETE FROM app_sessions WHERE expires_at <= now();

-- name: LookupSession :one
SELECT subject, last_used_at FROM app_sessions WHERE token_hash = $1 AND expires_at > now();

-- name: TouchSession :exec
UPDATE app_sessions SET last_used_at = now() WHERE token_hash = $1;

-- name: DeleteSession :exec
DELETE FROM app_sessions WHERE token_hash = $1;

-- name: DeleteSubjectSessions :execrows
DELETE FROM app_sessions WHERE subject = $1;
