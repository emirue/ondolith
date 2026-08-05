package auth

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// IssueResetToken invalidates the user's outstanding reset links and issues one.
//
// D19 P-104 asks for the invalidation: a person who clicks "재설정" three times
// because the mail is slow otherwise leaves three live links behind, and each
// one is an account takeover for as long as it lives. One link at a time means
// the window is 30 minutes total rather than 30 minutes per attempt.
//
// Both statements run in one transaction. Invalidating and then failing to
// insert would leave the account with no way in until the rate limit clears.
func (s *Store) IssueResetToken(ctx context.Context, userID string) (string, error) {
	raw, err := newRawToken()
	if err != nil {
		return "", err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`UPDATE password_reset_tokens SET used_at = now(), updated_at = now()
		 WHERE user_id = $1 AND used_at IS NULL`, userID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO password_reset_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, hashToken(raw), time.Now().Add(PasswordResetTTL)); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return raw, nil
}

// ResetPassword burns the token and sets the password in ONE transaction.
//
// D19 P-105 requires the single transaction, and the failure it prevents is
// specific: consume-then-update with a crash in between spends the only link
// the user has without changing anything, and the account is stuck until the
// person asks for another mail. Rolling back returns the link.
//
// An inactive account gets ErrTokenInvalid — the same answer as a wrong token,
// because saying "this account is disabled" turns a reset form into a status
// oracle. The token survives, since nothing committed.
func (s *Store) ResetPassword(ctx context.Context, raw, hash string) (time.Time, error) {
	var zero time.Time

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)

	// Same single-statement burn as ConsumeToken: two simultaneous clicks both
	// reach the database and exactly one updates a row, so two people cannot
	// set the password from one link.
	var userID string
	err = tx.QueryRow(ctx, `
		UPDATE password_reset_tokens SET used_at = now(), updated_at = now()
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
		RETURNING user_id`, hashToken(raw)).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, ErrTokenInvalid
	}
	if err != nil {
		return zero, err
	}

	// sessions_valid_from moves with the password (D15 5.4): a reset is what
	// someone does when they think the account is taken, so every session that
	// existed before this moment has to stop working.
	var cutoff time.Time
	err = tx.QueryRow(ctx, `
		UPDATE users SET password_hash = $2, sessions_valid_from = now(), updated_at = now()
		WHERE id = $1 AND is_active RETURNING sessions_valid_from`, userID, hash).Scan(&cutoff)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, ErrTokenInvalid
	}
	if err != nil {
		return zero, err
	}

	if err := tx.Commit(ctx); err != nil {
		return zero, err
	}
	return cutoff, nil
}

// ConsumeVerifyToken burns an email-verification token and reports whether it
// had already been spent.
//
// P-112 is the one screen that distinguishes "already used" from "wrong", and
// D19 says so explicitly: mail clients prefetch links, so the token is often
// spent before the human clicks it, and answering 400 to that click makes a
// verification that worked look broken. The leak it accepts is that someone
// holding a token can tell it was real — which they already know, because they
// are holding it.
func (s *Store) ConsumeVerifyToken(ctx context.Context, raw string) (userID string, already bool, err error) {
	err = s.pool.QueryRow(ctx, `
		UPDATE email_verification_tokens SET used_at = now(), updated_at = now()
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
		RETURNING user_id`, hashToken(raw)).Scan(&userID)
	if err == nil {
		return userID, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}

	// The miss path, and only the miss path, asks the second question. An
	// expired token is NOT "already": the link is dead either way and D19 puts
	// expiry in the 400 row.
	var spent bool
	err = s.pool.QueryRow(ctx, `
		SELECT used_at IS NOT NULL FROM email_verification_tokens
		WHERE token_hash = $1 AND expires_at > now()`, hashToken(raw)).Scan(&spent)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, ErrTokenInvalid
	}
	if err != nil {
		return "", false, err
	}
	if !spent {
		return "", false, ErrTokenInvalid
	}
	return "", true, nil
}
