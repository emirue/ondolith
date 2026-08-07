package admin

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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
		id: "u1", email: "op@example.com", password: "correct horse battery"}
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

	// 틀린 비밀번호도 막힌다.
	rec = postAdmin(t, d.RefundSave, "/admin/orders/"+order.OrderNo+"/refund",
		map[string]string{"no": order.OrderNo},
		url.Values{"item_id": {order.Items[0].ID}, "qty_" + order.Items[0].ID: {"1"},
			"password": {"틀린 비밀번호"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("틀린 비밀번호 = HTTP %d, want 403", rec.Code)
	}

	// **폼의 비밀번호로 창이 열린다** — 이 경로가 없으면 안내가 장식이 되고,
	// 15분이 지난 운영자는 로그아웃 전에는 환불을 못 한다.
	rec = postAdmin(t, d.RefundSave, "/admin/orders/"+order.OrderNo+"/refund",
		map[string]string{"no": order.OrderNo},
		url.Values{"item_id": {order.Items[0].ID}, "qty_" + order.Items[0].ID: {"1"},
			"password": {"correct horse battery"}})
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

	want := commerce.Next(commerce.StatusPaid, "A-506")
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

// deliveredAdminOrder walks a paid order to 배송완료 and opens a return.
func deliveredAdminOrder(t *testing.T, d *Deps, pool *pgxpool.Pool) (*commerce.OrderDetail, string) {
	t.Helper()
	ctx := context.Background()
	order, _ := paidAdminOrder(t, d, pool)
	for _, to := range []commerce.Status{
		commerce.StatusPreparing, commerce.StatusShipping, commerce.StatusDelivered,
	} {
		if err := d.Commerce.TransitionOrder(ctx, order.OrderNo, to, "A-506"); err != nil {
			t.Fatal(err)
		}
	}
	ret, err := d.Commerce.OpenReturn(ctx, order.OrderNo, commerce.ReturnRequest{
		Kind:  commerce.KindReturn,
		Lines: []commerce.RefundLine{{OrderItemID: order.Items[0].ID, Quantity: 1}},
	}, "P-511", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return order, ret.ReturnNo
}

// **수거 확인 전 환불 버튼이 서버에서 거부된다** (W3-31 기준, D14 「수거 우선」).
//
// 화면에 버튼이 보이든 말든 서버가 막는다 — 숨기는 것은 보안이 아니다.
func TestAdminSettleIsRefusedBeforePickup(t *testing.T) {
	// order.refund 를 함께 준다 — 이 테스트가 보는 것은 권한이 아니라
	// "수거 확인 없이 환불이 안 나간다" 이고, 권한이 먼저 막으면 그 규칙이
	// 검증되지 않는다.
	caller := &fakeCaller{perms: map[string]bool{"order.return": true, "order.refund": true},
		id: "", email: "op@example.com"}
	d, pool := fixture(t, caller)
	order, returnNo := deliveredAdminOrder(t, d, pool)
	ctx := context.Background()

	rec := postAdmin(t, d.ReturnAction, "/admin/orders/"+order.OrderNo+"/returns",
		map[string]string{"no": order.OrderNo},
		url.Values{"return_no": {returnNo}, "action": {"settle"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("수거 전 환불 = HTTP %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "admin/returns.html") {
		t.Errorf("폼을 다시 그리지 않았다: %q", rec.Body.String())
	}
	// 돈이 움직이지 않았다.
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM refunds`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("수거 전인데 환불 %d건이 생겼다", n)
	}

	// 수거를 확인하면 통과한다 — 위 단언이 "무엇이든 막힌다" 를 본 것이
	// 아니라는 것.
	rec = postAdmin(t, d.ReturnAction, "/admin/orders/"+order.OrderNo+"/returns",
		map[string]string{"no": order.OrderNo},
		url.Values{"return_no": {returnNo}, "action": {"pickup"},
			"fault": {"판매자"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("수거 확인 = HTTP %d (%q)", rec.Code, rec.Body.String())
	}
	rec = postAdmin(t, d.ReturnAction, "/admin/orders/"+order.OrderNo+"/returns",
		map[string]string{"no": order.OrderNo},
		url.Values{"return_no": {returnNo}, "action": {"settle"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("수거 뒤 환불 = HTTP %d (%q)", rec.Code, rec.Body.String())
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM refunds`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("환불 %d건, want 1", n)
	}
}

// 환불 확정에만 재인증이 걸린다 (D15 5.3-1). 수거 확인·거부는 돈이 나가지
// 않으므로 매번 비밀번호를 묻지 않는다 — 물으면 관리자가 저장하게 된다.
func TestAdminReturnReauthOnlyGuardsTheMoneyStep(t *testing.T) {
	// 권한은 갖춘 상태로 둔다 — 이 테스트가 보는 것은 재인증이 **어느
	// 단계에만** 걸리는가이고, 권한이 먼저 막으면 그 구분이 안 보인다.
	caller := &fakeCaller{perms: map[string]bool{"order.return": true, "order.refund": true},
		reauth: true, id: "", email: "op@example.com", password: "correct horse battery"}
	d, pool := fixture(t, caller)
	order, returnNo := deliveredAdminOrder(t, d, pool)

	// 수거 확인은 재인증 없이 된다.
	rec := postAdmin(t, d.ReturnAction, "/admin/orders/"+order.OrderNo+"/returns",
		map[string]string{"no": order.OrderNo},
		url.Values{"return_no": {returnNo}, "action": {"pickup"},
			"fault": {"판매자"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("수거 확인이 재인증에 막혔다: HTTP %d", rec.Code)
	}

	// 환불 확정은 막힌다.
	rec = postAdmin(t, d.ReturnAction, "/admin/orders/"+order.OrderNo+"/returns",
		map[string]string{"no": order.OrderNo},
		url.Values{"return_no": {returnNo}, "action": {"settle"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("환불 확정 = HTTP %d, want 403", rec.Code)
	}
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM refunds`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("재인증 없이 환불 %d건이 확정됐다", n)
	}

	// 폼의 비밀번호로 재인증을 마치면 통과한다.
	rec = postAdmin(t, d.ReturnAction, "/admin/orders/"+order.OrderNo+"/returns",
		map[string]string{"no": order.OrderNo},
		url.Values{"return_no": {returnNo}, "action": {"settle"},
			"password": {"correct horse battery"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("재인증 뒤 = HTTP %d (%q)", rec.Code, rec.Body.String())
	}
}

// 반품 처리가 작업 로그에 남는다 (D15 7절).
func TestAdminReturnActionsAreLogged(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"order.return": true, "order.refund": true},
		id: "", email: "op@example.com"}
	d, pool := fixture(t, caller)
	d.OpLog = content.NewStore(pool).OpLog()
	order, returnNo := deliveredAdminOrder(t, d, pool)

	// 거부도 로그에 남아야 한다 — 교환 재고 해제가 걸린 경로다.
	//
	// **그 반품 건이 속한 주문 경로로 보낸다.** 다른 주문 경로로 보내면
	// 404 이고, 그것이 소속 대조가 작동한다는 뜻이다 (별도 테스트가 본다).
	rejectOrder, rejectNo := deliveredAdminOrderFor(t, d, pool, order2Slug)
	if rec := postAdmin(t, d.ReturnAction, "/admin/orders/"+rejectOrder.OrderNo+"/returns",
		map[string]string{"no": rejectOrder.OrderNo},
		url.Values{"return_no": {rejectNo}, "action": {"reject"},
			"reason": {"확인 불가"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("거부 = HTTP %d (%q)", rec.Code, rec.Body.String())
	}

	for _, form := range []url.Values{
		{"return_no": {returnNo}, "action": {"pickup"}, "fault": {"판매자"}},
		{"return_no": {returnNo}, "action": {"settle"}},
	} {
		if rec := postAdmin(t, d.ReturnAction, "/admin/orders/"+order.OrderNo+"/returns",
			map[string]string{"no": order.OrderNo}, form); rec.Code != http.StatusSeeOther {
			t.Fatalf("%v = HTTP %d (%q)", form["action"], rec.Code, rec.Body.String())
		}
	}

	rows, err := pool.Query(context.Background(),
		`SELECT action FROM operation_logs ORDER BY created_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, a)
	}
	want := map[string]bool{"return.pickup": false, "return.settle": false, "return.reject": false}
	for _, a := range actions {
		if _, ok := want[a]; ok {
			want[a] = true
		}
	}
	for a, seen := range want {
		if !seen {
			t.Errorf("작업 로그에 %s 가 없다 (기록된 것: %v)", a, actions)
		}
	}
}

// A-511 은 order.return 을 요구한다.
func TestAdminReturnNeedsThePermission(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"order.refund": true}, id: "u1"}
	d, pool := fixture(t, caller)
	order, _ := paidAdminOrder(t, d, pool)

	for _, h := range []http.HandlerFunc{d.ReturnList, d.ReturnAction} {
		rec := postAdmin(t, h, "/admin/orders/"+order.OrderNo+"/returns",
			map[string]string{"no": order.OrderNo}, url.Values{})
		if rec.Code != http.StatusForbidden {
			t.Errorf("order.return 없이 HTTP %d, want 403", rec.Code)
		}
	}
}

// order2Slug is a second product slug, so one test can hold two orders.
const order2Slug = "cap"

// deliveredAdminOrderFor is deliveredAdminOrder with a chosen product slug.
func deliveredAdminOrderFor(t *testing.T, d *Deps, pool *pgxpool.Pool, slug string) (*commerce.OrderDetail, string) {
	t.Helper()
	ctx := context.Background()
	var productID, variantID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO products (slug,name,base_price,is_visible)
		 VALUES ($1,$1,12000,true) RETURNING id`, slug).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO product_variants (product_id,option_values,price_delta,stock)
		 VALUES ($1,'{"크기":"L"}',1000,10) RETURNING id`, productID).Scan(&variantID); err != nil {
		t.Fatal(err)
	}
	owner := commerce.CartOwner{GuestKey: "guest-" + slug + "-0123456"}
	if err := d.Commerce.AddToCart(ctx, owner, variantID, 2); err != nil {
		t.Fatal(err)
	}
	form := commerce.OrderForm{
		ReceiverName: "받는이", ReceiverPhone: "010-0000-0000",
		Postcode: "12345", Address1: "서울",
		OrdererEmail: "b@example.com", OrdererPhone: "010-2222-2222",
	}
	order, err := d.Commerce.CreateOrder(ctx, owner, "", form, commerce.Shipping{}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Commerce.ConfirmPayment(ctx, adminFakeGateway{}, "toss",
		order.OrderNo, "pk-"+slug, order.Total, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, to := range []commerce.Status{
		commerce.StatusPreparing, commerce.StatusShipping, commerce.StatusDelivered,
	} {
		if err := d.Commerce.TransitionOrder(ctx, order.OrderNo, to, "A-506"); err != nil {
			t.Fatal(err)
		}
	}
	detail, err := d.Commerce.OrderByNoUnscoped(ctx, order.OrderNo)
	if err != nil {
		t.Fatal(err)
	}
	ret, err := d.Commerce.OpenReturn(ctx, order.OrderNo, commerce.ReturnRequest{
		Kind:  commerce.KindReturn,
		Lines: []commerce.RefundLine{{OrderItemID: detail.Items[0].ID, Quantity: 1}},
	}, "P-511", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return detail, ret.ReturnNo
}

// **거부가 HTTP 배선까지 동작한다** — 교환 재고 해제가 걸린 경로다.
//
// 스토어의 RejectReturn 은 따로 검증돼 있지만, 폼 파싱·액션 분기·상태 복귀가
// 배선에서 어긋나면 그 검증은 아무것도 지키지 못한다.
func TestAdminRejectReleasesExchangeStockThroughTheHandler(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"order.return": true},
		id: "", email: "op@example.com"}
	d, pool := fixture(t, caller)
	ctx := context.Background()

	order, _ := paidAdminOrder(t, d, pool)
	for _, to := range []commerce.Status{
		commerce.StatusPreparing, commerce.StatusShipping, commerce.StatusDelivered,
	} {
		if err := d.Commerce.TransitionOrder(ctx, order.OrderNo, to, "A-506"); err != nil {
			t.Fatal(err)
		}
	}
	// 같은 상품의 두 번째 조합으로 교환을 건다.
	var productID string
	if err := pool.QueryRow(ctx,
		`SELECT product_id FROM order_items WHERE id = $1`, order.Items[0].ID).
		Scan(&productID); err != nil {
		t.Fatal(err)
	}
	var newVariant string
	if err := pool.QueryRow(ctx, `
		INSERT INTO product_variants (product_id,option_values,price_delta,stock)
		VALUES ($1,'{"크기":"M"}',1000,5) RETURNING id`, productID).Scan(&newVariant); err != nil {
		t.Fatal(err)
	}
	ret, err := d.Commerce.OpenReturn(ctx, order.OrderNo, commerce.ReturnRequest{
		Kind: commerce.KindExchange, NewVariantID: newVariant,
		Lines: []commerce.RefundLine{{OrderItemID: order.Items[0].ID, Quantity: 2}},
	}, "P-512", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	assertVariantStock(t, pool, newVariant, 3) // 예약됨

	rec := postAdmin(t, d.ReturnAction, "/admin/orders/"+order.OrderNo+"/returns",
		map[string]string{"no": order.OrderNo},
		url.Values{"return_no": {ret.ReturnNo}, "action": {"reject"},
			"reason": {"재고 확인 불가"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("거부 = HTTP %d (%q)", rec.Code, rec.Body.String())
	}

	// 예약이 풀렸다. 풀지 않으면 재고가 조용히 잠긴다.
	assertVariantStock(t, pool, newVariant, 5)
	// 주문이 배송완료로 돌아왔다.
	assertAdminOrderStatus(t, pool, order.OrderNo, commerce.StatusDelivered)
	// 거부 사유가 기록됐다.
	var reason, status string
	if err := pool.QueryRow(ctx,
		`SELECT status, reject_reason FROM returns WHERE return_no = $1`, ret.ReturnNo).
		Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "거부" || reason != "재고 확인 불가" {
		t.Errorf("반품 건 = %q / %q", status, reason)
	}
	// 처리 중 표시가 내려가 같은 품목에 다시 걸 수 있다.
	if _, err := d.Commerce.OpenReturn(ctx, order.OrderNo, commerce.ReturnRequest{
		Kind:  commerce.KindReturn,
		Lines: []commerce.RefundLine{{OrderItemID: order.Items[0].ID, Quantity: 1}},
	}, "P-511", time.Now()); err != nil {
		t.Errorf("거부 뒤 재접수가 막혔다: %v", err)
	}
}

// 관리자 화면이 과도한 배송비를 **422 로** 돌려준다.
//
// 500 이면 운영자는 무엇을 고쳐야 하는지 모르고, 그 반품 건은 화면상 원인
// 불명으로 멈춘 것처럼 보인다.
func TestAdminOversizedShippingFeeIs422(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"order.return": true},
		id: "", email: "op@example.com"}
	d, pool := fixture(t, caller)
	order, returnNo := deliveredAdminOrder(t, d, pool)
	// 정책값 자체가 환불 몫보다 크다. 폼이 아니라 A-512 에서 온다.
	setFeeSetting(t, pool, "차감", 99999999)

	rec := postAdmin(t, d.ReturnAction, "/admin/orders/"+order.OrderNo+"/returns",
		map[string]string{"no": order.OrderNo},
		url.Values{"return_no": {returnNo}, "action": {"pickup"},
			"fault": {"구매자"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("과도한 배송비 = HTTP %d, want 422 (%q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "admin/returns.html") {
		t.Errorf("폼을 다시 그리지 않았다: %q", rec.Body.String())
	}

	// **상태가 그대로라 아직 되돌릴 수 있다.** 이것이 이 검사의 목적이다.
	var status string
	if err := pool.QueryRow(context.Background(),
		`SELECT status FROM returns WHERE return_no = $1`, returnNo).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "반품접수" {
		t.Fatalf("거부된 수거 확인이 상태를 %q 로 옮겼다", status)
	}
	rec = postAdmin(t, d.ReturnAction, "/admin/orders/"+order.OrderNo+"/returns",
		map[string]string{"no": order.OrderNo},
		url.Values{"return_no": {returnNo}, "action": {"reject"}, "reason": {"재산정"}})
	if rec.Code != http.StatusSeeOther {
		t.Errorf("막다른 길이다 — 거부도 안 된다: HTTP %d (%q)", rec.Code, rec.Body.String())
	}
}

func assertVariantStock(t *testing.T, pool *pgxpool.Pool, variantID string, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(context.Background(),
		`SELECT stock FROM product_variants WHERE id = $1`, variantID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("재고 %d, want %d", got, want)
	}
}

// **환불 확정은 `order.refund` 를 요구한다** (D15 2.2: "A-507, A-511 (환불
// 확정 단계만)").
//
// 화면 권한(order.return)만으로 통과시키면, 반품 접수·수거만 맡기려고
// order.return 을 준 계정이 실제 환불까지 확정할 수 있다 — A-507 이
// order.refund 로 게이팅되는 것과 어긋난다.
func TestSettleNeedsRefundPermissionNotJustReturn(t *testing.T) {
	// 물류·CS 담당자: 반품 접수·수거는 하되 돈은 못 만진다.
	caller := &fakeCaller{perms: map[string]bool{"order.return": true},
		id: "", email: "cs@example.com"}
	d, pool := fixture(t, caller)
	order, returnNo := deliveredAdminOrder(t, d, pool)
	ctx := context.Background()

	// 수거 확인은 된다 — 돈이 나가지 않는다.
	if rec := postAdmin(t, d.ReturnAction, "/admin/orders/"+order.OrderNo+"/returns",
		map[string]string{"no": order.OrderNo},
		url.Values{"return_no": {returnNo}, "action": {"pickup"},
			"fault": {"판매자"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("수거 확인 = HTTP %d (%q)", rec.Code, rec.Body.String())
	}

	// 환불 확정은 막힌다.
	rec := postAdmin(t, d.ReturnAction, "/admin/orders/"+order.OrderNo+"/returns",
		map[string]string{"no": order.OrderNo},
		url.Values{"return_no": {returnNo}, "action": {"settle"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("order.refund 없이 환불 확정 = HTTP %d, want 403 (%q)", rec.Code, rec.Body.String())
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM refunds`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("권한 없이 환불 %d건이 확정됐다", n)
	}

	// order.refund 를 주면 통과한다.
	caller.perms["order.refund"] = true
	rec = postAdmin(t, d.ReturnAction, "/admin/orders/"+order.OrderNo+"/returns",
		map[string]string{"no": order.OrderNo},
		url.Values{"return_no": {returnNo}, "action": {"settle"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("order.refund 로 = HTTP %d (%q)", rec.Code, rec.Body.String())
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM refunds`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("환불 %d건, want 1", n)
	}
}

// 다른 주문의 반품 건은 관리자 화면에서도 조작할 수 없다.
func TestAdminReturnActionIsScopedToTheOrderInThePath(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"order.return": true, "order.refund": true},
		id: "", email: "op@example.com"}
	d, pool := fixture(t, caller)
	mine, _ := paidAdminOrder(t, d, pool)
	other, victimReturn := deliveredAdminOrderFor(t, d, pool, order2Slug)
	_ = other
	ctx := context.Background()

	// 내 주문 경로 + 남의 반품번호.
	rec := postAdmin(t, d.ReturnAction, "/admin/orders/"+mine.OrderNo+"/returns",
		map[string]string{"no": mine.OrderNo},
		url.Values{"return_no": {victimReturn}, "action": {"pickup"},
			"fault": {"판매자"}})
	if rec.Code != http.StatusNotFound {
		t.Errorf("남의 반품 수거 확인 = HTTP %d, want 404 (%q)", rec.Code, rec.Body.String())
	}
	var status string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM returns WHERE return_no = $1`, victimReturn).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "반품접수" {
		t.Errorf("남의 반품 상태가 %q 로 바뀌었다", status)
	}
}

// **폼이 보낸 배송비는 무시된다** (D19 A-511 받지 않는 필드).
//
// 수거 확인은 `order.return` 만으로 되고 재인증도 걸리지 않는다 — 창고 담당이
// 누르는 자리다. 그 자리에서 금액을 받으면 환불 확정만 `order.refund` 로
// 갈라놓은 것이 무의미해진다. 금액은 이미 정해진 뒤이기 때문이다.
func TestAdminPickupIgnoresShippingFeeFromForm(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"order.return": true}, id: "", email: "op@example.com"}
	d, pool := fixture(t, caller)
	order, returnNo := deliveredAdminOrder(t, d, pool)
	setFeeSetting(t, pool, "차감", 1000)

	rec := postAdmin(t, d.ReturnAction, "/admin/orders/"+order.OrderNo+"/returns",
		map[string]string{"no": order.OrderNo},
		url.Values{"return_no": {returnNo}, "action": {"pickup"},
			"fault": {"구매자"}, "fee_policy": {"별도청구"}, "fee_amount": {"99999999"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("수거 확인 = HTTP %d, want 303 (%q)", rec.Code, rec.Body.String())
	}

	var fee int
	var policy string
	if err := pool.QueryRow(context.Background(), `
		SELECT COALESCE(shipping_fee_amount, 0), COALESCE(shipping_fee_policy, '')
		FROM returns WHERE return_no = $1`, returnNo).Scan(&fee, &policy); err != nil {
		t.Fatal(err)
	}
	if fee != 1000 || policy != "차감" {
		t.Errorf("스냅샷 배송비 %d(%s) — 폼 값이 정책을 덮었다, want 1000(차감)", fee, policy)
	}
}

// A-512 설정값이 허용 범위를 벗어나면 422 다. 500 이면 운영자는 무엇을 고쳐야
// 하는지 모른다.
func TestAdminBadFeeSettingIs422(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"order.return": true}, id: "", email: "op@example.com"}
	d, pool := fixture(t, caller)
	order, returnNo := deliveredAdminOrder(t, d, pool)

	for _, bad := range []struct {
		policy string
		amount int
	}{{"차감", -5000}, {"반반", 1000}} {
		setFeeSetting(t, pool, bad.policy, bad.amount)
		rec := postAdmin(t, d.ReturnAction, "/admin/orders/"+order.OrderNo+"/returns",
			map[string]string{"no": order.OrderNo},
			url.Values{"return_no": {returnNo}, "action": {"pickup"}, "fault": {"구매자"}})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("정책 %q/%d = HTTP %d, want 422", bad.policy, bad.amount, rec.Code)
		}
	}
	var status string
	if err := pool.QueryRow(context.Background(),
		`SELECT status FROM returns WHERE return_no = $1`, returnNo).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "반품접수" {
		t.Errorf("거부된 설정이 상태를 %q 로 옮겼다", status)
	}
}

// setFeeSetting writes A-512's 반품 배송비 정책 (핸들러가 폼 대신 읽는 값).
func setFeeSetting(t *testing.T, pool *pgxpool.Pool, policy string, amount int) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO settings (key, value) VALUES ('order.return_fee_policy', $1),
		                                         ('order.return_fee_amount', $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
		policy, strconv.Itoa(amount))
	if err != nil {
		t.Fatal(err)
	}
}

// **A-511 에 교환 완료 동작이 실제로 연결돼 있다** (D19 A-511 「동작별 권한」:
// "교환 완료(재발송 송장) | POST | order.return").
//
// 상태머신에 `교환수거 → 교환발송` 화살표가 있어도 그것을 누를 창구가 없으면
// 교환은 수거된 채 멈춘다 — 예약 재고가 잠기고 그 품목은 다시 반품도 못 한다.
func TestAdminCanCompleteAnExchange(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"order.return": true},
		id: "", email: "op@example.com"}
	d, pool := fixture(t, caller)
	ctx := context.Background()
	order, _ := paidAdminOrder(t, d, pool)
	for _, to := range []commerce.Status{
		commerce.StatusPreparing, commerce.StatusShipping, commerce.StatusDelivered,
	} {
		if err := d.Commerce.TransitionOrder(ctx, order.OrderNo, to, "A-506"); err != nil {
			t.Fatal(err)
		}
	}
	// 같은 상품의 다른 조합 — 차액 0.
	var productID string
	if err := pool.QueryRow(ctx,
		`SELECT product_id FROM order_items WHERE id = $1`, order.Items[0].ID).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	var newVariant string
	if err := pool.QueryRow(ctx, `
		INSERT INTO product_variants (product_id,option_values,price_delta,stock)
		VALUES ($1,'{"크기":"M"}',1000,5) RETURNING id`, productID).Scan(&newVariant); err != nil {
		t.Fatal(err)
	}
	ret, err := d.Commerce.OpenReturn(ctx, order.OrderNo, commerce.ReturnRequest{
		Kind: commerce.KindExchange, NewVariantID: newVariant,
		Lines: []commerce.RefundLine{{OrderItemID: order.Items[0].ID, Quantity: 1}},
	}, "P-512", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	setFeeSetting(t, pool, "차감", 0)

	for _, action := range []string{"pickup", "exchange"} {
		rec := postAdmin(t, d.ReturnAction, "/admin/orders/"+order.OrderNo+"/returns",
			map[string]string{"no": order.OrderNo},
			url.Values{"return_no": {ret.ReturnNo}, "action": {action}, "fault": {"구매자"}})
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("%s = HTTP %d, want 303 (%q)", action, rec.Code, rec.Body.String())
		}
	}

	var status string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM returns WHERE return_no = $1`, ret.ReturnNo).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(commerce.StatusExchangeShipped) {
		t.Errorf("교환 완료 뒤 상태 %q, want %q — 교환수거에서 못 나갔다",
			status, commerce.StatusExchangeShipped)
	}
}

// **A-512 는 구매확정 기간이 반품 기간보다 길 것을 요구한다** (D19 A-512).
//
// 같거나 짧으면 구매확정이 먼저 닫혀서, 반품 기간이 남아 있는데 반품을 걸 수
// 없는 주문이 생긴다 — 상태머신은 `배송완료` 에서만 반품을 받는다 (FR-604).
func TestPolicyRejectsConfirmWindowShorterThanReturnWindow(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"settings.update": true},
		id: "u1", email: "op@example.com"}
	d, pool := fixture(t, caller)

	for _, tc := range []struct{ ret, confirm string }{{"7", "7"}, {"7", "6"}} {
		rec := postAdmin(t, d.PolicySave, "/admin/commerce/policy", nil, url.Values{
			"order.return_days": {tc.ret}, "order.confirm_days": {tc.confirm},
			"order.return_fee_policy": {"차감"}, "order.return_fee_amount": {"0"}})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("반품 %s / 확정 %s = HTTP %d, want 422", tc.ret, tc.confirm, rec.Code)
		}
	}
	// 아무것도 저장되지 않았다.
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM settings WHERE key LIKE 'order.%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("거부된 저장이 %d행 남았다", n)
	}

	// 길면 저장된다 — 위 단언이 "무엇이든 막힌다" 를 본 것이 아니라는 것.
	rec := postAdmin(t, d.PolicySave, "/admin/commerce/policy", nil, url.Values{
		"order.return_days": {"7"}, "order.confirm_days": {"8"},
		"order.return_fee_policy": {"별도청구"}, "order.return_fee_amount": {"3000"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("정상 저장 = HTTP %d (%q)", rec.Code, rec.Body.String())
	}
	kv, err := d.Content.Settings(context.Background(),
		"order.return_days", "order.confirm_days",
		"order.return_fee_policy", "order.return_fee_amount")
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"order.return_days": "7", "order.confirm_days": "8",
		"order.return_fee_policy": "별도청구", "order.return_fee_amount": "3000",
	} {
		if kv[k] != want {
			t.Errorf("%s = %q, want %q", k, kv[k], want)
		}
	}
}

// 배송비 부담 방식과 금액도 A-512 가 막는다. 여기서 새면 A-511 이 500 을 낸다.
func TestPolicyRejectsBadShippingFeeValues(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"settings.update": true},
		id: "u1", email: "op@example.com"}
	d, _ := fixture(t, caller)

	for _, tc := range []struct{ policy, amount string }{
		{"반반", "0"}, {"차감", "-1"}, {"차감", "삼천원"}, {"", "0"},
	} {
		rec := postAdmin(t, d.PolicySave, "/admin/commerce/policy", nil, url.Values{
			"order.return_days": {"7"}, "order.confirm_days": {"8"},
			"order.return_fee_policy": {tc.policy}, "order.return_fee_amount": {tc.amount}})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("방식 %q / 금액 %q = HTTP %d, want 422", tc.policy, tc.amount, rec.Code)
		}
	}
}

// **정책을 바꿔도 이미 접수된 반품의 환불액은 달라지지 않는다** (D19 A-512).
// 수거 확인 시점에 `returns` 로 복사되기 때문이다 — 참조로 뒀다면 정책 변경이
// 곧 과거 환불액 변경이다.
func TestPolicyChangeDoesNotApplyRetroactively(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"settings.update": true,
		"order.return": true, "order.refund": true}, id: "u1", email: "op@example.com"}
	d, pool := fixture(t, caller)
	order, returnNo := deliveredAdminOrder(t, d, pool)

	setFeeSetting(t, pool, "차감", 1000)
	if rec := postAdmin(t, d.ReturnAction, "/admin/orders/"+order.OrderNo+"/returns",
		map[string]string{"no": order.OrderNo},
		url.Values{"return_no": {returnNo}, "action": {"pickup"}, "fault": {"구매자"}},
	); rec.Code != http.StatusSeeOther {
		t.Fatalf("수거 확인 = HTTP %d (%q)", rec.Code, rec.Body.String())
	}

	// 정책을 바꾼다.
	if rec := postAdmin(t, d.PolicySave, "/admin/commerce/policy", nil, url.Values{
		"order.return_days": {"7"}, "order.confirm_days": {"8"},
		"order.return_fee_policy": {"차감"}, "order.return_fee_amount": {"9000"}},
	); rec.Code != http.StatusSeeOther {
		t.Fatalf("정책 저장 = HTTP %d", rec.Code)
	}

	// 이미 찍힌 스냅샷은 그대로다.
	var fee int
	if err := pool.QueryRow(context.Background(),
		`SELECT COALESCE(shipping_fee_amount,0) FROM returns WHERE return_no = $1`,
		returnNo).Scan(&fee); err != nil {
		t.Fatal(err)
	}
	if fee != 1000 {
		t.Errorf("스냅샷 배송비 %d, want 1000 — 정책 변경이 소급됐다", fee)
	}
}

// **A-503 은 재고 절대값을 조용히 무시하지 않고 거부한다** (D19 A-503).
//
// 무시하면 운영자는 재고가 자기 입력대로 됐다고 믿는다. 절대값 덮어쓰기는
// 동시 주문의 판매분을 지우므로 필드 자체가 없어야 하고, 실려 오면 거부다.
func TestVariantSaveRefusesAbsoluteStock(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"product.manage": true},
		id: "u1", email: "op@example.com"}
	d, pool := fixture(t, caller)
	ctx := context.Background()

	var productID, variantID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO products (slug,name,base_price,is_visible)
		 VALUES ('tee','티셔츠',12000,true) RETURNING id`).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO product_variants (product_id,option_values,price_delta,stock)
		 VALUES ($1,'{"크기":"L"}',0,7) RETURNING id`, productID).Scan(&variantID); err != nil {
		t.Fatal(err)
	}

	for _, field := range []string{"stock", "stock_absolute"} {
		rec := postAdmin(t, d.VariantSave, "/admin/products/"+productID+"/variants",
			map[string]string{"id": productID},
			url.Values{"variant_id": {variantID}, "delta_" + variantID: {"1"},
				"price_delta_" + variantID: {"0"}, field: {"999"}})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s 필드 = HTTP %d, want 422", field, rec.Code)
		}
	}
	// 재고가 그대로다 — 거부된 요청이 아무것도 바꾸지 않았다.
	var stock int
	if err := pool.QueryRow(ctx,
		`SELECT stock FROM product_variants WHERE id = $1`, variantID).Scan(&stock); err != nil {
		t.Fatal(err)
	}
	if stock != 7 {
		t.Errorf("재고 %d, want 7 — 거부된 요청이 반영됐다", stock)
	}

	// 증감만 보내면 통과한다.
	rec := postAdmin(t, d.VariantSave, "/admin/products/"+productID+"/variants",
		map[string]string{"id": productID},
		url.Values{"variant_id": {variantID}, "delta_" + variantID: {"3"},
			"price_delta_" + variantID: {"0"}, "version_" + variantID: {"7"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("증감 저장 = HTTP %d (%q)", rec.Code, rec.Body.String())
	}
	if err := pool.QueryRow(ctx,
		`SELECT stock FROM product_variants WHERE id = $1`, variantID).Scan(&stock); err != nil {
		t.Fatal(err)
	}
	if stock != 10 {
		t.Errorf("재고 %d, want 10", stock)
	}
}

// **A-502 도 재고·SKU·조합을 받지 않는다** (D19 A-502 받지 않는 필드).
// 받으면 재고 절대값을 덮어쓰는 경로가 하나 더 생긴다.
func TestProductSaveRefusesStockAndSkuFields(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"product.manage": true},
		id: "u1", email: "op@example.com"}
	d, pool := fixture(t, caller)
	var productID string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO products (slug,name,base_price,is_visible)
		 VALUES ('tee','티셔츠',12000,true) RETURNING id`).Scan(&productID); err != nil {
		t.Fatal(err)
	}

	for _, field := range []string{"stock", "sku", "variant_id"} {
		rec := postAdmin(t, d.ProductSave, "/admin/products/"+productID,
			map[string]string{"id": productID},
			url.Values{"name": {"티셔츠"}, "slug": {"tee"}, "base_price": {"12000"},
				field: {"999"}})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s 필드 = HTTP %d, want 422", field, rec.Code)
		}
	}
}

// 가격은 정수 minor unit 이다 (D50). 소수·문자열·음수는 422.
func TestProductSaveRefusesNonIntegerPrice(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"product.manage": true},
		id: "u1", email: "op@example.com"}
	d, pool := fixture(t, caller)
	var productID string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO products (slug,name,base_price,is_visible)
		 VALUES ('tee','티셔츠',12000,true) RETURNING id`).Scan(&productID); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{"12000.5", "12,000", "만이천원", "-1", ""} {
		rec := postAdmin(t, d.ProductSave, "/admin/products/"+productID,
			map[string]string{"id": productID},
			url.Values{"name": {"티셔츠"}, "slug": {"tee"}, "base_price": {bad}})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("가격 %q = HTTP %d, want 422", bad, rec.Code)
		}
	}
	var price int
	if err := pool.QueryRow(context.Background(),
		`SELECT base_price FROM products WHERE id = $1`, productID).Scan(&price); err != nil {
		t.Fatal(err)
	}
	if price != 12000 {
		t.Errorf("가격이 %d 로 바뀌었다", price)
	}
}

