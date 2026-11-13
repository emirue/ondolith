package admin

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/commerce"
)

// **조립 시점 키를 바꾸면 트리를 다시 조립한다** (D20 모듈 게이팅). 문서는 그렇게
// 적혀 있었는데 화면은 「재시작하세요」라고 안내했다 — 그래서 cms 로 설치한 뒤
// shop 으로 바꾼 사람이 결제 설정 404 를 봤다.
func TestSettingsSaveRebuildsTreeWhenAssemblyKeyChanges(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"settings.update": true}})
	calls := 0
	d.Rebuild = func() error { calls++; return nil }
	save := func(form url.Values) int {
		return post(d.SettingsSave, "/admin/settings", form).Code
	}

	if code := save(url.Values{"site.type": {"shop"}}); code != http.StatusSeeOther || calls != 1 {
		t.Fatalf("cms → shop: HTTP %d, 재조립 %d회 (303, 1회여야)", code, calls)
	}
	// 같은 값은 재조립하지 않는다 — 저장마다 풀을 새로 여는 것은 비용이다.
	if code := save(url.Values{"site.type": {"shop"}}); code != http.StatusSeeOther || calls != 1 {
		t.Fatalf("같은 값 재저장: HTTP %d, 재조립 %d회 (303, 1회여야)", code, calls)
	}
	// 관리자 배색도 렌더러가 조립 때 읽는다.
	if code := save(url.Values{"site.type": {"shop"}, "admin.theme": {"1d"}}); code != http.StatusSeeOther || calls != 2 {
		t.Fatalf("배색 변경: HTTP %d, 재조립 %d회 (303, 2회여야)", code, calls)
	}
	// 개발 모드도 조립 때 한 번 읽는다(템플릿 로더). 저장만 하고 재조립하지 않던 판은 재시작 전에는
	// 켜고 꺼도 아무 일이 없었다.
	if code := save(url.Values{"site.type": {"shop"}, "site.dev_mode": {"1"}}); code != http.StatusSeeOther || calls != 3 {
		t.Fatalf("개발 모드 변경: HTTP %d, 재조립 %d회 (303, 3회여야)", code, calls)
	}
	// 관리자 메뉴 숨김도 렌더러가 조립 때 읽는다 (FR-720).
	if code := save(url.Values{"site.type": {"shop"}, "admin.hidden_menus": {"", "A-206"}}); code != http.StatusSeeOther || calls != 4 {
		t.Fatalf("메뉴 숨김 변경: HTTP %d, 재조립 %d회 (303, 4회여야)", code, calls)
	}
}

