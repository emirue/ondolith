package app

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/emirue/ondolith/internal/commerce"
)

// sessPendingOrder holds the order number P-406 just created.
//
// **콜백은 이 값을 대조하는 데만 쓴다.** D19 P-408 이 못박은 것이 그것이다 —
// 콜백의 orderId 로 주문을 조회하면 남의 주문번호를 적어 넣어 그 주문을
// 승인시킬 수 있다. 세션에 있는 값이 무엇을 승인할지 정하고, 콜백 값은
// "같은 것을 말하고 있는가" 만 답한다.
const sessPendingOrder = "pending_order_no"

// P-405 GET /checkout — the order form.
func (d *shopDeps) checkoutForm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	owner, err := d.owner(r)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	items, err := d.store.CartItems(ctx, owner)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	if len(items) == 0 {
		http.Redirect(w, r, "/cart", http.StatusSeeOther)
		return
	}
	goods := 0
	for _, it := range items {
		goods += it.UnitPrice * it.Quantity
	}
	fee, err := d.shipping().Fee(goods)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	// 화면이 보여주는 약관과 서버가 요구하는 약관이 같은 함수에서 나온다.
	// 두 곳에서 고르면 화면은 v2 를, 서버는 v1 을 요구하는 일이 생긴다.
	terms, err := d.store.TermsInForce(ctx, time.Now())
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	a := ActorFrom(ctx)
	data := map[string]any{
		"Items": items, "Goods": goods, "Fee": fee, "Total": goods + fee, "Terms": terms,
	}
	if a.IsAuthenticated() {
		data["Email"] = a.User.Email
	}
	d.renderPage(w, r, "shop/checkout.html", http.StatusOK, d.shopView(r, "주문서", data))
}

// P-406 POST /checkout — create the order.
//
// 폼에 금액이 없다 (D19 P-406 「받지 않는 필드」). 품목도 받지 않는다 —
// 무엇을 사는지는 장바구니가 정한다.
func (d *shopDeps) checkoutCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	owner, err := d.owner(r)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	a := ActorFrom(ctx)

	form := commerce.OrderForm{
		ReceiverName:  r.PostFormValue("receiver_name"),
		ReceiverPhone: r.PostFormValue("receiver_phone"),
		Postcode:      r.PostFormValue("postcode"),
		Address1:      r.PostFormValue("address1"),
		Address2:      r.PostFormValue("address2"),
		DeliveryMemo:  r.PostFormValue("delivery_memo"),
		OrdererEmail:  r.PostFormValue("orderer_email"),
		OrdererPhone:  r.PostFormValue("orderer_phone"),
		AgreedTerms:   r.PostForm["agreed_terms"],
	}
	userID := ""
	if a.IsAuthenticated() {
		userID = a.User.ID
		// 회원은 세션 이메일을 쓴다 (D19 P-406). 폼 값을 쓰면 남의 주소로
		// 주문서를 보낼 수 있고, 그 주소가 곧 비회원 조회의 대조 키가 된다.
		form.OrdererEmail = a.User.Email
	}

	// 할인은 **서버가 정한다.** 지금은 0 이고, 그 값을 무엇이 정하는지(쿠폰
	// 발급 등)는 별도 설계다 (D50). 폼에 할인 필드를 두지 않는 것이 핵심이다 —
	// 두는 순간 FR-607 의 금액 대조가 자기 자신과의 대조가 된다.
	order, err := d.store.CreateOrder(ctx, owner, userID, form, d.shipping(), 0, time.Now())
	switch {
	case errors.Is(err, commerce.ErrCartEmpty):
		http.Redirect(w, r, "/cart", http.StatusSeeOther)
		return
	case errors.Is(err, commerce.ErrTermsRequired), errors.Is(err, commerce.ErrOrdererContact),
		errors.Is(err, commerce.ErrOutOfStock), errors.Is(err, commerce.ErrNotSellable):
		d.checkoutError(w, r, err)
		return
	case err != nil:
		d.serverError(w, r, err)
		return
	}

	// 세션이 무엇을 결제할지 정한다. 이 값 없이는 P-407·P-408 이 아무 주문도
	// 특정하지 못한다.
	d.sm.Put(ctx, sessPendingOrder, order.OrderNo)
	http.Redirect(w, r, "/checkout/pay", http.StatusSeeOther)
}

