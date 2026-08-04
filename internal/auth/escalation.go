package auth

import "errors"

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
	// D15 2.4
	ErrScopeNotAllowed = errors.New("auth: 스코프 부여는 is_scoped 권한에만 허용된다")
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
}

// CanAssignRole applies R1, R2 and R3 to "give target this role" and to
// "take it away" alike — R1 says both directions, and R3 says 부여·회수.
func CanAssignRole(actor Actor, targetUserID string, role Role) error {
	// R1 first: it has no exception, not even for a superuser. The rule exists
	// because `role.assign` alone would otherwise be a path to any role at all,
	// and "I only added it to myself" is exactly that path.
	if actor.UserID == targetUserID {
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

// CanAddPermissionToRole applies R4 and R5 to editing a role's composition.
func CanAddPermissionToRole(actor Actor, role Role, permission string) error {
	// R5 before R4: the superuser role is not editable by anyone, so checking
	// R4 first would answer "you lack that permission" to a superuser and
	// "allowed" to nobody — the wrong reason for the right refusal.
	if role.Superuser {
		return ErrSuperuserRoleEdit
	}
	if !actor.Perms.Can(permission) {
		return ErrAddUnheldPermission
	}
	return nil
}

// CanOperateOnAccount applies R6: deactivating, deleting or force-resetting the
// password of a superuser holder. Without it R3 is theatre — the role survives
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
