package auth

import "testing"

// Open redirect defence (D19 P-101). A rejected value is ignored, not an error:
// erroring shows the victim a failure while telling the attacker their payload
// was noticed.
func TestSafeNext(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"내부 경로", "/admin/pages", "/admin/pages"},
		{"루트", "/", "/"},
		{"쿼리 포함", "/search?q=1", "/search?q=1"},
		{"빈 값", "", "/"},
		// Starts with "/" — which is the check naive code writes — but browsers
		// read it as an authority.
		{"프로토콜 상대", "//evil.com", "/"},
		{"프로토콜 상대 (역슬래시)", "/\\evil.com", "/"},
		{"백슬래시 두 개", "\\\\evil.com", "/"},
		{"절대 URL", "https://evil.com/x", "/"},
		{"스킴만", "javascript:alert(1)", "/"},
		{"호스트 상대", "evil.com", "/"},
		{"널바이트", "/ok\x00/evil", "/"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SafeNext(tc.in); got != tc.want {
				t.Errorf("SafeNext(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The dummy hash has to be a real bcrypt hash, or the comparison it exists to
// pay for returns immediately and the timing channel is still open.
func TestDummyHashIsRealBcrypt(t *testing.T) {
	if len(dummyHash) < 59 {
		t.Fatalf("더미 해시가 bcrypt 형식이 아니다: %q", dummyHash)
	}
	if string(dummyHash[:4]) != "$2a$" {
		t.Errorf("더미 해시 접두사 = %q", dummyHash[:4])
	}
}

func TestHashPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	if h == "correct-horse-battery" {
		t.Fatal("평문이 그대로 저장됐다")
	}
	if len(h) < 59 || h[:4] != "$2a$" {
		t.Errorf("bcrypt 형식이 아니다: %q", h)
	}
}
