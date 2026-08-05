package app

import (
	"net/http"

	"github.com/emirue/ondolith/internal/admin"
)

// buildTree is the operating route table: every Phase 1 screen, with the D11
// id, the D15 security class and the permission each one requires.
//
// Nothing reaches the mux except through the registry (D15 4.4). That is what
// makes the boot checks meaningful — a handler attached with mux.HandleFunc
// carries no screen id, so no check can see it, and the checks would report
// "all clear" on a tree with a hole in it.
//
// Permission is "" only where D11 says the screen is open to anonymous callers.
// It is a declaration either way: the field cannot be skipped, only decided.
//
// The field does NOT enforce. Only the tree gate (admin.access) and each
// handler's own check refuse a request (D15 4.2) — what this declaration buys is
// the boot check: a key that does not exist stops the server, and a permission
// no route names is reported as dead weight in the role editor.
func buildTree(pub *publicDeps, lg *loginDeps, acc *accountDeps, bd *boardDeps,
	ad *admin.Deps, static http.HandlerFunc,
) *Registry {
	r := NewRegistry()

	// ---- 공개 ---------------------------------------------------------------
	r.Add(Route{Screen: "P-201", Method: "GET", Pattern: "/{$}", Class: SC1, Handler: pub.home})
	r.Add(Route{Screen: "P-906", Method: "GET", Pattern: "/static/{path...}", Class: SC7, Handler: static})

	// ---- 인증 ---------------------------------------------------------------
	r.Add(Route{Screen: "P-101", Method: "GET", Pattern: "/login", Class: SC2, Handler: lg.loginForm})
	r.Add(Route{Screen: "P-101", Method: "POST", Pattern: "/login", Class: SC2, Handler: lg.login})
	r.Add(Route{Screen: "P-102", Method: "POST", Pattern: "/logout", Class: SC2, Handler: lg.logout})
	r.Add(Route{Screen: "P-103", Method: "GET", Pattern: "/signup", Class: SC2, Handler: acc.signupForm})
	r.Add(Route{Screen: "P-103", Method: "POST", Pattern: "/signup", Class: SC2, Handler: acc.signup})
	r.Add(Route{Screen: "P-112", Method: "GET", Pattern: "/verify/{token}", Class: SC2, Handler: acc.verify})

	// ---- 내 계정 (SC-3: 소유자는 세션이 정한다) --------------------------------
	r.Add(Route{Screen: "P-108", Method: "GET", Pattern: "/me", Class: SC3, Handler: acc.profileForm})
	r.Add(Route{Screen: "P-108", Method: "POST", Pattern: "/me", Class: SC3, Handler: acc.updateProfile})
	r.Add(Route{Screen: "P-109", Method: "GET", Pattern: "/me/password", Class: SC3, Handler: acc.passwordForm})
	r.Add(Route{Screen: "P-109", Method: "POST", Pattern: "/me/password", Class: SC3, Handler: acc.changePassword})

	// ---- 게시판 (SC-1 은 읽기, SC-2 는 폼, SC-3 은 본인 글) ---------------------
	// Permission is "" on all of them: the board's permission is scoped, so it
	// cannot be judged from the path alone (the slug names the board, and the
	// board id is what role_permissions holds). Every handler calls CanOn with
	// the board it just loaded — D15 4.2's "the gate sees a prefix, the handler
	// sees the target", in its sharpest form.
	r.Add(Route{Screen: "P-203", Method: "GET", Pattern: "/board/{slug}", Class: SC1,
		Handler: bd.boardList})
	r.Add(Route{Screen: "P-205", Method: "GET", Pattern: "/board/{slug}/write", Class: SC2,
		Handler: bd.postForm})
	r.Add(Route{Screen: "P-205", Method: "POST", Pattern: "/board/{slug}/write", Class: SC2,
		Handler: bd.postCreate})
	r.Add(Route{Screen: "P-204", Method: "GET", Pattern: "/board/{slug}/{id}", Class: SC1,
		Handler: bd.postView})
	r.Add(Route{Screen: "P-206", Method: "GET", Pattern: "/board/{slug}/{id}/edit", Class: SC3,
		Handler: bd.postEditForm})
	r.Add(Route{Screen: "P-206", Method: "POST", Pattern: "/board/{slug}/{id}/edit", Class: SC3,
		Handler: bd.postUpdate})

	r.Add(Route{Screen: "P-207", Method: "POST", Pattern: "/board/{slug}/{id}/delete", Class: SC3,
		Handler: bd.postDelete})
	r.Add(Route{Screen: "P-208", Method: "POST", Pattern: "/board/{slug}/{id}/comments", Class: SC2,
		Handler: bd.commentCreate})
	// P-209·P-210 은 경로에 slug 가 없다 (D11). 게시판은 댓글의 글을 거쳐
	// 찾고, 그 게시판의 post.read 가 여전히 판정한다 — 그러지 않으면 댓글
	// id 가 못 여는 게시판으로 들어가는 문이 된다.
	r.Add(Route{Screen: "P-209", Method: "GET", Pattern: "/comments/{id}/edit", Class: SC3,
		Handler: bd.commentEditForm})
	r.Add(Route{Screen: "P-209", Method: "POST", Pattern: "/comments/{id}/edit", Class: SC3,
		Handler: bd.commentUpdate})
	r.Add(Route{Screen: "P-210", Method: "POST", Pattern: "/comments/{id}/delete", Class: SC3,
		Handler: bd.commentDelete})

	// ---- 관리자 -------------------------------------------------------------
	// admin.access is the tree gate's key; every screen below also names the
	// permission its own handler checks. The two are not redundant: the gate
	// sees a path prefix, the handler sees the target (D15 4.2).
	r.Add(Route{Screen: "A-101", Method: "GET", Pattern: "/admin/{$}", Class: SC4,
		Permission: "admin.access", Handler: ad.Dashboard})

	r.Add(Route{Screen: "A-201", Method: "GET", Pattern: "/admin/settings", Class: SC4,
		Permission: "settings.update", Handler: ad.SettingsForm})
	r.Add(Route{Screen: "A-201", Method: "POST", Pattern: "/admin/settings", Class: SC5,
		Permission: "settings.update", Handler: ad.SettingsSave})
	r.Add(Route{Screen: "A-205", Method: "GET", Pattern: "/admin/settings/mail", Class: SC4,
		Permission: "settings.update", Handler: ad.MailSettingsForm})
	r.Add(Route{Screen: "A-205", Method: "POST", Pattern: "/admin/settings/mail", Class: SC5,
		Permission: "settings.update", Handler: ad.MailSettingsSave})

	r.Add(Route{Screen: "A-202", Method: "GET", Pattern: "/admin/themes", Class: SC4,
		Permission: "theme.view", Handler: ad.ThemeList})
	r.Add(Route{Screen: "A-202", Method: "POST", Pattern: "/admin/themes", Class: SC5,
		Permission: "theme.activate", Handler: ad.ThemeActivate})

	r.Add(Route{Screen: "A-204", Method: "GET", Pattern: "/admin/menus", Class: SC4,
		Permission: "menu.manage", Handler: ad.MenuList})
	r.Add(Route{Screen: "A-204", Method: "POST", Pattern: "/admin/menus", Class: SC5,
		Permission: "menu.manage", Handler: ad.MenuCreate})
	r.Add(Route{Screen: "A-204", Method: "POST", Pattern: "/admin/menus/{id}", Class: SC5,
		Permission: "menu.manage", Handler: ad.MenuUpdate})
	r.Add(Route{Screen: "A-204", Method: "POST", Pattern: "/admin/menus/{id}/delete", Class: SC5,
		Permission: "menu.manage", Handler: ad.MenuDelete})

	r.Add(Route{Screen: "A-301", Method: "GET", Pattern: "/admin/pages", Class: SC4,
		Permission: "page.view", Handler: ad.PageList})
	r.Add(Route{Screen: "A-302", Method: "GET", Pattern: "/admin/pages/{id}", Class: SC4,
		Permission: "page.update", Handler: ad.PageForm})
	r.Add(Route{Screen: "A-302", Method: "POST", Pattern: "/admin/pages/{id}", Class: SC5,
		Permission: "page.update", Handler: ad.PageSave})
	r.Add(Route{Screen: "A-303", Method: "POST", Pattern: "/admin/pages/{id}/publish", Class: SC5,
		Permission: "page.publish", Handler: ad.PagePublish})
	// D11 gives A-301 only GET; D15 2.2 puts page.delete on A-302, and D19 reads
	// the two together as "삭제는 A-302 의 POST 동작이다".
	r.Add(Route{Screen: "A-302", Method: "POST", Pattern: "/admin/pages/{id}/delete", Class: SC5,
		Permission: "page.delete", Handler: ad.PageDelete})

	r.Add(Route{Screen: "A-401", Method: "GET", Pattern: "/admin/users", Class: SC4,
		Permission: "user.view", Handler: ad.UserList})
	r.Add(Route{Screen: "A-402", Method: "GET", Pattern: "/admin/users/{id}", Class: SC4,
		Permission: "user.update", Handler: ad.UserDetail})
	r.Add(Route{Screen: "A-402", Method: "POST", Pattern: "/admin/users/{id}", Class: SC5,
		Permission: "user.create", Handler: ad.UserCreate})
	// The three destructive account operations enforce re-authentication in the
	// handlers (D15 5.3-1), not here. A wrapper was written first and removed:
	// it duplicated a check the handlers already make, and the only behaviour it
	// added — a redirect to the form instead of a 403 — no mutation of it could
	// be made to fail. One enforced copy beats two, one of which nobody can
	// prove is running.
	r.Add(Route{Screen: "A-402", Method: "POST", Pattern: "/admin/users/{id}/deactivate", Class: SC5,
		Permission: "user.update", Handler: ad.UserDeactivate})
	r.Add(Route{Screen: "A-402", Method: "POST", Pattern: "/admin/users/{id}/delete", Class: SC5,
		Permission: "user.delete", Handler: ad.UserDelete})
	r.Add(Route{Screen: "A-402", Method: "POST", Pattern: "/admin/users/{id}/reset-password", Class: SC5,
		Permission: "user.reset_password", Handler: ad.UserResetPassword})

	r.Add(Route{Screen: "A-403", Method: "GET", Pattern: "/admin/roles", Class: SC4,
		Permission: "role.view", Handler: ad.RoleList})
	r.Add(Route{Screen: "A-404", Method: "POST", Pattern: "/admin/roles/{id}/permissions", Class: SC5,
		Permission: "role.manage", Handler: ad.RoleGrantPermission})
	r.Add(Route{Screen: "A-405", Method: "POST", Pattern: "/admin/users/{id}/roles", Class: SC5,
		Permission: "role.assign", Handler: ad.RoleAssign})

	r.Add(Route{Screen: "A-602", Method: "GET", Pattern: "/admin/system", Class: SC4,
		Permission: "settings.view", Handler: ad.System})

	// P-202 is last on purpose: `/{slug}` matches anything one segment long, and
	// ServeMux prefers the more specific pattern regardless of registration
	// order — but reading it here in the order a person would guess is worth the
	// line.
	r.Add(Route{Screen: "P-202", Method: "GET", Pattern: "/{slug}", Class: SC1, Handler: pub.page})

	return r
}
