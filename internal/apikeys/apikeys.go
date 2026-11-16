// Package apikeys holds the app keys a site's mobile app sends to /api/site/ ([D42](../../docs/42-site-extension.md) 6절).
//
// 키는 비밀이 아니다 — 앱을 풀면 꺼낼 수 있다(D42 지배 원칙 5). 「어느 앱인지」와 요청 상한에 쓰고, 폐기할 수 있으면
// 된다. 그래도 원문은 저장하지 않는다: 발급 화면에서 한 번 보이고, 표에는 SHA-256 만 있다.
package apikeys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emirue/ondolith/internal/apikeys/apikeysq"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Prefix starts every key, so secret scanners and people can tell an ondolith key apart.
const Prefix = "ondo_"

var (
	// ErrNotFound is a key that does not exist or is already revoked.
	ErrNotFound = errors.New("apikeys: 없는 키")
	// ErrName is a name outside 1~60 characters.
	ErrName = errors.New("apikeys: 이름은 1~60자다")
)

// Key is one issued key, without its raw value — that is never stored.
type Key struct {
	ID         string
	Name       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// Store reads and writes api_keys.
type Store struct{ q *apikeysq.Queries }

func New(pool *pgxpool.Pool) *Store { return &Store{q: apikeysq.New(pool)} }

func hash(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// Issue makes a key and returns its raw value — the only time it exists outside the app that
// receives it. createdBy is the administrator's user id, nil for none.
func (s *Store) Issue(ctx context.Context, name string, createdBy *string) (raw string, k Key, err error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 60 {
		return "", Key{}, ErrName
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", Key{}, err
	}
	raw = Prefix + base64.RawURLEncoding.EncodeToString(b)
	row, err := s.q.CreateAPIKey(ctx, apikeysq.CreateAPIKeyParams{Name: name, KeyHash: hash(raw), CreatedBy: createdBy})
	if err != nil {
		return "", Key{}, err
	}
	return raw, Key{ID: row.ID, Name: name, CreatedAt: row.CreatedAt}, nil
}

// Lookup finds the active key for a raw value and notes that it was used. A missing or revoked
// key is ok=false with no error — /api/site/ answers both with the same 401.
func (s *Store) Lookup(ctx context.Context, raw string) (Key, bool, error) {
	if !strings.HasPrefix(raw, Prefix) {
		return Key{}, false, nil
	}
	row, err := s.q.ActiveAPIKeyByHash(ctx, hash(raw))
	if errors.Is(err, pgx.ErrNoRows) {
		return Key{}, false, nil
	}
	if err != nil {
		return Key{}, false, err
	}
	if err := s.q.TouchAPIKey(ctx, row.ID); err != nil {
		return Key{}, false, err
	}
	return Key{ID: row.ID, Name: row.Name}, true, nil
}

// List is every key, newest first, revoked ones included — A-213 shows when each was revoked.
func (s *Store) List(ctx context.Context) ([]Key, error) {
	rows, err := s.q.ListAPIKeys(ctx)
	if err != nil {
		return nil, err
	}
	keys := make([]Key, 0, len(rows))
	for _, r := range rows {
		keys = append(keys, Key{ID: r.ID, Name: r.Name, CreatedAt: r.CreatedAt, LastUsedAt: r.LastUsedAt, RevokedAt: r.RevokedAt})
	}
	return keys, nil
}

var uuidShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Revoke ends a key and returns its name for the operation log. 없는 키·이미 폐기한 키·모양이 다른 id 는
// ErrNotFound 다 — 모양이 다른 id 를 DB 에 보내면 형 변환 오류(22P02)가 500 이 된다.
func (s *Store) Revoke(ctx context.Context, id string) (string, error) {
	if !uuidShape.MatchString(id) {
		return "", ErrNotFound
	}
	name, err := s.q.RevokeAPIKey(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}
