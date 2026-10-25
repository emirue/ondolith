package admin

import (
	"errors"
	"net/http"
	"net/url"
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
	// 기간 조회 (D19 0.6): 주문일 기준. 기본은 기간 조건 없음.
	q := r.URL.Query()
	status, page := q.Get("status"), pageOf(r)
	keep := url.Values{"status": {status}}
	p, perr := parsePeriod(q.Get("from"), q.Get("to"), time.Local)
	data := map[string]any{
		"Status": status, "Statuses": commerce.AllStatuses(), "From": p.From, "To": p.To,
		// 빠른 버튼은 상태 필터를 유지한다.
		"Quick": quickLinks("/admin/orders", keep, time.Now(), true),
	}
	if perr != nil {
		// 목록 대신 폼과 오류 문구를 다시 보인다.
		data["Error"] = perr.Error()
		d.Render(w, r, "admin/orders.html", http.StatusUnprocessableEntity, data)
		return
	}
	orders, more, err := d.Commerce.AdminOrders(r.Context(), status, p.Since, p.Until, page)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	data["Orders"] = orders
	pager(data, "/admin/orders", withPeriod(keep, p), page, more)
	d.Render(w, r, "admin/orders.html", http.StatusOK, data)
}

// OrderDetail is A-505.
func (d *Deps) OrderDetail(w http.ResponseWriter, r *http.Request) {
	_, ok := d.require(w, r, "order.view")
	if !ok {
		return
	}
	d.orderDetail(w, r, http.StatusOK, "")
}

