package app

import (
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/commerce"
)

// **네 화면에 시작일·종료일 날짜 칸과 빠른 버튼이 그려진다** (D19 0.6, FR-712, W3-45).
//
// 핸들러 테스트는 화면이 받은 값만 본다. 칸이 템플릿에 없으면 그 값은 어디에도
// 그려지지 않고, 핸들러 테스트는 여전히 초록이다.
func TestPeriodFieldsRenderOnAllFourScreens(t *testing.T) {
	quick := []struct{ Label, URL string }{
		{"1주일", "/admin/x?from=2025-03-24&to=2025-03-31"},
		{"1개월", "/admin/x?from=2025-02-28&to=2025-03-31"},
	}
	for _, name := range []string{"admin/orders.html", "admin/reconcile.html",
		"admin/oplog.html", "admin/webhooks.html"} {
		body := renderAdmin(t, name, map[string]any{
			"From": "2025-03-01", "To": "2025-03-31", "Quick": quick, "PageNo": 1, "Count": int64(0),
			"Status": "결제완료", "Statuses": []commerce.Status{"결제완료"},
			"PrevURL": "/admin/x?from=2025-03-01&to=2025-03-31",
			"NextURL": "/admin/x?from=2025-03-01&page=2&to=2025-03-31",
		})
		for _, want := range []string{
			`<input type="date" id="from" name="from" value="2025-03-01"`,
			`<input type="date" name="to" value="2025-03-31"`,
			// 빠른 버튼은 서버가 계산한 GET 링크다 — 주소가 그대로 나가야 한다.
			`<a class="adm-btn" href="/admin/x?from=2025-03-24&amp;to=2025-03-31">1주일</a>`,
			`<a class="adm-btn" href="/admin/x?from=2025-02-28&amp;to=2025-03-31">1개월</a>`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s 에 %s 가 없다", name, want)
			}
		}
		// A-508 의 「최근 N일」 숫자 칸은 사라졌다.
		if strings.Contains(body, `name="days"`) {
			t.Errorf("%s 에 days 칸이 남아 있다", name)
		}
		// 날짜 칸은 GET 폼 안에 있다 — 폼 밖이면 [조회]가 그 값을 보내지 않는다.
		form := body[strings.Index(body, `<form method="get"`):]
		form = form[:strings.Index(form, "</form>")]
		if !strings.Contains(form, `name="from"`) || !strings.Contains(form, `name="to"`) {
			t.Errorf("%s: 날짜 칸이 조회 폼 밖에 있다", name)
		}
		if name == "admin/orders.html" && !strings.Contains(form, `name="status"`) {
			t.Error("A-504: 상태 필터와 기간이 다른 폼이다 — 상태를 바꾸면 기간이 풀린다")
		}
		// 쪽 이동 링크는 핸들러가 만든 주소를 그대로 쓴다 (기간이 실려 있다).
		if name != "admin/reconcile.html" {
			for _, want := range []string{
				`href="/admin/x?from=2025-03-01&amp;to=2025-03-31" aria-label="이전"`,
				`href="/admin/x?from=2025-03-01&amp;page=2&amp;to=2025-03-31" aria-label="다음"`,
			} {
				if !strings.Contains(body, want) {
					t.Errorf("%s 에 쪽 이동 링크 %s 가 없다", name, want)
				}
			}
		}
	}
}