// **A-201 의 관리자 메뉴 숨김** (FR-720, D19 A-201). 체크박스 여러 개라 앞에 빈 hidden input 이
// 하나 오고, 저장값은 정렬한 화면 ID 를 쉼표로 이은 것이다.
func TestSiteSettingsSavesHiddenMenus(t *testing.T) {
	d, pool := fixture(t, &fakeCaller{perms: map[string]bool{"settings.update": true}})
	d.Rebuild = func() error { return nil }
	ctx := context.Background()
	save := func(form url.Values) int { return post(d.SettingsSave, "/admin/settings", form).Code }
	stored := func() string {
		kv, err := d.Content.Settings(ctx, "admin.hidden_menus")
		if err != nil {
			t.Fatal(err)
		}
		return kv["admin.hidden_menus"]
	}
	logged := func(summary string) bool {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM operation_logs WHERE summary = $1`, summary).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}

	if code := save(url.Values{"admin.hidden_menus": {"", "A-601", "A-206"}}); code != http.StatusSeeOther || stored() != "A-206,A-601" {
		t.Fatalf("저장: HTTP %d, 값 %q (303, \"A-206,A-601\" 이어야)", code, stored())
	}
	// 운영자가 소유자 화면에서 메뉴를 감출 수 있으므로 값까지 남긴다.
	if !logged("관리자 메뉴 숨김: (없음) → A-206,A-601") {
		t.Error("숨김 변경이 이전 → 새 값으로 작업 로그에 남지 않았다")
	}
	// 숨김 칸이 없는 저장(부분 POST)은 값을 건드리지 않는다.
	if code := save(url.Values{"site.name": {"다른 이름"}}); code != http.StatusSeeOther || stored() != "A-206,A-601" {
		t.Fatalf("부분 저장: HTTP %d, 값 %q (303, 그대로여야)", code, stored())
	}
	// 고정 화면은 422 이고 저장하지 않는다.
	for _, id := range []string{"A-201", "A-209", "A-999"} {
		if code := save(url.Values{"admin.hidden_menus": {"", id}}); code != http.StatusUnprocessableEntity || stored() != "A-206,A-601" {
			t.Fatalf("%s: HTTP %d, 값 %q (422, 그대로여야)", id, code, stored())
		}
	}
	// 모두 해제하면 빈 hidden input 하나만 온다.
	if code := save(url.Values{"admin.hidden_menus": {""}}); code != http.StatusSeeOther || stored() != "" {
		t.Fatalf("모두 해제: HTTP %d, 값 %q (303, 빈 값이어야)", code, stored())
	}
	if !logged("관리자 메뉴 숨김: A-206,A-601 → (없음)") {
		t.Error("해제가 작업 로그에 남지 않았다")
	}
}

// cms 사이트의 폼에는 커머스 선택지가 없다. 그 폼으로 저장해도 shop 때 숨긴 커머스 메뉴는 남는다.
func TestSiteSettingsKeepsHiddenCommerceMenusWhileCms(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"settings.update": true}})
	d.Rebuild = func() error { return nil }
	ctx := context.Background()
	if err := d.Content.PutSettings(ctx, map[string]string{"site.type": "cms", "admin.hidden_menus": "A-603"}); err != nil {
		t.Fatal(err)
	}
	if code := post(d.SettingsSave, "/admin/settings", url.Values{"admin.hidden_menus": {"", "A-206"}}).Code; code != http.StatusSeeOther {
		t.Fatalf("HTTP %d, want 303", code)
	}
	kv, err := d.Content.Settings(ctx, "admin.hidden_menus")
	if err != nil {
		t.Fatal(err)
	}
	if kv["admin.hidden_menus"] != "A-206,A-603" {
		t.Errorf("값 %q, want \"A-206,A-603\"", kv["admin.hidden_menus"])
	}
}

// 폼은 지금 사이트 유형의 선택지와 숨긴 화면을 받는다. 고정 화면은 선택지에 없다.
func TestSiteSettingsFormOffersHideChoices(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"settings.update": true}})
	var data map[string]any
	d.Render = func(w http.ResponseWriter, _ *http.Request, _ string, c int, v any) {
		w.WriteHeader(c)
		data, _ = v.(map[string]any)
	}
	if err := d.Content.PutSettings(context.Background(), map[string]string{"site.type": "shop", "admin.hidden_menus": "A-603"}); err != nil {
		t.Fatal(err)
	}
	get(d.SettingsForm, "/admin/settings")
	groups, _ := data["HideChoices"].([]NavGroup)
	offered := map[string]bool{}
	for _, g := range groups {
		for _, it := range g.Items {
			offered[it.Screen] = true
		}
	}
	if !offered["A-603"] || offered["A-201"] || offered["A-209"] {
		t.Errorf("선택지 %v — shop 이면 A-603 이 있고 고정 화면(A-201·A-209)은 없어야 한다", offered)
	}
	if hidden, _ := data["Hidden"].(map[string]bool); !hidden["A-603"] {
		t.Errorf("숨긴 화면 %v — A-603 이 체크돼 있어야 한다", data["Hidden"])
	}
}

// **개발 모드가 켜져 있으면 A-201 이 경고한다** (D19 A-201 — 거부가 아니라 경고, NFR-104). 매 요청
// 템플릿을 다시 읽어 느려지므로 운영에서 켜 둔 채로 두면 안 된다. 폼에 그 칸이 없어 켤 수도 끌 수도 없었다.
func TestSiteSettingsWarnWhileDevModeIsOn(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"settings.update": true}})
	var data map[string]any
	d.Render = func(w http.ResponseWriter, _ *http.Request, _ string, c int, v any) {
		w.WriteHeader(c)
		data, _ = v.(map[string]any)
	}
	if err := d.Content.PutSettings(context.Background(), map[string]string{"site.dev_mode": "1"}); err != nil {
		t.Fatal(err)
	}
	get(d.SettingsForm, "/admin/settings")
	if w, _ := data["Warning"].(string); !strings.Contains(w, "개발 모드") {
		t.Errorf("개발 모드가 켜져 있는데 경고하지 않았다: %q", w)
	}
}

// `shop → cms` 는 미완결 주문이 있으면 409 다 (D13 전환 규칙, D19 A-201). 결제
// 경로를 내리면 구매자가 중간에 갇힌다.
func TestSiteTypeSwitchToCMSRefusedWhileOrdersAreOpen(t *testing.T) {
	d, pool := fixture(t, &fakeCaller{perms: map[string]bool{"settings.update": true}})
	ctx := context.Background()
	calls := 0
	d.Rebuild = func() error { calls++; return nil }
	if err := d.Content.PutSettings(ctx, map[string]string{"site.type": "shop"}); err != nil {
		t.Fatal(err)
	}
	paidAdminOrder(t, d, pool) // 결제완료 — 종료 상태가 아니다

	rec := post(d.SettingsSave, "/admin/settings", url.Values{"site.type": {"cms"}})
	if rec.Code != http.StatusConflict || calls != 0 {
		t.Fatalf("미완결 주문이 있는데 HTTP %d, 재조립 %d회 (409, 0회여야)", rec.Code, calls)
	}
	var logged int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM operation_logs WHERE summary LIKE '사이트 유형 전환 거부%'`).Scan(&logged); err != nil {
		t.Fatal(err)
	}
	if logged != 1 {
		t.Errorf("거부가 작업 로그에 %d건 남았다 (1건이어야 — D19 「시도 기록」)", logged)
	}

	// 종료 상태에 닿으면 전환된다.
	if _, err := pool.Exec(ctx, `UPDATE orders SET status = $1`, string(commerce.StatusCancelled)); err != nil {
		t.Fatal(err)
	}
	rec = post(d.SettingsSave, "/admin/settings", url.Values{"site.type": {"cms"}})
	if rec.Code != http.StatusSeeOther || calls != 1 {
		t.Fatalf("주문 완결 뒤 HTTP %d, 재조립 %d회 (303, 1회여야)", rec.Code, calls)
	}
}
