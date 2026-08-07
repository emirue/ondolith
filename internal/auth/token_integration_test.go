package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTokenIsSingleUse(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "a@example.com")

	raw, err := s.IssueToken(ctx, KindPasswordReset, u)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ConsumeToken(ctx, KindPasswordReset, raw)
	if err != nil {
		t.Fatalf("첫 사용이 거부됐다: %v", err)
	}
	if got != u {
		t.Errorf("user_id = %q, want %q", got, u)
	}
	if _, err := s.ConsumeToken(ctx, KindPasswordReset, raw); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("두 번째 사용이 통과했다: %v", err)
	}
}

// Two clicks on the same link race. Checking first and updating after would let
// both through, and for a reset link that means two people set the password.
func TestTokenConcurrentUseAllowsExactlyOne(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "a@example.com")

	for round := 0; round < 20; round++ {
		raw, err := s.IssueToken(ctx, KindPasswordReset, u)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		wg.Add(2)
		for i := range errs {
			go func(i int) {
				defer wg.Done()
				<-start
				_, errs[i] = s.ConsumeToken(ctx, KindPasswordReset, raw)
			}(i)
		}
		close(start)
		wg.Wait()

		ok := 0
		for _, e := range errs {
			if e == nil {
				ok++
			}
		}
		if ok != 1 {
			t.Fatalf("%d회차: 동시 사용 성공 %d건, want 1건 (%v)", round, ok, errs)
		}
	}
}

func TestExpiredTokenIsRefused(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "a@example.com")

	raw, err := s.IssueToken(ctx, KindEmailVerify, u)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE email_verification_tokens SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeToken(ctx, KindEmailVerify, raw); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("만료 토큰이 통과했다: %v", err)
	}
}

// C5: the raw token exists in the mail and nowhere else. A database dump must
// not be replayable into account takeovers.
func TestRawTokenIsNotStored(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "a@example.com")

	// IssueResetToken is the path P-104 uses; IssueToken is the generic one.
	// Both have to hash, and testing only one leaves the other free to drift.
	raw, err := s.IssueResetToken(ctx, u)
	if err != nil {
		t.Fatal(err)
	}

	var stored []byte
	if err := pool.QueryRow(ctx, `SELECT token_hash FROM password_reset_tokens`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), raw) {
		t.Error("토큰 원문이 DB 에 저장됐다")
	}
	if len(stored) != 32 {
		t.Errorf("해시 길이 %d, want 32 (SHA-256)", len(stored))
	}
	// bcrypt would start with $2a$ and could not be looked up by value.
	if strings.HasPrefix(string(stored), "$2") {
		t.Error("bcrypt 로 저장됐다 — 해시로 조회할 수 없어 전 토큰을 스캔하게 된다")
	}
}

// The two kinds have different lifetimes, which is why they are separate
// tables. A token issued for one must not be usable as the other.
func TestTokenKindsDoNotCross(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "a@example.com")

	raw, err := s.IssueToken(ctx, KindEmailVerify, u)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeToken(ctx, KindPasswordReset, raw); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("인증 토큰이 재설정 토큰으로 쓰였다: %v", err)
	}
	if _, err := s.ConsumeToken(ctx, KindEmailVerify, raw); err != nil {
		t.Errorf("올바른 종류인데 거부됐다: %v", err)
	}
}

func TestTTLsMatchTheDocument(t *testing.T) {
	if PasswordResetTTL != 30*time.Minute {
		t.Errorf("재설정 TTL = %v, want 30분 (D30)", PasswordResetTTL)
	}
	if EmailVerifyTTL != 24*time.Hour {
		t.Errorf("인증 TTL = %v, want 24시간 (D30)", EmailVerifyTTL)
	}
}

