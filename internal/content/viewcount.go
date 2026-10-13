package content

import "sync"

// viewBuffer gathers P-204's view counts between flushes (FR-504).
//
// 글을 열 때마다 쓰던 판은 조회 하나가 HOT 이 아닌 갱신 하나였다 — view_count 가 조회순
// 인덱스의 키라 posts 의 모든 인덱스에 새 항목이 생기고 표가 서서히 부풀었다. 모아 두면 많이
// 읽히는 글도 반영 한 번에 한 줄이다.
//
// ponytail: 프로세스 안의 기억이다. 비정상 종료하면 마지막 반영 뒤의 조회수(viewFlushEvery
// 만큼)를 잃는다 — 조회수는 돈도 권한도 아니라 그 값을 받아들인다. 정상 종료에는 남은 것을
// 쓴다(app.go cleanup).
type viewBuffer struct {
	mu sync.Mutex
	n  map[string]int64
}

func (b *viewBuffer) add(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.n == nil {
		b.n = map[string]int64{}
	}
	b.n[id]++
}

// take hands over everything gathered so far and starts empty.
func (b *viewBuffer) take() map[string]int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	got := b.n
	b.n = nil
	return got
}

// putBack returns counts a flush could not write, merged with what came since.
func (b *viewBuffer) putBack(got map[string]int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.n == nil {
		b.n = map[string]int64{}
	}
	for id, n := range got {
		b.n[id] += n
	}
}
