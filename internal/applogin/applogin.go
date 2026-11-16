// Package applogin holds the site app's phone login: one-time codes and app tokens
// ([D42](../../docs/42-site-extension.md) 8절).
//
// 인증번호와 토큰 모두 원문을 저장하지 않는다 — 표에는 SHA-256 만 있다. 시한(5분·30일)은 DB 의 now() 로 잰다:
// 서버 여럿이나 시계가 어긋난 서버가 한 표를 봐도 판정이 같다.
package applogin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emirue/ondolith/internal/applogin/apploginq"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxAttempts is how many guesses one code allows (D42 8절).
const MaxAttempts = 5

var mobile = regexp.MustCompile(`^01[0-9]{8,9}$`)

// NormalizePhone keeps the digits of a Korean mobile number: "+82 10-1234-5678" → "01012345678".
// 모양이 하나여야 상한도 인증번호도 한 사람에게 하나다. 휴대폰 번호가 아니면 ok 는 false 다.
func NormalizePhone(s string) (string, bool) {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '+' || r == '(' || r == ')' || r == '.':
		default:
			return "", false
		}
	}
	d := b.String()
	if strings.HasPrefix(d, "82") {
		d = "0" + strings.TrimPrefix(strings.TrimPrefix(d, "82"), "0")
	}
	return d, mobile.MatchString(d)
}

// Store reads and writes app_login_codes and app_sessions.
type Store struct{ q *apploginq.Queries }

func New(pool *pgxpool.Pool) *Store { return &Store{q: apploginq.New(pool)} }

func sum(parts ...string) []byte {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return h[:]
}

// IssueCode makes a 6-digit code for phone, replacing any earlier one, and returns it to be sent.
// phone 은 NormalizePhone 을 거친 값이다. 지난 인증번호는 여기서 치운다 — 따로 도는 작업을 두지 않는다.
func (s *Store) IssueCode(ctx context.Context, phone string) (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	code := fmt.Sprintf("%06d", n.Int64())
	if err := s.q.DeleteExpiredCodes(ctx); err != nil {
		return "", err
	}
	if err := s.q.PutCode(ctx, apploginq.PutCodeParams{Phone: phone, CodeHash: sum(phone, code)}); err != nil {
		return "", err
	}
	return code, nil
}

// VerifyCode spends one guess on phone's code. 맞으면 그 번호를 지운다(한 번만 쓴다 — 지운 쪽만 참이다). 지났거나, 다섯 번을 다
// 썼거나, 요청한 적이 없으면 false 다 — 이유를 나누지 않는다.
func (s *Store) VerifyCode(ctx context.Context, phone, code string) (bool, error) {
	want, err := s.q.SpendAttempt(ctx, apploginq.SpendAttemptParams{Phone: phone, MaxAttempts: MaxAttempts})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if subtle.ConstantTimeCompare(want, sum(phone, code)) != 1 {
		return false, nil
	}
	n, err := s.q.DeleteCode(ctx, apploginq.DeleteCodeParams{Phone: phone, CodeHash: want})
	return n == 1, err
}

// StartSession issues an app token for subject (사이트가 정한 대상). 원문은 이 반환값뿐이다.
func (s *Store) StartSession(ctx context.Context, subject string) (token string, expires time.Time, err error) {
	if n := utf8.RuneCountInString(subject); n < 1 || n > 200 {
		return "", time.Time{}, errors.New("applogin: 대상은 1~200자다")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	if err := s.q.DeleteExpiredSessions(ctx); err != nil {
		return "", time.Time{}, err
	}
	expires, err = s.q.InsertSession(ctx, apploginq.InsertSessionParams{TokenHash: sum(token), Subject: subject})
	return token, expires, err
}

// LookupSession finds the subject of a live token. 마지막 사용 시각은 1분에 한 번만 고친다 — 요청마다 쓰면
// 화면 하나가 쓰기 하나를 더 한다.
func (s *Store) LookupSession(ctx context.Context, token string) (string, bool, error) {
	if token == "" {
		return "", false, nil
	}
	row, err := s.q.LookupSession(ctx, sum(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if row.LastUsedAt == nil || time.Since(*row.LastUsedAt) > time.Minute {
		if err := s.q.TouchSession(ctx, sum(token)); err != nil {
			return "", false, err
		}
	}
	return row.Subject, true, nil
}

// EndSession ends one token — 앱의 로그아웃.
func (s *Store) EndSession(ctx context.Context, token string) error {
	return s.q.DeleteSession(ctx, sum(token))
}

// EndSubject ends every token of subject — 그만둔 사람 (site.EndSessions). 끊은 개수를 돌려준다.
func (s *Store) EndSubject(ctx context.Context, subject string) (int64, error) {
	return s.q.DeleteSubjectSessions(ctx, subject)
}
