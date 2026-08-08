package commerce

import (
	"errors"
	"fmt"
)

var (
	// ErrCategoryCycle is 자기 자신 또는 자기 자손을 부모로 지정한 경우.
	ErrCategoryCycle = errors.New("commerce: 카테고리 계층에 순환이 생깁니다")
	// ErrCategoryDepth is the runaway guard, not a design limit.
	ErrCategoryDepth = errors.New("commerce: 카테고리 깊이 상한을 넘었습니다")
	// ErrCategoryMissing is a parent that does not exist.
	ErrCategoryMissing = errors.New("commerce: 존재하지 않는 상위 카테고리입니다")
	// ErrCategoryInUse is 소속 상품이나 하위 카테고리가 있어서 지울 수 없는
	// 경우다. DB 의 RESTRICT 가 판정하고 이 값은 그것을 옮긴다 (D19 A-509).
	ErrCategoryInUse = errors.New("commerce: 소속 상품이나 하위 카테고리가 있어 지울 수 없습니다")
)

// MaxCategoryDepth 는 설계 제약이 아니라 폭주 방지턱이다 (D30).
//
// depth 컬럼을 두지 않는 이유도 같다 — 물리화하면 서브트리를 옮길 때마다 갱신
// 코드가 붙고, 그 코드가 틀리면 상한이 지키는 것이 없어진다. 매번 걸어서 센다.
const MaxCategoryDepth = 10

// CheckReparent decides whether `child` may be moved under `newParent`.
//
// parents 는 현재 계층이다: 자식 ID → 부모 ID, 최상위는 빈 문자열. 카테고리는
// 수십 행이라 전부 읽어 와도 되고, 그렇게 하면 이 판정이 순수 함수가 된다.
//
// DB 는 이것을 막지 못한다. FK 는 순환을 보지 못하고 (D30 3절), `parent_id <> id`
// CHECK 는 **직접** 자기 참조만 잡는다 — A → B → A 는 두 행이 각각 합법이다.
// 그래서 이 검사가 유일한 방어이고, 동시 요청 두 건이 각각 통과해 함께 순환을
// 만드는 것은 저장소가 pg_advisory_xact_lock 으로 직렬화해서 막는다.
func CheckReparent(parents map[string]string, child, newParent string) error {
	if _, ok := parents[child]; !ok {
		return fmt.Errorf("%w: %s", ErrCategoryMissing, child)
	}
	if newParent == "" {
		return nil // 최상위로 올리는 것은 언제나 안전하다
	}
	if _, ok := parents[newParent]; !ok {
		return fmt.Errorf("%w: %s", ErrCategoryMissing, newParent)
	}
	if newParent == child {
		return fmt.Errorf("%w: 자기 자신을 부모로 지정했습니다", ErrCategoryCycle)
	}

	// newParent 에서 위로 걸어 올라가며 child 를 만나는지 본다. 만난다면 child 는
	// newParent 의 조상이고, 옮기는 순간 그 구간이 고리가 된다.
	//
	// 위로 거슬러 올라가는 이유는 아래로 훑는 것보다 짧기 때문이 아니라, 아래로
	// 훑으려면 자식 목록을 뒤집어 만들어야 하고 그 역인덱스가 틀리면 검사가
	// 조용히 통과하기 때문이다. 부모는 행이 이미 갖고 있다.
	seen := map[string]struct{}{newParent: {}}
	depth := 1 // newParent 아래로 들어가므로 child 의 깊이는 최소 1
	for cur := parents[newParent]; cur != ""; cur = parents[cur] {
		if cur == child {
			return fmt.Errorf("%w: %s 는 %s 의 자손입니다", ErrCategoryCycle, newParent, child)
		}
		if _, loop := seen[cur]; loop {
			// 이미 저장된 데이터가 깨져 있는 경우다. 여기서 멈추지 않으면
			// 이 함수가 도는 것으로 요청이 끝나지 않는다.
			return fmt.Errorf("%w: 기존 계층에 이미 고리가 있습니다", ErrCategoryCycle)
		}
		seen[cur] = struct{}{}
		depth++
	}
	// 걷는 도중에 깊이를 검사하지 않는다. 아래의 depth+sub 검사가 같은 것을
	// 잡고 (sub >= 0), 루프가 끝나는 것은 seen 이 보장한다 — 둘 다 두었더니
	// 어느 쪽을 지워도 테스트가 울지 않았다. 서로를 가려 주는 방어는 방어가
	// 아니라 검증되지 않은 코드다.

	// child 아래에 달린 것들도 함께 내려간다. 그 깊이까지 봐야 상한이 의미를
	// 갖는다 — child 만 보면 10단계 서브트리를 9단계 아래로 옮기는 것이 통과한다.
	sub := subtreeHeight(parents, child)
	if depth+sub > MaxCategoryDepth {
		return fmt.Errorf("%w: %d 단계 (하위 %d 단계 포함)", ErrCategoryDepth, depth+sub, sub)
	}
	return nil
}

// subtreeHeight returns how many levels hang below root, 0 for a leaf.
//
// 자식 인덱스를 한 번만 만들고 너비 우선으로 내려간다. 깊이는 MaxCategoryDepth
// 로 잘라 — 저장된 데이터가 이미 깨져 있어도 여기서 돌지 않는다.
func subtreeHeight(parents map[string]string, root string) int {
	children := make(map[string][]string, len(parents))
	for id, p := range parents {
		if p != "" {
			children[p] = append(children[p], id)
		}
	}
	level := []string{root}
	height := 0
	for height <= MaxCategoryDepth {
		var next []string
		for _, id := range level {
			next = append(next, children[id]...)
		}
		if len(next) == 0 {
			return height
		}
		level = next
		height++
	}
	return height
}