// D15 5.4: changing a password must end the other sessions, or the person the
// change was aimed at keeps their access.
func TestSetPasswordInvalidatesOtherSessions(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "a@example.com")

	before, err := s.FindUserByID(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := HashPassword("brand-new-password")
	if _, err := s.SetPassword(ctx, u, h); err != nil {
		t.Fatal(err)
	}
	after, err := s.FindUserByID(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if !after.SessionsValidFrom.After(before.SessionsValidFrom) {
		t.Error("비밀번호를 바꿨는데 다른 세션이 살아 있다")
	}
	if _, err := s.Authenticate(ctx, "a@example.com", "brand-new-password"); err != nil {
		t.Errorf("새 비밀번호로 로그인이 안 된다: %v", err)
	}
}

func TestMarkEmailVerified(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "a@example.com")

	got, err := s.FindUserByID(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if got.EmailVerifiedAt != nil {
		t.Fatal("가입 직후인데 인증됨으로 표시돼 있다")
	}
	if err := s.MarkEmailVerified(ctx, u); err != nil {
		t.Fatal(err)
	}
	got, _ = s.FindUserByID(ctx, u)
	if got.EmailVerifiedAt == nil {
		t.Error("인증 표시가 되지 않았다")
	}
	first := *got.EmailVerifiedAt

	// Re-verifying must not move the timestamp: the column records when the
	// address was first proven, and an installation that turns verification off
	// and on again must not make everyone verify twice (D30).
	if err := s.MarkEmailVerified(ctx, u); err != nil {
		t.Fatal(err)
	}
	got, _ = s.FindUserByID(ctx, u)
	if !got.EmailVerifiedAt.Equal(first) {
		t.Error("재인증이 시각을 덮어썼다")
	}
}

// 새 토큰을 발급하면 **같은 사용자의 미사용 토큰은 죽는다**
// (D19 P-104·P-113 「기존 토큰」).
//
// 유효한 링크가 여러 개 살아 있으면 재발송(P-113)이 공격 표면을 넓히는 동작이
// 된다 — 메일함 하나가 새면 그 안의 오래된 링크가 전부 아직 쓸 수 있다.
// 발급이 이것을 안 하고 있었고, 확인하는 테스트가 없어서 두 화면 모두에서
// 조용히 어긋나 있었다.
func TestIssuingATokenBurnsTheOldOnes(t *testing.T) {
	for _, kind := range []TokenKind{KindPasswordReset, KindEmailVerify} {
		t.Run(kind.table(), func(t *testing.T) {
			s, _ := testStore(t)
			ctx := context.Background()
			u := mkUser(t, s, "a@example.com")

			old, err := s.IssueToken(ctx, kind, u)
			if err != nil {
				t.Fatal(err)
			}
			fresh, err := s.IssueToken(ctx, kind, u)
			if err != nil {
				t.Fatal(err)
			}
			if old == fresh {
				t.Fatal("두 발급이 같은 토큰을 냈다 — 이 검사가 무의미해진다")
			}

			if _, err := s.ConsumeToken(ctx, kind, old); !errors.Is(err, ErrTokenInvalid) {
				t.Errorf("이전 토큰이 아직 살아 있다: %v", err)
			}
			// 새 것은 살아 있어야 한다. 전부 죽이면 사용자는 링크를 하나도
			// 갖지 못한 채 "보냈습니다" 를 본다.
			if _, err := s.ConsumeToken(ctx, kind, fresh); err != nil {
				t.Errorf("방금 발급한 토큰이 거부됐다: %v", err)
			}
		})
	}
}

// 다른 사용자의 토큰까지 태우면 안 된다. 그러면 남의 재설정을 무효화하는
// 도구가 된다 — 재발송은 로그인만 하면 누구나 부를 수 있다.
func TestIssuingATokenLeavesOtherUsersAlone(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	mine := mkUser(t, s, "mine@example.com")
	theirs := mkUser(t, s, "theirs@example.com")

	victim, err := s.IssueToken(ctx, KindPasswordReset, theirs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.IssueToken(ctx, KindPasswordReset, mine); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeToken(ctx, KindPasswordReset, victim); err != nil {
		t.Errorf("남의 발급이 이 토큰을 태웠다: %v", err)
	}
}
