package commerce

import (
	"strings"
	"testing"
	"time"
)

var orderNoAt = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

// SC-3 3항: 주문번호는 순번이 아니다. 순번이면 번호 하나로 이웃 주문을 훑을 수
// 있고, P-504 비회원 조회가 정확히 그 번호를 입력으로 받는다.
func TestOrderNoIsNotSequential(t *testing.T) {
	const n = 500
	seen := map[string]bool{}
	var prev string
	for i := range n {
		got := NewOrderNo(orderNoAt)
		if seen[got] {
			t.Fatalf("주문번호가 겹쳤다: %s", got)
		}
		seen[got] = true

		// 연속 두 개가 인접하면 순번이다. 같은 날짜 접두사를 뺀 나머지를 비교한다.
		if i > 0 {
			a := strings.SplitN(prev, "-", 2)[1]
			b := strings.SplitN(got, "-", 2)[1]
			if adjacent(a, b) {
				t.Errorf("연속한 두 주문번호가 인접하다: %s → %s", prev, got)
			}
			// 앞자리가 계속 같으면 뒷자리만 도는 순번이다.
			if a[:4] == b[:4] {
				t.Logf("주의: 앞 4자리가 같다 (%s, %s) — 우연이면 무시", a, b)
			}
		}
		prev = got
	}
	if len(seen) != n {
		t.Errorf("%d개 생성에 고유 %d개", n, len(seen))
	}
}

// adjacent reports whether b is a's immediate successor in the alphabet — the
// shape a sequence has.
func adjacent(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	// 마지막 글자만 한 칸 다르고 나머지가 같으면 순번이다.
	if a[:len(a)-1] != b[:len(b)-1] {
		return false
	}
	ia := strings.IndexByte(orderNoAlphabet, a[len(a)-1])
	ib := strings.IndexByte(orderNoAlphabet, b[len(b)-1])
	return ia >= 0 && ib == ia+1
}

// 읽어서 옮겨 적는 번호다 (P-504). 헷갈리는 글자를 넣지 않는다.
func TestOrderNoAvoidsConfusableCharacters(t *testing.T) {
	for range 200 {
		got := NewOrderNo(orderNoAt)
		tail := strings.SplitN(got, "-", 2)[1]
		for _, bad := range "01258BILOSZ" {
			if strings.ContainsRune(tail, bad) {
				t.Fatalf("헷갈리는 글자 %q 가 들어갔다: %s", bad, got)
			}
		}
	}
}

// D30: 32자 상한. 사람이 입력하는 값이라 길이도 제약이다.
func TestOrderNoFitsTheColumn(t *testing.T) {
	got := NewOrderNo(orderNoAt)
	if len(got) < 6 || len(got) > 32 {
		t.Errorf("길이 %d — 6~32 범위여야 한다: %s", len(got), got)
	}
	if !strings.HasPrefix(got, "20260805-") {
		t.Errorf("날짜 접두사가 없다: %s", got)
	}
}

// 접두사는 사람이 목록을 읽으라고 있는 것이고, 엔트로피는 뒤에 있다. 날짜가
// 같아도 번호는 겹치지 않는다.
func TestSameDayNumbersStillDiffer(t *testing.T) {
	a := NewOrderNo(orderNoAt)
	b := NewOrderNo(orderNoAt)
	if a == b {
		t.Errorf("같은 날짜에 같은 번호가 나왔다: %s", a)
	}
	if a[:8] != b[:8] {
		t.Errorf("같은 날짜인데 접두사가 다르다: %s, %s", a, b)
	}
}
