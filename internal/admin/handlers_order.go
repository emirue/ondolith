package admin

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/emirue/ondolith/internal/commerce"
)

// OrderList is A-504.
func (d *Deps) OrderList(w http.ResponseWriter, r *http.Request) {
	_, ok := d.require(w, r, "order.view")
	if !ok {
		return
	}
	orders, err := d.Commerce.AdminOrders(r.Context(), r.URL.Query().Get("status"), pageOf(r))
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.Render(w, r, "admin/orders.html", http.StatusOK, map[string]any{
		"Orders": orders, "Status": r.URL.Query().Get("status"),
		"Statuses": commerce.AllStatuses(),
	})
}

// OrderDetail is A-505.
func (d *Deps) OrderDetail(w http.ResponseWriter, r *http.Request) {
	_, ok := d.require(w, r, "order.view")
	if !ok {
		return
	}
	order, err := d.Commerce.OrderByNoUnscoped(r.Context(), r.PathValue("no"))
	if errors.Is(err, commerce.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	shipments, err := d.Commerce.Shipments(r.Context(), order.ID)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.Render(w, r, "admin/order.html", http.StatusOK, map[string]any{
		"Order": order, "Shipments": shipments,
		// 드롭다운은 **상태머신이 낸 목록**이다. 전체 상태를 나열하고 서버에서
		// 검증하지 않는 것이 D14 5절이 지목한 가장 흔한 구현 실수다. 여기서
		// 파생시키면 화면과 서버가 같은 표를 본다.
		"Next": commerce.Next(order.Status),
	})
}

// OrderTransition is A-506.
//
// 화면이 무엇을 보여줬든 서버가 상태머신에 묻는다. 드롭다운을 좁히는 것은
// 편의이고, 거부하는 것은 이 핸들러다 (D15 4.3: 숨기는 것은 보안이 아니다).
func (d *Deps) OrderTransition(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "order.update")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	orderNo := r.PathValue("no")
	to := commerce.Status(r.PostFormValue("to"))

	err := d.Commerce.TransitionOrder(r.Context(), orderNo, to, "A-506")
	switch {
	case errors.Is(err, commerce.ErrNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(err, commerce.ErrTransitionNotAllowed),
		errors.Is(err, commerce.ErrActorNotAllowed),
		errors.Is(err, commerce.ErrUnknownStatus):
		// 422 다. 요청은 이해했고 규칙이 거부한 것이다.
		d.Render(w, r, "admin/order.html", http.StatusUnprocessableEntity,
			map[string]any{"Error": err.Error()})
		return
	case err != nil:
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}

	d.log(r, c, "order.transition", "order", orderNo, "주문 상태를 "+string(to)+" 로 변경")
	http.Redirect(w, r, "/admin/orders/"+orderNo, http.StatusSeeOther)
}

// ShippingForm is A-510 GET.
func (d *Deps) ShippingForm(w http.ResponseWriter, r *http.Request) {
	_, ok := d.require(w, r, "order.update")
	if !ok {
		return
	}
	order, err := d.Commerce.OrderByNoUnscoped(r.Context(), r.PathValue("no"))
	if errors.Is(err, commerce.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	shipments, err := d.Commerce.Shipments(r.Context(), order.ID)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.Render(w, r, "admin/shipping.html", http.StatusOK,
		map[string]any{"Order": order, "Shipments": shipments})
}

// ShippingSave is A-510 POST — the tracking number.
//
// 송장을 넣는 것과 상태를 옮기는 것은 다른 연산이다. 여기서 함께 옮기면
// `배송준비 → 배송중` 을 일으키는 화면이 둘이 되고, FR-623 이 A-516 에 대해
// 지적한 것과 같은 문제가 된다.
func (d *Deps) ShippingSave(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "order.update")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	orderNo := r.PathValue("no")
	carrier := r.PostFormValue("carrier")
	tracking := r.PostFormValue("tracking_no")
	if carrier == "" || tracking == "" {
		d.Render(w, r, "admin/shipping.html", http.StatusUnprocessableEntity,
			map[string]any{"Error": "택배사와 송장번호를 입력하세요."})
		return
	}

	err := d.Commerce.RecordShipment(r.Context(), orderNo, carrier, tracking, time.Now())
	switch {
	case errors.Is(err, commerce.ErrNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(err, commerce.ErrShipmentExists):
		d.Render(w, r, "admin/shipping.html", http.StatusUnprocessableEntity,
			map[string]any{"Error": "이미 최초 발송이 기록된 주문입니다."})
		return
	case err != nil:
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}

	d.log(r, c, "order.shipment", "order", orderNo, "송장 "+carrier+" "+tracking+" 기록")
	http.Redirect(w, r, "/admin/orders/"+orderNo, http.StatusSeeOther)
}

// ProductList is A-501.
func (d *Deps) ProductList(w http.ResponseWriter, r *http.Request) {
	_, ok := d.require(w, r, "product.view")
	if !ok {
		return
	}
	// VisibleOnly 가 false 다 — 관리자는 숨긴 상품도 본다. 공개 화면과 같은
	// 함수를 쓰되 인자가 다르다.
	products, err := d.Commerce.ListProducts(r.Context(),
		commerce.ProductQuery{Page: pageOf(r), Sort: r.URL.Query().Get("sort")})
	if errors.Is(err, commerce.ErrUnknownSort) {
		http.Error(w, "알 수 없는 정렬입니다.", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.Render(w, r, "admin/products.html", http.StatusOK, map[string]any{"Products": products})
}

// CategoryList is A-509 GET.
func (d *Deps) CategoryList(w http.ResponseWriter, r *http.Request) {
	_, ok := d.require(w, r, "product.manage")
	if !ok {
		return
	}
	cats, err := d.Commerce.Categories(r.Context())
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.Render(w, r, "admin/categories.html", http.StatusOK, map[string]any{"Categories": cats})
}

// CategoryReparent is A-509 POST.
func (d *Deps) CategoryReparent(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "product.manage")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	err := d.Commerce.Reparent(r.Context(), r.PostFormValue("id"), r.PostFormValue("parent_id"))
	switch {
	case errors.Is(err, commerce.ErrCategoryCycle), errors.Is(err, commerce.ErrCategoryDepth),
		errors.Is(err, commerce.ErrCategoryMissing):
		cats, _ := d.Commerce.Categories(r.Context())
		d.Render(w, r, "admin/categories.html", http.StatusUnprocessableEntity,
			map[string]any{"Categories": cats, "Error": err.Error()})
		return
	case err != nil:
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.log(r, c, "category.reparent", "category", r.PostFormValue("id"), "상위 카테고리 변경")
	http.Redirect(w, r, "/admin/categories", http.StatusSeeOther)
}

// pageOf reads the page number, clamped to 1.
func pageOf(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// RefundForm is A-507 GET — cancel / partial refund.
func (d *Deps) RefundForm(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "order.refund")
	if !ok {
		return
	}
	order, err := d.Commerce.OrderByNoUnscoped(r.Context(), r.PathValue("no"))
	if errors.Is(err, commerce.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.renderRefund(w, r, c, order, http.StatusOK, "")
}

func (d *Deps) renderRefund(w http.ResponseWriter, r *http.Request, c Caller,
	order *commerce.OrderDetail, code int, msg string) {

	refunds, err := d.Commerce.Refunds(r.Context(), order.ID)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	approved, refunded, perr := d.Commerce.RefundedTotal(r.Context(), order.OrderNo)
	if perr != nil && !errors.Is(perr, commerce.ErrNoPayment) {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.Render(w, r, "admin/refund.html", code, map[string]any{
		"Order": order, "Refunds": refunds,
		"Approved": approved, "Refunded": refunded, "Remaining": approved - refunded,
		// D15 5.3-1: 최근 15분 내 확인이 있으면 비밀번호 필드를 그리지 않는다.
		// 매 클릭마다 물으면 관리자가 비밀번호를 브라우저에 저장하게 된다.
		"NeedsReauth": c.NeedsReauth(),
		"Error":       msg,
	})
}

// RefundSave is A-507 POST.
//
// **돈이 나간다** — D15 5.3-1 의 재인증 대상이다. 미충족이면 403 과 함께 그
// 화면의 폼을 다시 그린다 (리다이렉트하지 않는다, D19 C7).
func (d *Deps) RefundSave(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "order.refund")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	order, err := d.Commerce.OrderByNoUnscoped(r.Context(), r.PathValue("no"))
	if errors.Is(err, commerce.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	if c.NeedsReauth() {
		d.renderRefund(w, r, c, order, http.StatusForbidden, "비밀번호를 다시 입력하세요.")
		return
	}

	// 금액을 받지 않는다 (FR-625). 품목과 수량만 받고 서버가 스냅샷에서 계산
	// 한다 — 관리자 화면이라고 예외를 두면 그 경로만 위변조 가능해진다.
	lines := readAdminRefundLines(r)
	if len(lines) == 0 {
		d.renderRefund(w, r, c, order, http.StatusUnprocessableEntity,
			"환불할 품목과 수량을 고르세요.")
		return
	}
	key, err := commerce.NewRequestKey()
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}

	_, amount, err := d.Commerce.RequestRefund(r.Context(), order.OrderNo, lines,
		"관리자", r.PostFormValue("reason"), key)
	switch {
	case errors.Is(err, commerce.ErrNoPayment):
		d.renderRefund(w, r, c, order, http.StatusUnprocessableEntity, "환불할 결제가 없습니다.")
		return
	case errors.Is(err, commerce.ErrRefundQuantity), errors.Is(err, commerce.ErrQuantityRange):
		d.renderRefund(w, r, c, order, http.StatusUnprocessableEntity,
			"환불 수량이 남은 수량을 넘습니다.")
		return
	case errors.Is(err, commerce.ErrRefundExceeds):
		d.renderRefund(w, r, c, order, http.StatusUnprocessableEntity, "환불 가능 금액을 넘었습니다.")
		return
	case errors.Is(err, commerce.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}

	// 돈이 움직인 일은 반드시 로그에 남는다 (D15 7절).
	d.log(r, c, "order.refund", "order", order.OrderNo,
		"환불 "+itoa(amount)+"원 접수 ("+itoa(len(lines))+"개 품목)")
	http.Redirect(w, r, "/admin/orders/"+order.OrderNo+"/refund", http.StatusSeeOther)
}

func readAdminRefundLines(r *http.Request) []commerce.RefundLine {
	var out []commerce.RefundLine
	for _, id := range r.PostForm["item_id"] {
		qty, err := strconv.Atoi(r.PostFormValue("qty_" + id))
		if err != nil || qty < 1 {
			continue
		}
		out = append(out, commerce.RefundLine{OrderItemID: id, Quantity: qty})
	}
	return out
}

func itoa(n int) string { return strconv.Itoa(n) }

// ReturnList is A-511 GET — 반품·교환 처리.
func (d *Deps) ReturnList(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "order.return")
	if !ok {
		return
	}
	order, err := d.Commerce.OrderByNoUnscoped(r.Context(), r.PathValue("no"))
	if errors.Is(err, commerce.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.renderReturns(w, r, c, order, http.StatusOK, "")
}

func (d *Deps) renderReturns(w http.ResponseWriter, r *http.Request, c Caller,
	order *commerce.OrderDetail, code int, msg string) {

	returns, err := d.Commerce.Returns(r.Context(), order.ID)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.Render(w, r, "admin/returns.html", code, map[string]any{
		"Order": order, "Returns": returns,
		// 돈이 나가는 단계(환불 확정)에만 재인증이 걸린다 (D15 5.3-1).
		"NeedsReauth": c.NeedsReauth(),
		"Error":       msg,
	})
}

// ReturnAction is A-511 POST — 수거 확인 · 환불 확정 · 거부.
//
// 세 동작이 한 핸들러인 이유: 셋 다 같은 반품 건을 옮기고, 나누면 소유권과
// 재인증 검사가 세 벌이 되어 한 벌만 고쳐지는 일이 생긴다.
func (d *Deps) ReturnAction(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "order.return")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	order, err := d.Commerce.OrderByNoUnscoped(r.Context(), r.PathValue("no"))
	if errors.Is(err, commerce.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	returnNo := r.PostFormValue("return_no")
	action := r.PostFormValue("action")

	switch action {
	case "pickup":
		fee, _ := strconv.Atoi(r.PostFormValue("fee_amount"))
		err = d.Commerce.ConfirmPickup(r.Context(), returnNo,
			r.PostFormValue("fault"), r.PostFormValue("fee_policy"), fee, "A-511")
		if err == nil {
			d.log(r, c, "return.pickup", "return", returnNo, "수거 확인")
		}
	case "reject":
		err = d.Commerce.RejectReturn(r.Context(), returnNo, r.PostFormValue("reason"), "A-511")
		if err == nil {
			d.log(r, c, "return.reject", "return", returnNo, "반품·교환 거부")
		}
	case "settle":
		// **돈이 나간다** — 여기만 재인증을 요구한다 (D15 5.3-1).
		if c.NeedsReauth() {
			d.renderReturns(w, r, c, order, http.StatusForbidden, "비밀번호를 다시 입력하세요.")
			return
		}
		var key string
		key, err = commerce.NewRequestKey()
		if err == nil {
			var amount int
			amount, err = d.Commerce.SettleReturn(r.Context(), returnNo, "A-511", key)
			if err == nil {
				d.log(r, c, "return.settle", "return", returnNo,
					"반품 환불 "+itoa(amount)+"원 확정")
			}
		}
	default:
		d.renderReturns(w, r, c, order, http.StatusUnprocessableEntity, "알 수 없는 동작입니다.")
		return
	}

	switch {
	case err == nil:
		http.Redirect(w, r, "/admin/orders/"+order.OrderNo+"/returns", http.StatusSeeOther)
	case errors.Is(err, commerce.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, commerce.ErrPickupRequired):
		d.renderReturns(w, r, c, order, http.StatusUnprocessableEntity,
			"수거를 확인해야 환불할 수 있습니다.")
	case errors.Is(err, commerce.ErrTransitionNotAllowed), errors.Is(err, commerce.ErrActorNotAllowed),
		errors.Is(err, commerce.ErrReturnKind):
		d.renderReturns(w, r, c, order, http.StatusUnprocessableEntity,
			"지금은 할 수 없는 처리입니다.")
	case errors.Is(err, commerce.ErrRefundExceeds), errors.Is(err, commerce.ErrRefundQuantity):
		d.renderReturns(w, r, c, order, http.StatusUnprocessableEntity,
			"환불 가능 금액 또는 수량을 넘었습니다.")
	default:
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
	}
}
