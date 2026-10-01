-- name: InvalidateResetTokens :exec
UPDATE password_reset_tokens SET used_at = now(), updated_at = now()
WHERE user_id = $1 AND used_at IS NULL;

-- name: ResetActiveUserPassword :one
UPDATE users SET password_hash = $2, sessions_valid_from = now(), updated_at = now()
WHERE id = $1 AND is_active RETURNING sessions_valid_from;

-- name: VerifyTokenSpent :one
SELECT (used_at IS NOT NULL)::bool AS spent FROM email_verification_tokens
WHERE token_hash = $1 AND expires_at > now();
