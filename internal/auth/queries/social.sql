-- name: SocialAccounts :many
SELECT provider, provider_uid FROM social_accounts
WHERE user_id = $1 ORDER BY provider;

-- name: UserBySocialLink :one
-- 연결과 사용자(기준 시각 포함)를 한 문장으로 읽는다 — 둘 사이에 A-402 회수가 커밋되면 연결은
-- 옛 것, 기준 시각은 새 것을 읽어 회수 뒤에도 사는 세션이 나온다 (User.Cutoff).
SELECT u.id, u.email, u.display_name, u.is_active, u.sessions_valid_from, u.email_verified_at
FROM social_accounts s JOIN users u ON u.id = s.user_id
WHERE s.provider = $1 AND s.provider_uid = $2;

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
