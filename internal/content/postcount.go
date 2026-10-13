package content

import (
	"sync"
	"time"
)

// postCounts remembers each board's published-post count — P-203's 「전체 N건」.
//
// 세는 비용은 게시판 글 수에 비례한다(50만 건 게시판에서 캐시 밖 183ms, 2026-10-13) —
// 매 요청 세던 판은 글이 쌓일수록 모든 목록이 느려졌다. 글을 쓰거나 숨기거나 지우면
// 기억을 비우고 다음 요청이 다시 센다. 쓰기는 읽기보다 훨씬 드물다.
//
// **세는 사이에 쓰기가 있었으면 그 수는 버린다** (gen). 세기를 시작한 요청이 쓰기 전
// 스냅샷의 수를 쓰기 뒤에 넣으면, 비운 자리에 옛 수가 countTTL 동안 남는다.
//
// DB 의 집계 열(트리거)로 두지 않은 이유: 글 하나마다 게시판 한 행을 고쳐 그 행에 쓰기가
// 몰리고 팽창한다. 데이터만 붓는 복원(`pg_restore --data-only`)은 트리거를 다시 돌려 수를
// 틀리게 한다.
//
// ponytail: 한 프로세스 안의 기억이다. 바이너리 하나가 DB 하나를 쓰는 배포(NFR-102)에서는
// 쓰기가 모두 이 저장소를 지난다. 프로세스 밖에서 posts 를 고치면(psql 등) countTTL 뒤에
// 보인다 — 여러 프로세스를 띄우게 되면 DB 의 집계 표로 옮긴다.
type postCounts struct {
	mu  sync.Mutex
	m   map[string]postCount
	gen uint64
}

type postCount struct {
	n  int64
	at time.Time
}

// countTTL bounds how stale a count can be when a write did not pass through
// this process.
const countTTL = time.Minute

// get returns the remembered count, or the generation a fresh count must be
// stored under.
func (c *postCounts) get(board string, now time.Time) (n int64, ok bool, gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[board]
	if !ok || now.Sub(e.at) > countTTL {
		return 0, false, c.gen
	}
	return e.n, true, c.gen
}

// put stores n unless a write reset the counts since get handed out gen.
func (c *postCounts) put(board string, n int64, now time.Time, gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		return
	}
	if c.m == nil {
		c.m = map[string]postCount{}
	}
	c.m[board] = postCount{n: n, at: now}
}

// reset forgets every board. 글 하나가 어느 게시판의 것인지 묻는 질의를 쓰기마다 더하는
// 것보다, 게시판 수만큼 다시 세는 편이 싸다.
func (c *postCounts) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	clear(c.m)
}
