package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

// P-112 tells "already used" apart from "wrong" and nothing else. Expiry is
// deliberately on the "wrong" side (D19): the link is dead either way, and
// answering 200 to an expired one would tell the visitor it had worked.
func TestConsumeVerifyTokenDistinguishesUsedFromWrongAndExpired(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "v@example.com")

	raw, err := s.IssueToken(ctx, KindEmailVerify, u)
	if err != nil {
		t.Fatal(err)
	}
	id, already, err := s.ConsumeVerifyToken(ctx, raw)
	if err != nil || already || id != u {
		t.Fatalf("첫 사용 = (%q, %v, %v)", id, already, err)
	}
	id, already, err = s.ConsumeVerifyToken(ctx, raw)
	if err != nil || !already || id != "" {
		t.Fatalf("재방문 = (%q, %v, %v) — 소모되지 않았거나 사용자를 다시 내줬다", id, already, err)
	}

	if _, _, err := s.ConsumeVerifyToken(ctx, "그런-토큰은-없다"); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("없는 토큰 = %v", err)
	}

	// A spent token whose 24 hours have run out is "wrong", not "already".
	if _, err := pool.Exec(ctx,
		`UPDATE email_verification_tokens SET expires_at = now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	if _, already, err := s.ConsumeVerifyToken(ctx, raw); !errors.Is(err, ErrTokenInvalid) || already {
		t.Errorf("만료된 사용필 토큰 = (%v, %v), want ErrTokenInvalid", already, err)
	}
}

// The reset link is single-use and expires (D19 P-105, 30분).
func TestResetTokenExpires(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "r@example.com")

	raw, err := s.IssueResetToken(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE password_reset_tokens SET expires_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResetPassword(ctx, raw, "$2a$10$"+string(make([]byte, 53))); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("만료된 링크 = %v, want ErrTokenInvalid", err)
	}

	// The lifetime is the documented one, not "some future moment".
	var expires time.Time
	raw2, err := s.IssueResetToken(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	_ = raw2
	if err := pool.QueryRow(ctx,
		`SELECT expires_at FROM password_reset_tokens WHERE used_at IS NULL`).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if d := time.Until(expires); d > PasswordResetTTL || d < PasswordResetTTL-time.Minute {
		t.Errorf("만료까지 %v, want ≈%v", d, PasswordResetTTL)
	}
}
