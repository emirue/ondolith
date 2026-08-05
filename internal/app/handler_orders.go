package app

import (
	"errors"
	"net/http"

	"github.com/emirue/ondolith/internal/commerce"
)

// sessGuestOrder is the one order a guest lookup opened.
//
// **범위가 그 주문 하나다** (SC-3 4항). 목록을 담으면 조회 한 번이 여러 주문의
// 열쇠가 되고, 세션이 살아 있는 동안 계속 열린다.
const sessGuestOrder = "guest_order_no"

// P-501 GET /orders — my orders.
func (d *shopDeps) orderList(w http.ResponseWriter, r *http.Request) {
	a := ActorFrom(r.Context())
	if !a.IsAuthenticated() {
		http.Redirect(w, r, loginPath, http.StatusSeeOther)
		return
	}
	orders, err := d.store.MyOrders(r.Context(), a.User.ID, pageOf(r))
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	d.renderPage(w, r, "order/list.html", http.StatusOK,
		d.shopView(r, "주문 내역", map[string]any{"Orders": orders, "Page": pageOf(r)}))
}

// visibleOrder loads one order for whoever is asking, or ErrNotFound.
//
// 회원은 소유권 술어로, 비회원은 **이 세션이 조회에 성공한 그 주문번호**로만
// 연다. 경로의 주문번호를 그대로 조회 키로 쓰지 않는 이유는 그러면 번호 하나가
// 곧 열쇠가 되기 때문이다 (SC-3).
func (d *shopDeps) visibleOrder(r *http.Request, orderNo string) (*commerce.OrderDetail, error) {
	ctx := r.Context()
	if a := ActorFrom(ctx); a.IsAuthenticated() {
		return d.store.OrderByNo(ctx, orderNo, a.User.ID, "")
	}
	if granted := d.sm.GetString(ctx, sessGuestOrder); granted != "" && granted == orderNo {
		return d.store.OrderByNoUnscoped(ctx, orderNo)
	}
	return nil, commerce.ErrNotFound
}

// P-502 GET /orders/{orderNo} — one order.
func (d *shopDeps) orderDetail(w http.ResponseWriter, r *http.Request) {
	order, err := d.visibleOrder(r, r.PathValue("orderNo"))
	if errors.Is(err, commerce.ErrNotFound) {
		d.notFound(w, r)
		return
	}
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	d.renderPage(w, r, "order/view.html", http.StatusOK,
		d.shopView(r, "주문 "+order.OrderNo, map[string]any{"Order": order}))
}

// P-505 GET /orders/{orderNo}/shipping — tracking.
func (d *shopDeps) orderShipping(w http.ResponseWriter, r *http.Request) {
	order, err := d.visibleOrder(r, r.PathValue("orderNo"))
	if errors.Is(err, commerce.ErrNotFound) {
		d.notFound(w, r)
		return
	}
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	shipments, err := d.store.Shipments(r.Context(), order.ID)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	d.renderPage(w, r, "order/shipping.html", http.StatusOK,
		d.shopView(r, "배송 조회", map[string]any{"Order": order, "Shipments": shipments}))
}

// P-503 GET /orders/guest — the lookup form.
func (d *shopDeps) guestLookupForm(w http.ResponseWriter, r *http.Request) {
	d.renderPage(w, r, "order/guest-form.html", http.StatusOK,
		d.shopView(r, "비회원 주문 조회", nil))
}

// guestLookupFailure is the ONE answer every failure gets.
//
// 없는 주문·연락처 불일치·회원 전용 주문·형식 오류가 전부 같은 문장이다
// (D19 P-504). 구분하면 주문번호 목록을 훑어 어느 것이 존재하는지 알 수 있다.
const guestLookupFailure = "주문 정보를 찾을 수 없습니다."

// P-504 POST /orders/guest — run the lookup.
func (d *shopDeps) guestLookup(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	orderNo := r.PostFormValue("order_no")

	// NFR-207: IP당 5회/분, 주문번호당 3회/시간. 두 겹인 이유는 서로 다른 공격을
	// 막기 때문이다 — IP 제한은 한 곳에서 많은 번호를 훑는 것을, 번호별 제한은
	// 여러 곳에서 한 번호의 연락처를 맞히는 것을 막는다.
	if !d.limiter.Allow("guest-order:ip:"+clientIP(r), d.limits.GuestOrderIP) ||
		!d.limiter.Allow("guest-order:no:"+orderNo, d.limits.GuestOrderNo) {
		w.Header().Set("Retry-After", "60")
		d.renderPage(w, r, "order/guest-form.html", http.StatusTooManyRequests,
			d.shopView(r, "비회원 주문 조회",
				map[string]any{"Error": "조회 시도가 너무 잦습니다. 잠시 후 다시 시도하세요."}))
		return
	}

	// 대조는 쿼리 안에서 한다 (D19 P-504). 조회한 뒤 Go 에서 비교하면 주문
	// 행이 이미 프로세스에 들어와 있고, 그 뒤의 실수 하나가 곧 유출이다.
	order, err := d.store.GuestOrder(ctx, orderNo,
		r.PostFormValue("phone"), r.PostFormValue("email"))
	if err != nil {
		// 실패 구분은 로그에만 남는다.
		d.log.Info("비회원 주문 조회 실패", "order_no", orderNo, "ip", clientIP(r), "err", err)
		d.renderPage(w, r, "order/guest-form.html", http.StatusBadRequest,
			d.shopView(r, "비회원 주문 조회", map[string]any{"Error": guestLookupFailure}))
		return
	}

	// 이 주문 **하나만** 연다 (SC-3 4항).
	d.sm.Put(ctx, sessGuestOrder, order.OrderNo)
	http.Redirect(w, r, "/orders/"+order.OrderNo, http.StatusSeeOther)
}
