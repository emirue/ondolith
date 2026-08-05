package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNoUser        = errors.New("auth: 사용자가 없습니다")
	ErrEmailTaken    = errors.New("auth: 이미 사용 중인 이메일입니다")
	ErrLastSuperuser = errors.New("auth: 마지막 관리자는 비활성·삭제할 수 없습니다")
	ErrUserInUse     = errors.New("auth: 다른 기록이 이 사용자를 참조하고 있습니다")
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
		        array_agg(p.key || ' ' || coalesce(rp.board_id::text, ''))
		            FILTER (WHERE p.key IS NOT NULL),
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
	return NewPermissions(superuser != nil && *superuser, parseGrants(keys)), nil
}

// parseGrants splits the "<permission> <board_id>" pairs the queries above pack
// into one array.
//
// Two columns would need two aggregates and a second pass to line them up; one
// string keeps the whole permission set at ONE query, which is what D15 4.3-1
// and NFR-105 both ask for. The separator is a space because neither a
// permission key nor a uuid can contain one (both are CHECK-constrained, D30).
func parseGrants(rows []string) []Grant {
	out := make([]Grant, 0, len(rows))
	for _, r := range rows {
		key, board, _ := strings.Cut(r, " ")
		out = append(out, Grant{Permission: key, Board: BoardID(board)})
	}
	return out
}