// reconcileGateway 는 조회 API 가 무엇을 말하든 그대로 돌려준다.
type reconcileGateway struct {
	status commerce.PaymentStatus
	amount int
	err    error
}

func (g reconcileGateway) Confirm(context.Context, commerce.ConfirmRequest) (*commerce.Payment, error) {
	return nil, nil
}
func (g reconcileGateway) Cancel(context.Context, commerce.CancelRequest) (*commerce.Payment, error) {
	return nil, nil
}
func (g reconcileGateway) Get(_ context.Context, key string) (*commerce.Payment, error) {
	if g.err != nil {
		return nil, g.err
	}
	// **PG 는 우리 PK 를 모른다.** 조회 키가 아니면 "기록 없음" 이다 — 진짜
	// 게이트웨이가 그렇게 답하므로 여기서도 그렇게 답해야, 잘못된 키를
	// 넘기는 변이가 잡힌다.
	if !strings.HasPrefix(key, "pk-") {
		return nil, nil
	}
	return &commerce.Payment{PaymentKey: key, Status: g.status, Amount: g.amount}, nil
}
func (g reconcileGateway) VerifyWebhook(context.Context, []byte) (*commerce.WebhookEvent, error) {
	return nil, nil
}

// **A-508 은 「PG 는 승인, 우리는 대기」를 찾는 유일한 화면이다** (D50).
//
// 그 상태는 돈이 나갔는데 주문에 반영되지 않은 것이고, 다른 어떤 화면도
// 그것을 보여주지 않는다.
func TestReconcileFindsApprovedButUnrecorded(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"payment.view": true}, id: "u1"}
	d, pool := fixture(t, caller)
	ctx := context.Background()

	// 승인 API 는 성공했는데 우리 트랜잭션이 실패한 모습: payments 가 `대기`.
	var orderID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO orders (order_no, status, receiver_name, receiver_phone,
		                    postcode, address1, orderer_email, orderer_phone, total_amount)
		VALUES ('RC0001','결제대기','받는이','010-0000-0000','12345','서울',
		        'a@example.com','010-1111-1111',15000) RETURNING id`).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO payments (order_id, kind, status, pg, payment_key, approved_amount)
		VALUES ($1,'주문결제','대기','toss','pk-rc',15000)`, orderID); err != nil {
		t.Fatal(err)
	}
	d.Gateway = func() commerce.Gateway {
		return reconcileGateway{status: commerce.PaymentApproved, amount: 15000}
	}

	var rows []commerce.ReconcileRow
	d.Render = func(_ http.ResponseWriter, _ *http.Request, _ string, _ int, data any) {
		if m, ok := data.(map[string]any); ok {
			rows, _ = m["Rows"].([]commerce.ReconcileRow)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/reconcile", nil)
	d.Reconcile(httptest.NewRecorder(), req)

	if len(rows) != 1 {
		t.Fatalf("대사 행 %d개, want 1", len(rows))
	}
	if !strings.Contains(rows[0].Diff, "돈이 나갔는데") {
		t.Errorf("차이 설명 %q — 가장 위험한 상태를 지목하지 않았다", rows[0].Diff)
	}
	// **우리 기록을 고치지 않는다.** 조회 응답 하나가 장부를 바꾸면 안 된다.
	var status string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM payments WHERE payment_key = 'pk-rc'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "대기" {
		t.Errorf("대사가 우리 기록을 %q 로 바꿨다 — 조회는 대조지 수정이 아니다", status)
	}
}

// 일치하면 차이가 비어 있다 — 위 검사가 "무엇이든 불일치" 를 본 것이 아니다.
func TestReconcileReportsNoDiffWhenTheyAgree(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"payment.view": true}, id: "u1"}
	d, pool := fixture(t, caller)
	order, total := paidAdminOrder(t, d, pool)
	_ = order
	d.Gateway = func() commerce.Gateway {
		return reconcileGateway{status: commerce.PaymentApproved, amount: total}
	}

	var rows []commerce.ReconcileRow
	d.Render = func(_ http.ResponseWriter, _ *http.Request, _ string, _ int, data any) {
		if m, ok := data.(map[string]any); ok {
			rows, _ = m["Rows"].([]commerce.ReconcileRow)
		}
	}
	d.Reconcile(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/admin/reconcile", nil))
	if len(rows) != 1 {
		t.Fatalf("대사 행 %d개", len(rows))
	}
	if rows[0].Diff != "" {
		t.Errorf("일치인데 차이 %q", rows[0].Diff)
	}
}

