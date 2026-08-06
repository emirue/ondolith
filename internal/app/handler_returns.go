package app

import (
	"errors"
	"net/http"
	"time"

	"github.com/emirue/ondolith/internal/commerce"
)

// P-511 GET · P-512 GET — the request form.
//
// 반품과 교환이 폼 하나를 공유한다 (D17 order/return-form.html). 경로가 종류를
// 정하고, 폼은 그것을 hidden 으로 싣지 않는다 — 실으면 반품 URL 로 교환을
// 접수하는 요청이 성립한다.
func (d *shopDeps) returnForm(w http.ResponseWriter, r *http.Request) {
	d.returnKindForm(w, r, commerce.KindReturn)
}

func (d *shopDeps) returnKindForm(w http.ResponseWriter, r *http.Request, kind commerce.ReturnKind) {
	order, err := d.visibleOrder(r, r.PathValue("orderNo"))
	if err != nil {
		d.notFound(w, r)
		return
	}
	d.renderReturnForm(w, r, order, kind, http.StatusOK, "")
}

func (d *shopDeps) renderReturnForm(w http.ResponseWriter, r *http.Request,
	order *commerce.OrderDetail, kind commerce.ReturnKind, code int, msg string) {

	data := map[string]any{"Order": order, "Kind": kind, "Error": msg}
	if kind == commerce.KindExchange && len(order.Items) > 0 {
		// 교환은 **같은 상품의 다른 조합**만 고를 수 있다 (FR-618). 화면이
		// 그 목록만 보여주면 사용자가 없는 선택지를 고르는 일이 없다 —
		// 거부하는 것은 여전히 서버다.
		variants, err := d.store.VariantsForExchange(r.Context(), order.Items[0].ID)
		if err != nil {
			d.serverError(w, r, err)
			return
		}
		data["Variants"] = variants
	}
	d.renderPage(w, r, "order/return-form.html", code, d.shopView(r, "반품·교환 요청", data))
}

// P-511 POST · P-512 POST — submit.
func (d *shopDeps) returnCreate(w http.ResponseWriter, r *http.Request) {
	d.returnKindCreate(w, r, commerce.KindReturn, "P-511")
}

