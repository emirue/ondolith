package content

import "errors"

var (
	ErrMenuCycle  = errors.New("content: 메뉴 parent_id 에 순환이 있습니다")
	ErrMenuParent = errors.New("content: 존재하지 않는 상위 메뉴입니다")
)

// MenuItem is one row of menus, flat, as it comes out of the database.
type MenuItem struct {
	ID       string
	ParentID string // empty for a top-level item
	Title    string
	URL      string
	Sort     int
	// Permission gates the item. Empty means everyone sees it. A-204 stores
	// this so that a private board does not leak through the menu — hiding is
	// not security (D15 4.3), but a menu that advertises what you cannot open
	// is a directory of targets.
	Permission string
	// Board scopes Permission when the target is one board.
	Board string
}

// MenuNode is the assembled tree handed to the theme.
type MenuNode struct {
	MenuItem
	Children []*MenuNode
}

// CanFunc answers the permission question. Passing it in keeps this package
// free of the auth import and lets the filter be tested with a plain closure.
type CanFunc func(permission, board string) bool

// BuildMenu turns the flat rows into a tree, dropping what the caller may not
// see. Ordering follows sort_order then insertion, matching the index the
// query reads (D30 menus_parent_sort_idx).
//
// A cycle in parent_id returns an error rather than looping. The database
// cannot prevent one — a foreign key does not see cycles (D30 3절) — so the
// only thing standing between a bad row and a hung request is this check.
func BuildMenu(items []MenuItem, can CanFunc) ([]*MenuNode, error) {
	byID := make(map[string]*MenuNode, len(items))
	for _, it := range items {
		byID[it.ID] = &MenuNode{MenuItem: it}
	}

	// Every parent must exist. A dangling parent_id would otherwise silently
	// drop a whole subtree from the menu with nothing to explain why.
	for _, it := range items {
		if it.ParentID == "" {
			continue
		}
		if _, ok := byID[it.ParentID]; !ok {
			return nil, ErrMenuParent
		}
	}

	// Walk each item up to the root. Bounded by the number of items, so a cycle
	// is detected instead of spun on.
	for _, it := range items {
		seen := map[string]struct{}{it.ID: {}}
		for cur := it.ParentID; cur != ""; {
			if _, loop := seen[cur]; loop {
				return nil, ErrMenuCycle
			}
			seen[cur] = struct{}{}
			cur = byID[cur].ParentID
		}
	}

	var roots []*MenuNode
	for _, it := range items {
		n := byID[it.ID]
		if it.ParentID == "" {
			roots = append(roots, n)
			continue
		}
		p := byID[it.ParentID]
		p.Children = append(p.Children, n)
	}

	sortNodes(roots)
	return filter(roots, can), nil
}

func sortNodes(nodes []*MenuNode) {
	// Insertion sort: menus are tens of items, and a stable sort keeps rows
	// with equal sort_order in query order.
	for i := 1; i < len(nodes); i++ {
		for j := i; j > 0 && nodes[j].Sort < nodes[j-1].Sort; j-- {
			nodes[j], nodes[j-1] = nodes[j-1], nodes[j]
		}
	}
	for _, n := range nodes {
		sortNodes(n.Children)
	}
}

// filter removes what the caller cannot see, bottom-up.
//
// The rule that matters: a parent whose children are all removed is removed
// too, unless it is a link in its own right. Otherwise the menu keeps a heading
// that opens nothing — which both looks broken and tells the visitor that
// something exists behind it.
func filter(nodes []*MenuNode, can CanFunc) []*MenuNode {
	if can == nil {
		return nodes
	}
	var out []*MenuNode
	for _, n := range nodes {
		n.Children = filter(n.Children, can)
		if n.Permission != "" && !can(n.Permission, n.Board) {
			continue
		}
		// A node with no URL is a heading: it exists only to hold children.
		if n.URL == "" && len(n.Children) == 0 {
			continue
		}
		out = append(out, n)
	}
	return out
}
