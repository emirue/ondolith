package auth

import (
	"errors"
	"testing"
)

func actorWith(id string, super bool, perms ...string) Actor {
	g := make([]Grant, 0, len(perms))
	for _, p := range perms {
		g = append(g, Grant{Permission: p, Board: Global})
	}
	return Actor{UserID: id, Perms: NewPermissions(super, g)}
}

// D15 5.1. Each rule gets a violation and an allowed case: a rule that only
// ever refuses is indistinguishable from a function that always refuses, and
// that difference is the whole feature.
func TestLadderRules(t *testing.T) {
	editor := Role{Key: "editor", Permissions: []string{"page.view", "page.create"}}
	admin := Role{Key: "admin", Superuser: true}

	t.Run("R1 자기 역할 변경 거부", func(t *testing.T) {
		a := actorWith("u1", false, "page.view", "page.create")
		if err := CanAssignRole(a, "u1", editor); !errors.Is(err, ErrSelfRoleChange) {
			t.Errorf("자기 자신에게 부여가 허용됐다: %v", err)
		}
		// R1 has no exception — not even for a superuser, who could otherwise
		// use it as the one legitimate-looking self-promotion.
		s := actorWith("u1", true)
		if err := CanAssignRole(s, "u1", editor); !errors.Is(err, ErrSelfRoleChange) {
			t.Errorf("superuser 의 자기 부여가 허용됐다: %v", err)
		}
	})
	t.Run("R1 남에게는 허용", func(t *testing.T) {
		a := actorWith("u1", false, "page.view", "page.create")
		if err := CanAssignRole(a, "u2", editor); err != nil {
			t.Errorf("남에게 부여가 거부됐다: %v", err)
		}
	})

	t.Run("R2 자기 권한을 넘는 역할 부여 거부", func(t *testing.T) {
		a := actorWith("u1", false, "page.view") // page.create 를 갖지 않았다
		if err := CanAssignRole(a, "u2", editor); !errors.Is(err, ErrGrantExceedsOwn) {
			t.Errorf("자기 권한 초과 부여가 허용됐다: %v", err)
		}
	})
	t.Run("R2 부분집합이면 허용", func(t *testing.T) {
		a := actorWith("u1", false, "page.view", "page.create", "settings.update")
		if err := CanAssignRole(a, "u2", editor); err != nil {
			t.Errorf("부분집합 부여가 거부됐다: %v", err)
		}
	})

	t.Run("R3 비-superuser 의 superuser 역할 부여 거부", func(t *testing.T) {
		a := actorWith("u1", false, "role.assign")
		if err := CanAssignRole(a, "u2", admin); !errors.Is(err, ErrSuperuserRoleAssign) {
			t.Errorf("비-superuser 가 superuser 역할을 부여했다: %v", err)
		}
	})
	t.Run("R3 superuser 는 허용", func(t *testing.T) {
		a := actorWith("u1", true)
		if err := CanAssignRole(a, "u2", admin); err != nil {
			t.Errorf("superuser 의 superuser 부여가 거부됐다: %v", err)
		}
	})

	t.Run("R4 미보유 권한을 역할에 추가 거부", func(t *testing.T) {
		a := actorWith("u1", false, "role.manage")
		if err := CanAddPermissionToRole(a, editor, "settings.update"); !errors.Is(err, ErrAddUnheldPermission) {
			t.Errorf("미보유 권한 추가가 허용됐다: %v", err)
		}
	})
	t.Run("R4 보유 권한은 허용", func(t *testing.T) {
		a := actorWith("u1", false, "role.manage", "settings.update")
		if err := CanAddPermissionToRole(a, editor, "settings.update"); err != nil {
			t.Errorf("보유 권한 추가가 거부됐다: %v", err)
		}
	})

	t.Run("R5 superuser 역할 편집 거부", func(t *testing.T) {
		// Even a superuser is refused: the role is not an editable object.
		// Otherwise one mis-click removes a permission from the only role that
		// cannot be repaired from inside the product.
		s := actorWith("u1", true)
		if err := CanAddPermissionToRole(s, admin, "page.view"); !errors.Is(err, ErrSuperuserRoleEdit) {
			t.Errorf("superuser 역할 편집이 허용됐다: %v", err)
		}
	})
	t.Run("R5 평범한 역할은 편집 허용", func(t *testing.T) {
		a := actorWith("u1", false, "role.manage", "page.view")
		if err := CanAddPermissionToRole(a, editor, "page.view"); err != nil {
			t.Errorf("평범한 역할 편집이 거부됐다: %v", err)
		}
	})

	t.Run("R6 superuser 보유자 계정 조작 거부", func(t *testing.T) {
		a := actorWith("u1", false, "user.update", "user.delete")
		if err := CanOperateOnAccount(a, true); !errors.Is(err, ErrSuperuserAccountOp) {
			t.Errorf("비-superuser 가 superuser 보유자를 조작했다: %v", err)
		}
	})
	t.Run("R6 평범한 계정은 허용, superuser 는 전부 허용", func(t *testing.T) {
		a := actorWith("u1", false, "user.update")
		if err := CanOperateOnAccount(a, false); err != nil {
			t.Errorf("평범한 계정 조작이 거부됐다: %v", err)
		}
		s := actorWith("u2", true)
		if err := CanOperateOnAccount(s, true); err != nil {
			t.Errorf("superuser 의 조작이 거부됐다: %v", err)
		}
	})
}

// R1 is checked before R3, so a superuser assigning the superuser role to
// themselves is refused for the right reason. If the order flipped, the message
// would say "only a superuser may do this" to a superuser.
func TestR1PrecedesOtherRules(t *testing.T) {
	s := actorWith("u1", true)
	err := CanAssignRole(s, "u1", Role{Key: "admin", Superuser: true})
	if !errors.Is(err, ErrSelfRoleChange) {
		t.Errorf("R1 이 먼저 걸리지 않았다: %v", err)
	}
}

// D15 2.4: the database cannot express this, so both the grant handler and the
// seed must route through here. The test below is what makes "same function"
// checkable — it is the only definition of the rule in the package.
func TestValidateGrantScope(t *testing.T) {
	tests := []struct {
		name   string
		scoped bool
		board  BoardID
		wantOK bool
	}{
		{"스코프 권한에 게시판 지정", true, "free", true},
		{"스코프 권한을 전역 부여", true, Global, true},
		{"비스코프 권한을 전역 부여", false, Global, true},
		{"비스코프 권한에 게시판 지정", false, "free", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateGrantScope(tc.scoped, tc.board)
			if tc.wantOK && err != nil {
				t.Errorf("허용되어야 하는데 거부됐다: %v", err)
			}
			if !tc.wantOK && !errors.Is(err, ErrScopeNotAllowed) {
				t.Errorf("거부되어야 하는데 통과했다: %v", err)
			}
		})
	}
}
