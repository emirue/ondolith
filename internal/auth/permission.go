// Package auth decides what a request is allowed to do.
//
// The decision itself is a pure function over a permission set that was loaded
// once at the start of the request (D15 4.3-1). Nothing here touches the
// database: keeping the judgement separate from the query is what lets every
// rule in D15 be tested without a server, and what stops "we already checked in
// the middleware" from becoming a reason to skip the real check (D15 4.2).
package auth

// BoardID scopes a grant to one board. The zero value means "every board",
// which is how role_permissions stores a global grant (board_id IS NULL).
type BoardID string

// Global is the absence of a board scope, on both sides of the comparison: a
// grant with Global applies everywhere, and a question asked with Global is
// about a resource that has no board.
const Global BoardID = ""

// Grant is one row of role_permissions, already resolved to the permission key.
type Grant struct {
	Permission string
	// Board is Global for a grant that applies to every board.
	Board BoardID
}

// Permissions is what a request may do. Build it once per request from the
// caller's effective roles (D15 4.3-1) and ask it as often as needed — every
// question after that is memory, which is why hiding a menu item per entry
// costs no extra query (D15 4.3).
type Permissions struct {
	// Superuser bypasses the grant table entirely. That is not a shortcut: it
	// is what makes a release that adds a permission avoid a per-installation
	// data migration, and what stops an administrator from locking everyone
	// out by editing their own role (D15 1.3).
	Superuser bool

	global map[string]struct{}
	scoped map[string]map[BoardID]struct{}
}

// NewPermissions builds the set. A caller with no grants gets a set that says
// no to everything — the zero value is fail-closed, which matters because a
// board with no grant rows must be invisible (D15 2.4).
func NewPermissions(superuser bool, grants []Grant) *Permissions {
	p := &Permissions{
		Superuser: superuser,
		global:    make(map[string]struct{}),
		scoped:    make(map[string]map[BoardID]struct{}),
	}
	for _, g := range grants {
		if g.Board == Global {
			p.global[g.Permission] = struct{}{}
			continue
		}
		if p.scoped[g.Permission] == nil {
			p.scoped[g.Permission] = make(map[BoardID]struct{})
		}
		p.scoped[g.Permission][g.Board] = struct{}{}
	}
	return p
}

// CanOn reports whether perm is held for board, per D15 2.4:
//
//	CanOn(perm, board) = ∃ row (role ∈ 유효역할) ∧ (permission = perm)
//	                          ∧ (board_id IS NULL ∨ board_id = board)
//
// There is no implication between permissions (D15 2.1): holding board.manage
// does not answer board.view. An implication rule always grows an exception,
// and an exception in this function is a hole in every screen at once.
func (p *Permissions) CanOn(perm string, board BoardID) bool {
	if p == nil {
		return false
	}
	if p.Superuser {
		return true
	}
	if _, ok := p.global[perm]; ok {
		return true
	}
	// No special case for board == Global. NewPermissions files every global
	// grant under p.global, so p.scoped never holds the empty key and the
	// lookup below already answers false — a guard here would be a branch no
	// test could distinguish from its own absence.
	_, ok := p.scoped[perm][board]
	return ok
}

// Can answers for a resource that has no board scope.
func (p *Permissions) Can(perm string) bool { return p.CanOn(perm, Global) }

// Builtin role keys. The set is closed: roles are data an installation may add
// to (D15 P2), but these five are the ones the code reasons about.
const (
	RoleAnonymous = "anonymous"
	RoleMember    = "member"
)

// EffectiveRoles returns the roles a request carries, per D15 1.1:
//
//	익명 요청  → {anonymous}
//	인증 요청  → {anonymous, member} ∪ user_roles 의 역할
//
// anonymous is included for authenticated callers too. Leaving it out produces
// the accident of "the board is open to anonymous but members cannot see it":
// anonymous is the floor every request stands on, not a ceiling.
//
// assigned is what user_roles holds; it is ignored when authenticated is false,
// because an anonymous request has no assignments to carry.
func EffectiveRoles(authenticated bool, assigned []string) []string {
	if !authenticated {
		return []string{RoleAnonymous}
	}
	out := make([]string, 0, len(assigned)+2)
	out = append(out, RoleAnonymous, RoleMember)
	seen := map[string]struct{}{RoleAnonymous: {}, RoleMember: {}}
	for _, r := range assigned {
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return out
}
