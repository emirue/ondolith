package secretbox

import (
	"errors"
	"strings"
	"testing"
)

func box(t *testing.T) (*Box, string) {
	t.Helper()
	k, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(k)
	if err != nil {
		t.Fatal(err)
	}
	return b, k
}

func TestSealOpenRoundTrip(t *testing.T) {
	b, _ := box(t)
	sealed, err := b.Seal("pg.secret_key", "test_sk_abc")
	if err != nil {
		t.Fatal(err)
	}
	if !IsSealed(sealed) || strings.Contains(sealed, "test_sk_abc") {
		t.Fatalf("봉인된 값에 평문이 보인다: %q", sealed)
	}
	got, was, err := b.Open("pg.secret_key", sealed)
	if err != nil || !was || got != "test_sk_abc" {
		t.Fatalf("Open = (%q, %v, %v)", got, was, err)
	}
	// 같은 평문도 매번 다르게 봉인된다 (난수 nonce).
	again, _ := b.Seal("pg.secret_key", "test_sk_abc")
	if again == sealed {
		t.Error("두 번 봉인한 값이 같다 — nonce 가 고정됐다")
	}
}

func TestWrongKeyAndMovedValueDoNotOpen(t *testing.T) {
	b, _ := box(t)
	sealed, _ := b.Seal("pg.secret_key", "test_sk_abc")

	other, _ := box(t)
	if _, _, err := other.Open("pg.secret_key", sealed); !errors.Is(err, ErrCorrupt) {
		t.Errorf("다른 키로 열렸다: %v", err)
	}
	// 설정 키 이름이 AAD 다: 값을 다른 설정 자리로 옮기면 열리지 않는다.
	if _, _, err := b.Open("mail.smtp_password", sealed); !errors.Is(err, ErrCorrupt) {
		t.Errorf("다른 설정 이름으로 열렸다: %v", err)
	}
	if _, _, err := b.Open("pg.secret_key", sealed[:len(sealed)-2]+"AA"); !errors.Is(err, ErrCorrupt) {
		t.Errorf("손상된 값이 열렸다: %v", err)
	}
}

func TestLegacyPlaintextPassesThroughUnsealed(t *testing.T) {
	b, _ := box(t)
	got, was, err := b.Open("pg.secret_key", "test_sk_plain")
	if err != nil || was || got != "test_sk_plain" {
		t.Fatalf("평문 = (%q, %v, %v)", got, was, err)
	}
	if s, _ := b.Seal("x", ""); s != "" {
		t.Errorf("빈 값을 봉인했다: %q — 「설정 없음」 비교가 깨진다", s)
	}
}

func TestKeyMustBe32Bytes(t *testing.T) {
	for _, bad := range []string{"", "abc", "AAAA", strings.Repeat("A", 20)} {
		if _, err := New(bad); !errors.Is(err, ErrBadKey) {
			t.Errorf("New(%q) = %v, want ErrBadKey", bad, err)
		}
	}
}
