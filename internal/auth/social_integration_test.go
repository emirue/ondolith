package auth

import (
	"context"
	"errors"
	"testing"
)

// **같은 이메일이어도 자동으로 연결하지 않는다** (D18 닫은 결정, D12 P-107).
//
// 자동 연결을 허용하면 프로바이더 계정 하나를 뚫는 것이 곧 우리 계정을 뚫는
// 것이 된다. 그래서 조회는 `(provider, provider_uid)` 로만 한다 — 이메일로
// 찾는 경로가 코드에 없어야 한다.
func TestSocialLookupNeverMatchesByEmail(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()

	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateUser(ctx, "same@example.com", hash, "로컬 계정")
	if err != nil {
		t.Fatal(err)
	}

	// 프로바이더가 같은 이메일을 확인해 줬다 해도, 연결이 없으면 로그인이
	// 성립하지 않는다.
	if _, err := s.UserBySocial(ctx, "google", "google-uid-1"); !errors.Is(err, ErrNoUser) {
		t.Fatalf("연결 없이 소셜 로그인이 성립했다: %v", err)
	}

	// 계정 주인이 연결한 뒤에야 성립한다.
	if err := s.LinkSocial(ctx, id, "google", "google-uid-1"); err != nil {
		t.Fatal(err)
	}
	got, err := s.UserBySocial(ctx, "google", "google-uid-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id {
		t.Errorf("연결한 계정과 다른 계정이 나왔다")
	}
}

// 하나의 소셜 계정이 우리 계정 둘에 붙지 않는다. 붙으면 어느 쪽으로
// 로그인하는지 알 수 없다.
func TestOneSocialAccountCannotAttachToTwoUsers(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	hash, _ := HashPassword("correct horse battery")
	a, err := s.CreateUser(ctx, "a@example.com", hash, "A")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateUser(ctx, "b@example.com", hash, "B")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.LinkSocial(ctx, a, "google", "uid-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkSocial(ctx, b, "google", "uid-1"); !errors.Is(err, ErrSocialTaken) {
		t.Fatalf("= %v, want ErrSocialTaken", err)
	}
	// 같은 계정에 같은 프로바이더를 두 번 붙이는 것과 구분된다 — 계정 주인이
	// 할 일이 다르다.
	if err := s.LinkSocial(ctx, a, "google", "uid-2"); !errors.Is(err, ErrSocialLinked) {
		t.Fatalf("= %v, want ErrSocialLinked", err)
	}
}

// **마지막 로그인 수단은 해제할 수 없다** (FR-213).
//
// 비밀번호가 없는 계정에서 마지막 소셜을 떼면 그 계정으로 들어올 방법이
// 사라지고, 되돌릴 화면은 로그인 뒤에 있다.
func TestLastLoginMethodCannotBeUnlinked(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	hash, _ := HashPassword("correct horse battery")
	id, err := s.CreateUser(ctx, "social-only@example.com", hash, "소셜 전용")
	if err != nil {
		t.Fatal(err)
	}
	// 소셜 전용 계정을 만든다 (비밀번호 없음).
	if _, err := pool.Exec(ctx, `UPDATE users SET password_hash = '' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkSocial(ctx, id, "google", "uid-1"); err != nil {
		t.Fatal(err)
	}

	if err := s.UnlinkSocial(ctx, id, "google"); !errors.Is(err, ErrLastLoginMethod) {
		t.Fatalf("= %v, want ErrLastLoginMethod", err)
	}
	links, err := s.SocialAccounts(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 {
		t.Fatalf("연결이 %d개 — 거부됐는데 지워졌다", len(links))
	}

	// 둘째 연결이 생기면 하나는 뗄 수 있다.
	if err := s.LinkSocial(ctx, id, "kakao", "uid-2"); err != nil {
		t.Fatal(err)
	}
	if err := s.UnlinkSocial(ctx, id, "google"); err != nil {
		t.Fatalf("두 번째가 있는데 해제가 막혔다: %v", err)
	}

	// 비밀번호가 있으면 마지막 소셜도 뗄 수 있다.
	if _, err := pool.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, id, hash); err != nil {
		t.Fatal(err)
	}
	if err := s.UnlinkSocial(ctx, id, "kakao"); err != nil {
		t.Fatalf("비밀번호가 있는데 해제가 막혔다: %v", err)
	}
}

// 비활성 계정은 소셜로도 들어올 수 없다. 로컬 로그인만 막고 여기를 열어 두면
// 정지된 계정이 다른 문으로 들어온다.
func TestDeactivatedAccountCannotLogInWithSocial(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	hash, _ := HashPassword("correct horse battery")
	id, err := s.CreateUser(ctx, "off@example.com", hash, "정지")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkSocial(ctx, id, "google", "uid-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserBySocial(ctx, "google", "uid-1"); err != nil {
		t.Fatalf("활성 계정인데 막혔다: %v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE users SET is_active = false WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserBySocial(ctx, "google", "uid-1"); !errors.Is(err, ErrNoUser) {
		t.Error("정지된 계정이 소셜로 들어왔다")
	}
}

// 프로바이더 목록은 **코드가** 정한다 (D15 P1). 목록 밖은 만들 수 없다.
func TestProviderAllowListIsCodeNotData(t *testing.T) {
	for _, bad := range []string{"", "facebook", "GOOGLE", "google ", "../google"} {
		if _, err := NewSocialProvider(bad, "id", "secret", "https://x/cb"); err == nil {
			t.Errorf("%q 로 프로바이더가 만들어졌다", bad)
		}
	}
	for _, ok := range SocialProviderKeys() {
		if _, err := NewSocialProvider(ok, "id", "secret", "https://x/cb"); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
		// 자격증명이 없으면 만들지 않는다 — 켜 두면 P-106 이 즉시 깨진다.
		if _, err := NewSocialProvider(ok, "", "secret", "https://x/cb"); err == nil {
			t.Errorf("%q: 클라이언트 ID 없이 만들어졌다", ok)
		}
		if _, err := NewSocialProvider(ok, "id", "", "https://x/cb"); err == nil {
			t.Errorf("%q: 시크릿 없이 만들어졌다", ok)
		}
	}
}

// 콜백 URL 은 서버가 만든다. 프로바이더 콘솔에 붙여넣을 값이므로 한 글자도
// 달라지면 안 된다.
func TestSocialCallbackURLIsDerived(t *testing.T) {
	for _, tc := range []struct{ base, want string }{
		{"https://shop.example.com", "https://shop.example.com/auth/google/callback"},
		{"https://shop.example.com/", "https://shop.example.com/auth/google/callback"},
		{"http://localhost:8080", "http://localhost:8080/auth/google/callback"},
	} {
		if got := SocialCallbackURL(tc.base, "google"); got != tc.want {
			t.Errorf("SocialCallbackURL(%q) = %q, want %q", tc.base, got, tc.want)
		}
	}
}
