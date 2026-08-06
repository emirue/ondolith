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

	if err := CheckPick(lines, scanned, "v9"); !errors.Is(err, ErrPickNotInOrder) {
		t.Errorf("주문에 없는 조합 = %v, want ErrPickNotInOrder", err)
	}

	for i := range 2 {
		if err := CheckPick(lines, scanned, "v1"); err != nil {
			t.Fatalf("%d번째 정상 스캔이 막혔다: %v", i+1, err)
		}
		scanned["v1"]++
	}
	err := CheckPick(lines, scanned, "v1")
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
