package auth

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNoUser        = errors.New("auth: 사용자가 없습니다")
	ErrEmailTaken    = errors.New("auth: 이미 사용 중인 이메일입니다")
	ErrLastSuperuser = errors.New("auth: 마지막 관리자는 비활성·삭제할 수 없습니다")
)

// Store is the database side of authentication. The judgement lives in
// permission.go and escalation.go; this file only fetches and writes.
type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// User is what a request needs to know about its caller.
type User struct {
	ID                string
	Email             string
	DisplayName       string
	IsActive          bool
	SessionsValidFrom time.Time
	EmailVerifiedAt   *time.Time
}

// LoadPermissions returns the caller's whole permission set in ONE query.
//
// One query is the reason D15 4.3 can say "judging every menu entry costs no
// extra query", which is in turn why a private board can be hidden from the
// menu at all. Two queries here would make that claim false and the feature
// would quietly become expensive per item.
//
// Nothing is cached beyond the request: a revoked role must bite on the next
// request, not after the session expires (D15 4.3-1).
func (s *Store) LoadPermissions(ctx context.Context, userID string) (*Permissions, error) {
	const q = `
		WITH effective AS (
		    SELECT r.id, r.is_superuser
		    FROM roles r
		    WHERE r.key IN ('anonymous', 'member')
		       OR r.id IN (SELECT ur.role_id FROM user_roles ur WHERE ur.user_id = $1)
		)
		SELECT
		    bool_or(e.is_superuser) AS superuser,
		    coalesce(
		        array_agg(p.key) FILTER (WHERE p.key IS NOT NULL),
		        '{}'
		    ) AS perms
		FROM effective e
		LEFT JOIN role_permissions rp ON rp.role_id = e.id
		LEFT JOIN permissions p       ON p.id = rp.permission_id`

	var superuser *bool
	var keys []string
	if err := s.pool.QueryRow(ctx, q, userID).Scan(&superuser, &keys); err != nil {
		return nil, err
	}
	grants := make([]Grant, 0, len(keys))
	for _, k := range keys {
		// board_id is Phase 2; until then every grant is global. Scoped grants
		// arrive with the boards table, and this is the one place that changes.
		grants = append(grants, Grant{Permission: k, Board: Global})
	}
	return NewPermissions(superuser != nil && *superuser, grants), nil
}

// LoadAnonymousPermissions is the same for a request with no user. It exists so
// that the anonymous path is a query, not an empty set assumed in a handler —
// an installation may grant permissions to `anonymous` (D15 2.5).
func (s *Store) LoadAnonymousPermissions(ctx context.Context) (*Permissions, error) {
	const q = `
		SELECT coalesce(array_agg(p.key) FILTER (WHERE p.key IS NOT NULL), '{}')
		FROM roles r
		LEFT JOIN role_permissions rp ON rp.role_id = r.id
		LEFT JOIN permissions p       ON p.id = rp.permission_id
		WHERE r.key = 'anonymous'`
	var keys []string
	if err := s.pool.QueryRow(ctx, q).Scan(&keys); err != nil {
		return nil, err
	}
	grants := make([]Grant, 0, len(keys))
	for _, k := range keys {
		grants = append(grants, Grant{Permission: k, Board: Global})
	}
	return NewPermissions(false, grants), nil
}

// FindActiveUserByEmail is the login lookup. Inactive accounts are filtered in
// the WHERE clause rather than after the fetch: a caller who forgets the Go-side
// check would otherwise log in a deactivated account, and there is no way to
// forget a predicate that is not there to forget.
func (s *Store) FindActiveUserByEmail(ctx context.Context, email string) (*User, string, error) {
	const q = `
		SELECT id, email, display_name, is_active, sessions_valid_from,
		       email_verified_at, password_hash
		FROM users
		WHERE email = $1 AND is_active`
	var u User
	var hash string
	err := s.pool.QueryRow(ctx, q, email).Scan(
		&u.ID, &u.Email, &u.DisplayName, &u.IsActive, &u.SessionsValidFrom,
		&u.EmailVerifiedAt, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrNoUser
	}
	if err != nil {
		return nil, "", err
	}
	return &u, hash, nil
}

// FindUserByID loads the session's subject. It does NOT filter on is_active:
// the middleware needs to tell "no such user" from "deactivated while logged
// in", and the second must end the session rather than look like a stale ID.
func (s *Store) FindUserByID(ctx context.Context, id string) (*User, error) {
	const q = `
		SELECT id, email, display_name, is_active, sessions_valid_from, email_verified_at
		FROM users WHERE id = $1`
	var u User
	err := s.pool.QueryRow(ctx, q, id).Scan(
		&u.ID, &u.Email, &u.DisplayName, &u.IsActive, &u.SessionsValidFrom, &u.EmailVerifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoUser
	}
	return &u, err
}

// CreateUser inserts and lets the database decide on duplicates. Checking first
// and inserting after lets two simultaneous signups both pass the check; the
// UNIQUE index is the only thing that actually serialises them.
func (s *Store) CreateUser(ctx context.Context, email, hash, displayName string) (string, error) {
	const q = `
		INSERT INTO users (email, password_hash, display_name)
		VALUES ($1, $2, $3) RETURNING id`
	var id string
	err := s.pool.QueryRow(ctx, q, email, hash, displayName).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return "", ErrEmailTaken
	}
	return id, err
}

// InvalidateSessions moves the cutoff forward, ending every session issued
// before now. Used on password change and on forced logout (D15 5.4).
func (s *Store) InvalidateSessions(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE users SET sessions_valid_from = now(), updated_at = now() WHERE id = $1`, userID)
	return err
}

// SetActive deactivates or reactivates an account, refusing to switch off the
// last superuser holder.
//
// The count and the update run in ONE transaction with the holders locked
// FOR UPDATE. Without the lock two administrators deactivating each other both
// read "2 remaining", both proceed, and the site is left with nobody who can
// let anyone back in (D15 5.2).
func (s *Store) SetActive(ctx context.Context, userID string, active bool) error {
	if active {
		_, err := s.pool.Exec(ctx,
			`UPDATE users SET is_active = true, updated_at = now() WHERE id = $1`, userID)
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	const lockHolders = `
		SELECT u.id
		FROM users u
		JOIN user_roles ur ON ur.user_id = u.id
		JOIN roles r       ON r.id = ur.role_id
		WHERE r.is_superuser AND u.is_active
		FOR UPDATE`
	rows, err := tx.Query(ctx, lockHolders)
	if err != nil {
		return err
	}
	var holders []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		holders = append(holders, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	isHolder := false
	for _, h := range holders {
		if h == userID {
			isHolder = true
			break
		}
	}
	if isHolder && len(holders) <= 1 {
		return ErrLastSuperuser
	}

	if _, err := tx.Exec(ctx,
		`UPDATE users SET is_active = false, updated_at = now() WHERE id = $1`, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// UpdateDisplayName changes the one profile field P-108 accepts.
//
// The predicate is the session's user id. There is no id in the form, so there
// is nothing to tamper with — SC-3's ownership rule expressed as an absence
// rather than as a check somebody has to remember.
func (s *Store) UpdateDisplayName(ctx context.Context, userID, name string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE users SET display_name = $2, updated_at = now() WHERE id = $1`, userID, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNoUser
	}
	return nil
}
