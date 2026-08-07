package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// **만들기와 고치기는 다른 권한이다** (D15 2.2: page.create / page.update).
//
// 둘 다 page.update 로 받고 있었다. 그래서 `page.create` 는 역할 편집기에
// 나타나고 부여할 수 있는데 아무 데서도 판정되지 않았고, 「고칠 수만 있고 만들
// 수는 없는」 역할을 만들 방법이 없었다.
func TestPageCreateAndUpdateAreSeparatePermissions(t *testing.T) {
	// 고치기만 가진 역할.
	updater := &fakeCaller{perms: map[string]bool{"page.update": true}, email: "e@example.com"}
	d, _ := fixture(t, updater)

	rec := postAs(d, "/admin/pages/new", "new", url.Values{
		"slug": {"about"}, "title": {"회사 소개"}, "body": {"본문"},
	})
	if rec.Code != http.StatusForbidden {
		t.Errorf("page.update 만으로 새 페이지를 만들었다 (HTTP %d)", rec.Code)
	}

	// 만들기를 가진 역할은 통과한다. 이것이 없으면 위 단언은 "아무도 못 만든다"
	// 를 확인한 것일 뿐이다.
	creator := &fakeCaller{perms: map[string]bool{"page.create": true}, email: "e@example.com"}
	d2, _ := fixture(t, creator)
	rec2 := postAs(d2, "/admin/pages/new", "new", url.Values{
		"slug": {"about"}, "title": {"회사 소개"}, "body": {"본문"},
	})
	if rec2.Code == http.StatusForbidden {
		t.Error("page.create 를 가졌는데 만들지 못했다")
	}
}

// **취소는 order.cancel 이다** (D15 2.2). 상태 전이 권한 하나로 취소까지
// 보내면 「배송 상태는 만지되 취소는 못 하는」 역할을 만들 수 없다.
func TestCancellingAnOrderNeedsOrderCancel(t *testing.T) {
	mover := &fakeCaller{perms: map[string]bool{"order.update": true}, email: "e@example.com"}
	d, _ := fixture(t, mover)

	rec := postAs(d, "/admin/orders/NOPE/transition", "", url.Values{"to": {"취소"}})
	if rec.Code != http.StatusForbidden {
		t.Errorf("order.update 만으로 취소했다 (HTTP %d)", rec.Code)
	}

	// 같은 권한으로 **다른 전이는 통과해야** 한다 — 통과하지 못하면 위 단언은
	// "이 핸들러가 아무것도 못 한다" 를 본 것이다. 없는 주문이라 404 로 끝나는
	// 것이 정상이고, 중요한 것은 403 이 아니라는 점이다.
	rec2 := postAs(d, "/admin/orders/NOPE/transition", "", url.Values{"to": {"배송중"}})
	if rec2.Code == http.StatusForbidden {
		t.Error("order.update 를 가졌는데 일반 전이도 막혔다 — 검사가 너무 넓다")
	}
}

// postAs 는 경로 변수를 실어 핸들러를 직접 부른다. 트리를 통과시키지 않는 것은
// 여기서 보려는 것이 라우트가 아니라 **핸들러의 권한 판정**이기 때문이다.
func postAs(d *Deps, target, id string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	if strings.Contains(target, "/pages/") {
		req.SetPathValue("id", id)
		d.PageSave(rec, req)
		return rec
	}
	req.SetPathValue("no", "NOPE")
	d.OrderTransition(rec, req)
	return rec
}
