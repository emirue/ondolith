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

// **같은 uuid 의 다른 표기도 같은 사람이다.** PostgreSQL 은 대문자·중괄호·하이픈
// 생략을 같은 uuid 로 받는데, 문자열 `==` 는 다른 사람으로 봐서 R1(자기 역할 변경
// 금지)이 비켜 갔다 (2026-10-21 점검).
func TestSameIDReadsUUIDSpellingsAlike(t *testing.T) {
	const id = "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"
	for _, other := range []string{
		"A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11",
		"{a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11}",
		"a0eebc999c0b4ef8bb6d6bb9bd380a11",
	} {
		if !SameID(id, other) {
			t.Errorf("SameID(%q, %q) = false", id, other)
		}
	}
	if SameID(id, "b0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11") {
		t.Error("다른 uuid 를 같다고 했다")
	}
	a := actorWith(id, true)
	if err := CanAssignRole(a, "A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11", Role{Key: "editor"}); !errors.Is(err, ErrSelfRoleChange) {
		t.Errorf("대문자 표기로 자기 역할을 바꿨다: %v", err)
	}
}

// D41 4절의 불변식. DB 는 「theme_screen 은 theme.edit_content 에만」을 말할 수
// 없으므로 부여 처리기와 시드가 이 함수 하나를 지난다.
func TestValidateScreenGrant(t *testing.T) {
	tests := []struct {
		name   string
		perm   string
		board  BoardID
		screen string
		wantOK bool
	}{
		{"화면 없이는 무엇이든 통과", "menu.manage", Global, "", true},
		{"테마 화면 권한에 화면 지정", ThemeContentPermission, Global, "hero", true},
		{"다른 권한에 화면 지정", "menu.manage", Global, "hero", false},
		{"게시판과 화면을 함께", ThemeContentPermission, "free", "hero", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateScreenGrant(tc.perm, tc.board, tc.screen)
			if tc.wantOK && err != nil {
				t.Errorf("허용되어야 하는데 거부됐다: %v", err)
			}
			if !tc.wantOK && !errors.Is(err, ErrScreenScopeNotAllowed) {
				t.Errorf("거부되어야 하는데 %v", err)
			}
		})
	}
}

// **R9: 모든 요청이 갖는 역할(anonymous·member)에는 방문자 권한만 준다.** 그 역할에 준 관리 권한은
// 가입한 누구에게나(member) 또는 아무에게나(anonymous) 간다 — 한 번의 잘못된 부여가 사이트를 연다.
// 방문자 권한은 게시판 프리셋이 그 역할에 주는 것(읽기·쓰기·댓글)이다. 다른 역할은 R9 과 상관없다.
func TestEveryoneRolesTakeOnlyVisitorPermissions(t *testing.T) {
	su := actorWith("u1", true)
	for _, role := range []string{"anonymous", "member"} {
		r := Role{Key: role}
		for _, p := range []string{"admin.access", "settings.update", "post.moderate", "comment.moderate", "post.read_secret", "role.manage"} {
			if err := CanAddPermissionToRole(su, r, p); !errors.Is(err, ErrEveryoneRolePermission) {
				t.Errorf("%s 에 %s = %v, want ErrEveryoneRolePermission", role, p, err)
			}
		}
		for _, p := range []string{"post.read", "post.write", "comment.write"} {
			if err := CanAddPermissionToRole(su, r, p); err != nil {
				t.Errorf("%s 에 방문자 권한 %s 가 거부됐다: %v", role, p, err)
			}
		}
	}
	// 부여 대상 역할은 R9 과 상관없다 — editor·operator 에 admin.access 를 주는 것이 정상 운영이다.
	for _, role := range []string{"editor", "operator", "custom"} {
		if err := CanAddPermissionToRole(su, Role{Key: role}, "admin.access"); err != nil {
			t.Errorf("%s 에 admin.access 가 거부됐다: %v", role, err)
		}
	}
	if !EveryoneRole("anonymous") || !EveryoneRole("member") || EveryoneRole("editor") {
		t.Error("EveryoneRole 이 anonymous·member 만 골라야 한다")
	}
}

// 권한 빼기는 R5(superuser 역할은 편집 대상이 아니다)와 R4(자기가 갖지 않은 권한은 손대지 않는다)를 따른다.
// R9 은 빼기를 막지 않는다 — 잘못 들어간 것을 빼는 것이 이 기능의 목적이다.
func TestRemovingAPermissionFollowsR4AndR5(t *testing.T) {
	if err := CanRemovePermissionFromRole(actorWith("u1", true), Role{Key: "admin", Superuser: true}, "page.view"); !errors.Is(err, ErrSuperuserRoleEdit) {
		t.Errorf("superuser 역할에서 뺐다: %v", err)
	}
	if err := CanRemovePermissionFromRole(actorWith("u1", false, "role.manage"), Role{Key: "editor"}, "page.view"); !errors.Is(err, ErrAddUnheldPermission) {
		t.Errorf("갖지 않은 권한을 뺐다: %v", err)
	}
	if err := CanRemovePermissionFromRole(actorWith("u1", true), Role{Key: "anonymous"}, "admin.access"); err != nil {
		t.Errorf("anonymous 에 잘못 들어간 admin.access 를 빼지 못한다: %v", err)
	}
}