// orderDetail draws A-505 for the order in the path, with msg above it when a
// change was refused.
func (d *Deps) orderDetail(w http.ResponseWriter, r *http.Request, code int, msg string) {
	order, err := d.Commerce.OrderByNoUnscoped(r.Context(), r.PathValue("no"))
	if d.fail(w, r, err) {
		return
	}
	shipments, err := d.Commerce.Shipments(r.Context(), order.ID)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	d.Render(w, r, "admin/order.html", code, map[string]any{
		"Order": order, "Shipments": shipments, "Error": msg,
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
		d.refuse(w, r, http.StatusBadRequest, "잘못된 요청입니다.")
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
		d.forbid(w, r)
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
		// 422 다. 요청은 이해했고 규칙이 거부한 것이다. 화면은 A-505 를 그대로
		// 다시 그린다 — 오류만 넘기면 주문 없이 빈 상세가 그려졌다.
		d.orderDetail(w, r, http.StatusUnprocessableEntity, transitionMessage(err))
		return
	case err != nil:
		d.serverError(w, r, err)
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
		d.serverError(w, r, err)
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
		d.refuse(w, r, http.StatusBadRequest, "잘못된 요청입니다.")
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
		d.serverError(w, r, err)
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
	sort, page := r.URL.Query().Get("sort"), pageOf(r)
	products, more, err := d.Commerce.ListProductsMore(r.Context(),
		commerce.ProductQuery{Page: page, Sort: sort})
	if errors.Is(err, commerce.ErrUnknownSort) {
		d.refuse(w, r, http.StatusBadRequest, "알 수 없는 정렬입니다.")
		return
	}
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	data := map[string]any{"Products": products}
	pager(data, "/admin/products", url.Values{"sort": {sort}}, page, more)
	d.Render(w, r, "admin/products.html", http.StatusOK, data)
}

// CategoryList is A-509 GET.
func (d *Deps) CategoryList(w http.ResponseWriter, r *http.Request) {
	_, ok := d.require(w, r, "product.manage")
	if !ok {
		return
	}
	cats, err := d.Commerce.Categories(r.Context())
	if err != nil {
		d.serverError(w, r, err)
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
		d.refuse(w, r, http.StatusBadRequest, "잘못된 요청입니다.")
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
			categoryMessage(commerce.ErrCategoryMissing))
		return
	}
	if cat.Name == "" {
		d.categoryError(w, r, http.StatusUnprocessableEntity, "이름을 입력하세요.")
		return
	}
	if content.TooLong(cat.Name, content.MaxNameLen) {
		d.categoryError(w, r, http.StatusUnprocessableEntity, "이름은 100자까지 쓸 수 있습니다.")
		return
	}
	// 슬러그는 URL 이 된다 — 형식과 예약어를 게시판·페이지와 **같은 함수**로
	// 검사한다 (D19 A-509). 여기서 따로 정규식을 쓰면 그 둘과 갈라진다.
	if err := content.ValidateSlug(cat.Slug); err != nil {
		d.categoryError(w, r, http.StatusUnprocessableEntity, slugMessage(err))
		return
	}
	switch _, err := d.Commerce.CreateCategory(r.Context(), cat); {
	case errors.Is(err, commerce.ErrSlugTaken), errors.Is(err, commerce.ErrCategoryMissing):
		d.categoryError(w, r, http.StatusUnprocessableEntity, categoryMessage(err))
		return
	case err != nil:
		d.serverError(w, r, err)
		return
	}
	d.log(r, c, "category.create", "category", cat.Slug, "카테고리 생성")
	http.Redirect(w, r, "/admin/categories", http.StatusSeeOther)
}

// CategoryUpdate is A-509 POST /admin/categories/{id} — 이름·순서를 고친다.
//
// 라우트가 만들기·지우기·상위 바꾸기뿐이라 오타 하나도 지우고 다시 만들어야 했고, 그때
// 상품과의 연결이 끊겼다. 주소(slug)는 받지 않는다 — 공개 URL 이다.
func (d *Deps) CategoryUpdate(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "product.manage")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		d.refuse(w, r, http.StatusBadRequest, "잘못된 요청입니다.")
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" || content.TooLong(name, content.MaxNameLen) {
		d.categoryError(w, r, http.StatusUnprocessableEntity, "이름은 1~100자입니다.")
		return
	}
	sortOrder := 0
	if v := strings.TrimSpace(r.PostFormValue("sort_order")); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil {
			d.categoryError(w, r, http.StatusUnprocessableEntity, "순서는 정수로 입력하세요.")
			return
		}
		sortOrder = int(n)
	}
	id := r.PathValue("id")
	switch err := d.Commerce.RenameCategory(r.Context(), id, name, sortOrder); {
	case errors.Is(err, commerce.ErrNotFound):
		d.categoryError(w, r, http.StatusNotFound, "없는 카테고리입니다.")
		return
	case err != nil:
		d.serverError(w, r, err)
		return
	}
	d.log(r, c, "category.update", "category", id, "카테고리 이름·순서 변경: "+name)
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
		d.categoryError(w, r, http.StatusConflict, categoryMessage(err))
		return
	case errors.Is(err, commerce.ErrNotFound):
		d.categoryError(w, r, http.StatusNotFound, "없는 카테고리입니다.")
		return
	case err != nil:
		d.serverError(w, r, err)
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
		d.refuse(w, r, http.StatusBadRequest, "잘못된 요청입니다.")
		return
	}
	id, parent := r.PostFormValue("id"), r.PostFormValue("parent_id")
	if !content.IsUUID(id) || (parent != "" && !content.IsUUID(parent)) {
		d.categoryError(w, r, http.StatusUnprocessableEntity,
			categoryMessage(commerce.ErrCategoryMissing))
		return
	}
	err := d.Commerce.Reparent(r.Context(), id, parent)
	switch {
	case errors.Is(err, commerce.ErrCategoryCycle), errors.Is(err, commerce.ErrCategoryDepth),
		errors.Is(err, commerce.ErrCategoryMissing):
		cats, _ := d.Commerce.Categories(r.Context())
		d.Render(w, r, "admin/categories.html", http.StatusUnprocessableEntity,
			map[string]any{"Categories": cats, "Error": categoryMessage(err)})
		return
	case err != nil:
		d.serverError(w, r, err)
		return
	}
	d.log(r, c, "category.reparent", "category", id, "상위 카테고리 변경")
	http.Redirect(w, r, "/admin/categories", http.StatusSeeOther)
}

// adminMaxPage 는 관리자 목록의 쪽 번호 상한이다. 공개 목록(100쪽)과 달리 오래된
// 주문까지 닿아야 해서 사실상 열어 두되, 가장 큰 쪽 크기(첨부 200)를 곱해도 OFFSET 이
// int32 를 넘지 않게 한다 — 상한이 없던 판은 쪽 번호 하나로 음수 OFFSET 500 이 났다.
const adminMaxPage = 1_000_000

