-- 토큰 테이블은 둘(D30)이고 sqlc 질의는 테이블명을 바인딩할 수 없으므로 같은
-- 문장을 테이블마다 한 벌씩 둔다. 어느 벌을 쓸지는 token.go 가 TokenKind 로 고른다.

-- name: BurnUnusedResetTokens :exec
UPDATE password_reset_tokens SET used_at = now()
WHERE user_id = $1 AND used_at IS NULL;

-- name: BurnUnusedVerifyTokens :exec
UPDATE email_verification_tokens SET used_at = now()
WHERE user_id = $1 AND used_at IS NULL;

-- name: InsertResetToken :exec
INSERT INTO password_reset_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, $3);

-- name: InsertVerifyToken :exec
INSERT INTO email_verification_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, $3);

-- name: ConsumeResetToken :one
UPDATE password_reset_tokens SET used_at = now(), updated_at = now()
WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
RETURNING user_id;

-- name: ConsumeVerifyToken :one
UPDATE email_verification_tokens SET used_at = now(), updated_at = now()
WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
RETURNING user_id;

-- name: MarkEmailVerified :exec
UPDATE users SET email_verified_at = now(), updated_at = now()
WHERE id = $1 AND email_verified_at IS NULL;

-- name: SetPassword :one
UPDATE users SET password_hash = $2, sessions_valid_from = now(), updated_at = now()
WHERE id = $1 RETURNING sessions_valid_from;
