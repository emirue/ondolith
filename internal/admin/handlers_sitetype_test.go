package admin

import (
	"context"
	"net/http"
	"net/url"
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
