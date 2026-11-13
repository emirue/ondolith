package auth

import (
	"errors"
	"strings"
)

// The ladder blocks of D15 5.1. Every one of them answers the same question:
// can the caller end up with more power than they started with? A hole in any
// of them makes the operator/administrator boundary in D15 1.2 decorative.
//
// These live next to CanOn rather than in the handlers because a rule written
// once per screen is a rule that is missing from a screen.
var (
	// R1
	ErrSelfRoleChange = errors.New("auth: 자기 계정의 역할은 자기 요청으로 바꿀 수 없다 (R1)")
	// R2
	ErrGrantExceedsOwn = errors.New("auth: 자기 유효 권한을 넘는 역할은 부여할 수 없다 (R2)")
	// R3
	ErrSuperuserRoleAssign = errors.New("auth: superuser 역할은 superuser 만 부여·회수한다 (R3)")
	// R4
	ErrAddUnheldPermission = errors.New("auth: 자기가 갖지 않은 권한은 어떤 역할에도 추가할 수 없다 (R4)")
	// R5
	ErrSuperuserRoleEdit = errors.New("auth: superuser 역할의 권한 구성은 편집 대상이 아니다 (R5)")
	// R6
	ErrSuperuserAccountOp = errors.New("auth: superuser 보유자에 대한 계정 조작은 superuser 만 한다 (R6)")
	// R9
	ErrEveryoneRolePermission = errors.New("auth: 모든 요청이 갖는 역할(anonymous·member)에는 방문자 권한만 준다 (R9)")
	// D15 2.4
	ErrScopeNotAllowed = errors.New("auth: 스코프 부여는 is_scoped 권한에만 허용된다")
	// D41 4절
	ErrScreenScopeNotAllowed = errors.New("auth: 테마 화면 스코프는 theme.edit_content 에만, 게시판 없이 허용된다")
)

// Actor is the caller, as far as these rules are concerned.
type Actor struct {
	UserID string
	// Perms is the caller's own effective permission set. R2 and R4 compare
	// against this, which is why they cannot be evaluated in the database.
	Perms *Permissions
}

// Role is the role being granted or edited.
type Role struct {
	Key string
	// Superuser marks the one role that bypasses the grant table. R3 and R5
	// hang off this flag rather than off the key, so renaming `admin` cannot
	// disarm them.
	Superuser bool
	// Permissions the role holds, as permission keys. Used by R2.
	Permissions []string
	// Assignable is roles.is_assignable — anonymous·member 는 모두가 이미 갖고 있어 사람에게
	// 부여하지 않는다. A-405 의 선택 목록이 이것으로 거른다(부여해도 0행이라 성공처럼 보였다).
	Assignable bool
}

// SameID reports whether two user ids name the same uuid.
//
// PostgreSQL accepts one uuid in several spellings — 대문자, 중괄호, 하이픈 생략 —
// and a string `==` reads them as two people: a request that names itself in
// capitals walked past R1 and the last-superuser guard. Ids from the database
// are already canonical; ids from a request path are not.
func SameID(a, b string) bool { return canonicalID(a) == canonicalID(b) }

var idNoise = strings.NewReplacer("-", "", "{", "", "}", "")

func canonicalID(s string) string { return strings.ToLower(idNoise.Replace(s)) }

// CanAssignRole applies R1, R2 and R3 to "give target this role" and to
// "take it away" alike — R1 says both directions, and R3 says 부여·회수.
func CanAssignRole(actor Actor, targetUserID string, role Role) error {
	// R1 first: it has no exception, not even for a superuser. The rule exists
	// because `role.assign` alone would otherwise be a path to any role at all,
	// and "I only added it to myself" is exactly that path.
	if SameID(actor.UserID, targetUserID) {
		return ErrSelfRoleChange
	}
	if role.Superuser && !actor.Perms.isSuperuser() {
		return ErrSuperuserRoleAssign
	}
	// R2: the role must be a subset of what the caller already holds. Without
	// it, `role.assign` hands administrator to an accomplice.
	for _, p := range role.Permissions {
		if !actor.Perms.Can(p) {
			return ErrGrantExceedsOwn
		}
	}
	return nil
}

