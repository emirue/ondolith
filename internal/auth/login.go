package auth

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// ErrBadCredentials is the ONE error the login path returns for "no such
// account", "wrong password" and "account deactivated" alike.
//
// Three distinct answers would let anyone enumerate which addresses have
// accounts here, which is a list worth having before a credential-stuffing run
// (FR-201). The caller logs which of the three it really was; the visitor is
// told the same thing every time.
var ErrBadCredentials = errors.New("auth: 이메일 또는 비밀번호가 올바르지 않습니다")

// dummyHash is a real bcrypt hash of a value nobody uses. When no account
// exists we compare against it anyway.
//
// Without this, "no account" returns in microseconds while "wrong password"
// spends ~60ms in bcrypt, and the difference answers the same question the
// identical error message refuses to answer.
var dummyHash = []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")

// Authenticate checks credentials in constant-ish time.
//
// The password length floor (10) is NOT enforced here. It applies when a
// password is set; enforcing it at login would lock out every account created
// before the rule, and the rule exists to make new passwords better, not to
// evict people.
func (s *Store) Authenticate(ctx context.Context, email, password string) (*User, error) {
	u, hash, err := s.FindActiveUserByEmail(ctx, email)
	if errors.Is(err, ErrNoUser) {
		// Spend the same time as a real comparison would.
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return nil, ErrBadCredentials
	}
	if err != nil {
		return nil, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return nil, ErrBadCredentials
	}
	return u, nil
}

// HashPassword is the one place a password becomes a hash.
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(b), err
}

// SafeNext validates a post-login redirect target.
//
// Open redirect defence. Only a path on this site is accepted, and a rejected
// value is not an error — it is ignored and the caller goes to "/" (D19 P-101).
// Erroring would turn a phishing attempt into a visible failure for the victim
// while telling the attacker their payload was noticed.
//
// The cases that matter:
//
//	//evil.com     browsers read this as a protocol-relative URL to evil.com,
//	               and it starts with "/" — the check naive code writes
//	\\evil.com     some browsers normalise backslashes to forward slashes
//	https://…      absolute URL
//	/\evil.com     mixed
func SafeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") {
		return "/"
	}
	// Reject anything whose second character starts another authority.
	if len(next) > 1 && (next[1] == '/' || next[1] == '\\') {
		return "/"
	}
	if strings.ContainsAny(next, "\\\x00") {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return "/"
	}
	return next
}
