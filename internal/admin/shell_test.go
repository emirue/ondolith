package admin

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func allowOnly(perms ...string) func(string) bool {
	set := map[string]bool{}
	for _, p := range perms {
		set[p] = true
	}
	return func(p string) bool { return set[p] }
}

func flatten(groups []NavGroup) []string {
	var out []string
	for _, g := range groups {
		for _, it := range g.Items {
			out = append(out, it.Screen)
		}
	}
	return out
}

// A menu that advertises screens the caller cannot open is a list of things to
// go and try. Hiding is UX (D15 4.3) — the paths still check — but the list
// should not be a directory of targets.
func TestNavHidesItemsWithoutPermission(t *testing.T) {
	got := flatten(Nav(allowOnly("admin.access", "page.view")))
	want := map[string]bool{"A-101": true, "A-301": true}

	if len(got) != len(want) {
		t.Fatalf("항목 %v, want %v", got, want)
	}
	for _, s := range got {
		if !want[s] {
			t.Errorf("권한 없는 항목이 렌더링됐다: %s", s)
		}
	}
}

func TestNavShowsEverythingToASuperuser(t *testing.T) {
	got := flatten(Nav(func(string) bool { return true }))
	if len(got) != len(nav) {
		t.Errorf("항목 %d개, want %d개", len(got), len(nav))
	}
}

func TestNavIsEmptyWithoutPermissions(t *testing.T) {
	if got := Nav(func(string) bool { return false }); len(got) != 0 {
		t.Errorf("권한이 없는데 %d 그룹이 나왔다", len(got))
	}
}

func TestNavIsOrdered(t *testing.T) {
	groups := Nav(func(string) bool { return true })
	last := -1
	for _, g := range groups {
		for _, it := range g.Items {
			if it.Order < last {
				t.Errorf("정렬이 깨졌다: %s(%d) 가 %d 뒤에 왔다", it.Screen, it.Order, last)
			}
			last = it.Order
		}
	}
}

// Every menu entry must name a real permission — a typo means the item is
// invisible to everyone and nobody notices, because an invisible menu item
// looks exactly like one you lack the permission for.
func TestNavPermissionsAreSpelledLikePermissions(t *testing.T) {
	for _, it := range nav {
		if it.Permission == "" {
			t.Errorf("%s 에 권한 선언이 없다", it.Screen)
			continue
		}
		if !strings.Contains(it.Permission, ".") {
			t.Errorf("%s 의 권한 %q 가 <리소스>.<동작> 형식이 아니다", it.Screen, it.Permission)
		}
	}
}

func TestNavPathsAreUnderTheAdminTree(t *testing.T) {
	for _, it := range nav {
		if it.Path != "/admin" && !strings.HasPrefix(it.Path, "/admin/") {
			t.Errorf("%s 의 경로가 관리자 트리 밖이다: %s", it.Screen, it.Path)
		}
	}
}

func TestCurrentGroupMatchesDeepestPath(t *testing.T) {
	groups := Nav(func(string) bool { return true })
	tests := map[string]string{
		// `/admin/` with the slash: A-101's pattern is `/admin/{$}`, so that is
		// the URL the route actually serves.
		"/admin/":               "/admin/",
		"/admin/settings":       "/admin/settings",
		"/admin/settings/mail":  "/admin/settings/mail",
		"/admin/pages/new":      "/admin/pages",
		"/admin/does-not-exist": "",
	}
	for path, want := range tests {
		if got := CurrentGroup(groups, path); got != want {
			t.Errorf("CurrentGroup(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestForbiddenIs403(t *testing.T) {
	rec := httptest.NewRecorder()
	Forbidden(rec)
	if rec.Code != 403 {
		t.Errorf("HTTP %d, want 403", rec.Code)
	}
}
