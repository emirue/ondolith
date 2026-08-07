package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrTokenInvalid = errors.New("auth: 링크가 올바르지 않거나 만료되었습니다")
	ErrTokenUsed    = errors.New("auth: 이미 사용된 링크입니다")
)

// Token lifetimes (D30). They differ, which is why the two token tables are not
// merged behind a `kind` column — merging pushes the constant into row data and
// produces a branch on every read path.
const (
	PasswordResetTTL = 30 * time.Minute
	EmailVerifyTTL   = 24 * time.Hour
)

// TokenKind selects the table.
type TokenKind int

const (
	KindPasswordReset TokenKind = iota
	KindEmailVerify
)

func (k TokenKind) table() string {
	if k == KindEmailVerify {
		return "email_verification_tokens"
	}
	return "password_reset_tokens"
}

func (k TokenKind) ttl() time.Duration {
	if k == KindEmailVerify {
		return EmailVerifyTTL
	}
	return PasswordResetTTL
}

// hashToken is SHA-256, NOT bcrypt.
//
// "Store a hash" reads like bcrypt, and that is the trap: bcrypt salts every
// row, so the hash cannot be looked up — verification would scan every token
// and run a bcrypt compare against each. A 256-bit random value is not
// dictionary attackable, so an unsalted SHA-256 is correct, and it is what
// makes UNIQUE (token_hash) resolve the lookup in one index probe (D30).
func hashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// newRawToken is 256 bits of crypto/rand in RawURLEncoding — no padding, and
// the alphabet is already path-safe, which is what lets the value sit in
// `/verify/{token}` and `/password/reset/{token}` without escaping (D11).
func newRawToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// IssueToken creates a single-use token and returns the RAW value.
//
// The raw value exists only in this return and in the email. The database
// stores the hash, so a database dump cannot be replayed into account
// takeovers, and the raw value must never be logged (C5).
func (s *Store) IssueToken(ctx context.Context, kind TokenKind, userID string) (string, error) {
	raw, err := newRawToken()
	if err != nil {
		return "", err
	}

	// 같은 사용자의 **미사용 토큰을 먼저 태운다** (D19 P-104·P-113 「기존 토큰」).
	// 유효한 링크가 여러 개 살아 있을 이유가 없고, 살아 있으면 재발송이 공격
	// 표면을 넓히는 동작이 된다 — 메일함 하나가 새면 그 안의 오래된 링크도
	// 전부 쓸 수 있다. 재발송 화면(P-113)이 있는 이상 이것은 선택이 아니다.
	//
	// 발급과 같은 트랜잭션이어야 한다. 지우고 나서 넣기 사이에 실패하면
	// 사용자는 링크를 하나도 갖지 못한 채 "보냈습니다" 를 보게 된다.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // 커밋됐으면 무의미하다
	if _, err := tx.Exec(ctx,
		`UPDATE `+kind.table()+` SET used_at = now()
		 WHERE user_id = $1 AND used_at IS NULL`, userID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO `+kind.table()+` (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, hashToken(raw), time.Now().Add(kind.ttl())); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return raw, nil
}

// ConsumeToken verifies and burns a token in ONE statement.
//
// The UPDATE ... WHERE used_at IS NULL ... RETURNING form is what makes it
// single-use: two simultaneous clicks on the same link both reach the database,
// and exactly one of them updates a row. Checking first and updating after
// would let both through, which for a password-reset link means two people can
// set the password.
func (s *Store) ConsumeToken(ctx context.Context, kind TokenKind, raw string) (string, error) {
	const q = `
		UPDATE %s SET used_at = now(), updated_at = now()
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
		RETURNING user_id`
	var userID string
	err := s.pool.QueryRow(ctx, sprintfTable(q, kind.table()), hashToken(raw)).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Expired, already used, or never existed — one answer for all three.
		// Distinguishing them tells a guesser which of their attempts was
		// close.
		return "", ErrTokenInvalid
	}
	if err != nil {
		return "", err
	}
	return userID, nil
}

// sprintfTable substitutes a table name that is NOT caller input: it comes from
// TokenKind, a closed set of two. Table names cannot be bind parameters, so the
// safety here is that no request value ever reaches this function.
func sprintfTable(q, table string) string {
	out := make([]byte, 0, len(q)+len(table))
	for i := 0; i < len(q); i++ {
		if q[i] == '%' && i+1 < len(q) && q[i+1] == 's' {
			out = append(out, table...)
			i++
			continue
		}
		out = append(out, q[i])
	}
	return string(out)
}

// MarkEmailVerified records that the account passed verification (FR-214).
func (s *Store) MarkEmailVerified(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE users SET email_verified_at = now(), updated_at = now()
		 WHERE id = $1 AND email_verified_at IS NULL`, userID)
	return err
}

// SetPassword replaces the hash, ends every other session, and returns the new
// cutoff.
//
// The two go together on purpose: a password change that leaves other sessions
// alive does not lock out whoever the password was being changed because of
// (D15 5.4).
//
// The cutoff is RETURNED because the caller has to stamp the surviving session
// with it. Stamping with the application's own clock instead compares two
// clocks — the database wrote `now()`, the process reads `time.Now()` — and a
// few milliseconds of skew logs the user out of the session they just proved
// they own. One clock, handed back, removes the question. It comes back as a
// [DBTime] so the caller cannot substitute its own clock and still compile.
func (s *Store) SetPassword(ctx context.Context, userID, hash string) (DBTime, error) {
	var cutoff time.Time
	err := s.pool.QueryRow(ctx,
		`UPDATE users SET password_hash = $2, sessions_valid_from = now(), updated_at = now()
		 WHERE id = $1 RETURNING sessions_valid_from`, userID, hash).Scan(&cutoff)
	return DBTime{cutoff}, err
}
