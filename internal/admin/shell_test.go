package admin

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/theme"
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
	got := flatten(Nav(allowOnly("admin.access", "page.view"), false))
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
	// shop=true 여야 전부다. cms 에서는 커머스 항목이 빠지는 것이 정상이고,
	// 그 차이를 아래 테스트가 따로 본다.
	got := flatten(Nav(func(string) bool { return true }, true))
	if len(got) != len(nav) {
		t.Errorf("항목 %d개, want %d개", len(got), len(nav))
	}
}

// FR-710: cms 모드에서는 커머스 메뉴가 없다.
//
// 등록되지 않은 경로를 가리키는 메뉴는 404 링크이고, 그것은 권한이 없어 안
// 열리는 링크와 똑같이 보인다 — 아무도 신고하지 않는다.
func TestNavHidesCommerceInCmsMode(t *testing.T) {
	cms := flatten(Nav(func(string) bool { return true }, false))
	shop := flatten(Nav(func(string) bool { return true }, true))

	if len(shop) <= len(cms) {
		t.Fatalf("shop %d개, cms %d개 — 커머스 항목이 없다", len(shop), len(cms))
	}
	inCms := map[string]bool{}
	for _, id := range cms {
		inCms[id] = true
	}
	marked := map[string]bool{}
	for _, it := range nav {
		marked[it.Screen] = it.Shop
	}
	extra := 0
	for _, id := range shop {
		if inCms[id] {
			continue
		}
		extra++
		if !marked[id] {
			t.Errorf("%s 가 shop 에만 있는데 Shop 표시가 없다", id)
		}
	}
	if extra == 0 {
		t.Error("커머스 전용 항목을 하나도 찾지 못했다")
	}
	// 반대 방향: cms 에만 있는 항목은 없어야 한다.
	inShop := map[string]bool{}
	for _, id := range shop {
		inShop[id] = true
	}
	for _, id := range cms {
		if !inShop[id] {
			t.Errorf("cms 에만 있는 메뉴: %s", id)
		}
	}
}

func TestNavIsEmptyWithoutPermissions(t *testing.T) {
	if got := Nav(func(string) bool { return false }, false); len(got) != 0 {
		t.Errorf("권한이 없는데 %d 그룹이 나왔다", len(got))
	}
}

func TestNavIsOrdered(t *testing.T) {
	groups := Nav(func(string) bool { return true }, false)
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
	groups := Nav(func(string) bool { return true }, false)
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

// **같은 화면이 메뉴에 두 번 있으면 안 된다.**
//
// A-207 약관과 사업자 정보 화면이 표에 각각 두 줄씩 있었다. Order 도 같아서
// 정렬해도 붙어 나왔고, 관리자 사이드바에 같은 링크가 나란히 두 번 그려졌다.
// 순서 테스트는 정렬된 순서만 봤으므로 통과했다 — 중복은 순서를 어기지 않는다.
func TestNoScreenAppearsTwiceInTheMenu(t *testing.T) {
	seen := map[string]string{}
	for _, it := range nav {
		if prev, dup := seen[it.Screen]; dup {
			t.Errorf("%s 가 메뉴에 두 번 있다 (%q, %q)", it.Screen, prev, it.Title)
		}
		seen[it.Screen] = it.Title
	}
	if len(seen) < 10 {
		t.Fatalf("메뉴 항목을 %d개밖에 못 읽었다 — 검사가 헛돌았다", len(seen))
	}

	// 경로도 마찬가지다. 화면 ID 를 다르게 적으면서 같은 곳을 가리킬 수 있다.
	byPath := map[string]string{}
	for _, it := range nav {
		if prev, dup := byPath[it.Path]; dup {
			t.Errorf("경로 %s 가 메뉴에 두 번 있다 (%q, %q)", it.Path, prev, it.Title)
		}
		byPath[it.Path] = it.Title
	}
}

// D41 4·5절: 테마 화면은 화면 단위 판정을 통과한 것만 메뉴에 나오고, 선언의 묶음·순서로
// 코어 메뉴와 같은 줄에서 정렬된다. 같은 이름의 묶음은 합쳐진다.
func TestThemeNavJoinsTheMenuByScope(t *testing.T) {
	screens := []theme.Screen{
		{Key: "hero", Title: "메인 배너", Group: "콘텐츠", Order: 11},
		{Key: "partners", Title: "협력사", Group: "테마", Order: 100},
		{Key: "secret", Title: "숨은 화면", Group: "테마", Order: 101},
	}
	extra := ThemeNav(screens, func(key string) bool { return key != "secret" })
	if len(extra) != 2 || extra[0].Path != "/admin/theme/hero" || extra[0].Screen != "A-212" {
		t.Fatalf("ThemeNav = %+v", extra)
	}
	groups := Nav(func(string) bool { return true }, false, extra...)
	var content, themeGroup []string
	for _, g := range groups {
		for _, it := range g.Items {
			switch g.Title {
			case "콘텐츠":
				content = append(content, it.Title)
			case "테마":
				themeGroup = append(themeGroup, it.Title)
			}
		}
	}
	if strings.Join(content, ",") != "페이지,메인 배너,게시판,글 관리,댓글 관리,첨부 관리,메뉴" {
		t.Errorf("콘텐츠 묶음에 순서대로 끼어야 한다: %v", content)
	}
	if strings.Join(themeGroup, ",") != "협력사" {
		t.Errorf("판정을 통과한 화면만: %v", themeGroup)
	}
	if got := CurrentGroup(groups, "/admin/theme/partners/0b6e6f43-8d6e-4a3c-9a39-0d7c3d6b1c11"); got != "/admin/theme/partners" {
		t.Errorf("행 화면에서 강조할 메뉴 = %q", got)
	}
}