// checkoutError re-draws P-405 with the message, keeping what the user typed.
func (d *shopDeps) checkoutError(w http.ResponseWriter, r *http.Request, cause error) {
	ctx := r.Context()
	owner, _ := d.owner(r)
	items, _ := d.store.CartItems(ctx, owner)
	terms, _ := d.store.TermsInForce(ctx, time.Now())
	goods := 0
	for _, it := range items {
		goods += it.UnitPrice * it.Quantity
	}
	fee, _ := d.shipping().Fee(goods)
	d.renderPage(w, r, "shop/checkout.html", http.StatusUnprocessableEntity,
		d.shopView(r, "주문서", map[string]any{
			"Items": items, "Goods": goods, "Fee": fee, "Total": goods + fee,
			"Terms": terms, "Error": cause.Error(),
		}))
}

// pendingOrder loads the order the session is paying for.
//
// 주문번호를 인자로 받지 않는다. 받는 순간 그 값이 조회 키가 되고, 남의
// 주문번호를 적어 넣는 것만으로 그 주문의 결제 화면이 열린다 (SC-3).
func (d *shopDeps) pendingOrder(r *http.Request) (*commerce.OrderDetail, error) {
	ctx := r.Context()
	orderNo := d.sm.GetString(ctx, sessPendingOrder)
	if orderNo == "" {
		return nil, commerce.ErrNotFound
	}
	a := ActorFrom(ctx)
	if a.IsAuthenticated() {
		return d.store.OrderByNo(ctx, orderNo, a.User.ID, "")
	}
	// 비회원은 세션이 만든 주문이므로 소유 판정을 세션이 이미 한 셈이다.
	// 그래도 저장소는 대조 키를 요구하므로 주문의 연락처로 연다 — 이 경로에
	// 사용자 입력이 끼어들 자리가 없다.
	return d.store.OrderByNoUnscoped(ctx, orderNo)
}

// P-407 GET /checkout/pay — hand the browser what the PG SDK needs.
func (d *shopDeps) checkoutPay(w http.ResponseWriter, r *http.Request) {
	order, err := d.pendingOrder(r)
	if errors.Is(err, commerce.ErrNotFound) {
		d.notFound(w, r)
		return
	}
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	if order.Status != commerce.StatusPaymentPending {
		// 이미 결제된(또는 취소된) 주문의 결제창을 다시 열지 않는다.
		http.Redirect(w, r, "/checkout/complete", http.StatusSeeOther)
		return
	}
	// 금액은 저장된 값이다 (FR-607). clientKey 는 공개 키이고 시크릿은 어떤
	// 경로로도 화면에 오지 않는다 (D19 P-407).
	d.renderPage(w, r, "shop/pay.html", http.StatusOK, d.shopView(r, "결제", map[string]any{
		"Order": order, "ClientKey": d.pgClientKey(),
	}))
}

