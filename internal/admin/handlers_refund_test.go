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

// paidAdminOrder makes an approved order with two units of one item.
func paidAdminOrder(t *testing.T, d *Deps, pool *pgxpool.Pool) (*commerce.OrderDetail, int) {
	t.Helper()
	ctx := context.Background()
	var productID, variantID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO products (slug,name,base_price,is_visible)
		 VALUES ('tee','티셔츠',12000,true) RETURNING id`).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO product_variants (product_id,option_values,price_delta,stock)
		 VALUES ($1,'{"크기":"L"}',1000,10) RETURNING id`, productID).Scan(&variantID); err != nil {
		t.Fatal(err)
	}
	owner := commerce.CartOwner{GuestKey: "guest-admin-0123456"}
	if err := d.Commerce.AddToCart(ctx, owner, variantID, 2); err != nil {
		t.Fatal(err)
	}
	form := commerce.OrderForm{
		ReceiverName: "받는이", ReceiverPhone: "010-0000-0000",
		Postcode: "12345", Address1: "서울",
		OrdererEmail: "a@example.com", OrdererPhone: "010-1111-1111",
	}
	order, err := d.Commerce.CreateOrder(ctx, owner, "", form, commerce.Shipping{}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Commerce.ConfirmPayment(ctx, adminFakeGateway{}, "toss",
		order.OrderNo, "pk-admin", order.Total, time.Now()); err != nil {
		t.Fatal(err)
	}
	detail, err := d.Commerce.OrderByNoUnscoped(ctx, order.OrderNo)
	if err != nil {
		t.Fatal(err)
	}
	return detail, order.Total
}

type adminFakeGateway struct{}

func (adminFakeGateway) Confirm(_ context.Context, req commerce.ConfirmRequest) (*commerce.Payment, error) {
	return &commerce.Payment{PaymentKey: req.PaymentKey, OrderNo: req.OrderNo,
		Status: commerce.PaymentApproved, Amount: req.Amount, Raw: []byte(`{"status":"DONE"}`)}, nil
}
func (adminFakeGateway) Cancel(context.Context, commerce.CancelRequest) (*commerce.Payment, error) {
	return nil, nil
}
func (adminFakeGateway) Get(context.Context, string) (*commerce.Payment, error) { return nil, nil }
func (adminFakeGateway) VerifyWebhook(context.Context, []byte) (*commerce.WebhookEvent, error) {
	return nil, nil
}

// D15 5.3-1: 돈이 나가는 화면은 재인증을 요구한다. 미충족이면 **403 + 그 화면의
// 폼 재표시**이고 리다이렉트하지 않는다 (D19 C7).
func TestAdminRefundRequiresReauth(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"order.refund": true}, reauth: true,
		id: "u1", email: "op@example.com"}
	d, pool := fixture(t, caller)
	order, _ := paidAdminOrder(t, d, pool)

	rec := postAdmin(t, d.RefundSave, "/admin/orders/"+order.OrderNo+"/refund",
		map[string]string{"no": order.OrderNo},
		url.Values{"item_id": {order.Items[0].ID}, "qty_" + order.Items[0].ID: {"1"}})

	if rec.Code != http.StatusForbidden {
		t.Fatalf("재인증 미충족 = HTTP %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "admin/refund.html") {
		t.Errorf("폼을 다시 그리지 않았다: %q", rec.Body.String())
	}
	// 아무것도 접수되지 않았다.
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM refunds`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("재인증 없이 환불 %d건이 접수됐다", n)
	}

	// 재인증을 마치면 통과한다 — 위 단언이 "무엇이든 막힌다" 를 본 것이
	// 아니라는 것.
	caller.reauth = false
	rec = postAdmin(t, d.RefundSave, "/admin/orders/"+order.OrderNo+"/refund",
		map[string]string{"no": order.OrderNo},
		url.Values{"item_id": {order.Items[0].ID}, "qty_" + order.Items[0].ID: {"1"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("재인증 뒤 = HTTP %d (%q)", rec.Code, rec.Body.String())
	}
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM refunds`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("환불 %d건, want 1", n)
	}
}

// 관리자 화면도 **금액을 받지 않는다** (FR-625). 예외를 두면 그 경로만
// 위변조 가능해진다.
func TestAdminRefundIgnoresAnyAmountField(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"order.refund": true},
		id: "u1", email: "op@example.com"}
	d, pool := fixture(t, caller)
	order, total := paidAdminOrder(t, d, pool)
	item := order.Items[0]

	// 폼에 금액을 실어 보낸다. 서버는 읽지 않는다.
	rec := postAdmin(t, d.RefundSave, "/admin/orders/"+order.OrderNo+"/refund",
		map[string]string{"no": order.OrderNo},
		url.Values{
			"item_id": {item.ID}, "qty_" + item.ID: {"1"},
			"amount": {"999999"}, "refund_amount": {"999999"},
		})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("HTTP %d (%q)", rec.Code, rec.Body.String())
	}

	var amount int
	if err := pool.QueryRow(context.Background(),
		`SELECT amount FROM refunds`).Scan(&amount); err != nil {
		t.Fatal(err)
	}
	if amount != total/2 {
		t.Errorf("환불액 %d, want %d — 폼의 금액이 쓰였다", amount, total/2)
	}
}

