package content

import (
	"testing"
	"time"
)

// 기억은 countTTL 이 지나면 버린다 — 이 프로세스를 지나지 않은 쓰기가 보이는 한계다.
func TestRememberedCountExpires(t *testing.T) {
	var c postCounts
	t0 := time.Unix(1_700_000_000, 0)
	_, _, gen := c.get("b", t0)
	c.put("b", 7, t0, gen)
	if n, ok, _ := c.get("b", t0.Add(countTTL)); !ok || n != 7 {
		t.Errorf("TTL 끝에서 = %d·%v, want 7·true", n, ok)
	}
	if _, ok, _ := c.get("b", t0.Add(countTTL+time.Second)); ok {
		t.Error("TTL 이 지났는데 기억을 썼다")
	}
}

// 세는 사이에 쓰기가 있었으면 그 수는 넣지 않는다 — 넣으면 비운 자리에 쓰기 전의 수가
// countTTL 동안 남는다.
func TestCountStartedBeforeAWriteIsNotStored(t *testing.T) {
	var c postCounts
	t0 := time.Unix(1_700_000_000, 0)
	_, _, gen := c.get("b", t0) // 읽기가 세기 시작
	c.reset()                   // 그 사이 글이 써졌다
	c.put("b", 5, t0, gen)      // 쓰기 전 스냅샷의 수
	if n, ok, _ := c.get("b", t0); ok {
		t.Errorf("쓰기 전의 수 %d 가 기억에 남았다", n)
	}
}
