package auth

import (
	"context"
	"slices"
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

	one, err := s.RoleByKey(ctx, "mod_x")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(one.Permissions, want) {
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
		if !slices.Equal(r.Permissions, want) {
			t.Errorf("Roles 권한 %v, want %v", r.Permissions, want)
		}
	}
	if !found {
		t.Fatal("Roles 에 mod_x 가 없다 — 검사가 헛돌았다")
	}
}

// A-401: 쪽을 먼저 고르고 역할은 그 쪽의 회원에게만 모은다. 쪽의 크기는 **회원**
// 수다 — 역할이 둘인 회원이 두 자리를 차지하지 않는다. 순서는 가입 최신순이고
// 쪽을 이어 붙이면 전원이 한 번씩 나온다.
func TestListUsersPagesUsersNotRoleRows(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()

	oldest := mkUser(t, s, "a@example.com")
	middle := mkUser(t, s, "b@example.com")
	newest := mkUser(t, s, "c@example.com")
	grantRole(t, pool, middle, "operator")
	grantRole(t, pool, middle, "admin")
	grantRole(t, pool, oldest, "operator")

	var ids []string
	roles := map[string][]string{}
	for offset := 0; offset < 4; offset += 2 {
		page, err := s.ListUsers(ctx, 2, offset)
		if err != nil {
			t.Fatal(err)
		}
		if offset == 0 && len(page) != 2 {
			t.Fatalf("첫 쪽 %d명, want 2 — 역할 행이 쪽의 자리를 차지했다", len(page))
		}
		for _, u := range page {
			ids = append(ids, u.ID)
			roles[u.ID] = u.Roles
		}
	}
	if want := []string{newest, middle, oldest}; !slices.Equal(ids, want) {
		t.Errorf("두 쪽을 이은 순서 %v, want %v (가입 최신순, 한 번씩)", ids, want)
	}
	if want := []string{"admin", "operator"}; !slices.Equal(roles[middle], want) {
		t.Errorf("역할 둘인 회원 %v, want %v (키 순)", roles[middle], want)
	}
	if !slices.Equal(roles[oldest], []string{"operator"}) {
		t.Errorf("역할 하나인 회원 %v, want [operator] — 남의 역할이 섞였다", roles[oldest])
	}
	if len(roles[newest]) != 0 {
		t.Errorf("역할 없는 회원 %v, want 빈 목록", roles[newest])
	}
}