// LoadAnonymousPermissions is the same for a request with no user. It exists so
// that the anonymous path is a query, not an empty set assumed in a handler —
// an installation may grant permissions to `anonymous` (D15 2.5).
func (s *Store) LoadAnonymousPermissions(ctx context.Context) (*Permissions, error) {
	const q = `
		SELECT coalesce(
		    array_agg(p.key || ' ' || coalesce(rp.board_id::text, ''))
		        FILTER (WHERE p.key IS NOT NULL),
		    '{}')
		FROM roles r
		LEFT JOIN role_permissions rp ON rp.role_id = r.id
		LEFT JOIN permissions p       ON p.id = rp.permission_id
		WHERE r.key = 'anonymous'`
	var keys []string
	if err := s.pool.QueryRow(ctx, q).Scan(&keys); err != nil {
		return nil, err
	}
	return NewPermissions(false, parseGrants(keys)), nil
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

// PermissionKeys lists every permission the database holds. The boot check
// compares the route table against it: a route naming a key that is not there
// judges always-false, and one nobody names is dead weight in the role editor
// (D15 4.4).
func (s *Store) PermissionKeys(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT key FROM permissions ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// UserRow is one line of A-401. It is deliberately narrower than User: a list
// screen has no use for the session cutoff, and `password_hash` has no business
// leaving the database at all (D19 A-401).
type UserRow struct {
	ID          string
	Email       string
	DisplayName string
	IsActive    bool
	Verified    bool
	Roles       []string
}

// ListUsers reads one page of the user list.
//
// Roles arrive in the same query rather than one lookup per row: the list is
// the screen most likely to grow, and a per-row query turns it into N+1 the
// moment it does.
func (s *Store) ListUsers(ctx context.Context, limit, offset int) ([]UserRow, error) {
	const q = `
		SELECT u.id, u.email, u.display_name, u.is_active,
		       u.email_verified_at IS NOT NULL,
		       coalesce(array_agg(r.key ORDER BY r.key) FILTER (WHERE r.key IS NOT NULL), '{}')
		FROM users u
		LEFT JOIN user_roles ur ON ur.user_id = u.id
		LEFT JOIN roles r ON r.id = ur.role_id
		GROUP BY u.id
		ORDER BY u.created_at DESC, u.id
		LIMIT $1 OFFSET $2`
	rows, err := s.pool.Query(ctx, q, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserRow
	for rows.Next() {
		var u UserRow
		if err := rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.IsActive, &u.Verified, &u.Roles); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
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

// withLastSuperuserGuard runs apply in a transaction that has every active
// superuser holder locked FOR UPDATE, refusing when userID is the last one.
//
// Deactivation and deletion share it deliberately. Without the lock two
// administrators removing each other both read "2 remaining", both proceed, and
// the site is left with nobody who can let anyone back in (D15 5.2) — and a
// second copy of this logic is exactly where the lock goes missing.
func (s *Store) withLastSuperuserGuard(ctx context.Context, userID string, apply func(pgx.Tx) error) error {
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

	if err := apply(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SetActive deactivates or reactivates an account, refusing to switch off the
// last superuser holder.
func (s *Store) SetActive(ctx context.Context, userID string, active bool) error {
	if active {
		_, err := s.pool.Exec(ctx,
			`UPDATE users SET is_active = true, updated_at = now() WHERE id = $1`, userID)
		return err
	}
	return s.withLastSuperuserGuard(ctx, userID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE users SET is_active = false, updated_at = now() WHERE id = $1`, userID)
		return err
	})
}

// DeleteUser removes an account, under the same last-superuser lock as
// deactivation: the two operations reach the same end state, so guarding only
// one of them guards neither (D19 A-402).
//
// A row another table still references (orders are RESTRICT, D30 3-1) comes
// back as ErrUserInUse rather than a 500: the refusal is the designed
// behaviour, not a failure.
func (s *Store) DeleteUser(ctx context.Context, userID string) error {
	return s.withLastSuperuserGuard(ctx, userID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return ErrUserInUse
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNoUser
		}
		return nil
	})
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

// HoldsSuperuser reports whether the user holds the superuser role.
//
// R6 needs this before any destructive account operation: without it, revoking
// the role is blocked while switching off its holder is not, and the two reach
// the same end (D15 5.1).
func (s *Store) HoldsSuperuser(ctx context.Context, userID string) (bool, error) {
	var yes bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM user_roles ur
		    JOIN roles r ON r.id = ur.role_id
		    WHERE ur.user_id = $1 AND r.is_superuser
		)`, userID).Scan(&yes)
	return yes, err
}

// ErrNoRole reports an unknown role key.
var ErrNoRole = errors.New("auth: 역할이 없습니다")

// Roles lists every role for A-403.
func (s *Store) Roles(ctx context.Context) ([]Role, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.key, r.name, r.is_superuser,
		       coalesce(array_agg(p.key) FILTER (WHERE p.key IS NOT NULL), '{}')
		FROM roles r
		LEFT JOIN role_permissions rp ON rp.role_id = r.id
		LEFT JOIN permissions p       ON p.id = rp.permission_id
		GROUP BY r.id, r.key, r.name, r.is_superuser
		ORDER BY r.key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Role
	for rows.Next() {
		var r Role
		var name string
		if err := rows.Scan(&r.Key, &name, &r.Superuser, &r.Permissions); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RoleByKey loads one role with its permissions, which R2 and R5 both need.
func (s *Store) RoleByKey(ctx context.Context, key string) (Role, error) {
	var r Role
	err := s.pool.QueryRow(ctx, `
		SELECT r.key, r.is_superuser,
		       coalesce(array_agg(p.key) FILTER (WHERE p.key IS NOT NULL), '{}')
		FROM roles r
		LEFT JOIN role_permissions rp ON rp.role_id = r.id
		LEFT JOIN permissions p       ON p.id = rp.permission_id
		WHERE r.key = $1
		GROUP BY r.id, r.key, r.is_superuser`, key).
		Scan(&r.Key, &r.Superuser, &r.Permissions)
	if errors.Is(err, pgx.ErrNoRows) {
		return Role{}, ErrNoRole
	}
	return r, err
}

// PermissionIsScoped reports whether a permission may carry a board_id.
func (s *Store) PermissionIsScoped(ctx context.Context, key string) (bool, error) {
	var scoped bool
	err := s.pool.QueryRow(ctx,
		`SELECT is_scoped FROM permissions WHERE key = $1`, key).Scan(&scoped)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, errors.New("auth: 권한이 없습니다: " + key)
	}
	return scoped, err
}

// GrantPermission adds a permission to a role, ignoring a repeat.
func (s *Store) GrantPermission(ctx context.Context, roleKey, permKey string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO role_permissions (role_id, permission_id)
		SELECT r.id, p.id FROM roles r, permissions p
		WHERE r.key = $1 AND p.key = $2
		ON CONFLICT ON CONSTRAINT role_permissions_uniq DO NOTHING`, roleKey, permKey)
	return err
}

// AssignRole gives a user a role, ignoring a repeat.
func (s *Store) AssignRole(ctx context.Context, userID, roleKey string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_roles (user_id, role_id)
		SELECT $1, r.id FROM roles r WHERE r.key = $2 AND r.is_assignable
		ON CONFLICT ON CONSTRAINT user_roles_uniq DO NOTHING`, userID, roleKey)
	return err
}

// BoardsWithGrants reports which boards have at least one scoped grant.
//
// A board with none is invisible to everyone including the person who just
// made it (D14 4.2), and A-304 marks those rows — without the mark the operator
// sees a normal-looking board and goes looking for the bug somewhere else.
func (s *Store) BoardsWithGrants(ctx context.Context) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT board_id::text FROM role_permissions WHERE board_id IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
