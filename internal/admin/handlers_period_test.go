package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emirue/ondolith/internal/commerce"
	"github.com/emirue/ondolith/internal/content"
)

// periodDay 는 테스트가 쓰는 하루다. 서버 시간대(time.Local)의 날짜로 만든다 —
// 핸들러가 그 시간대로 경계를 자른다.
var periodDay = time.Date(2025, 3, 10, 0, 0, 0, 0, time.Local)

// edgeTimes 는 그 하루의 안팎 네 순간이다: 전날 끝(밖) · 00:00(안) · 23:59:59(안) ·
// 다음 날 00:00(밖).
func edgeTimes() []time.Time {
	return []time.Time{
		periodDay.Add(-time.Second), periodDay,
		periodDay.Add(24*time.Hour - time.Second), periodDay.Add(24 * time.Hour),
	}
}

// view 는 핸들러를 GET 으로 불러 상태 코드와 화면이 받은 값을 돌려준다.
func view(t *testing.T, d *Deps, h http.HandlerFunc, target string) (code int, data map[string]any) {
	t.Helper()
	d.Render = func(w http.ResponseWriter, _ *http.Request, _ string, c int, v any) {
		w.WriteHeader(c)
		data = v.(map[string]any)
	}
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec.Code, data
}

func insertOrder(t *testing.T, pool *pgxpool.Pool, no, status string, at time.Time) (id string) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO orders (order_no, status, receiver_name, receiver_phone, postcode, address1,
		                    orderer_email, orderer_phone, total_amount, created_at)
		VALUES ($1, $2, '받는이', '010-0000-0000', '12345', '서울',
		        'a@example.com', '010-1111-1111', 15000, $3) RETURNING id`,
		no, status, at).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// rowsOf·strOf 는 화면이 받은 값을 꺼낸다. 없으면 빈 값이다 — 단언 실패로 패닉하면
// 그 뒤의 검사가 전부 가려진다.
func rowsOf[T any](data map[string]any, key string) []T {
	v, _ := data[key].([]T)
	return v
}

func strOf(data map[string]any, key string) string {
	s, _ := data[key].(string)
	return s
}

func quickLabels(data map[string]any) string {
	var out []string
	for _, l := range rowsOf[quickLink](data, "Quick") {
		out = append(out, l.Label)
	}
	return strings.Join(out, "·")
}

// **네 화면 모두 시작일 > 종료일과 날짜가 아닌 값을 422 로 거부한다** (D19 0.6).
// 목록 대신 폼과 오류 문구를 다시 보이고, 받은 날짜는 칸에 남는다.
func TestPeriodRefusalsOnAllFourScreens(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"order.view": true, "payment.view": true, "log.view": true}}
	d, _ := fixture(t, caller)
	for name, s := range map[string]struct {
		h    http.HandlerFunc
		path string
		rows string
	}{
		"A-504": {d.OrderList, "/admin/orders", "Orders"},
		"A-508": {d.Reconcile, "/admin/reconcile", "Rows"},
		"A-601": {d.OpLogList, "/admin/oplog", "Entries"},
		"A-603": {d.WebhookLog, "/admin/webhooks", "Rows"},
	} {
		code, data := view(t, d, s.h, s.path+"?from=2025-03-11&to=2025-03-10")
		if code != http.StatusUnprocessableEntity || data["Error"] != "시작일이 종료일보다 늦습니다" {
			t.Errorf("%s 시작일 > 종료일 = HTTP %d, %v — want 422", name, code, data["Error"])
		}
		if data["From"] != "2025-03-11" || data["To"] != "2025-03-10" {
			t.Errorf("%s: 날짜 칸이 받은 값을 잃었다: %v ~ %v", name, data["From"], data["To"])
		}
		if _, drew := data[s.rows]; drew {
			t.Errorf("%s: 거부했는데 목록을 그렸다", name)
		}
		if code, _ := view(t, d, s.h, s.path+"?from=2025-13-01"); code != http.StatusUnprocessableEntity {
			t.Errorf("%s 날짜가 아닌 값 = HTTP %d, want 422", name, code)
		}
		// 같은 날은 유효하다 — 하루치 조회다.
		if code, data := view(t, d, s.h, s.path+"?from=2025-03-10&to=2025-03-10"); code != http.StatusOK {
			t.Errorf("%s 같은 날 = HTTP %d (%v), want 200", name, code, data["Error"])
		}
	}
}

// **A-504 는 주문일로 거르고, 상태 필터와 쪽 이동이 기간을 싣는다** (FR-712, W3-45).
func TestOrderListFiltersByPeriodAndCarriesIt(t *testing.T) {
	d, pool := fixture(t, &fakeCaller{perms: map[string]bool{"order.view": true}})
	for i, at := range edgeTimes() {
		insertOrder(t, pool, "PD000"+string(rune('0'+i)), "결제대기", at)
	}
	orderNos := func(data map[string]any) (out []string) {
		for _, o := range rowsOf[commerce.OrderDetail](data, "Orders") {
			out = append(out, o.OrderNo)
		}
		return out
	}

	// 기본은 기간 조건 없음 — 전부 나온다.
	code, data := view(t, d, d.OrderList, "/admin/orders")
	if code != http.StatusOK || len(orderNos(data)) != 4 {
		t.Fatalf("기간 없음 = HTTP %d, %v — want 4건", code, orderNos(data))
	}
	if data["From"] != "" || data["To"] != "" || quickLabels(data) != "1주일·1개월·3개월" {
		t.Errorf("기본 화면: 날짜 칸 %v ~ %v, 버튼 %s", data["From"], data["To"], quickLabels(data))
	}

	// 하루: 00:00 과 23:59:59 만 든다. 최신순.
	_, data = view(t, d, d.OrderList, "/admin/orders?from=2025-03-10&to=2025-03-10")
	if got := strings.Join(orderNos(data), ","); got != "PD0002,PD0001" {
		t.Errorf("2025-03-10 하루 → %s, want PD0002,PD0001 (시작일 00:00 ≤ t < 다음 날 00:00)", got)
	}
	// 한쪽 끝이 열린다.
	_, data = view(t, d, d.OrderList, "/admin/orders?from=2025-03-10")
	if got := len(orderNos(data)); got != 3 {
		t.Errorf("시작일만 → %d건, want 3", got)
	}
	_, data = view(t, d, d.OrderList, "/admin/orders?to=2025-03-10")
	if got := len(orderNos(data)); got != 3 {
		t.Errorf("종료일만 → %d건, want 3", got)
	}

	// 기간 안에 21건 — 다음 쪽 링크가 기간과 상태를 싣는다.
	for i := range 21 {
		insertOrder(t, pool, "PX000"+string(rune('A'+i)), "결제완료", periodDay.Add(time.Duration(i)*time.Minute))
	}
	_, data = view(t, d, d.OrderList, "/admin/orders?status=결제완료&from=2025-03-10&to=2025-03-10")
	next, err := url.Parse(strOf(data, "NextURL"))
	if err != nil {
		t.Fatal(err)
	}
	if q := next.Query(); q.Get("from") != "2025-03-10" || q.Get("to") != "2025-03-10" ||
		q.Get("status") != "결제완료" || q.Get("page") != "2" {
		t.Errorf("다음 쪽 링크 %s 가 기간·상태를 싣지 않는다", next)
	}
	_, data = view(t, d, d.OrderList, next.String())
	if got := orderNos(data); len(got) != 1 {
		t.Errorf("2쪽 → %v, want 1건", got)
	}
	prev, _ := url.Parse(strOf(data, "PrevURL"))
	if q := prev.Query(); q.Get("from") != "2025-03-10" || q.Get("status") != "결제완료" {
		t.Errorf("이전 쪽 링크 %s 가 기간·상태를 싣지 않는다", prev)
	}
	// 빠른 버튼이 상태를 유지한다.
	for _, l := range rowsOf[quickLink](data, "Quick") {
		if u, _ := url.Parse(l.URL); u.Query().Get("status") != "결제완료" || u.Query().Has("page") {
			t.Errorf("%s 버튼 %s — 상태를 유지하고 쪽은 싣지 않아야 한다", l.Label, l.URL)
		}
	}
}

// **A-508 은 기본이 1주일이고, 종료일 − 시작일이 31일을 넘으면 422 다** (D19 0.6).
// 「최근 N일」(`days`)은 사라졌다.
func TestReconcilePeriodDefaultsToAWeekAndCapsAt31Days(t *testing.T) {
	d, pool := fixture(t, &fakeCaller{perms: map[string]bool{"payment.view": true}})
	ctx := context.Background()
	for i, at := range edgeTimes() {
		id := insertOrder(t, pool, "RC000"+string(rune('0'+i)), "결제대기", at)
		if _, err := pool.Exec(ctx, `
			INSERT INTO payments (order_id, kind, status, pg, payment_key, approved_amount, created_at)
			VALUES ($1,'주문결제','대기','toss',$2,15000,$3)`, id, "pk"+string(rune('0'+i)), at); err != nil {
			t.Fatal(err)
		}
	}
	rowNos := func(data map[string]any) (out []string) {
		for _, r := range rowsOf[commerce.ReconcileRow](data, "Rows") {
			out = append(out, r.OrderNo)
		}
		return out
	}

	today := time.Now()
	code, data := view(t, d, d.Reconcile, "/admin/reconcile")
	if code != http.StatusOK {
		t.Fatalf("기본 = HTTP %d", code)
	}
	if data["From"] != today.AddDate(0, 0, -7).Format(dateLayout) || data["To"] != today.Format(dateLayout) {
		t.Errorf("기본 범위 %v ~ %v, want 오늘에서 7일 전 ~ 오늘", data["From"], data["To"])
	}
	if quickLabels(data) != "1주일·1개월" {
		t.Errorf("버튼 %s, want 1주일·1개월 (3개월 없음)", quickLabels(data))
	}
	// `days` 는 더 이상 읽지 않는다 — 보내도 기본 범위다.
	if _, withDays := view(t, d, d.Reconcile, "/admin/reconcile?days=1"); withDays["From"] != data["From"] {
		t.Errorf("days=1 이 범위를 %v 로 바꿨다", withDays["From"])
	}

	// 하루의 경계.
	_, data = view(t, d, d.Reconcile, "/admin/reconcile?from=2025-03-10&to=2025-03-10")
	if got := strings.Join(rowNos(data), ","); got != "RC0002,RC0001" {
		t.Errorf("2025-03-10 하루 → %s, want RC0002,RC0001", got)
	}

	// 차이 31일은 통과, 32일은 422.
	if code, data := view(t, d, d.Reconcile, "/admin/reconcile?from=2025-03-01&to=2025-04-01"); code != http.StatusOK {
		t.Errorf("차이 31일 = HTTP %d (%v), want 200", code, data["Error"])
	}
	code, data = view(t, d, d.Reconcile, "/admin/reconcile?from=2025-03-01&to=2025-04-02")
	if code != http.StatusUnprocessableEntity || data["Error"] != "결제 목록은 최대 31일까지 조회합니다" {
		t.Errorf("차이 32일 = HTTP %d, %v — want 422", code, data["Error"])
	}
	// 한쪽만 와도 열린 범위가 되지 않는다 — 상한을 지나가는 길이다.
	if code, _ := view(t, d, d.Reconcile, "/admin/reconcile?from=2020-01-01"); code != http.StatusUnprocessableEntity {
		t.Errorf("오래된 시작일만 = HTTP %d, want 422", code)
	}
	code, data = view(t, d, d.Reconcile, "/admin/reconcile?to=2025-03-10")
	if code != http.StatusOK || data["From"] != "2025-03-03" {
		t.Errorf("종료일만 = HTTP %d, 시작일 %v — want 200, 2025-03-03", code, data["From"])
	}
}

// **A-601 은 기록 시각으로 거르고, 건수와 쪽 이동이 그 기간을 따른다.**
func TestOpLogFiltersByPeriodAndCarriesIt(t *testing.T) {
	d, pool := fixture(t, &fakeCaller{perms: map[string]bool{"log.view": true}})
	ctx := context.Background()
	for i, at := range edgeTimes() {
		if _, err := pool.Exec(ctx, `
			INSERT INTO operation_logs (actor_email, action, target_type, summary, created_at)
			VALUES ('op@example.com', 'test.period', 'x', $1, $2)`, "e"+string(rune('0'+i)), at); err != nil {
			t.Fatal(err)
		}
	}
	summaries := func(data map[string]any) (out []string) {
		for _, e := range rowsOf[content.LogEntry](data, "Entries") {
			out = append(out, e.Summary)
		}
		return out
	}

	_, data := view(t, d, d.OpLogList, "/admin/oplog")
	if len(summaries(data)) != 4 || data["Count"] != int64(4) || quickLabels(data) != "1주일·1개월·3개월" {
		t.Errorf("기간 없음: %v, 건수 %v, 버튼 %s", summaries(data), data["Count"], quickLabels(data))
	}
	_, data = view(t, d, d.OpLogList, "/admin/oplog?from=2025-03-10&to=2025-03-10")
	if got := strings.Join(summaries(data), ","); got != "e2,e1" || data["Count"] != int64(2) {
		t.Errorf("2025-03-10 하루 → %s, 건수 %v — want e2,e1, 2건", got, data["Count"])
	}
	if data["NextURL"] != nil || data["PrevURL"] != nil {
		t.Errorf("한 쪽뿐인데 링크가 있다: %v %v", data["PrevURL"], data["NextURL"])
	}

	// 기간 안에 한 쪽을 넘게 넣는다. 다음 링크가 기간을 싣고, 이 화면의 쪽은 0 부터다.
	if _, err := pool.Exec(ctx, `
		INSERT INTO operation_logs (actor_email, action, target_type, summary, created_at)
		SELECT 'op@example.com', 'test.period', 'x', 'bulk', $1::timestamptz + make_interval(secs => n)
		FROM generate_series(1, $2) n`, periodDay, oplogPageSize); err != nil {
		t.Fatal(err)
	}
	_, data = view(t, d, d.OpLogList, "/admin/oplog?from=2025-03-10&to=2025-03-10")
	if want := "/admin/oplog?from=2025-03-10&page=1&to=2025-03-10"; data["NextURL"] != want {
		t.Errorf("다음 쪽 링크 %v, want %s", data["NextURL"], want)
	}
	_, data = view(t, d, d.OpLogList, strOf(data, "NextURL"))
	if len(summaries(data)) != 2 || data["NextURL"] != nil {
		t.Errorf("둘째 쪽: %d건, 다음 %v — want 2건, 다음 없음", len(summaries(data)), data["NextURL"])
	}
	if want := "/admin/oplog?from=2025-03-10&to=2025-03-10"; data["PrevURL"] != want {
		t.Errorf("이전 쪽 링크 %v, want %s", data["PrevURL"], want)
	}
}

// **A-603 은 수신 시각으로 거르고, 쪽 이동이 기간을 싣는다.**
func TestWebhookLogFiltersByPeriodAndCarriesIt(t *testing.T) {
	d, pool := fixture(t, &fakeCaller{perms: map[string]bool{"payment.view": true}})
	ctx := context.Background()
	for i, at := range edgeTimes() {
		if _, err := pool.Exec(ctx, `
			INSERT INTO webhook_events (pg, event_id, status, payload, created_at)
			VALUES ('toss', $1, '처리완료', '{}', $2)`, "e"+string(rune('0'+i)), at); err != nil {
			t.Fatal(err)
		}
	}
	events := func(data map[string]any) (out []string) {
		for _, r := range rowsOf[commerce.WebhookRow](data, "Rows") {
			out = append(out, r.EventID)
		}
		return out
	}

	_, data := view(t, d, d.WebhookLog, "/admin/webhooks")
	if len(events(data)) != 4 || quickLabels(data) != "1주일·1개월·3개월" {
		t.Errorf("기간 없음: %v, 버튼 %s", events(data), quickLabels(data))
	}
	_, data = view(t, d, d.WebhookLog, "/admin/webhooks?from=2025-03-10&to=2025-03-10")
	if got := strings.Join(events(data), ","); got != "e2,e1" {
		t.Errorf("2025-03-10 하루 → %s, want e2,e1", got)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO webhook_events (pg, event_id, status, payload, created_at)
		SELECT 'toss', 'bulk' || n, '처리완료', '{}', $1::timestamptz + make_interval(secs => n)
		FROM generate_series(1, $2) n`, periodDay, webhookPageSize); err != nil {
		t.Fatal(err)
	}
	_, data = view(t, d, d.WebhookLog, "/admin/webhooks?from=2025-03-10&to=2025-03-10")
	if want := "/admin/webhooks?from=2025-03-10&page=2&to=2025-03-10"; data["NextURL"] != want {
		t.Errorf("다음 쪽 링크 %v, want %s", data["NextURL"], want)
	}
	if got := len(events(data)); got != webhookPageSize {
		t.Errorf("1쪽 %d건, want %d", got, webhookPageSize)
	}
	_, data = view(t, d, d.WebhookLog, strOf(data, "NextURL"))
	if got := len(events(data)); got != 2 || data["NextURL"] != nil {
		t.Errorf("2쪽 %d건, 다음 %v — want 2건, 다음 없음", got, data["NextURL"])
	}
}