// pageOf reads the page number, clamped to 1..adminMaxPage — from the query, or
// from the form of a POST that returns to the page it came from.
func pageOf(r *http.Request) int {
	n, err := strconv.Atoi(r.FormValue("page"))
	if err != nil || n < 1 {
		return 1
	}
	return min(n, adminMaxPage)
}

// pager fills the previous/next links of a 1-based list (A-501, A-504, FR-706).
//
// **주소를 통째로 만들어 넘긴다.** 템플릿에서 `?a={{.A}}&page={{.N}}` 로 이으면
// 조건이 하나 늘 때마다 화면마다 고쳐야 하고, 한 화면이 빠뜨리면 다음 쪽에서
// 필터가 풀린다. 빈 값은 싣지 않는다.
func pager(data map[string]any, path string, q url.Values, page int, more bool) {
	data["PageNo"] = page
	if page > 1 {
		data["PrevURL"] = ListURL(path, q, page-1)
	}
	if more {
		data["NextURL"] = ListURL(path, q, page+1)
	}
}

// ListURL is one list address: 빈 조건과 1쪽은 싣지 않는다. 공개 목록(app)도 쓴다.
func ListURL(path string, q url.Values, page int) string {
	v := url.Values{}
	for k, vals := range q {
		if len(vals) > 0 && vals[0] != "" {
			v.Set(k, vals[0])
		}
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	if len(v) == 0 {
		return path
	}
	return path + "?" + v.Encode()
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
		d.serverError(w, r, err)
		return
	}
	approved, refunded, perr := d.Commerce.RefundedTotal(r.Context(), order.OrderNo)
	if perr != nil && !errors.Is(perr, commerce.ErrNoPayment) {
		d.serverError(w, r, perr)
		return
	}
	// 환불은 끝났는데 열린 채 남은 주문이면 「주문 종료」 단추를 그린다.
	closeTo, err := d.Commerce.RefundCloseTarget(r.Context(), order.ID, order.Status, "A-507")
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	// 요청 키는 화면을 그릴 때 발급하고 폼이 되돌려 준다 (D19 A-507). POST 마다
	// 만들면 같은 폼의 재전송이 매번 새 키라 UNIQUE 가 아무것도 막지 못한다.
	key, err := commerce.NewRequestKey()
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	d.Render(w, r, "admin/refund.html", code, map[string]any{
		"Order": order, "Refunds": refunds, "RequestKey": key,
		"Approved": approved, "Refunded": refunded, "Remaining": approved - refunded, "CloseTo": closeTo,
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
		d.refuse(w, r, http.StatusBadRequest, "잘못된 요청입니다.")
		return
	}
	order, err := d.Commerce.OrderByNoUnscoped(r.Context(), r.PathValue("no"))
	if d.fail(w, r, err) {
		return
	}

	// 접수된 건의 「실행」·「거부」 (D19 A-507). 대상은 구매자가 P-507 로 낸 요청,
	// PG 가 거절해 '요청' 으로 돌아온 건, 결과를 모른 채 '승인' 에 멈춘 건이다 —
	// 이것이 없으면 그 행들은 한도를 잡은 채 나갈 길이 없다. 동작이 없으면 새 접수다.
	switch r.PostFormValue("action") {
	case "close":
		// 「주문 종료」 — PG 확정 뒤 종료 기록이 실패해 열린 채 남은 주문을 닫는다
		// (commerce.ErrRefundSentNotSettled). 돈이 나가지 않으므로 재인증을 묻지 않는다.
		to, err := d.Commerce.CloseRefundedOrder(r.Context(), order.OrderNo, "A-507")
		if err != nil {
			d.serverError(w, r, err)
			return
		}
		if to == "" {
			d.renderRefund(w, r, c, order, http.StatusUnprocessableEntity, "닫을 수 있는 주문이 아닙니다.")
			return
		}
		d.log(r, c, "order.refund", "order", order.OrderNo, "환불이 끝난 주문 종료 — 주문 "+string(to))
		http.Redirect(w, r, "/admin/orders/"+order.OrderNo+"/refund", http.StatusSeeOther)
		return
	case "reject":
		target, ok := d.refundInOrder(w, r, order)
		if !ok {
			return
		}
		// 돈이 나가지 않으므로 재인증을 묻지 않는다 (A-511 의 거부와 같다). 사유는
		// 받는다 — 구매자가 P-508 에서 읽고, 요청 때의 사유를 덮어쓴다.
		reason := strings.TrimSpace(r.PostFormValue("reason"))
		if reason == "" {
			d.renderRefund(w, r, c, order, http.StatusUnprocessableEntity, "거부 사유를 적으세요.")
			return
		}
		err := d.Commerce.RejectRefund(r.Context(), target.ID, reason)
		if errors.Is(err, commerce.ErrNotFound) { // '요청' 이 아니다
			d.renderRefund(w, r, c, order, http.StatusUnprocessableEntity, "거부할 수 있는 상태가 아닙니다.")
			return
		}
		if err != nil {
			d.serverError(w, r, err)
			return
		}
		d.log(r, c, "order.refund", "order", order.OrderNo,
			"환불 "+itoa(target.Amount)+"원 요청 거부 — 한도 해제, 환불 건 "+target.ID)
		http.Redirect(w, r, "/admin/orders/"+order.OrderNo+"/refund", http.StatusSeeOther)
		return
	case "execute":
		target, ok := d.refundInOrder(w, r, order)
		if !ok {
			return
		}
		if target.Status != "요청" && target.Status != "승인" {
			d.renderRefund(w, r, c, order, http.StatusUnprocessableEntity, "실행할 수 있는 상태가 아닙니다.")
			return
		}
		// **돈이 나간다** — 새 접수와 같은 재인증 (D15 5.3-1).
		if !reauthOK(c, r) {
			d.renderRefund(w, r, c, order, http.StatusForbidden, "비밀번호를 다시 입력하세요.")
			return
		}
		to, err := d.Commerce.RetryRefund(r.Context(), d.pgGateway(), target.ID, time.Now(), "A-507")
		if errors.Is(err, commerce.ErrRefundRetryExpired) {
			d.log(r, c, "order.refund", "order", order.OrderNo,
				"환불 "+itoa(target.Amount)+"원 재실행 거부 — 멱등키 기간 경과, 환불 건 "+target.ID)
			d.renderRefund(w, r, c, order, http.StatusUnprocessableEntity,
				"결과를 모르는 채 14일이 지나 다시 보낼 수 없습니다. 결제사 관리자 화면에서 취소 여부를 확인하세요.")
			return
		}
		d.finishRefund(w, r, c, order, target.ID, target.Amount, to, err)
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
	formKey := r.PostFormValue("request_key")
	if !commerce.ValidRequestKey(formKey) {
		d.renderRefund(w, r, c, order, http.StatusUnprocessableEntity,
			"요청이 올바르지 않습니다. 화면을 새로 고친 뒤 다시 시도하세요.")
		return
	}

	refundID, amount, err := d.Commerce.RequestRefund(r.Context(), order.OrderNo, lines,
		"관리자", r.PostFormValue("reason"), commerce.AdminRefundKey(order.ID, formKey))
	switch {
	case errors.Is(err, commerce.ErrRefundDuplicate):
		// 같은 폼의 재전송이다. 첫 요청이 이미 접수했다 — 두 번째 환불을 만들지
		// 않고 지금 상태를 보여 준다. 로그는 남기지 않는다 (D19 A-507 오류 표).
		d.renderRefund(w, r, c, order, http.StatusOK, "이미 처리된 요청입니다.")
		return
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
		d.serverError(w, r, err)
		return
	}

	// 돈이 움직인 일은 반드시 로그에 남는다 (D15 7절).
	d.log(r, c, "order.refund", "order", order.OrderNo,
		"환불 "+itoa(amount)+"원 접수 ("+itoa(len(lines))+"개 품목)")

	// 순서는 DB 한도 선점 → PG 호출 → 확정이다 (D13, D19 A-507). 선점은 위에서
	// 끝났고 여기서 돈이 나간다.
	to, err := d.Commerce.ExecuteRefund(r.Context(), d.pgGateway(), refundID, "A-507")
	d.finishRefund(w, r, c, order, refundID, amount, to, err)
}

// refundInOrder 는 폼의 refund_id 가 경로의 주문의 건일 때만 돌려준다. 번호만
// 믿으면 아무 주문 화면에서 남의 환불을 실행할 수 있다. 아니면 응답을 쓰고 false.
func (d *Deps) refundInOrder(w http.ResponseWriter, r *http.Request,
	order *commerce.OrderDetail) (*commerce.Refund, bool) {

	refunds, err := d.Commerce.Refunds(r.Context(), order.ID)
	if err != nil {
		d.serverError(w, r, err)
		return nil, false
	}
	id := r.PostFormValue("refund_id")
	for i := range refunds {
		if refunds[i].ID == id {
			return &refunds[i], true
		}
	}
	http.NotFound(w, r)
	return nil, false
}

// pgGateway 는 지금 설정된 PG 다. 클로저가 nil 인 경우(배선 전)와 반환값이 nil 인
// 경우(PG 「사용 안 함」)를 한 경로로 모은다 — 스토어가 nil 을 받아 답한다.
func (d *Deps) pgGateway() commerce.Gateway {
	if d.Gateway == nil {
		return nil
	}
	return d.Gateway()
}

// finishRefund 는 PG 로 보낸 결과를 기록하고 화면을 정한다. 새 접수와 「실행」이
// 같은 길을 지난다. 실패는 502 — 선점은 유지되고, 작업 로그에 그 사실을
// 남긴다 (D19 A-507 「선점 해제 여부까지 기록」).
func (d *Deps) finishRefund(w http.ResponseWriter, r *http.Request, c Caller,
	order *commerce.OrderDetail, refundID string, amount int, to commerce.Status, err error) {

	if err != nil && !errors.Is(err, commerce.ErrRefundSentNotSettled) {
		d.Logger.Error("환불 실행", "order", order.OrderNo, "refund", refundID, "err", err)
		d.log(r, c, "order.refund", "order", order.OrderNo,
			"환불 "+itoa(amount)+"원 PG 처리 실패 — 한도 선점 유지, 환불 건 "+refundID)
		d.renderRefund(w, r, c, order, http.StatusBadGateway,
			"결제사 처리에 실패했습니다. 잠시 후 확인하세요.")
		return
	}
	d.log(r, c, "order.refund", "order", order.OrderNo, "환불 "+itoa(amount)+"원 PG 확정")
	// 환불이 끝났으면 commerce 가 주문을 끝냈다(to). 돈은 이미 움직였으므로 그 기록의
	// 실패는 로그만 남기고 화면은 정상으로 돌아간다.
	if err != nil {
		d.Logger.Error("전액 환불 뒤 주문 종료", "order", order.OrderNo, "err", err)
	} else if to != "" {
		d.log(r, c, "order.refund", "order", order.OrderNo, "환불이 끝나 주문 "+string(to))
	}
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
		d.serverError(w, r, err)
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
		d.refuse(w, r, http.StatusBadRequest, "잘못된 요청입니다.")
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
			var refundID string
			amount, refundID, err = d.Commerce.SettleReturn(r.Context(), order.OrderNo, returnNo, "A-511", key)
			if err == nil {
				d.log(r, c, "return.settle", "return", returnNo,
					"반품 환불 "+itoa(amount)+"원 확정")
				// 정산이 만든 환불 건을 PG 로 보낸다 (A-507 과 같은 순서).
				// 주문은 같은 tx 에서 이미 환불(최종)이라 종료할 것이 없다.
				if _, err = d.Commerce.ExecuteRefund(r.Context(), d.pgGateway(), refundID, "A-511"); errors.Is(err, commerce.ErrRefundSentNotSettled) {
					d.Logger.Error("반품 환불 뒤 주문 종료", "return", returnNo, "err", err)
					err = nil
				}
				if err != nil {
					d.Logger.Error("반품 환불 실행", "return", returnNo, "err", err)
					d.log(r, c, "return.settle", "return", returnNo,
						"반품 환불 PG 처리 실패 — 한도 선점 유지, 환불 건 "+refundID)
					d.renderReturns(w, r, c, order, http.StatusBadGateway,
						"결제사 처리에 실패했습니다. 잠시 후 확인하세요.")
					return
				}
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
		d.serverError(w, r, err)
	}
}
