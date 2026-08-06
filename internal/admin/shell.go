// Package admin builds the administrator tree.
//
// The screens here answer to D15's SC-4 (read) and SC-5/SC-6 (change)
// checklists. The tree gate has already checked admin.access by the time a
// handler runs; every handler still checks its own permission, because the gate
// saw a path prefix and not a target (D15 4.2).
package admin

import (
	"net/http"
	"sort"
)

// NavItem is one entry in the administrator menu.
type NavItem struct {
	Screen     string
	Title      string
	Path       string
	Group      string
	Permission string
	Order      int
	// Shop marks a commerce screen. cms 모드에서는 라우트가 등록되지 않으므로
	// 메뉴에도 없어야 한다 — 부팅 점검이 "메뉴가 없는 경로를 가리킨다" 로
	// 잡는다.
	Shop bool
}

// nav is the administrator menu.
//
// A-307·A-308 의 Permission 은 전역 post.moderate · comment.moderate 다.
// 화면의 실제 판정은 게시판 단위이지만(D15 2.4), 메뉴는 게시판을 모른다 —
// 전역 부여를 가진 사람에게만 보여 주는 쪽이, admin.access 만 있는 사람에게
// 열리지 않는 메뉴를 보여 주는 것보다 낫다. 숨기는 것은 UX 이고 경로는
// 스스로 검사한다 (D15 4.3).
//
// 메뉴는 등록된 화면에서 파생되어야 하지만(D20) 아직은 별도 목록이다.
// 부팅 시 라우트와 대조해 죽은 링크를 막는다 (internal/app/app.go).
//
// D20 says the menu comes from the route table. A hand-kept menu drifts the
// moment a screen is added or a path changes, and the drift is invisible: the
// screen works, the link does not exist, and nobody finds out until somebody
// looks for it.
var nav = []NavItem{
	{Screen: "A-101", Title: "대시보드", Path: "/admin/", Group: "", Permission: "admin.access", Order: 0},
	{Screen: "A-301", Title: "페이지", Path: "/admin/pages", Group: "콘텐츠", Permission: "page.view", Order: 10},
	{Screen: "A-304", Title: "게시판", Path: "/admin/boards", Group: "콘텐츠", Permission: "board.view", Order: 12},
	{Screen: "A-307", Title: "글 관리", Path: "/admin/posts", Group: "콘텐츠", Permission: "post.moderate", Order: 14},
	{Screen: "A-308", Title: "댓글 관리", Path: "/admin/comments", Group: "콘텐츠", Permission: "comment.moderate", Order: 16},
	{Screen: "A-309", Title: "첨부 관리", Path: "/admin/attachments", Group: "콘텐츠", Permission: "post.moderate", Order: 18},
	{Screen: "A-204", Title: "메뉴", Path: "/admin/menus", Group: "콘텐츠", Permission: "menu.manage", Order: 20},
	{Screen: "A-401", Title: "사용자", Path: "/admin/users", Group: "사용자", Permission: "user.view", Order: 30},
	{Screen: "A-403", Title: "역할·권한", Path: "/admin/roles", Group: "사용자", Permission: "role.view", Order: 40},
	{Screen: "A-201", Title: "사이트 설정", Path: "/admin/settings", Group: "설정", Permission: "settings.update", Order: 50},
	{Screen: "A-205", Title: "메일", Path: "/admin/settings/mail", Group: "설정", Permission: "settings.update", Order: 60},
	{Screen: "A-202", Title: "테마", Path: "/admin/themes", Group: "설정", Permission: "theme.view", Order: 80},
	{Screen: "A-601", Title: "작업 로그", Path: "/admin/oplog", Group: "설정", Permission: "log.view", Order: 85},
	// 커머스 항목. Shop=true 라 site.type=cms 에서는 메뉴에도 트리에도 없다
	// (FR-710) — 등록되지 않은 경로를 가리키는 메뉴는 404 링크이고, 그것은
	// 권한이 없어 안 열리는 링크와 똑같이 보인다.
	{Screen: "A-501", Title: "상품", Path: "/admin/products", Group: "커머스", Permission: "product.view", Order: 22, Shop: true},
	{Screen: "A-509", Title: "카테고리", Path: "/admin/categories", Group: "커머스", Permission: "product.manage", Order: 24, Shop: true},
	{Screen: "A-504", Title: "주문", Path: "/admin/orders", Group: "커머스", Permission: "order.view", Order: 26, Shop: true},
	{Screen: "A-512", Title: "커머스 정책", Path: "/admin/commerce/policy", Group: "커머스", Permission: "settings.update", Order: 28, Shop: true},
	{Screen: "A-207", Title: "약관", Path: "/admin/terms", Group: "커머스", Permission: "settings.update", Order: 30, Shop: true},
	{Screen: "A-208", Title: "사업자 정보", Path: "/admin/business", Group: "커머스", Permission: "settings.update", Order: 32, Shop: true},
	{Screen: "A-508", Title: "결제 대사", Path: "/admin/reconcile", Group: "커머스", Permission: "payment.view", Order: 34, Shop: true},
	{Screen: "A-603", Title: "웹훅 이력", Path: "/admin/webhooks", Group: "커머스", Permission: "payment.view", Order: 36, Shop: true},
	{Screen: "A-514", Title: "스캔 입고", Path: "/admin/scan/receive", Group: "커머스", Permission: "product.manage", Order: 38, Shop: true},
	{Screen: "A-515", Title: "재고 실사", Path: "/admin/scan/stocktake", Group: "커머스", Permission: "product.manage", Order: 40, Shop: true},
	{Screen: "A-517", Title: "스캔 조회", Path: "/admin/scan/lookup", Group: "커머스", Permission: "product.view", Order: 42, Shop: true},
	{Screen: "A-207", Title: "약관", Path: "/admin/terms", Group: "커머스", Permission: "settings.update", Order: 30, Shop: true},
	{Screen: "A-208", Title: "사업자 정보", Path: "/admin/business", Group: "커머스", Permission: "settings.update", Order: 32, Shop: true},
	{Screen: "A-602", Title: "시스템 정보", Path: "/admin/system", Group: "설정", Permission: "settings.view", Order: 90},
}

