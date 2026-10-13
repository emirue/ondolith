package content

import "testing"

// 조회는 글마다 모아 두었다가 한 번에 쓴다 — 꺼내면 비고, 꺼낸 뒤의 조회는 다음 몫이다.
func TestViewBufferAddsUpAndEmpties(t *testing.T) {
	var b viewBuffer
	for range 3 {
		b.add("a")
	}
	b.add("b")
	got := b.take()
	if len(got) != 2 || got["a"] != 3 || got["b"] != 1 {
		t.Fatalf("take = %v, want a:3 b:1", got)
	}
	if again := b.take(); len(again) != 0 {
		t.Errorf("꺼낸 뒤 다시 꺼낸 것 = %v, want 비어 있음", again)
	}
	// 쓰지 못한 몫은 되돌려 다음에 다시 쓴다 — 그 사이 들어온 조회와 합쳐진다.
	b.add("a")
	b.putBack(got)
	if back := b.take(); back["a"] != 4 || back["b"] != 1 {
		t.Errorf("되돌린 뒤 = %v, want a:4 b:1", back)
	}
}
