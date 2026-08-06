package commerce

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// **배포된 약관을 고치는 경로가 코드에 없다** (D13, FR-619).
//
// `order_agreements` 가 가리키는 본문이 바뀌면 동의 이력이 거짓이 되고,
// "나중에 재현된다" 는 약속이 깨진다. 검사로 막는 것이 아니라 **UPDATE 자체가
// 없어야** 한다 — 검사는 다음 사람이 지울 수 있지만 없는 함수는 못 부른다.
func TestNoUpdatePathForTerms(t *testing.T) {
	src, err := os.ReadFile("terms.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "UPDATE terms") {
		t.Error("terms 를 UPDATE 하는 경로가 있다 — 개정은 새 행이어야 한다")
	}
	if strings.Contains(string(src), "DELETE FROM terms") {
		t.Error("terms 를 삭제하는 경로가 있다 — 동의 이력이 가리키는 행이 사라진다")
	}
}

// 같은 (종류, 버전) 은 두 번 들어가지 않는다. 겹치면 어느 본문에 동의했는지
// 특정할 수 없다.
func TestTermsVersionIsUniquePerKind(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	now := time.Now()
	base := Terms{Kind: "service", Version: "1.0", Body: "본문",
		EffectiveAt: now.Add(24 * time.Hour), Required: true}

	if _, err := s.AddTerms(ctx, base, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddTerms(ctx, base, now); !errors.Is(err, ErrTermsVersionTaken) {
		t.Fatalf("= %v, want ErrTermsVersionTaken", err)
	}
	// 다른 버전은 들어간다 — 위 단언이 "두 번째는 늘 막힌다" 가 아니다.
	next := base
	next.Version = "1.1"
	if _, err := s.AddTerms(ctx, next, now); err != nil {
		t.Errorf("다음 버전이 막혔다: %v", err)
	}
}

// **소급 시행일은 거부한다** (D50). 소급이 되면 "주문 시점에 유효했던 약관" 이
// 나중에 바뀔 수 있고, FR-619 가 기록하는 동의 버전이 재현을 보장하지 못한다.
func TestTermsCannotBeBackdated(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	now := time.Now()

	_, err := s.AddTerms(ctx, Terms{Kind: "service", Version: "0.9", Body: "본문",
		EffectiveAt: now.Add(-48 * time.Hour), Required: true}, now)
	if !errors.Is(err, ErrTermsBackdated) {
		t.Fatalf("= %v, want ErrTermsBackdated", err)
	}
}

// P-405 가 받는 것은 **시행된** 최신 필수 약관이다. 미래 시행 버전은 등록해
// 두고 그날부터 적용된다.
func TestRequiredTermsPicksTheEffectiveLatest(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	now := time.Now()

	for _, tc := range []struct {
		version string
		days    int
		req     bool
	}{
		{"1.0", 0, true},  // 오늘 시행
		{"2.0", 30, true}, // 미래 시행 — 아직 아니다
	} {
		if _, err := s.AddTerms(ctx, Terms{Kind: "service", Version: tc.version,
			Body: "본문 " + tc.version, EffectiveAt: now.AddDate(0, 0, tc.days),
			Required: tc.req}, now); err != nil {
			t.Fatal(err)
		}
	}
	// 선택 약관은 필수 목록에 오지 않는다.
	if _, err := s.AddTerms(ctx, Terms{Kind: "marketing", Version: "1.0",
		Body: "선택", EffectiveAt: now, Required: false}, now); err != nil {
		t.Fatal(err)
	}

	got, err := s.RequiredTerms(ctx, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("필수 약관 %d건, want 1: %+v", len(got), got)
	}
	if got[0].Version != "1.0" {
		t.Errorf("버전 %q, want 1.0 — 미래 시행 버전이 먼저 적용됐다", got[0].Version)
	}

	// 그날이 오면 2.0 이다.
	later, err := s.RequiredTerms(ctx, now.AddDate(0, 0, 31))
	if err != nil {
		t.Fatal(err)
	}
	if len(later) != 1 || later[0].Version != "2.0" {
		t.Errorf("시행일 이후 %+v, want 2.0", later)
	}
}

// 여덟 항목 중 빈 것을 이름으로 알려준다 (FR-711). 저장을 막지는 않는다.
func TestMissingBusinessKeysNamesTheEmptyOnes(t *testing.T) {
	full := map[string]string{}
	for _, k := range BusinessKeys {
		full[k] = "값"
	}
	if got := MissingBusinessKeys(full); len(got) != 0 {
		t.Errorf("다 채웠는데 %v 가 비었다고 한다", got)
	}

	partial := map[string]string{"business.name": "온돌리스"}
	got := MissingBusinessKeys(partial)
	if len(got) != len(BusinessKeys)-1 {
		t.Errorf("빈 항목 %d개, want %d", len(got), len(BusinessKeys)-1)
	}
	for _, name := range got {
		if name == "상호" {
			t.Error("채운 항목이 빈 것으로 나왔다")
		}
	}
	// 이름으로 나와야 한다 — 키(`business.reg_no`)로 나오면 운영자가 못 읽는다.
	if !strings.Contains(strings.Join(got, ","), "사업자등록번호") {
		t.Errorf("항목 이름이 아니라 키가 나온다: %v", got)
	}
}
