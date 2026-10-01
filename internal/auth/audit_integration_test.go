package auth

import (
	"context"
	"testing"
)

// 게시판 범위 권한은 게시판마다 한 행이다. 역할 화면(A-403)의 「N개 권한」이 그
// 행 수를 세고 있었다 — 같은 권한을 두 게시판에 주면 2개로 보였다.
func TestRolePermissionsAreListedOncePerKey(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`INSERT INTO roles (key, name) VALUES ('mod_x', '모더레이터')`); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"b1", "b2"} {
		var board string
		if err := pool.QueryRow(ctx,
			`INSERT INTO boards (slug, name) VALUES ($1, $1) RETURNING id`, slug).Scan(&board); err != nil {
			t.Fatal(err)
		}
		if err := s.GrantPermission(ctx, "mod_x", "post.moderate", BoardID(board)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.GrantPermission(ctx, "mod_x", "post.read_secret", Global); err != nil {
		t.Fatal(err)
	}
	want := []string{"post.moderate", "post.read_secret"}
	same := func(got []string) bool {
		return len(got) == 2 && got[0] == want[0] && got[1] == want[1]
	}

	one, err := s.RoleByKey(ctx, "mod_x")
	if err != nil {
		t.Fatal(err)
	}
	if !same(one.Permissions) {
		t.Errorf("RoleByKey 권한 %v, want %v", one.Permissions, want)
	}

	all, err := s.Roles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range all {
		if r.Key != "mod_x" {
			continue
		}
		found = true
		if !same(r.Permissions) {
			t.Errorf("Roles 권한 %v, want %v", r.Permissions, want)
		}
	}
	if !found {
		t.Fatal("Roles 에 mod_x 가 없다 — 검사가 헛돌았다")
	}
}