// 남은 수량을 넘는 요청은 422 다. 관리자라고 넘길 수 없다.
func TestAdminRefundRespectsRemainingQuantity(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"order.refund": true},
		id: "u1", email: "op@example.com"}
	d, pool := fixture(t, caller)
	order, _ := paidAdminOrder(t, d, pool)
	item := order.Items[0]

	rec := postAdmin(t, d.RefundSave, "/admin/orders/"+order.OrderNo+"/refund",
		map[string]string{"no": order.OrderNo},
		url.Values{"item_id": {item.ID}, "qty_" + item.ID: {"3"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("수량 초과 = HTTP %d, want 422", rec.Code)
	}
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM refunds`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("초과 요청이 %d건 접수됐다", n)
	}
}

// 권한 없는 사람은 열지 못한다.
func TestAdminRefundNeedsThePermission(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"order.view": true}, id: "u1"}
	d, pool := fixture(t, caller)
	order, _ := paidAdminOrder(t, d, pool)

	for _, h := range []http.HandlerFunc{d.RefundForm, d.RefundSave} {
		rec := postAdmin(t, h, "/admin/orders/"+order.OrderNo+"/refund",
			map[string]string{"no": order.OrderNo}, url.Values{})
		if rec.Code != http.StatusForbidden {
			t.Errorf("order.refund 없이 HTTP %d, want 403", rec.Code)
		}
	}
}

// postAdmin runs one handler with path values set, the way the mux would.
func postAdmin(t *testing.T, h http.HandlerFunc, target string,
	pathValues map[string]string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range pathValues {
		req.SetPathValue(k, v)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// A-506: **드롭다운이 상태머신에서 나온다.** 전체 상태를 나열하고 서버에서
// 검증하지 않는 것이 D14 5절이 지목한 가장 흔한 구현 실수다.
func TestAdminOrderDetailOffersOnlyReachableStates(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"order.view": true}, id: "u1"}
	d, pool := fixture(t, caller)
	order, _ := paidAdminOrder(t, d, pool)

	var got []commerce.Status
	d.Render = func(_ http.ResponseWriter, _ *http.Request, _ string, _ int, data any) {
		if m, ok := data.(map[string]any); ok {
			if n, ok := m["Next"].([]commerce.Status); ok {
				got = n
			}
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/orders/"+order.OrderNo, nil)
	req.SetPathValue("no", order.OrderNo)
	d.OrderDetail(httptest.NewRecorder(), req)

	want := commerce.Next(commerce.StatusPaid)
	if len(got) != len(want) {
		t.Fatalf("선택지 %v, want %v (상태머신이 낸 목록)", got, want)
	}
	// 전체 상태 목록이 아니어야 한다 — 그것이 D14 가 지목한 실수다.
	if len(got) >= len(commerce.AllStatuses()) {
		t.Errorf("선택지가 전체 상태다: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("선택지 %v, want %v", got, want)
			break
		}
	}
}

// A-506: 서버가 상태머신에 묻는다. 화면이 무엇을 보여줬든 표에 없는 전이는
// 거부된다 — 드롭다운을 좁히는 것은 편의이고 거부하는 것은 핸들러다.
func TestAdminTransitionIsCheckedByTheServer(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"order.update": true, "order.view": true},
		id: "u1", email: "op@example.com"}
	d, pool := fixture(t, caller)
	order, _ := paidAdminOrder(t, d, pool)
	ctx := context.Background()

	// 결제완료 → 배송완료 는 표에 없다 (배송준비·배송중을 거쳐야 한다).
	rec := postAdmin(t, d.OrderTransition, "/admin/orders/"+order.OrderNo+"/transition",
		map[string]string{"no": order.OrderNo},
		url.Values{"to": {string(commerce.StatusDelivered)}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("표에 없는 전이 = HTTP %d, want 422", rec.Code)
	}
	assertAdminOrderStatus(t, pool, order.OrderNo, commerce.StatusPaid)

	// 모르는 상태도 거부된다.
	rec = postAdmin(t, d.OrderTransition, "/admin/orders/"+order.OrderNo+"/transition",
		map[string]string{"no": order.OrderNo}, url.Values{"to": {"발송대기"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("모르는 상태 = HTTP %d, want 422", rec.Code)
	}
	assertAdminOrderStatus(t, pool, order.OrderNo, commerce.StatusPaid)

	// 표에 있는 전이는 통과한다.
	rec = postAdmin(t, d.OrderTransition, "/admin/orders/"+order.OrderNo+"/transition",
		map[string]string{"no": order.OrderNo},
		url.Values{"to": {string(commerce.StatusPreparing)}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("정상 전이 = HTTP %d (%q)", rec.Code, rec.Body.String())
	}
	assertAdminOrderStatus(t, pool, order.OrderNo, commerce.StatusPreparing)

	// 배송완료로 옮기면 delivered_at 이 남는다 — A-512 의 반품 기간·자동
	// 확정이 전부 이 시각 기준이고, 작업 로그는 운영 데이터가 아니다 (D30).
	for _, to := range []commerce.Status{commerce.StatusShipping, commerce.StatusDelivered} {
		if rec := postAdmin(t, d.OrderTransition, "/admin/orders/"+order.OrderNo+"/transition",
			map[string]string{"no": order.OrderNo},
			url.Values{"to": {string(to)}}); rec.Code != http.StatusSeeOther {
			t.Fatalf("%s 전이 = HTTP %d", to, rec.Code)
		}
	}
	var deliveredAt *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT delivered_at FROM orders WHERE order_no = $1`, order.OrderNo).
		Scan(&deliveredAt); err != nil {
		t.Fatal(err)
	}
	if deliveredAt == nil {
		t.Error("배송완료인데 delivered_at 이 비었다")
	}
}