// visitorPermissions are what the roles every request carries may hold (R9): 게시판 프리셋이
// anonymous·member 에 주는 읽기·쓰기·댓글이 전부다(content.presetGrants). post.read_secret 은 빠진다 —
// 남의 비밀글을 모두에게 여는 것이고, 프리셋도 주지 않는다. 프리셋이 이 밖의 것을 주면
// app 의 시험(TestPresetsGiveEveryoneOnlyVisitorPermissions)이 잡는다.
var visitorPermissions = map[string]bool{"post.read": true, "post.write": true, "comment.write": true}

// IsVisitorPermission reports whether p may sit on a role every request carries (R9).
func IsVisitorPermission(p string) bool { return visitorPermissions[p] }

// VisitorPermissions lists them for the queries that filter on load.
func VisitorPermissions() []string { return []string{"comment.write", "post.read", "post.write"} }

// EveryoneRole reports whether key is a role every request carries: anonymous 는 모든 요청,
// member 는 로그인한 모든 요청 (EffectiveRoles, LoadPermissions 가 이 두 키로 붙인다).
func EveryoneRole(key string) bool { return key == RoleAnonymous || key == RoleMember }

// CanAddPermissionToRole applies R4, R5 and R9 to editing a role's composition.
func CanAddPermissionToRole(actor Actor, role Role, permission string) error {
	// R5 before R4: the superuser role is not editable by anyone, so checking
	// R4 first would answer "you lack that permission" to a superuser and
	// "allowed" to nobody — the wrong reason for the right refusal.
	if role.Superuser {
		return ErrSuperuserRoleEdit
	}
	// R9 before R4: superuser 도 막는다 — 그 역할에 준 관리 권한은 가입한 누구에게나 간다.
	if EveryoneRole(role.Key) && !IsVisitorPermission(permission) {
		return ErrEveryoneRolePermission
	}
	if !actor.Perms.Can(permission) {
		return ErrAddUnheldPermission
	}
	return nil
}

// CanRemovePermissionFromRole applies R5 and R4 to taking a permission out of a role. R9 does not apply:
// 모두가 갖는 역할에 잘못 들어간 권한을 빼는 것이 이 길의 목적이다.
func CanRemovePermissionFromRole(actor Actor, role Role, permission string) error {
	if role.Superuser {
		return ErrSuperuserRoleEdit
	}
	if !actor.Perms.Can(permission) {
		return ErrAddUnheldPermission
	}
	return nil
}

// CanOperateOnAccount applies R6: deactivating, reactivating, deleting or
// force-resetting the password of a superuser holder. Without it R3 is theatre — the role survives
// while its holder is switched off, which reaches the same end by another road.
func CanOperateOnAccount(actor Actor, targetHoldsSuperuser bool) error {
	if targetHoldsSuperuser && !actor.Perms.isSuperuser() {
		return ErrSuperuserAccountOp
	}
	return nil
}

func (p *Permissions) isSuperuser() bool { return p != nil && p.Superuser }

// ValidateGrantScope enforces D15 2.4: only permissions marked is_scoped may
// carry a board_id. The database cannot express this — a CHECK constraint
// cannot read another table — so it is an application invariant, and an
// invariant with two implementations has one that is wrong. The grant handler
// and the seed both call this.
//
// scoped reports whether the permission being granted is is_scoped.
func ValidateGrantScope(permissionIsScoped bool, board BoardID) error {
	if board != Global && !permissionIsScoped {
		return ErrScopeNotAllowed
	}
	return nil
}

// ValidateScreenGrant enforces D41 4절: a theme screen scope belongs to
// theme.edit_content alone and never rides with a board. The column CHECK only
// sees its own row's shape, not which permission the row names, so the grant
// handler and the seed share this function — 같은 이유로 ValidateGrantScope 가 있다.
func ValidateScreenGrant(perm string, board BoardID, screen string) error {
	if screen == "" {
		return nil
	}
	if perm != ThemeContentPermission || board != Global {
		return ErrScreenScopeNotAllowed
	}
	return nil
}