// NavGroup is the menu as the template draws it.
type NavGroup struct {
	Title string
	Items []NavItem
}

// Nav returns the menu the caller may see.
//
// Hiding an item is UX, not security (D15 4.3) — every path still checks on its
// own. But a menu that advertises screens the caller cannot open is a list of
// things to go and try, so the filter is worth having.
func Nav(can func(string) bool, shop bool) []NavGroup {
	byGroup := map[string][]NavItem{}
	var order []string
	items := append([]NavItem(nil), nav...)
	sort.Slice(items, func(i, j int) bool { return items[i].Order < items[j].Order })

	for _, it := range items {
		if it.Shop && !shop {
			continue
		}
		if it.Permission != "" && !can(it.Permission) {
			continue
		}
		if _, seen := byGroup[it.Group]; !seen {
			order = append(order, it.Group)
		}
		byGroup[it.Group] = append(byGroup[it.Group], it)
	}
	out := make([]NavGroup, 0, len(order))
	for _, g := range order {
		out = append(out, NavGroup{Title: g, Items: byGroup[g]})
	}
	return out
}

// NavScreens is the set of screen ids the menu points at, so a test can compare
// it against the registered routes.
func NavScreens(shop bool) []string {
	out := make([]string, 0, len(nav))
	for _, it := range nav {
		if it.Shop && !shop {
			continue
		}
		out = append(out, it.Screen)
	}
	sort.Strings(out)
	return out
}

// NavPaths is the same for paths.
func NavPaths(shop bool) []string {
	out := make([]string, 0, len(nav))
	for _, it := range nav {
		if it.Shop && !shop {
			continue
		}
		out = append(out, it.Path)
	}
	sort.Strings(out)
	return out
}

// CurrentGroup marks the active item for the template.
func CurrentGroup(groups []NavGroup, path string) string {
	best := ""
	for _, g := range groups {
		for _, it := range g.Items {
			if path == it.Path || (len(path) > len(it.Path) && path[:len(it.Path)] == it.Path &&
				it.Path != "/admin" && path[len(it.Path)] == '/') {
				if len(it.Path) > len(best) {
					best = it.Path
				}
			}
		}
	}
	return best
}

// Forbidden is the one refusal shape for the admin tree.
func Forbidden(w http.ResponseWriter) {
	http.Error(w, "권한이 없습니다.", http.StatusForbidden)
}
