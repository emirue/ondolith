package content

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/emirue/ondolith/internal/content/contentq"
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
	return l.store.q.RecordOpLog(ctx, contentq.RecordOpLogParams{
		ActorUserID: strPtr(e.ActorID), ActorEmail: e.ActorEmail,
		Action: e.Action, TargetType: e.TargetType,
		TargetID: strPtr(e.TargetID), Summary: summary, Ip: ipOrNil(e.IP),
	})
}

// strPtr is the nullable-text parameter shape sqlc generates: nil for "", so
// the column stays NULL rather than holding an empty string.
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ipOrNil keeps the inet column NULL for anything that is not an address —
// normaliseIP 가 비주소를 떨어뜨리는 것과 같은 이유다.
func ipOrNil(s string) *netip.Addr {
	a, err := netip.ParseAddr(normaliseIP(s))
	if err != nil {
		return nil
	}
	return &a
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
	rows, err := l.store.q.RecentOpLog(ctx, contentq.RecentOpLogParams{
		Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return nil, err
	}
	var out []LogEntry
	for _, r := range rows {
		out = append(out, LogEntry{ID: r.ID, ActorEmail: r.ActorEmail, Action: r.Action,
			TargetType: r.TargetType, TargetID: r.TargetID, Summary: r.Summary,
			IP: r.Ip, CreatedAt: r.CreatedAt})
	}
	return out, nil
}

func (l *OpLog) Count(ctx context.Context) (int64, error) {
	return l.store.q.CountOpLog(ctx)
}