// A-506 은 order.update 를 요구한다.
func TestAdminTransitionNeedsThePermission(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"order.view": true}, id: "u1"}
	d, pool := fixture(t, caller)
	order, _ := paidAdminOrder(t, d, pool)

	rec := postAdmin(t, d.OrderTransition, "/admin/orders/"+order.OrderNo+"/transition",
		map[string]string{"no": order.OrderNo},
		url.Values{"to": {string(commerce.StatusPreparing)}})
	if rec.Code != http.StatusForbidden {
		t.Errorf("order.update 없이 HTTP %d, want 403", rec.Code)
	}
	assertAdminOrderStatus(t, pool, order.OrderNo, commerce.StatusPaid)

	// 송장 입력도 마찬가지다.
	rec = postAdmin(t, d.ShippingSave, "/admin/orders/"+order.OrderNo+"/shipping",
		map[string]string{"no": order.OrderNo},
		url.Values{"carrier": {"cj"}, "tracking_no": {"T-1"}})
	if rec.Code != http.StatusForbidden {
		t.Errorf("송장 입력이 order.update 없이 HTTP %d", rec.Code)
	}
}

func assertAdminOrderStatus(t *testing.T, pool *pgxpool.Pool, orderNo string, want commerce.Status) {
	t.Helper()
	var got string
	if err := pool.QueryRow(context.Background(),
		`SELECT status FROM orders WHERE order_no = $1`, orderNo).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("주문 상태 %q, want %q", got, want)
	}
}

// D15 7절: 돈이 움직인 일은 반드시 작업 로그에 남는다.
//
// 로그가 조용히 멈춘 감사 흔적은 없는 것보다 나쁘다 — 공백이 "아무 일도
// 없었다" 로 읽힌다.
func TestAdminRefundIsRecordedInTheOperationLog(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"order.refund": true},
		id: "", email: "op@example.com"}
	d, pool := fixture(t, caller)
	d.OpLog = content.NewStore(pool).OpLog()
	order, total := paidAdminOrder(t, d, pool)
	item := order.Items[0]

	rec := postAdmin(t, d.RefundSave, "/admin/orders/"+order.OrderNo+"/refund",
		map[string]string{"no": order.OrderNo},
		url.Values{"item_id": {item.ID}, "qty_" + item.ID: {"1"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("HTTP %d (%q)", rec.Code, rec.Body.String())
	}

	var action, target, summary string
	if err := pool.QueryRow(context.Background(), `
		SELECT action, target_id, summary FROM operation_logs
		ORDER BY created_at DESC LIMIT 1`).Scan(&action, &target, &summary); err != nil {
		t.Fatalf("작업 로그가 없다: %v", err)
	}
	if action != "order.refund" {
		t.Errorf("동작 %q, want order.refund", action)
	}
	if target != order.OrderNo {
		t.Errorf("대상 %q, want %q", target, order.OrderNo)
	}
	// 금액이 요약에 있어야 A-601 을 읽는 사람이 무슨 일이 있었는지 안다.
	if !strings.Contains(summary, itoa(total/2)) {
		t.Errorf("요약에 금액이 없다: %q", summary)
	}
}
