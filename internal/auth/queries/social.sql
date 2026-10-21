-- name: SocialAccounts :many
SELECT provider, provider_uid FROM social_accounts
WHERE user_id = $1 ORDER BY provider;

-- name: UserIDBySocial :one
SELECT user_id FROM social_accounts WHERE provider = $1 AND provider_uid = $2;

-- name: LinkSocial :exec
INSERT INTO social_accounts (user_id, provider, provider_uid) VALUES ($1, $2, $3);

-- name: LockSessionValid :one
-- 사용자 행을 잠그고, auth_at 에 로그인한 세션이 아직 유효한지 답한다 (A-402 회수와 직렬화).
SELECT is_active AND sessions_valid_from <= sqlc.arg('auth_at') AS valid
FROM users WHERE id = sqlc.arg('id') FOR UPDATE;

-- name: LockUserHasPassword :one
SELECT password_hash <> '' AS has_password FROM users WHERE id = $1 FOR UPDATE;

-- name: CountSocialAccounts :one
SELECT count(*) FROM social_accounts WHERE user_id = $1;

-- name: UnlinkSocial :execrows
DELETE FROM social_accounts WHERE user_id = $1 AND provider = $2;

-- name: HasPassword :one
SELECT password_hash <> '' AS has_password FROM users WHERE id = $1;

-- name: UnlinkAllSocial :execrows
DELETE FROM social_accounts WHERE user_id = $1;
