package content

import (
	"errors"
	"testing"
	"time"
)

func allow(...string) bool { return true }

// names flattens a tree to "a>b" paths so the assertions read as the menu does.
func names(nodes []*MenuNode, prefix string) []string {
	var out []string
	for _, n := range nodes {
		p := n.Title
		if prefix != "" {
			p = prefix + ">" + n.Title
		}
		out = append(out, p)
		out = append(out, names(n.Children, p)...)
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("메뉴 = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("메뉴 = %v, want %v", got, want)
		}
	}
}

func TestBuildMenuNests(t *testing.T) {
	items := []MenuItem{
		{ID: "1", Title: "회사", URL: "/about", Sort: 1},
		{ID: "2", Title: "연혁", URL: "/history", Sort: 1, ParentID: "1"},
		{ID: "3", Title: "공지", URL: "/notice", Sort: 2},
	}
	got, err := BuildMenu(items, func(string, string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	eq(t, names(got, ""), []string{"회사", "회사>연혁", "공지"})
}

func TestBuildMenuSortsBySortOrder(t *testing.T) {
	items := []MenuItem{
		{ID: "1", Title: "셋", URL: "/c", Sort: 3},
		{ID: "2", Title: "하나", URL: "/a", Sort: 1},
		{ID: "3", Title: "둘", URL: "/b", Sort: 2},
		{ID: "4", Title: "자식둘", URL: "/a2", Sort: 2, ParentID: "2"},
		{ID: "5", Title: "자식하나", URL: "/a1", Sort: 1, ParentID: "2"},
	}
	got, err := BuildMenu(items, func(string, string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	eq(t, names(got, ""), []string{"하나", "하나>자식하나", "하나>자식둘", "둘", "셋"})
}

// A foreign key cannot see a cycle (D30 3절), so a bad row reaches this code.
// Returning an error is the difference between a 500 and a hung request that
// holds a connection until the timeout.
func TestBuildMenuRejectsCycle(t *testing.T) {
	cases := map[string][]MenuItem{
		"자기 자신이 부모": {
			{ID: "1", Title: "a", URL: "/a", ParentID: "1"},
		},
		"두 항목이 서로 부모": {
			{ID: "1", Title: "a", URL: "/a", ParentID: "2"},
			{ID: "2", Title: "b", URL: "/b", ParentID: "1"},
		},
		"세 항목 순환": {
			{ID: "1", Title: "a", URL: "/a", ParentID: "3"},
			{ID: "2", Title: "b", URL: "/b", ParentID: "1"},
			{ID: "3", Title: "c", URL: "/c", ParentID: "2"},
		},
	}
	for name, items := range cases {
		t.Run(name, func(t *testing.T) {
			// A bare `<-done` would hang with the code under test instead of
			// reporting it: removing the cycle check made this spin until the
			// 10-minute CI timeout, which reads as "the suite is stuck", not
			// "the guard is gone". Time it out here so the guard's absence is
			// a failure with a message.
			done := make(chan error, 1)
			go func() {
				_, err := BuildMenu(items, allowAll)
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, ErrMenuCycle) {
					t.Errorf("err = %v, want ErrMenuCycle", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("BuildMenu 가 돌아오지 않는다 — 순환에서 무한 루프")
			}
		})
	}
}

func allowAll(string, string) bool { return true }

func TestBuildMenuRejectsDanglingParent(t *testing.T) {
	items := []MenuItem{{ID: "1", Title: "a", URL: "/a", ParentID: "없음"}}
	if _, err := BuildMenu(items, allowAll); !errors.Is(err, ErrMenuParent) {
		t.Errorf("err = %v, want ErrMenuParent", err)
	}
}

func TestPermissionFilterRemovesItems(t *testing.T) {
	items := []MenuItem{
		{ID: "1", Title: "공개", URL: "/pub"},
		{ID: "2", Title: "비공개", URL: "/secret", Permission: "post.read", Board: "hidden"},
	}
	got, err := BuildMenu(items, func(p, b string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	eq(t, names(got, ""), []string{"공개"})
}

// The rule W1-12 names: a parent must not survive as an orphan heading after
// its children are filtered away. A heading that opens nothing both looks
// broken and advertises that something is there.
func TestParentWithAllChildrenRemovedDisappears(t *testing.T) {
	items := []MenuItem{
		{ID: "1", Title: "게시판"}, // heading: no URL of its own
		{ID: "2", Title: "비밀", URL: "/b/secret", Permission: "post.read", Board: "secret", ParentID: "1"},
		{ID: "3", Title: "공지", URL: "/notice"},
	}
	got, err := BuildMenu(items, func(p, b string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	eq(t, names(got, ""), []string{"공지"})
}

// ...but a parent that is itself a link stays, because removing it would take
// away a page the caller may open.
func TestParentWithOwnURLSurvivesChildRemoval(t *testing.T) {
	items := []MenuItem{
		{ID: "1", Title: "게시판", URL: "/boards"},
		{ID: "2", Title: "비밀", URL: "/b/secret", Permission: "post.read", Board: "secret", ParentID: "1"},
	}
	got, err := BuildMenu(items, func(p, b string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	eq(t, names(got, ""), []string{"게시판"})
}

// Removal is bottom-up: a grandchild going away must be able to take its parent
// and grandparent with it in one pass.
func TestRemovalPropagatesUpMultipleLevels(t *testing.T) {
	items := []MenuItem{
		{ID: "1", Title: "1층"},
		{ID: "2", Title: "2층", ParentID: "1"},
		{ID: "3", Title: "3층", URL: "/deep", Permission: "post.read", ParentID: "2"},
		{ID: "4", Title: "남는 것", URL: "/keep"},
	}
	got, err := BuildMenu(items, func(p, b string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	eq(t, names(got, ""), []string{"남는 것"})
}

// The permission and the board must both reach the check: a menu item scoped to
// one board that asked only by permission would show every board's entry.
func TestFilterPassesPermissionAndBoard(t *testing.T) {
	items := []MenuItem{
		{ID: "1", Title: "자유", URL: "/b/free", Permission: "post.read", Board: "free"},
		{ID: "2", Title: "공지", URL: "/b/notice", Permission: "post.read", Board: "notice"},
	}
	got, err := BuildMenu(items, func(p, b string) bool { return p == "post.read" && b == "free" })
	if err != nil {
		t.Fatal(err)
	}
	eq(t, names(got, ""), []string{"자유"})
}

func TestNilCanFuncKeepsEverything(t *testing.T) {
	items := []MenuItem{{ID: "1", Title: "a", URL: "/a", Permission: "x"}}
	got, err := BuildMenu(items, nil)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, names(got, ""), []string{"a"})
}
