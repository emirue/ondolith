package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/emirue/ondolith/internal/commerce"
	"github.com/emirue/ondolith/internal/content"
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
	if d.fail(w, r, err) {
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
		"Next": commerce.Next(order.Status, "A-506"),
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

	// **취소는 order.cancel 이다** (D15 2.2). 상태 전이 권한 하나로 취소까지
	// 보내면 `order.cancel` 은 역할 편집기에 나타나기만 하고 아무 데서도
	// 판정되지 않는 죽은 권한이 되고, 「배송 상태는 만지되 취소는 못 하는」
	// 역할을 만들 방법이 사라진다. 돈이 돌아가는 전이라 다른 전이와 같은
	// 문턱에 두지 않는다.
	if to == commerce.StatusCancelled && !c.Can("order.cancel") {
		Forbidden(w)
		return
	}

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
	if d.fail(w, r, err) {
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

// categoryError re-renders A-509 with the message. 세 핸들러가 같은 화면으로
// 돌아가므로 한 곳에 둔다.
func (d *Deps) categoryError(w http.ResponseWriter, r *http.Request, code int, msg string) {
	cats, _ := d.Commerce.Categories(r.Context())
	d.Render(w, r, "admin/categories.html", code,
		map[string]any{"Categories": cats, "Error": msg})
}

// CategoryCreate is A-509 POST /admin/categories/new.
//
// **이것이 없어서 카테고리를 만들 수 없었다.** A-509 는 「이동」만 있었고,
// 그래서 P-302 는 어떤 주소로도 열리지 않는 화면이었다 — 만들 수 없는 것의
// 목록 화면이었다.
func (d *Deps) CategoryCreate(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "product.manage")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	cat := commerce.Category{
		Name:     strings.TrimSpace(r.PostFormValue("name")),
		Slug:     strings.TrimSpace(r.PostFormValue("slug")),
		ParentID: strings.TrimSpace(r.PostFormValue("parent_id")),
	}
	cat.SortOrder, _ = strconv.Atoi(r.PostFormValue("sort_order"))
	// 상위는 선택 입력이다 — 비어 있으면 최상위. 값이 있는데 형식이 깨졌으면
	// 없는 상위와 같다 (그대로 내려가면 `::uuid` 캐스트가 22P02 로 터진다).
	if cat.ParentID != "" && !content.IsUUID(cat.ParentID) {
		d.categoryError(w, r, http.StatusUnprocessableEntity,
			commerce.ErrCategoryMissing.Error())
		return
	}
	if cat.Name == "" {
		d.categoryError(w, r, http.StatusUnprocessableEntity, "이름을 입력하세요.")
		return
	}
	// 슬러그는 URL 이 된다 — 형식과 예약어를 게시판·페이지와 **같은 함수**로
	// 검사한다 (D19 A-509). 여기서 따로 정규식을 쓰면 그 둘과 갈라진다.
	if err := content.ValidateSlug(cat.Slug); err != nil {
		d.categoryError(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	switch _, err := d.Commerce.CreateCategory(r.Context(), cat); {
	case errors.Is(err, commerce.ErrSlugTaken), errors.Is(err, commerce.ErrCategoryMissing):
		d.categoryError(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	case err != nil:
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.log(r, c, "category.create", "category", cat.Slug, "카테고리 생성")
	http.Redirect(w, r, "/admin/categories", http.StatusSeeOther)
}

// CategoryDelete is A-509 POST /admin/categories/{id}/delete.
//
// 브라우저 폼은 DELETE 를 보낼 수 없다 — 메뉴·페이지 삭제와 같은 규약이다.
func (d *Deps) CategoryDelete(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "product.manage")
	if !ok {
		return
	}
	id := r.PathValue("id")
	switch err := d.Commerce.DeleteCategory(r.Context(), id); {
	case errors.Is(err, commerce.ErrCategoryInUse):
		// 409 다. 요청은 올바르고 지금 상태가 거부한 것이다 (D19 A-509).
		d.categoryError(w, r, http.StatusConflict, err.Error())
		return
	case errors.Is(err, commerce.ErrNotFound):
		d.categoryError(w, r, http.StatusNotFound, "없는 카테고리입니다.")
		return
	case err != nil:
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.log(r, c, "category.delete", "category", id, "카테고리 삭제")
	http.Redirect(w, r, "/admin/categories", http.StatusSeeOther)
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
	id, parent := r.PostFormValue("id"), r.PostFormValue("parent_id")
	if !content.IsUUID(id) || (parent != "" && !content.IsUUID(parent)) {
		d.categoryError(w, r, http.StatusUnprocessableEntity,
			commerce.ErrCategoryMissing.Error())
		return
	}
	err := d.Commerce.Reparent(r.Context(), id, parent)
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
	d.log(r, c, "category.reparent", "category", id, "상위 카테고리 변경")
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
	if d.fail(w, r, err) {
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
	if d.fail(w, r, err) {
		return
	}
	if !reauthOK(c, r) {
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
	if d.fail(w, r, err) {
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
	if d.fail(w, r, err) {
		return
	}
	returnNo := r.PostFormValue("return_no")
	action := r.PostFormValue("action")

	switch action {
	case "pickup":
		// 배송비는 받지 않는다 — A-512 정책을 서버가 읽는다 (D19 A-511
		// 받지 않는 필드). 폼에서 받으면 order.refund 없이 환불액을 정하는
		// 창구가 된다.
		err = d.Commerce.ConfirmPickup(r.Context(), order.OrderNo, returnNo,
			r.PostFormValue("fault"), "A-511")
		if err == nil {
			d.log(r, c, "return.pickup", "return", returnNo, "수거 확인")
		}
	case "reject":
		err = d.Commerce.RejectReturn(r.Context(), order.OrderNo, returnNo,
			r.PostFormValue("reason"), "A-511")
		if err == nil {
			d.log(r, c, "return.reject", "return", returnNo, "반품·교환 거부")
		}
	case "exchange":
		// 교환 완료 (재발송). D19 A-511 「동작별 권한」은 order.return 만
		// 요구한다 — 돈이 나가지 않는다. 차액이 양수면 여기서 끝나지 않고
		// 차액결제대기로 가고, 받는 것은 P-514 다.
		var to commerce.Status
		to, err = d.Commerce.CompleteExchange(r.Context(), order.OrderNo, returnNo, "A-511")
		if err == nil {
			d.log(r, c, "return.exchange", "return", returnNo, "교환 "+string(to))
		}
	case "settle":
		// **환불 확정 단계는 `order.refund` 다** (D15 2.2: "A-507, A-511
		// (환불 확정 단계만)"). 화면 권한(order.return)만으로 통과시키면,
		// 반품 접수·수거만 맡기려고 order.return 을 준 계정이 실제 환불까지
		// 확정할 수 있다 — A-507 이 order.refund 로 게이팅되는 것과 어긋난다.
		if !c.Can("order.refund") {
			d.renderReturns(w, r, c, order, http.StatusForbidden,
				"환불 확정 권한이 없습니다.")
			return
		}
		// **돈이 나간다** — 여기만 재인증을 요구한다 (D15 5.3-1).
		if !reauthOK(c, r) {
			d.renderReturns(w, r, c, order, http.StatusForbidden, "비밀번호를 다시 입력하세요.")
			return
		}
		var key string
		key, err = commerce.NewRequestKey()
		if err == nil {
			var amount int
			amount, err = d.Commerce.SettleReturn(r.Context(), order.OrderNo, returnNo, "A-511", key)
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
	case errors.Is(err, commerce.ErrShippingFeeTooLarge):
		// 500 이 아니다. 운영자가 고칠 수 있는 값이고, 무엇을 고쳐야 하는지
		// 말해 줘야 한다 — 수거 확인 단계에서 이것을 거부하는 것이 그 반품
		// 건이 멈추지 않게 하는 유일한 지점이다.
		d.renderReturns(w, r, c, order, http.StatusUnprocessableEntity,
			"반품 배송비가 환불 금액 이상입니다. 커머스 정책(A-512)에서 배송비를 낮추거나 별도청구로 바꾸세요.")
	case errors.Is(err, commerce.ErrFeeSetting):
		d.renderReturns(w, r, c, order, http.StatusUnprocessableEntity,
			"커머스 정책(A-512)의 반품 배송비 값이 올바르지 않습니다.")
	case errors.Is(err, commerce.ErrPriceNegative):
		d.renderReturns(w, r, c, order, http.StatusUnprocessableEntity,
			"금액이 올바르지 않습니다.")
	default:
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
	}
}
