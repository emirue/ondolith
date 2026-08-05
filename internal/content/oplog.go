package content

import (
	"context"
	"net"
	"strings"
	"time"
)

// OpLog is D15 7절's audit trail.
//
// The table is append-only and the database enforces it (D30): there is no
// Update and no Delete here, and adding one would not work anyway.
type OpLog struct {
	store *Store
}

func (s *Store) OpLog() *OpLog { return &OpLog{store: s} }

// Entry is one row. Everything a caller can set is here, and the things D15
// says must never be recorded — passwords, hashes, session tokens, card
// numbers, PG secrets, raw reset tokens — have no field to go in.
//
// That is the point: a struct with no `Password` field cannot log a password by
// accident. The redaction below is the second line, for the case where somebody
// puts a secret in Summary.
type Entry struct {
	ActorID    string
	ActorEmail string
	// Action is <resource>.<verb>, the same two-segment shape as a permission
	// key (D15 2.1) — no second naming convention.
	Action     string
	TargetType string
	TargetID   string
	Summary    string
	IP         string
}

// secretish are substrings that must never appear in a summary.
//
// Korean is in the list because the summaries are written in Korean: the first
// version of this list was English-only and let "새 비밀번호: hunter2" straight
// through — the words people actually type are the words that matter, and this
// codebase's are not English.
//
// The list is short on purpose. It catches the shapes people paste, not every
// word that could be sensitive, and the real defence is that Entry has no field
// for a credential.
var secretish = []string{
	"password", "passwd", "secret", "token", "authorization",
	"card_number", "cvc", "api_key", "apikey", "private_key",
	"비밀번호", "암호", "토큰", "시크릿", "카드번호", "인증키", "비번",
}

// Redacted reports whether a summary looks like it carries a credential.
//
// It is not a filter that cleans the text — a "cleaned" secret still went
// through the logging call, and the caller learns nothing. It replaces the
// whole summary, so the entry says a value was withheld and where to look.
func Redacted(summary string) (string, bool) {
	low := strings.ToLower(summary)
	for _, s := range secretish {
		if strings.Contains(low, s) {
			return "[요약에 자격증명으로 보이는 값이 있어 기록하지 않음]", true
		}
	}
	return summary, false
}

// Record appends one entry.
//
// It never returns an error to the caller's control flow by design at the call
// site — see Deps.Log in the admin package — but it does return one here so the
// caller can log the failure. An audit trail that silently stops recording is
// worse than one that is missing: the gap looks like "nothing happened".
func (l *OpLog) Record(ctx context.Context, e Entry) error {
	summary, _ := Redacted(e.Summary)
	const q = `
		INSERT INTO operation_logs
		    (actor_user_id, actor_email, action, target_type, target_id, summary, ip)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`
	_, err := l.store.pool.Exec(ctx, q,
		nullIfEmpty(e.ActorID), e.ActorEmail, e.Action, e.TargetType,
		nullIfEmpty(e.TargetID), summary, nullIfEmpty(normaliseIP(e.IP)))
	return err
}

// normaliseIP drops anything that is not an address. The column is `inet`, so a
// bad value would fail the insert and take the operation's audit entry with it.
func normaliseIP(s string) string {
	if s == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(s); err == nil {
		s = h
	}
	if net.ParseIP(s) == nil {
		return ""
	}
	return s
}

// LogEntry is one row as A-601 reads it.
type LogEntry struct {
	ID         string
	ActorEmail string
	Action     string
	TargetType string
	TargetID   string
	Summary    string
	IP         string
	CreatedAt  time.Time
}

// Recent reads the log newest first (A-601). There is no filter by actor yet:
// the screen that needs it can add one, and an unused parameter is a shape
// nobody checked.
func (l *OpLog) Recent(ctx context.Context, limit, offset int) ([]LogEntry, error) {
	const q = `
		SELECT id, actor_email, action, target_type, coalesce(target_id, ''),
		       summary, coalesce(host(ip), ''), created_at
		FROM operation_logs ORDER BY created_at DESC, id DESC LIMIT $1 OFFSET $2`
	rows, err := l.store.pool.Query(ctx, q, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogEntry
	for rows.Next() {
		var e LogEntry
		if err := rows.Scan(&e.ID, &e.ActorEmail, &e.Action, &e.TargetType,
			&e.TargetID, &e.Summary, &e.IP, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (l *OpLog) Count(ctx context.Context) (int64, error) {
	var n int64
	err := l.store.pool.QueryRow(ctx, `SELECT count(*) FROM operation_logs`).Scan(&n)
	return n, err
}