// P-408 GET /checkout/success — approve.
func (d *shopDeps) checkoutSuccess(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	order, err := d.pendingOrder(r)
	if errors.Is(err, commerce.ErrNotFound) {
		d.notFound(w, r)
		return
	}
	if err != nil {
		d.serverError(w, r, err)
		return
	}

	// 콜백의 orderId 는 **대조용일 뿐**이다. 이 값으로 주문을 조회하지 않는다
	// (D19 P-408) — 조회 키로 쓰면 남의 주문번호로 남의 주문을 승인시킬 수 있다.
	if q := r.URL.Query().Get("orderId"); q != order.OrderNo {
		d.renderFail(w, r, "결제 정보가 주문과 맞지 않습니다.")
		return
	}
	amount, err := strconv.Atoi(r.URL.Query().Get("amount"))
	if err != nil {
		d.renderFail(w, r, "결제 정보가 올바르지 않습니다.")
		return
	}

	_, err = d.store.ConfirmPayment(ctx, d.gateway(), d.pgName(),
		order.OrderNo, r.URL.Query().Get("paymentKey"), amount, time.Now())
	switch {
	case err == nil:
		http.Redirect(w, r, "/checkout/complete", http.StatusSeeOther)
	case errors.Is(err, commerce.ErrAlreadyPaid):
		// 새로고침이 여기로 온다. 오류가 아니라 완료로 보낸다.
		http.Redirect(w, r, "/checkout/complete", http.StatusSeeOther)
	case errors.Is(err, commerce.ErrAuthWindowClosed):
		d.renderFail(w, r, "결제 시간이 지났습니다. 다시 결제해 주세요.")
	case errors.Is(err, commerce.ErrAmountMismatch):
		d.renderFail(w, r, "결제 금액이 주문 금액과 다릅니다.")
	case errors.Is(err, commerce.ErrPaymentUnknown):
		d.renderFail(w, r, "결제 결과를 확인하지 못했습니다. 잠시 후 주문 내역을 확인해 주세요.")
	default:
		d.log.Warn("결제 승인 실패", "order", order.OrderNo, "err", err)
		d.renderFail(w, r, "결제가 완료되지 않았습니다.")
	}
}

// P-409 GET /checkout/fail — the PG sent them back.
//
// `message` 는 읽되 화면에 출력하지 않는다 (D19 P-409). PG 가 붙인 임의
// 문자열이라 그대로 그리면 반사형 XSS·피싱 문구 주입 표면이 된다.
func (d *shopDeps) checkoutFail(w http.ResponseWriter, r *http.Request) {
	order, err := d.pendingOrder(r)
	if err == nil {
		if q := r.URL.Query().Get("orderId"); q == order.OrderNo {
			// 시도 기록만 남긴다. 주문은 결제대기에 머문다 (D14 5-1).
			if ferr := d.store.FailPayment(r.Context(), order.OrderNo, ""); ferr != nil &&
				!errors.Is(ferr, commerce.ErrNotFound) {
				d.log.Warn("결제 실패 기록", "order", order.OrderNo, "err", ferr)
			}
		}
	}
	d.renderFail(w, r, failMessage(r.URL.Query().Get("code")))
}

// failMessages is our own vocabulary. PG 코드가 여기에 없으면 일반 실패다 —
// 모르는 코드를 그대로 보여주면 그것이 곧 PG 문자열 출력이다.
var failMessages = map[string]string{
	"PAY_PROCESS_CANCELED":    "결제를 취소하셨습니다.",
	"PAY_PROCESS_ABORTED":     "결제가 중단되었습니다.",
	"REJECT_CARD_COMPANY":     "카드사에서 결제를 거절했습니다.",
	"INVALID_CARD_EXPIRATION": "카드 유효기간을 확인해 주세요.",
}

func failMessage(code string) string {
	if m, ok := failMessages[code]; ok {
		return m
	}
	return "결제가 완료되지 않았습니다."
}

func (d *shopDeps) renderFail(w http.ResponseWriter, r *http.Request, msg string) {
	d.renderPage(w, r, "shop/fail.html", http.StatusOK,
		d.shopView(r, "결제 실패", map[string]any{"Message": msg}))
}

// P-410 GET /checkout/complete — done.
func (d *shopDeps) checkoutComplete(w http.ResponseWriter, r *http.Request) {
	order, err := d.pendingOrder(r)
	if errors.Is(err, commerce.ErrNotFound) {
		d.notFound(w, r)
		return
	}
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	// 화면은 스냅샷만 쓴다 (FR-612).
	d.renderPage(w, r, "shop/complete.html", http.StatusOK,
		d.shopView(r, "주문 완료", map[string]any{"Order": order}))
}
