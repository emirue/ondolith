package auth

import (
	"context"
	"testing"
)

// **사이트 권한은 부팅마다 넣고, 이름이 바뀌면 고치며, 두 번 넣어도 하나다** (D42 7절). 역할 편집기는 키 옆에 그
// 이름을 보인다 — 사이트 권한은 키만으로는 무슨 화면인지 모른다.
func TestEnsurePermissionUpsertsTheName(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	for _, name := range []string{"인부 관리", "인부 관리", "인부·입금 관리"} {
		if err := s.EnsurePermission(ctx, "site.workers", name); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := s.PermissionKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, k := range keys {
		if k == "site.workers" {
			n++
		}
	}
	names, err := s.PermissionNames(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || names["site.workers"] != "인부·입금 관리" || names["admin.access"] == "" {
		t.Errorf("행 %d개(want 1), 이름 %q, 코어 권한 이름 %q", n, names["site.workers"], names["admin.access"])
	}
}
