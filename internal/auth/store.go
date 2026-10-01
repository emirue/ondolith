package auth

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/emirue/ondolith/internal/auth/authq"
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
//
// The SQL lives in queries/*.sql and q is what sqlc generated from it (D22
// 6절); pool is kept only to open transactions.
type Store struct {
	pool *pgxpool.Pool
	q    *authq.Queries
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool, q: authq.New(pool)} }

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
	row, err := s.q.LoadPermissions(ctx, userID)
	if err != nil {
		return nil, err
	}
	return NewPermissions(row.Superuser, parseGrants(row.Perms)), nil
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
	keys, err := s.q.LoadAnonymousPermissions(ctx)
	if err != nil {
		return nil, err
	}
	return NewPermissions(false, parseGrants(keys)), nil
}

// FindActiveUserByEmail is the login lookup. Inactive accounts are filtered in
// the WHERE clause rather than after the fetch: a caller who forgets the Go-side
// check would otherwise log in a deactivated account, and there is no way to
// forget a predicate that is not there to forget.
func (s *Store) FindActiveUserByEmail(ctx context.Context, email string) (*User, string, error) {
	r, err := s.q.FindActiveUserByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrNoUser
	}
	if err != nil {
		return nil, "", err
	}
	return &User{ID: r.ID, Email: r.Email, DisplayName: r.DisplayName, IsActive: r.IsActive,
		SessionsValidFrom: r.SessionsValidFrom, EmailVerifiedAt: r.EmailVerifiedAt}, r.PasswordHash, nil
}

// FindUserByID loads the session's subject. It does NOT filter on is_active:
// the middleware needs to tell "no such user" from "deactivated while logged
// in", and the second must end the session rather than look like a stale ID.
func (s *Store) FindUserByID(ctx context.Context, id string) (*User, error) {
	r, err := s.q.FindUserByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoUser
	}
	return &User{ID: r.ID, Email: r.Email, DisplayName: r.DisplayName, IsActive: r.IsActive,
		SessionsValidFrom: r.SessionsValidFrom, EmailVerifiedAt: r.EmailVerifiedAt}, err
}

// PermissionKeys lists every permission the database holds. The boot check
// compares the route table against it: a route naming a key that is not there
// judges always-false, and one nobody names is dead weight in the role editor
// (D15 4.4).
func (s *Store) PermissionKeys(ctx context.Context) ([]string, error) {
	return s.q.PermissionKeys(ctx)
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
	// Custom 은 A-406 이 정의한 회원 항목의 값이다 (FR-215). 목록에 보일지는
	// `show_in_list` 가 정하고, 그 판단은 화면이 한다 — 저장소는 값만 낸다.
	Custom map[string]any
}

