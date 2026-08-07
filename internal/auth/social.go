package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrSocialTaken 은 그 프로바이더 계정이 이미 **다른** 우리 계정에 붙어
	// 있다는 뜻이다. `(provider, provider_uid)` 유니크가 막는다 — 하나의
	// 소셜 계정이 우리 계정 둘에 붙으면 어느 쪽으로 로그인하는지 알 수 없다.
	ErrSocialTaken = errors.New("auth: 이미 다른 계정에 연결된 소셜 계정입니다")
	// ErrSocialLinked 는 그 프로바이더가 이 계정에 이미 붙어 있다는 뜻이다.
	ErrSocialLinked = errors.New("auth: 이미 연결된 프로바이더입니다")
	// ErrLastLoginMethod 는 마지막 로그인 수단을 해제하려 한 경우다 (FR-213).
	// 해제하면 그 계정으로 들어올 방법이 사라지고, 되돌릴 화면은 로그인 뒤에 있다.
	ErrLastLoginMethod = errors.New("auth: 마지막 로그인 수단은 해제할 수 없습니다")
)

// SocialAccount is one linked provider identity.
type SocialAccount struct {
	Provider string
	UID      string
}

// SocialAccounts lists what one user has linked.
func (s *Store) SocialAccounts(ctx context.Context, userID string) ([]SocialAccount, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT provider, provider_uid FROM social_accounts
		 WHERE user_id = $1 ORDER BY provider`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SocialAccount
	for rows.Next() {
		var a SocialAccount
		if err := rows.Scan(&a.Provider, &a.UID); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UserBySocial finds the account a provider identity is linked to.
//
// **이메일로 찾지 않는다.** `(provider, provider_uid)` 로만 찾는다 — 같은
// 이메일의 로컬 계정에 자동으로 붙이면, 프로바이더 계정 하나를 뚫는 것이 곧
// 우리 계정을 뚫는 것이 된다 (D18 닫은 결정, D12 P-107).
func (s *Store) UserBySocial(ctx context.Context, provider, uid string) (*User, error) {
	var id string
	err := s.pool.QueryRow(ctx,
		`SELECT user_id FROM social_accounts WHERE provider = $1 AND provider_uid = $2`,
		provider, uid).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoUser
	}
	if err != nil {
		return nil, err
	}
	u, err := s.FindUserByID(ctx, id)
	if err != nil {
		return nil, err
	}
	// **비활성 계정은 소셜로도 들어올 수 없다.** 로컬 로그인만 막고 여기를
	// 열어 두면 정지된 계정이 다른 문으로 들어온다.
	if !u.IsActive {
		return nil, ErrNoUser
	}
	return u, nil
}

// LinkSocial attaches a provider identity to an existing account (P-111).
//
// **연결은 로그인한 계정 주인만 만든다.** 콜백이 스스로 만들지 않는다 —
// 그것이 곧 자동 연결이다.
func (s *Store) LinkSocial(ctx context.Context, userID, provider, uid string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO social_accounts (user_id, provider, provider_uid) VALUES ($1, $2, $3)`,
		userID, provider, uid)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		// 어느 유니크에 걸렸는지 구분한다. 접으면 "이미 연결됨" 과 "남의
		// 계정에 붙어 있음" 이 같은 메시지가 되어, 계정 주인이 무엇을 해야
		// 하는지 알 수 없다.
		if pgErr.ConstraintName == "social_accounts_user_provider_uniq" {
			return ErrSocialLinked
		}
		return ErrSocialTaken
	}
	return err
}

// UnlinkSocial removes one link (P-111).
//
// **마지막 로그인 수단은 해제할 수 없다** (FR-213). 비밀번호가 없는 계정에서
// 마지막 소셜을 떼면 그 계정으로 들어올 방법이 사라진다.
//
// 판단과 삭제가 한 트랜잭션이다: 따로 하면 두 요청이 각자 "아직 하나 더
// 있다" 를 읽고 둘 다 지운다.
func (s *Store) UnlinkSocial(ctx context.Context, userID, provider string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// 계정 행을 잠근다. 비밀번호 유무와 연결 수를 함께 읽어야 하고, 그 사이
	// 비밀번호가 지워지면 판단이 거짓이 된다.
	var hasPassword bool
	err = tx.QueryRow(ctx,
		`SELECT password_hash <> '' FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&hasPassword)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoUser
	}
	if err != nil {
		return err
	}
	var links int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM social_accounts WHERE user_id = $1`, userID).Scan(&links); err != nil {
		return err
	}
	if !hasPassword && links <= 1 {
		return fmt.Errorf("%w: 비밀번호를 먼저 설정하세요", ErrLastLoginMethod)
	}

	tag, err := tx.Exec(ctx,
		`DELETE FROM social_accounts WHERE user_id = $1 AND provider = $2`, userID, provider)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNoUser
	}
	return tx.Commit(ctx)
}

// HasPassword reports whether the account can log in with a password.
//
// 화면이 「마지막 로그인 수단」을 미리 보여주는 데 쓴다. **거부하는 것은
// UnlinkSocial 이다** — 여기서 true 를 받아도 그 사이 비밀번호가 지워질 수
// 있고, 그래서 판단과 삭제가 한 트랜잭션에 있다.
func (s *Store) HasPassword(ctx context.Context, userID string) (bool, error) {
	var has bool
	err := s.pool.QueryRow(ctx,
		`SELECT password_hash <> '' FROM users WHERE id = $1`, userID).Scan(&has)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNoUser
	}
	return has, err
}
