package applogin

import "testing"

// **번호는 숫자만 남겨 한 모양으로 둔다** (D42 8절). 앱이 「010-1234-5678」이든 「+82 10 1234 5678」이든 같은
// 사람이다 — 모양이 둘이면 상한도 인증번호도 둘로 갈린다. 휴대폰 번호(01로 시작, 10~11자리)만 받는다.
func TestNormalizePhone(t *testing.T) {
	for in, want := range map[string]string{
		"01012345678": "01012345678", "010-1234-5678": "01012345678", " 010 1234 5678 ": "01012345678",
		"+82 10-1234-5678": "01012345678", "821012345678": "01012345678", "0111234567": "0111234567",
		"+82-010-1234-5678": "01012345678",
	} {
		if got, ok := NormalizePhone(in); !ok || got != want {
			t.Errorf("NormalizePhone(%q) = %q, %v, want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "0212345678", "010123", "010123456789", "abc", "01012345678x", "1012345678", "82"} {
		if got, ok := NormalizePhone(in); ok {
			t.Errorf("NormalizePhone(%q) = %q — 휴대폰 번호가 아니다", in, got)
		}
	}
}
