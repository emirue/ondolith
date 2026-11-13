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
	got := flatten(Nav(allowOnly("admin.access", "page.view"), false, nil))
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
	got := flatten(Nav(func(string) bool { return true }, true, nil))
	if len(got) != len(nav) {
		t.Errorf("항목 %d개, want %d개", len(got), len(nav))
	}
}

// FR-710: cms 모드에서는 커머스 메뉴가 없다.
//
// 등록되지 않은 경로를 가리키는 메뉴는 404 링크이고, 그것은 권한이 없어 안
// 열리는 링크와 똑같이 보인다 — 아무도 신고하지 않는다.
func TestNavHidesCommerceInCmsMode(t *testing.T) {
	cms := flatten(Nav(func(string) bool { return true }, false, nil))
	shop := flatten(Nav(func(string) bool { return true }, true, nil))

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
	if got := Nav(func(string) bool { return false }, false, nil); len(got) != 0 {
		t.Errorf("권한이 없는데 %d 그룹이 나왔다", len(got))
	}
}

func TestNavIsOrdered(t *testing.T) {
	groups := Nav(func(string) bool { return true }, false, nil)
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
	groups := Nav(func(string) bool { return true }, false, nil)
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
	groups := Nav(func(string) bool { return true }, false, nil, extra...)
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

func allowAll(string) bool { return true }

// FR-720: A-201 에서 숨긴 메뉴는 최고 관리자에게도 빠진다 — 권한 필터와 별개다.
func TestNavHidesHiddenScreensEvenForSuperuser(t *testing.T) {
	got := flatten(Nav(allowAll, true, map[string]bool{"A-206": true, "A-603": true}))
	for _, s := range got {
		if s == "A-206" || s == "A-603" {
			t.Errorf("숨긴 화면이 메뉴에 나왔다: %s", s)
		}
	}
	if len(got) != len(nav)-2 {
		t.Errorf("항목 %d개, want %d개", len(got), len(nav)-2)
	}
}

// 고정 화면은 저장값에 있어도 숨지 않는다. DB 를 손으로 고쳐도 숨김을 되돌릴 A-201 은 남는다.
func TestNavNeverHidesLockedScreens(t *testing.T) {
	hidden := map[string]bool{"A-101": true, "A-201": true, "A-207": true, "A-209": true, "A-403": true}
	if got := flatten(Nav(allowAll, true, hidden)); len(got) != len(nav) {
		t.Errorf("고정 화면이 숨었다: 항목 %d개, want %d개", len(got), len(nav))
	}
}

// 코어 업그레이드로 없어진 화면 ID 가 저장값에 남아 있어도 메뉴는 그대로 그려진다.
// 테마 화면(A-212)은 숨김 대상이 아니다 — 저장값에 A-212 가 있어도 테마 메뉴는 남는다.
func TestNavIgnoresUnknownAndThemeHiddenIDs(t *testing.T) {
	extra := ThemeNav([]theme.Screen{{Key: "hero", Title: "메인 배너", Group: "콘텐츠", Order: 11}},
		func(string) bool { return true })
	got := flatten(Nav(allowAll, true, map[string]bool{"Z-999": true, "A-212": true}, extra...))
	if len(got) != len(nav)+1 {
		t.Errorf("항목 %d개, want %d개 (코어 전부 + 테마 화면 하나)", len(got), len(nav)+1)
	}
}

// 폼의 선택지에는 고정 화면이 없다. 커머스 메뉴는 shop 일 때만 나온다.
func TestHideChoicesNeverOfferLockedScreens(t *testing.T) {
	locked := map[string]bool{"A-101": true, "A-201": true, "A-207": true, "A-209": true, "A-403": true}
	for _, shop := range []bool{false, true} {
		got := flatten(HideChoices(shop))
		hasCommerce := false
		for _, id := range got {
			if locked[id] {
				t.Errorf("shop=%v: 고정 화면 %s 가 선택지에 있다", shop, id)
			}
			if id == "A-603" {
				hasCommerce = true
			}
		}
		if hasCommerce != shop {
			t.Errorf("shop=%v: 커머스 메뉴(A-603) 선택지 = %v", shop, hasCommerce)
		}
		lockedHere := 0
		for _, id := range flatten(Nav(allowAll, shop, nil)) {
			if locked[id] {
				lockedHere++
			}
		}
		if want := len(flatten(Nav(allowAll, shop, nil))) - lockedHere; len(got) != want {
			t.Errorf("shop=%v: 선택지 %d개, want %d개", shop, len(got), want)
		}
	}
}

func TestHiddenNavValue(t *testing.T) {
	tests := []struct {
		name      string
		submitted []string
		prev      string
		shop      bool
		want      string
		wantErr   bool
	}{
		{"정렬해 쉼표로 잇는다", []string{"A-603", "A-206"}, "", true, "A-206,A-603", false},
		{"빈 값(폼의 hidden input)은 건너뛴다", []string{"", "A-206"}, "", false, "A-206", false},
		{"모두 해제", []string{""}, "A-206", false, "", false},
		{"같은 ID 두 번", []string{"A-206", "A-206"}, "", false, "A-206", false},
		{"고정 화면 — 사이트 설정", []string{"A-201"}, "", false, "", true},
		{"고정 화면 — 결제 설정", []string{"A-209"}, "", true, "", true},
		{"고정 화면 — 약관", []string{"A-207"}, "", true, "", true},
		{"고정 화면 — 역할·권한", []string{"A-403"}, "", false, "", true},
		{"고정 화면 — 대시보드", []string{"A-101"}, "", false, "", true},
		{"모르는 ID", []string{"Z-999"}, "", false, "", true},
		{"테마 화면은 대상이 아니다", []string{"A-212"}, "", false, "", true},
		{"cms 저장은 숨긴 커머스를 남긴다", nil, "A-603,A-206", false, "A-603", false},
		{"shop 저장은 폼이 전부다", nil, "A-603", true, "", false},
		{"없어진 ID 는 버린다", []string{"A-206"}, "Z-999,A-206", true, "A-206", false},
		{"cms 에서도 이전 값의 고정 화면은 남기지 않는다", nil, "A-209", false, "", false},
	}
	for _, tc := range tests {
		got, err := HiddenNavValue(tc.submitted, tc.prev, tc.shop)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tc.name, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestParseHiddenNav(t *testing.T) {
	got := ParseHiddenNav(" A-206, ,A-603")
	if len(got) != 2 || !got["A-206"] || !got["A-603"] {
		t.Errorf("ParseHiddenNav = %v", got)
	}
	if len(ParseHiddenNav("")) != 0 {
		t.Error("빈 값이 항목을 만들었다")
	}
}