// ListUsers reads one page of the user list.
//
// Roles arrive in the same query rather than one lookup per row: the list is
// the screen most likely to grow, and a per-row query turns it into N+1 the
// moment it does.
func (s *Store) ListUsers(ctx context.Context, limit, offset int) ([]UserRow, error) {
	rows, err := s.q.ListUsers(ctx, authq.ListUsersParams{Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return nil, err
	}
	var out []UserRow
	for _, r := range rows {
		u := UserRow{ID: r.ID, Email: r.Email, DisplayName: r.DisplayName,
			IsActive: r.IsActive, Verified: r.Verified, Roles: r.Roles}
		if err := json.Unmarshal(r.CustomFields, &u.Custom); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

// CreateUser inserts and lets the database decide on duplicates. Checking first
// and inserting after lets two simultaneous signups both pass the check; the
// UNIQUE index is the only thing that actually serialises them.
func (s *Store) CreateUser(ctx context.Context, email, hash, displayName string) (string, error) {
	id, err := s.q.CreateUser(ctx, authq.CreateUserParams{
		Email: email, PasswordHash: hash, DisplayName: displayName})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return "", ErrEmailTaken
	}
	return id, err
}

// DBTime is a moment that came off the database's clock.
//
// **Its field is unexported, so nothing outside this package can make one.**
// That is the entire purpose of the type, and it is worth the small awkwardness
// of the Time() unwrap.
//
// The session gate compares `auth_at` against `sessions_valid_from` (D15 5.4,
// withActor). The right-hand side is written by `now()` — the database. Stamping
// the left-hand side with `time.Now()` compares two clocks, and a database
// running a few milliseconds ahead of the application makes a session issued
// *after* the cutoff look older than it: the middleware destroys the session the
// user just opened. Signing up and being bounced straight back to the login form
// is that bug, and it repeats on every retry because the skew does not go away.
//
// A comment saying "use the database clock here" does not survive the next
// edit, and neither does a checker that greps for `time.Now()` — the natural way
// to write the bug is `at := time.Now()` two lines up, and any check that looks
// at the argument expression sails right past it. A type the wrong value cannot
// be spelled as does survive: `stampAuthAt` takes a DBTime, and the only way to
// obtain one is to ask the database.
type DBTime struct{ t time.Time }

// Time unwraps the moment for storage and comparison.
func (d DBTime) Time() time.Time { return d.t }

// Now reads the database's clock. See DBTime for why this is not time.Now().
func (s *Store) Now(ctx context.Context) (DBTime, error) {
	t, err := s.q.DBNow(ctx)
	return DBTime{t}, err
}

// InvalidateSessions moves the cutoff forward, ending every session issued
// before now. Used on password change and on forced logout (D15 5.4).
func (s *Store) InvalidateSessions(ctx context.Context, userID string) error {
	return s.q.InvalidateSessions(ctx, userID)
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

	holders, err := s.q.WithTx(tx).LockSuperuserHolders(ctx)
	if err != nil {
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
		return s.q.ActivateUser(ctx, userID)
	}
	return s.withLastSuperuserGuard(ctx, userID, func(tx pgx.Tx) error {
		return s.q.WithTx(tx).DeactivateUser(ctx, userID)
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
		n, err := s.q.WithTx(tx).DeleteUser(ctx, userID)
		var pgErr *pgconn.PgError
		// 23503 은 FK 위반, **23001 은 RESTRICT 위반**이다. 둘은 다른 코드다 —
		// `orders.user_id` 가 RESTRICT 이므로(00018) 여기서 오는 것은 23001 이고,
		// 23503 만 보면 「주문 이력이 있어 지울 수 없다」가 통째로 500 이 된다.
		// commerce.DeleteProduct 는 같은 함정을 이미 알고 둘 다 본다.
		if errors.As(err, &pgErr) && (pgErr.Code == "23503" || pgErr.Code == "23001") {
			return ErrUserInUse
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNoUser
		}
		return nil
	})
}

// UpdateProfile changes the fields P-109 accepts: the display name and the
// profile items an operator defined (FR-215).
//
// The predicate is the session's user id. There is no id in the form, so there
// is nothing to tamper with — SC-3's ownership rule expressed as an absence
// rather than as a check somebody has to remember.
//
// **custom 은 이미 검증된 것만 온다.** 이 패키지는 항목의 정의를 모른다 —
// 스키마와 검증은 content 가 갖고 있고(DEC-3.9), 여기로는 그것을 통과한
// map 만 넘어온다. 그래서 auth 가 content 를 알 필요가 없다.
func (s *Store) UpdateProfile(ctx context.Context, userID, name string, custom map[string]any) error {
	if custom == nil {
		custom = map[string]any{}
	}
	raw, err := json.Marshal(custom)
	if err != nil {
		return err
	}
	n, err := s.q.UpdateProfile(ctx, authq.UpdateProfileParams{
		ID: userID, DisplayName: name, CustomFields: raw})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNoUser
	}
	return nil
}

// CustomFields reads one user's profile item values.
func (s *Store) CustomFields(ctx context.Context, userID string) (map[string]any, error) {
	raw, err := s.q.CustomFields(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoUser
	}
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// HoldsSuperuser reports whether the user holds the superuser role.
//
// R6 needs this before any destructive account operation: without it, revoking
// the role is blocked while switching off its holder is not, and the two reach
// the same end (D15 5.1).
func (s *Store) HoldsSuperuser(ctx context.Context, userID string) (bool, error) {
	return s.q.HoldsSuperuser(ctx, userID)
}

// ErrNoRole reports an unknown role key.
var ErrNoRole = errors.New("auth: 역할이 없습니다")

// Roles lists every role for A-403.
func (s *Store) Roles(ctx context.Context) ([]Role, error) {
	rows, err := s.q.Roles(ctx)
	if err != nil {
		return nil, err
	}
	var out []Role
	for _, r := range rows {
		out = append(out, Role{Key: r.Key, Superuser: r.IsSuperuser, Permissions: r.Permissions})
	}
	return out, nil
}

// RoleByKey loads one role with its permissions, which R2 and R5 both need.
func (s *Store) RoleByKey(ctx context.Context, key string) (Role, error) {
	r, err := s.q.RoleByKey(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return Role{}, ErrNoRole
	}
	return Role{Key: r.Key, Superuser: r.IsSuperuser, Permissions: r.Permissions}, err
}

// PermissionIsScoped reports whether a permission may carry a board_id.
func (s *Store) PermissionIsScoped(ctx context.Context, key string) (bool, error) {
	scoped, err := s.q.PermissionIsScoped(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, errors.New("auth: 권한이 없습니다: " + key)
	}
	return scoped, err
}

// GrantPermission adds a permission to a role, ignoring a repeat.
// **board 가 곧 범위다** (D15 2.4). 검증만 하고 버리면 게시판 하나에 주려던
// 권한이 전역으로 저장되고, A-403 목록은 범위를 보여주지 않아 그 사실이
// 보이지 않는다 — 받은 쪽은 모든 게시판에서 그 권한을 갖는다.
func (s *Store) GrantPermission(ctx context.Context, roleKey, permKey string, board BoardID) error {
	return s.q.GrantPermission(ctx, authq.GrantPermissionParams{
		RoleKey: roleKey, PermKey: permKey, Board: string(board)})
}

// AssignRole gives a user a role, ignoring a repeat.
func (s *Store) AssignRole(ctx context.Context, userID, roleKey string) error {
	return s.q.AssignRole(ctx, authq.AssignRoleParams{UserID: userID, RoleKey: roleKey})
}

// BoardsWithGrants reports which boards have at least one scoped grant.
//
// A board with none is invisible to everyone including the person who just
// made it (D14 4.2), and A-304 marks those rows — without the mark the operator
// sees a normal-looking board and goes looking for the bug somewhere else.
func (s *Store) BoardsWithGrants(ctx context.Context) (map[string]bool, error) {
	ids, err := s.q.BoardsWithGrants(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}