func (d *shopDeps) returnKindCreate(w http.ResponseWriter, r *http.Request,
	kind commerce.ReturnKind, actor commerce.Actor) {

	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	orderNo := r.PathValue("orderNo")
	order, err := d.visibleOrder(r, orderNo)
	if err != nil {
		d.notFound(w, r)
		return
	}

	req := commerce.ReturnRequest{
		Kind:         kind,
		Lines:        readRefundLines(r),
		Reason:       r.PostFormValue("reason"),
		NewVariantID: r.PostFormValue("new_variant_id"),
	}
	if kind == commerce.KindReturn {
		// 반품 폼이 교환 필드를 실어 보내도 무시가 아니라 비운다 — 저장소는
		// 실려 있으면 거부하고, 여기서 비우는 것이 폼 공유의 대가다.
		req.NewVariantID = ""
	}
	if len(req.Lines) == 0 {
		d.renderReturnForm(w, r, order, kind, http.StatusUnprocessableEntity,
			"반품·교환할 품목과 수량을 고르세요.")
		return
	}

	_, err = d.store.OpenReturn(r.Context(), orderNo, req, actor, time.Now())
	switch {
	case errors.Is(err, commerce.ErrNotFound):
		d.notFound(w, r)
		return
	case errors.Is(err, commerce.ErrTransitionNotAllowed), errors.Is(err, commerce.ErrActorNotAllowed):
		d.renderReturnForm(w, r, order, kind, http.StatusUnprocessableEntity,
			"지금은 반품·교환을 요청할 수 없는 주문입니다.")
		return
	case errors.Is(err, commerce.ErrReturnInProgress):
		d.renderReturnForm(w, r, order, kind, http.StatusUnprocessableEntity,
			"이미 처리 중인 요청이 있습니다.")
		return
	case errors.Is(err, commerce.ErrExchangeVariant):
		d.renderReturnForm(w, r, order, kind, http.StatusUnprocessableEntity,
			"교환은 같은 상품의 다른 옵션으로만 가능합니다.")
		return
	case errors.Is(err, commerce.ErrRefundQuantity), errors.Is(err, commerce.ErrQuantityRange),
		errors.Is(err, commerce.ErrReturnKind):
		d.renderReturnForm(w, r, order, kind, http.StatusUnprocessableEntity,
			"수량 또는 선택이 올바르지 않습니다.")
		return
	case errors.Is(err, commerce.ErrOutOfStock):
		d.renderReturnForm(w, r, order, kind, http.StatusUnprocessableEntity,
			"교환할 옵션의 재고가 없습니다.")
		return
	case err != nil:
		d.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/orders/"+orderNo+"/returns", http.StatusSeeOther)
}

// P-513 GET /orders/{orderNo}/returns — history.
func (d *shopDeps) returnList(w http.ResponseWriter, r *http.Request) {
	order, err := d.visibleOrder(r, r.PathValue("orderNo"))
	if err != nil {
		d.notFound(w, r)
		return
	}
	returns, err := d.store.Returns(r.Context(), order.ID)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	d.renderPage(w, r, "order/returns.html", http.StatusOK,
		d.shopView(r, "반품·교환 내역", map[string]any{"Order": order, "Returns": returns}))
}

// P-512 는 같은 핸들러를 쓰되 경로가 다르다.
//
// `{kind}` 와일드카드 대신 라우트를 둘로 나눈 이유: D11 이 두 화면에 서로
// 다른 URL 을 주었고, 와일드카드로 합치면 `/orders/x/anything` 이 전부
// 반품으로 해석된다.
func (d *shopDeps) returnFormExchange(w http.ResponseWriter, r *http.Request) {
	d.returnKindForm(w, r, commerce.KindExchange)
}

func (d *shopDeps) returnCreateExchange(w http.ResponseWriter, r *http.Request) {
	d.returnKindCreate(w, r, commerce.KindExchange, "P-512")
}

// P-514 GET /orders/{orderNo}/exchange/{returnNo}/pay — 확정된 차액을 보여준다.
//
// **금액을 다시 계산하지 않는다.** A-511 의 수거 확인 트랜잭션에서 확정돼
// `returns.price_difference` 에 있고, 화면은 그것을 읽기만 한다 (FR-607).
func (d *shopDeps) exchangePayForm(w http.ResponseWriter, r *http.Request) {
	diff, order, err := d.exchangeDiff(r)
	switch {
	case errors.Is(err, commerce.ErrNotFound):
		// 없는 건과 남의 건이 같은 404 다 — 갈리면 그 차이가 존재를 알려준다.
		d.notFound(w, r)
		return
	case errors.Is(err, commerce.ErrNoPriceDiff):
		d.renderPage(w, r, "order/exchange-pay.html", http.StatusConflict,
			d.shopView(r, "교환 차액 결제", map[string]any{
				"Order": order, "Error": "결제할 차액이 없습니다."}))
		return
	case err != nil:
		d.serverError(w, r, err)
		return
	}
	d.renderPage(w, r, "order/exchange-pay.html", http.StatusOK,
		d.shopView(r, "교환 차액 결제", map[string]any{
			"Order": order, "Return": diff, "ClientKey": d.pgClientKey()}))
}

// P-514 POST — 승인. 폼은 금액을 싣지 않는다.
func (d *shopDeps) exchangePayConfirm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	diff, order, err := d.exchangeDiff(r)
	switch {
	case errors.Is(err, commerce.ErrNotFound):
		d.notFound(w, r)
		return
	case errors.Is(err, commerce.ErrNoPriceDiff):
		d.renderPage(w, r, "order/exchange-pay.html", http.StatusConflict,
			d.shopView(r, "교환 차액 결제", map[string]any{
				"Order": order, "Error": "결제할 차액이 없습니다."}))
		return
	case err != nil:
		d.serverError(w, r, err)
		return
	}

	// **금액은 폼에서 오지 않는다** (D19 P-514 받지 않는 필드). PG 콜백이
	// 실어 보낸 값은 대조에만 쓰고, 청구하는 것은 확정된 차액이다.
	userID := ""
	if a := ActorFrom(r.Context()); a.IsAuthenticated() {
		userID = a.User.ID
	}
	err = d.store.ConfirmExchangeDiff(r.Context(), d.gateway(), d.pgName(),
		order.OrderNo, diff.ReturnNo, userID, r.PostFormValue("paymentKey"),
		diff.Amount, time.Now())
	switch {
	case err == nil:
		http.Redirect(w, r, "/orders/"+order.OrderNo+"/returns", http.StatusSeeOther)
	case errors.Is(err, commerce.ErrAlreadyPaid):
		d.renderPage(w, r, "order/exchange-pay.html", http.StatusConflict,
			d.shopView(r, "교환 차액 결제", map[string]any{
				"Order": order, "Error": "이미 결제되었습니다."}))
	case errors.Is(err, commerce.ErrAmountMismatch), errors.Is(err, commerce.ErrNoPriceDiff):
		d.renderPage(w, r, "order/exchange-pay.html", http.StatusConflict,
			d.shopView(r, "교환 차액 결제", map[string]any{
				"Order": order, "Error": "결제할 차액이 없습니다."}))
	default:
		d.serverError(w, r, err)
	}
}

// exchangeDiff resolves both path values under the session's ownership.
func (d *shopDeps) exchangeDiff(r *http.Request) (*commerce.ExchangeDiff, *commerce.OrderDetail, error) {
	order, err := d.visibleOrder(r, r.PathValue("orderNo"))
	if err != nil {
		return nil, nil, commerce.ErrNotFound
	}
	userID := ""
	if a := ActorFrom(r.Context()); a.IsAuthenticated() {
		userID = a.User.ID
	}
	diff, err := d.store.ExchangeDiffDue(r.Context(), order.OrderNo, r.PathValue("returnNo"), userID)
	return diff, order, err
}
