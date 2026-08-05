package commerce

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// chain builds a→b→c…: "a" 가 최상위, 각 원소의 부모가 앞 원소다.
func chain(ids ...string) map[string]string {
	m := map[string]string{}
	for i, id := range ids {
		if i == 0 {
			m[id] = ""
			continue
		}
		m[id] = ids[i-1]
	}
	return m
}

func TestCheckReparent(t *testing.T) {
	// a → b → c → d
	tree := chain("a", "b", "c", "d")
	tree["x"] = "" // 별개 최상위

	cases := []struct {
		why              string
		child, newParent string
		wantErr          error
	}{
		{"남의 가지로 옮기는 것은 정상이다", "b", "x", nil},
		{"최상위로 올리기", "c", "", nil},
		{"자기 자신을 부모로", "b", "b", ErrCategoryCycle},
		{"직속 자식을 부모로", "b", "c", ErrCategoryCycle},
		{"먼 자손을 부모로 — CHECK (parent_id <> id) 는 이것을 못 잡는다",
			"a", "d", ErrCategoryCycle},
		{"없는 카테고리", "없음", "x", ErrCategoryMissing},
		{"없는 부모", "b", "없음", ErrCategoryMissing},
		{"부모를 자기 부모로 두는 것은 변화가 없지만 합법이다", "c", "b", nil},
	}
	for _, c := range cases {
		err := CheckReparent(tree, c.child, c.newParent)
		if !errors.Is(err, c.wantErr) {
			t.Errorf("%s → %s: %v, want %v — %s", c.child, c.newParent, err, c.wantErr, c.why)
		}
	}
}

// 상한은 폭주 방지턱이다. 없으면 A-509 에서 열 번 누른 것이 백 단계가 되고,
// 그 트리를 그리는 화면과 질의가 함께 느려진다.
func TestCategoryDepthLimit(t *testing.T) {
	// 최상위부터 MaxCategoryDepth 단계까지 이어진 사슬.
	ids := make([]string, MaxCategoryDepth+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("c%d", i)
	}
	deep := chain(ids...)
	deep["new"] = ""

	// 마지막 노드 아래로 하나 더 = 상한 초과.
	if err := CheckReparent(deep, "new", ids[len(ids)-1]); !errors.Is(err, ErrCategoryDepth) {
		t.Errorf("상한 초과 = %v, want ErrCategoryDepth", err)
	}
	// 한 단계 위는 통과한다 — 경계가 상한과 같은 값이어야 한다.
	if err := CheckReparent(deep, "new", ids[len(ids)-2]); err != nil {
		t.Errorf("상한 이내가 막혔다: %v", err)
	}
}

// child 만 보면 10단계 서브트리를 9단계 아래로 옮기는 것이 통과한다. 옮긴 뒤의
// 깊이는 하위 트리까지 합친 값이다.
func TestDepthCountsTheSubtreeBeingMoved(t *testing.T) {
	// 옮겨 갈 곳: a → b (깊이 1)
	// 옮길 것:  s0 → s1 → … → s8 (높이 8)
	tree := chain("a", "b")
	subIDs := make([]string, 9)
	for i := range subIDs {
		subIDs[i] = fmt.Sprintf("s%d", i)
	}
	for k, v := range chain(subIDs...) {
		tree[k] = v
	}

	// b 아래(깊이 2)로 높이 8 짜리를 넣으면 2+8 = 10 — 상한과 같으므로 통과.
	if err := CheckReparent(tree, "s0", "b"); err != nil {
		t.Errorf("2 + 8 = 10 단계가 막혔다: %v", err)
	}
	// 한 단계 더 깊은 곳으로 넣으면 11 — 거부.
	tree["c"] = "b"
	if err := CheckReparent(tree, "s0", "c"); !errors.Is(err, ErrCategoryDepth) {
		t.Errorf("3 + 8 = 11 단계 = %v, want ErrCategoryDepth", err)
	}
}

// 저장된 데이터가 이미 깨져 있어도 요청이 끝나야 한다. 여기서 도는 것은
// 사용자에게 500 이 아니라 응답 없음으로 보인다.
func TestBrokenHierarchyDoesNotSpin(t *testing.T) {
	// p → q → p 고리에 r 이 매달려 있다.
	broken := map[string]string{"p": "q", "q": "p", "r": "p", "z": ""}

	done := make(chan error, 1)
	go func() { done <- CheckReparent(broken, "z", "r") }()
	select {
	case err := <-done:
		// 순환이라고 말해야 한다. "깊이 상한" 으로 답하면 운영자가 계층을
		// 줄이려 들고, 줄일 수 있는 계층이 아니다.
		if !errors.Is(err, ErrCategoryCycle) {
			t.Errorf("깨진 계층 = %v, want ErrCategoryCycle", err)
		}
	case <-timeoutAfterSeconds(5):
		t.Fatal("깨진 계층에서 돌았다 — 요청이 끝나지 않는다")
	}
}

// 서브트리 높이 자체도 깨진 데이터에서 멈춰야 한다.
func TestSubtreeHeightStopsOnBrokenData(t *testing.T) {
	broken := map[string]string{"p": "q", "q": "p"}
	done := make(chan int, 1)
	go func() { done <- subtreeHeight(broken, "p") }()
	select {
	case h := <-done:
		if h > MaxCategoryDepth+1 {
			t.Errorf("높이 %d — 상한에서 자르지 않았다", h)
		}
	case <-timeoutAfterSeconds(5):
		t.Fatal("subtreeHeight 가 돌았다")
	}
}

func timeoutAfterSeconds(n int) <-chan time.Time { return time.After(time.Duration(n) * time.Second) }
