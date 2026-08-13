package auth

import (
	"encoding/base64"
	"strings"
	"testing"
)

// **DB 없이 도는 검사가 이 파일의 전부다.** token.go 의 순수 함수 넷은 지금까지
// `ONDOLITH_TEST_DSN` 이 있어야만 도는 통합 테스트 뒤에 있었고, 그래서 기본
// 실행(`make check`)에서 한 번도 실행되지 않았다. `newRawToken` 이 상수를
// 돌려주도록 고쳐도 auth·app 전 패키지가 초록이었다 — 즉 시스템의 모든 재설정·
// 인증 토큰이 같은 값이 되는 변경을 게이트가 통과시킨다 (직접 넣어 확인했다).
// 이 함수들은 DB 를 하나도 쓰지 않으므로 DB 뒤에 둘 이유가 없다.

func TestNewRawTokenIsUnpredictable(t *testing.T) {
	const n = 64
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		raw, err := newRawToken()
		if err != nil {
			t.Fatalf("newRawToken: %v", err)
		}
		if seen[raw] {
			t.Fatalf("%d 번째에 같은 토큰이 다시 나왔다: %q — 재설정 토큰이 추측 가능해진다", i, raw)
		}
		seen[raw] = true

		// 256 비트여야 한다. 짧아지면 무차별 대입의 값이 달라진다.
		b, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil {
			t.Fatalf("RawURLEncoding 으로 디코드되지 않는다 (%q): %v", raw, err)
		}
		if len(b) != 32 {
			t.Fatalf("토큰이 %d 바이트다 — 32 여야 한다 (%q)", len(b), raw)
		}
		// 주소에 그대로 들어간다 (`/password/reset/{token}`). 패딩이나 `+`·`/`
		// 가 섞이면 이스케이프 없이 못 쓴다.
		if strings.ContainsAny(raw, "+/=") {
			t.Fatalf("경로에 넣을 수 없는 문자가 있다: %q", raw)
		}
	}
}

func TestHashTokenIsStableAndDistinct(t *testing.T) {
	a, b := hashToken("같은 값"), hashToken("같은 값")
	if string(a) != string(b) {
		t.Fatal("같은 입력에 다른 해시가 나왔다 — 조회가 성립하지 않는다 (bcrypt 가 아닌 이유)")
	}
	if len(a) != 32 {
		t.Fatalf("해시가 %d 바이트다 — SHA-256 은 32 다", len(a))
	}
	if string(hashToken("다른 값")) == string(a) {
		t.Fatal("다른 입력에 같은 해시가 나왔다")
	}
	if string(a) == "같은 값" {
		t.Fatal("원문이 그대로 저장된다 — 덤프를 그대로 재생할 수 있게 된다")
	}
}

func TestKindMapsToItsOwnTableAndTTL(t *testing.T) {
	if KindEmailVerify.table() == KindPasswordReset.table() {
		t.Fatal("두 종류가 같은 테이블을 가리킨다 — 한쪽 토큰으로 다른 쪽이 열린다")
	}
	if KindEmailVerify.ttl() != EmailVerifyTTL {
		t.Errorf("이메일 인증 TTL = %v, 기대 %v", KindEmailVerify.ttl(), EmailVerifyTTL)
	}
	if KindPasswordReset.ttl() != PasswordResetTTL {
		t.Errorf("비밀번호 재설정 TTL = %v, 기대 %v", KindPasswordReset.ttl(), PasswordResetTTL)
	}
	if PasswordResetTTL <= 0 || EmailVerifyTTL <= 0 {
		t.Fatal("TTL 이 0 이하다 — 발급 즉시 만료거나 영구 유효다")
	}
}

// sprintfTable 은 SQL 문자열을 만든다. 입력이 닫힌 집합이라 안전하다는 것이
// 근거이므로, 치환이 실제로 그 자리만 바꾸는지는 확인해 둔다.
func TestSprintfTableSubstitutesOnlyThePlaceholder(t *testing.T) {
	for _, c := range []struct{ in, table, want string }{
		{"SELECT 1 FROM %s WHERE id = $1", "tok", "SELECT 1 FROM tok WHERE id = $1"},
		{"no placeholder", "tok", "no placeholder"},
		{"%s %s", "t", "t t"},
		// `%` 하나만 있는 경우와 끝에 오는 경우 — 잘라먹으면 SQL 이 깨진다.
		{"100%", "t", "100%"},
		{"a %d b", "t", "a %d b"},
	} {
		if got := sprintfTable(c.in, c.table); got != c.want {
			t.Errorf("sprintfTable(%q, %q) = %q, 기대 %q", c.in, c.table, got, c.want)
		}
	}
}
