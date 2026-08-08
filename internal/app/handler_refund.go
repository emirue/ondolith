package app

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/emirue/ondolith/internal/commerce"
)

// P-506 POST /orders/{orderNo}/cancel — cancel before dispatch.
//
// 구매자가 직접 일으킬 수 있다. 배송 전이라 물건이 움직이지 않았고 돌려줄
// 것은 돈뿐이라 손실 방향이 한쪽이다 (D14 5-1). 배송 후는 A-507 이 승인한다.
func (d *shopDeps) orderCancel(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	orderNo := r.PathValue("orderNo")
	// 소유권을 먼저 판정한다. 취소는 상태를 바꾸므로 "볼 수 있는가" 가 아니라
	// "이 사람의 주문인가" 를 먼저 물어야 한다.
	if _, err := d.visibleOrder(r, orderNo); err != nil {
		d.notFound(w, r)
		return
	}

	// 멱등 키는 서버가 만든다. 폼에서 받으면 같은 키를 두 번 보내 접수를
	// 막거나, 매번 다른 키를 보내 이중 접수를 만들 수 있다.
	key, err := commerce.NewRequestKey()
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	err = d.store.CancelOrder(r.Context(), orderNo, "P-506", key)
	switch {
	case errors.Is(err, commerce.ErrNotFound):
		d.notFound(w, r)
		return
	case errors.Is(err, commerce.ErrTransitionNotAllowed), errors.Is(err, commerce.ErrActorNotAllowed):
		d.refundError(w, r, orderNo, "지금은 취소할 수 없는 주문입니다.")
		return
	case err != nil:
		d.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/orders/"+orderNo+"/refunds", http.StatusSeeOther)
}

// P-507 POST /orders/{orderNo}/refund — partial refund request.
//
// 요청 행만 만든다. 구매자가 자기 주문을 `환불` 로 보낼 수 없다 — 물건은 이미
// 갔으므로 A-507 이 승인해야 전이한다 (D14 5-1).
func (d *shopDeps) refundRequest(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	orderNo := r.PathValue("orderNo")
	if _, err := d.visibleOrder(r, orderNo); err != nil {
		d.notFound(w, r)
		return
	}
	// **금액을 받지 않는다** (FR-617, FR-625). 품목과 수량만 받고 금액은
	// 서버가 스냅샷에서 계산한다 — 여러 개 산 것 중 하나만 취소하는 경우가
	// 정상이고, 그때 할인이 붙어 있으면 단가 × 수량은 틀린 답이다.
	lines := readRefundLines(r)
	if len(lines) == 0 {
		d.refundError(w, r, orderNo, "환불할 품목과 수량을 고르세요.")
		return
	}
	key, err := commerce.NewRequestKey()
	if err != nil {
		d.serverError(w, r, err)
		return
	}

	_, _, err = d.store.RequestRefund(r.Context(), orderNo, lines,
		"구매자", r.PostFormValue("reason"), key)
	switch {
	case errors.Is(err, commerce.ErrNoPayment):
		d.refundError(w, r, orderNo, "환불할 결제가 없습니다.")
		return
	case errors.Is(err, commerce.ErrRefundExceeds):
		d.refundError(w, r, orderNo, "환불 가능 금액을 넘었습니다.")
		return
	case errors.Is(err, commerce.ErrRefundDuplicate):
		d.refundError(w, r, orderNo, "이미 접수된 요청입니다.")
		return
	case errors.Is(err, commerce.ErrRefundQuantity), errors.Is(err, commerce.ErrQuantityRange):
		d.refundError(w, r, orderNo, "환불 수량이 남은 수량을 넘습니다.")
		return
	case errors.Is(err, commerce.ErrNotFound):
		d.notFound(w, r)
		return
	case err != nil:
		d.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/orders/"+orderNo+"/refunds", http.StatusSeeOther)
}

// P-508 GET /orders/{orderNo}/refunds — request status.
func (d *shopDeps) refundStatus(w http.ResponseWriter, r *http.Request) {
	order, err := d.visibleOrder(r, r.PathValue("orderNo"))
	if err != nil {
		d.notFound(w, r)
		return
	}
	refunds, err := d.store.Refunds(r.Context(), order.ID)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	approved, refunded, perr := d.store.RefundedTotal(r.Context(), order.OrderNo)
	if perr != nil && !errors.Is(perr, commerce.ErrNoPayment) {
		d.serverError(w, r, perr)
		return
	}
	d.renderPage(w, r, "order/refunds.html", http.StatusOK,
		d.shopView(r, "취소·환불", map[string]any{
			"Order": order, "Refunds": refunds,
			"Approved": approved, "Refunded": refunded, "Remaining": approved - refunded,
		}))
}

func (d *shopDeps) refundError(w http.ResponseWriter, r *http.Request, orderNo, msg string) {
	order, err := d.visibleOrder(r, orderNo)
	if err != nil {
		d.notFound(w, r)
		return
	}
	refunds, _ := d.store.Refunds(r.Context(), order.ID)
	approved, refunded, _ := d.store.RefundedTotal(r.Context(), orderNo)
	d.renderPage(w, r, "order/refunds.html", http.StatusUnprocessableEntity,
		d.shopView(r, "취소·환불", map[string]any{
			"Order": order, "Refunds": refunds, "Error": msg,
			"Approved": approved, "Refunded": refunded, "Remaining": approved - refunded,
		}))
}

// P-509 GET /orders/{orderNo}/receipt — the receipt.
//
// 스냅샷만으로 재발행한다 (FR-612). 상품 표를 조인하면 상품이 바뀐 뒤 영수증
// 내용이 달라지고, 그 영수증은 증빙이 아니다.
func (d *shopDeps) orderReceipt(w http.ResponseWriter, r *http.Request) {
	order, err := d.visibleOrder(r, r.PathValue("orderNo"))
	if err != nil {
		d.notFound(w, r)
		return
	}
	d.renderPage(w, r, "order/receipt.html", http.StatusOK,
		d.shopView(r, "주문서·영수증", map[string]any{"Order": order}))
}

// P-510 POST /orders/{orderNo}/confirm — the buyer confirms.
func (d *shopDeps) orderConfirm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	orderNo := r.PathValue("orderNo")
	if _, err := d.visibleOrder(r, orderNo); err != nil {
		d.notFound(w, r)
		return
	}
	err := d.store.ConfirmPurchase(r.Context(), orderNo, "P-510")
	switch {
	case errors.Is(err, commerce.ErrNotFound):
		d.notFound(w, r)
		return
	case errors.Is(err, commerce.ErrTransitionNotAllowed), errors.Is(err, commerce.ErrActorNotAllowed):
		d.refundError(w, r, orderNo, "지금은 구매확정할 수 없습니다.")
		return
	case err != nil:
		d.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/orders/"+orderNo, http.StatusSeeOther)
}

// readRefundLines reads the item/quantity pairs the form posted.
//
// 금액은 읽지 않는다. 폼에 없기도 하지만, 있더라도 읽지 않는 것이 규칙이다 —
// 값이 오는 순간 누군가는 그것을 쓴다 (FR-617).
func readRefundLines(r *http.Request) []commerce.RefundLine {
	var out []commerce.RefundLine
	for _, id := range r.PostForm["item_id"] {
		// **빈 값은 거른다.** 빈 문자열이 그대로 내려가면 `uuid` 컬럼과
		// 비교하다 22P02 로 터져 500 이 된다 — 잘못 만든 폼 하나가 서버
		// 오류가 되는 자리이고, 이것은 사용자 입력이므로 422 로 끝나야 한다.
		if id == "" {
			continue
		}
		qty, err := strconv.Atoi(r.PostFormValue("qty_" + id))
		if err != nil || qty < 1 {
			continue
		}
		out = append(out, commerce.RefundLine{OrderItemID: id, Quantity: qty})
	}
	return out
}