// A-603 은 **처리되지 않은 웹훅을 상단에** 올린다. 자동 재처리를 두지 않기로
// 했으므로 (D50) 사람이 그것을 봐야 한다.
func TestWebhookLogPutsUnprocessedFirst(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"payment.view": true}, id: "u1"}
	d, pool := fixture(t, caller)
	ctx := context.Background()

	// 처리완료가 **나중에** 들어간다 — 시각순이면 이것이 위로 온다.
	for _, tc := range []struct{ id, status string }{
		{"ev-old-unprocessed", "수신"},
		{"ev-new-done", "처리완료"},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO webhook_events (pg, event_id, status, payload)
			VALUES ('toss', $1, $2, '{}'::jsonb)`, tc.id, tc.status); err != nil {
			t.Fatal(err)
		}
	}

	var rows []commerce.WebhookRow
	var warning string
	d.Render = func(_ http.ResponseWriter, _ *http.Request, _ string, _ int, data any) {
		if m, ok := data.(map[string]any); ok {
			rows, _ = m["Rows"].([]commerce.WebhookRow)
			warning, _ = m["Warning"].(string)
		}
	}
	d.WebhookLog(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/admin/webhooks", nil))

	if len(rows) != 2 {
		t.Fatalf("이력 %d행, want 2", len(rows))
	}
	if rows[0].Status != "수신" {
		t.Errorf("상단이 %q — 처리되지 않은 행이 먼저 와야 한다", rows[0].Status)
	}
	if !strings.Contains(warning, "처리되지 않은") {
		t.Errorf("경고 %q — 남은 건을 알리지 않았다", warning)
	}
}

// **A-515 는 조정값을 받지 않는다** (D19 A-515 받지 않는 필드).
//
// 받으면 실사가 임의 재고 조작 창구가 된다. 조용히 무시하면 운영자는 자기가
// 보낸 조정값이 적용됐다고 믿는다.
func TestStocktakeRefusesAClientSuppliedDelta(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"product.manage": true},
		id: "u1", email: "op@example.com"}
	d, pool := fixture(t, caller)
	ctx := context.Background()

	var productID, variantID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO products (slug,name,base_price,is_visible)
		 VALUES ('tee','티셔츠',12000,true) RETURNING id`).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO product_variants (product_id,option_values,price_delta,stock)
		 VALUES ($1,'{"크기":"L"}',0,10) RETURNING id`, productID).Scan(&variantID); err != nil {
		t.Fatal(err)
	}

	for _, field := range []string{"delta", "adjustment"} {
		rec := postAdmin(t, d.Stocktake, "/admin/scan/stocktake", nil, url.Values{
			"scanned": {variantID}, "ledger": {"10"}, "counted": {"10"}, field: {"999"}})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s 필드 = HTTP %d, want 422", field, rec.Code)
		}
	}
	var stock int
	if err := pool.QueryRow(ctx,
		`SELECT stock FROM product_variants WHERE id = $1`, variantID).Scan(&stock); err != nil {
		t.Fatal(err)
	}
	if stock != 10 {
		t.Errorf("재고 %d — 거부된 요청이 반영됐다", stock)
	}

	// 실측만 보내면 서버가 조정값을 계산한다.
	rec := postAdmin(t, d.Stocktake, "/admin/scan/stocktake", nil, url.Values{
		"scanned": {variantID}, "ledger": {"10"}, "counted": {"7"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("정상 실사 = HTTP %d (%q)", rec.Code, rec.Body.String())
	}
	if err := pool.QueryRow(ctx,
		`SELECT stock FROM product_variants WHERE id = $1`, variantID).Scan(&stock); err != nil {
		t.Fatal(err)
	}
	if stock != 7 {
		t.Errorf("재고 %d, want 7", stock)
	}
}

// **작업 로그에 장부·실측·조정 셋이 모두 남는다** (FR-622, D15 7절).
// 하나라도 빠지면 무엇을 근거로 재고가 바뀌었는지 재구성할 수 없다.
func TestStocktakeLogsAllThreeNumbers(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"product.manage": true},
		id: "u1", email: "op@example.com"}
	d, pool := fixture(t, caller)
	ctx := context.Background()

	var productID, variantID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO products (slug,name,base_price,is_visible)
		 VALUES ('tee','티셔츠',12000,true) RETURNING id`).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO product_variants (product_id,option_values,price_delta,stock)
		 VALUES ($1,'{"크기":"L"}',0,10) RETURNING id`, productID).Scan(&variantID); err != nil {
		t.Fatal(err)
	}

	postAdmin(t, d.Stocktake, "/admin/scan/stocktake", nil, url.Values{
		"scanned": {variantID}, "ledger": {"10"}, "counted": {"7"}})

	var summary string
	if err := pool.QueryRow(ctx,
		`SELECT summary FROM operation_logs ORDER BY created_at DESC LIMIT 1`).Scan(&summary); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"장부 10", "실측 7", "조정 -3"} {
		if !strings.Contains(summary, want) {
			t.Errorf("작업 로그에 %q 가 없다: %q", want, summary)
		}
	}
}

// **A-516 은 주문 상태와 재고를 건드리지 않는다** (FR-623). 거부는 로그에 남는다.
func TestPickCheckRefusesAndLogsWithoutChangingAnything(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"order.update": true},
		id: "u1", email: "op@example.com"}
	d, pool := fixture(t, caller)
	ctx := context.Background()
	order, _ := paidAdminOrder(t, d, pool)

	var statusBefore string
	var stockBefore int
	if err := pool.QueryRow(ctx, `
		SELECT o.status, v.stock FROM orders o
		JOIN order_items oi ON oi.order_id = o.id
		JOIN product_variants v ON v.id = oi.variant_id
		WHERE o.order_no = $1`, order.OrderNo).Scan(&statusBefore, &stockBefore); err != nil {
		t.Fatal(err)
	}

	// 주문에 없는 조합을 스캔한다.
	rec := postAdmin(t, d.PickCheck, "/admin/orders/"+order.OrderNo+"/pick",
		map[string]string{"no": order.OrderNo},
		url.Values{"scanned": {"00000000-0000-4000-8000-000000000000"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("주문에 없는 조합 = HTTP %d, want 422", rec.Code)
	}

	var logged string
	if err := pool.QueryRow(ctx,
		`SELECT summary FROM operation_logs ORDER BY created_at DESC LIMIT 1`).Scan(&logged); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logged, "피킹 대조 거부") {
		t.Errorf("거부가 로그에 남지 않았다: %q", logged)
	}

	var statusAfter string
	var stockAfter int
	if err := pool.QueryRow(ctx, `
		SELECT o.status, v.stock FROM orders o
		JOIN order_items oi ON oi.order_id = o.id
		JOIN product_variants v ON v.id = oi.variant_id
		WHERE o.order_no = $1`, order.OrderNo).Scan(&statusAfter, &stockAfter); err != nil {
		t.Fatal(err)
	}
	if statusAfter != statusBefore {
		t.Errorf("주문 상태가 %s → %s 로 바뀌었다", statusBefore, statusAfter)
	}
	if stockAfter != stockBefore {
		t.Errorf("재고가 %d → %d 로 바뀌었다 — 이중 차감이다", stockBefore, stockAfter)
	}
}

// **QR 이 담는 값은 `product_variants.id` 다** (FR-620, W3-37 완료 기준).
//
// SKU 를 담으면 외부 시스템이 SKU 를 바꾸는 순간 이미 붙은 스티커가 다른
// 조합을 가리키거나 아무것도 가리키지 않게 된다. 그림만 봐서는 알 수 없으므로
// **같은 id 로 독립적으로 인코딩한 결과와 대조**하고, SKU 를 바꾼 뒤에도
// 같은 그림이 나오는지 본다.
func TestQRLabelEncodesTheVariantIDNotTheSKU(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"product.view": true}, id: "", email: "op@example.com"}
	d, pool := fixture(t, caller)
	ctx := context.Background()

	var productID, withSKU, noSKU string
	if err := pool.QueryRow(ctx,
		`INSERT INTO products (slug,name,base_price,is_visible)
		 VALUES ('tee','티셔츠',12000,true) RETURNING id`).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO product_variants (product_id,option_values,price_delta,stock,sku)
		 VALUES ($1,'{"크기":"L"}',0,3,'SKU-A') RETURNING id`, productID).Scan(&withSKU); err != nil {
		t.Fatal(err)
	}
	// **SKU 가 NULL 인 조합도 발행된다** — 라벨이 없으면 그 조합은 입고 스캔을
	// 할 수 없어 영원히 품절로 남는다.
	if err := pool.QueryRow(ctx,
		`INSERT INTO product_variants (product_id,option_values,price_delta,stock)
		 VALUES ($1,'{"크기":"M"}',0,0) RETURNING id`, productID).Scan(&noSKU); err != nil {
		t.Fatal(err)
	}

	render := func() map[string]string {
		t.Helper()
		out := map[string]string{}
		d.Render = func(_ http.ResponseWriter, _ *http.Request, _ string, _ int, data any) {
			m, _ := data.(map[string]any)
			labels, _ := m["Labels"].([]map[string]any)
			for _, l := range labels {
				v := l["Variant"].(commerce.Variant)
				out[v.ID] = string(l["SVG"].(template.HTML))
			}
		}
		req := httptest.NewRequest(http.MethodGet, "/admin/products/"+productID+"/labels", nil)
		req.SetPathValue("id", productID)
		d.QRLabel(httptest.NewRecorder(), req)
		return out
	}

	before := render()
	if len(before) != 2 {
		t.Fatalf("라벨 %d개, want 2 (SKU 없는 조합도 발행된다)", len(before))
	}
	// **독립 인코딩과 같아야 한다** — 다르면 담긴 값이 id 가 아니다.
	for _, id := range []string{withSKU, noSKU} {
		assertQRCarries(t, before[id], id)
	}

	// SKU 를 바꾼다. 스티커는 그대로여야 한다.
	if _, err := pool.Exec(ctx,
		`UPDATE product_variants SET sku = 'SKU-CHANGED' WHERE id = $1`, withSKU); err != nil {
		t.Fatal(err)
	}
	after := render()
	if after[withSKU] != before[withSKU] {
		t.Error("SKU 를 바꿨더니 QR 이 달라졌다 — 이미 붙은 스티커가 무의미해진다")
	}
}
