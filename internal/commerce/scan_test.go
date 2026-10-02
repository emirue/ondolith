package commerce

import (
	"errors"
	"strings"
	"testing"
)

// 형식 오류(422)와 없는 조합(404)을 구분한다 — 고치는 사람이 다르다.
// 형식은 스캐너 설정 문제이고, 없는 조합은 라벨이 오래된 것이다.
func TestLooksLikeUUIDSeparatesFormatFromExistence(t *testing.T) {
	for _, ok := range []string{
		"550e8400-e29b-41d4-a716-446655440000",
		"550E8400-E29B-41D4-A716-446655440000",
	} {
		if !looksLikeUUID(ok) {
			t.Errorf("%q 를 형식 오류로 봤다", ok)
		}
	}
	for _, bad := range []string{
		"", "SKU-1234", "550e8400e29b41d4a716446655440000", // 하이픈 없음
		"550e8400-e29b-41d4-a716-44665544000",   // 35자
		"550e8400-e29b-41d4-a716-4466554400000", // 37자
		"550e8400-e29b-41d4-a716-44665544000g",  // 16진수 아님
		"550e8400+e29b-41d4-a716-446655440000",  // 구분자 위치
		"'; DROP TABLE product_variants; --",
	} {
		if looksLikeUUID(bad) {
			t.Errorf("%q 를 형식으로 통과시켰다", bad)
		}
	}
}

// **주문에 없는 조합과 수량 초과는 거부된다** (FR-623).
func TestCheckPickRefusesWrongItemAndOverCount(t *testing.T) {
	lines := []PickLine{
		{VariantID: "v1", ProductName: "티셔츠", Ordered: 2},
		{VariantID: "v2", ProductName: "모자", Ordered: 1},
	}
	scanned := map[string]int{}

	if err := CheckPick(lines, scanned, "v9", 1); !errors.Is(err, ErrPickNotInOrder) {
		t.Errorf("주문에 없는 조합 = %v, want ErrPickNotInOrder", err)
	}

	for i := range 2 {
		if err := CheckPick(lines, scanned, "v1", 1); err != nil {
			t.Fatalf("%d번째 정상 스캔이 막혔다: %v", i+1, err)
		}
		scanned["v1"]++
	}
	err := CheckPick(lines, scanned, "v1", 1)
	if !errors.Is(err, ErrPickOverCount) {
		t.Fatalf("수량 초과 = %v, want ErrPickOverCount", err)
	}
	// 무엇이 몇 개짜리인지 말해 줘야 사람이 고칠 수 있다.
	if !strings.Contains(err.Error(), "티셔츠") {
		t.Errorf("오류에 상품명이 없다: %v", err)
	}
}

// 전 품목 대조 완료가 판정된다. 하나라도 모자라면 완료가 아니다.
func TestPickCompleteNeedsEveryLine(t *testing.T) {
	lines := []PickLine{
		{VariantID: "v1", Ordered: 2},
		{VariantID: "v2", Ordered: 1},
	}
	if PickComplete(lines, map[string]int{"v1": 2}) {
		t.Error("한 품목이 빠졌는데 완료로 봤다")
	}
	if PickComplete(lines, map[string]int{"v1": 1, "v2": 1}) {
		t.Error("수량이 모자라는데 완료로 봤다")
	}
	if !PickComplete(lines, map[string]int{"v1": 2, "v2": 1}) {
		t.Error("전부 대조했는데 완료가 아니다")
	}
	// 빈 목록은 완료가 아니다 — 아무것도 없는 주문을 "다 챙겼다" 로 읽으면
	// 없는 주문에 대해서도 완료가 찍힌다.
	if PickComplete(nil, map[string]int{}) {
		t.Error("빈 목록을 완료로 봤다")
	}
}

// **수량을 한 번에 세도 누적이 주문 수량을 넘지 못한다** (FR-623, W3-43).
//
// 스캔 1번이 1개이던 때는 `이미 센 수 >= 주문 수량` 만 보면 됐다. 수량 칸이
// 생기면 그 비교는 3개 주문에 2개를 센 뒤 2개를 더 세는 것을 통과시킨다.
func TestCheckPickCountsAQuantityAtOnce(t *testing.T) {
	lines := []PickLine{{VariantID: "v1", ProductName: "티셔츠", Ordered: 3}}

	if err := CheckPick(lines, map[string]int{}, "v1", 3); err != nil {
		t.Errorf("주문 수량만큼 한 번에 세는 것이 막혔다: %v", err)
	}
	if err := CheckPick(lines, map[string]int{}, "v1", 4); !errors.Is(err, ErrPickOverCount) {
		t.Errorf("한 번에 4개 = %v, want ErrPickOverCount", err)
	}
	if err := CheckPick(lines, map[string]int{"v1": 2}, "v1", 2); !errors.Is(err, ErrPickOverCount) {
		t.Errorf("2개 센 뒤 2개 = %v, want ErrPickOverCount", err)
	}
	if err := CheckPick(lines, map[string]int{"v1": 2}, "v1", 1); err != nil {
		t.Errorf("2개 센 뒤 1개가 막혔다: %v", err)
	}
	for _, bad := range []int{0, -1} {
		if err := CheckPick(lines, map[string]int{}, "v1", bad); !errors.Is(err, ErrQuantityRange) {
			t.Errorf("수량 %d = %v, want ErrQuantityRange", bad, err)
		}
	}
}

// One 은 정확 일치 하나만 고른다. 이름으로 찾은 것과 둘 이상은 고르지 않는다.
func TestVariantMatchesOnePicksOnlyASingleExactMatch(t *testing.T) {
	a, b := ScannedVariant{ID: "a"}, ScannedVariant{ID: "b"}

	got, err := (&VariantMatches{Rows: []ScannedVariant{a}, Exact: true}).One()
	if err != nil || got.ID != "a" {
		t.Errorf("정확 일치 하나 = %v, %v", got, err)
	}
	if _, err := (&VariantMatches{Rows: []ScannedVariant{a, b}, Exact: true}).One(); !errors.Is(err, ErrPickAmbiguous) {
		t.Errorf("정확 일치 둘 = %v, want ErrPickAmbiguous", err)
	}
	// 이름 부분 일치는 하나여도 고르지 않는다 — 확인이 아니라 선택이 된다.
	if _, err := (&VariantMatches{Rows: []ScannedVariant{a}}).One(); !errors.Is(err, ErrPickNotInOrder) {
		t.Errorf("이름 일치 하나 = %v, want ErrPickNotInOrder", err)
	}
	if _, err := (&VariantMatches{}).One(); !errors.Is(err, ErrPickNotInOrder) {
		t.Errorf("일치 없음 = %v, want ErrPickNotInOrder", err)
	}
}
