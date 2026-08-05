package app

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/alexedwards/scs/v2"

	"github.com/emirue/ondolith/internal/commerce"
	"github.com/emirue/ondolith/internal/theme"
)

// shopDeps is what the storefront screens need.
type shopDeps struct {
	*publicDeps
	sm    *scs.SessionManager
	store *commerce.Store
	log   *slog.Logger
	// shipping is read per request, not captured: A-512 changes it and the
	// checkout screen must show what is true now, not what was true at boot.
	shipping func() commerce.Shipping
	// gateway·pgName·pgClientKey 도 요청마다 읽는다. A-2xx 가 PG 설정을
	// 바꾸면 재시작 없이 반영돼야 한다.
	gateway     func() commerce.Gateway
	pgName      func() string
	pgClientKey func() string
}

// guestKey returns the session-scoped cart key for a visitor who is not
// logged in, creating one on first use.
//
// 세션에 둔다. 쿠키에 직접 두면 값이 클라이언트 것이 되고, 남의 키를 적어 넣는
// 것만으로 남의 장바구니를 연다 (SC-3).
func (d *shopDeps) owner(r *http.Request) (commerce.CartOwner, error) {
	if a := ActorFrom(r.Context()); a.IsAuthenticated() {
		return commerce.CartOwner{UserID: a.User.ID}, nil
	}
	ctx := r.Context()
	key := d.sm.GetString(ctx, sessCartKey)
	if key == "" {
		var err error
		key, err = commerce.NewGuestKey()
		if err != nil {
			return commerce.CartOwner{}, err
		}
		d.sm.Put(ctx, sessCartKey, key)
	}
	return commerce.CartOwner{GuestKey: key}, nil
}

// P-301 GET /shop — the product list.
func (d *shopDeps) productList(w http.ResponseWriter, r *http.Request) {
	d.renderList(w, r, "", r.URL.Query().Get("sort"), "")
}

// P-302 GET /shop/c/{slug} — one category.
func (d *shopDeps) categoryList(w http.ResponseWriter, r *http.Request) {
	cat, err := d.store.CategoryBySlug(r.Context(), r.PathValue("slug"))
	if errors.Is(err, commerce.ErrNotFound) {
		d.notFound(w, r)
		return
	}
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	d.renderList(w, r, cat.ID, r.URL.Query().Get("sort"), cat.Name)
}

// P-305 GET /shop/search — search.
func (d *shopDeps) productSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		// 빈 검색어로 전체를 훑지 않는다. 목록 화면이 이미 그 일을 한다.
		d.renderPage(w, r, "shop/search.html", http.StatusOK,
			d.shopView(r, "상품 검색", map[string]any{"Query": ""}))
		return
	}
	products, err := d.store.SearchProducts(r.Context(), q, pageOf(r))
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	d.renderPage(w, r, "shop/search.html", http.StatusOK,
		d.shopView(r, "상품 검색", map[string]any{"Query": q, "Products": products}))
}

func (d *shopDeps) renderList(w http.ResponseWriter, r *http.Request, categoryID, sort, title string) {
	products, err := d.store.ListProducts(r.Context(), commerce.ProductQuery{
		VisibleOnly: true, CategoryID: categoryID, Sort: sort, Page: pageOf(r),
	})
	if errors.Is(err, commerce.ErrUnknownSort) {
		// 정렬 값이 허용 목록에 없다. 400 이지 500 이 아니다 — 사용자가 URL 을
		// 고친 것이고 서버가 고장난 것이 아니다.
		http.Error(w, "알 수 없는 정렬입니다.", http.StatusBadRequest)
		return
	}
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	if title == "" {
		title = "상품"
	}
	cats, err := d.store.Categories(r.Context())
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	d.renderPage(w, r, "shop/list.html", http.StatusOK, d.shopView(r, title, map[string]any{
		"Products": products, "Categories": cats, "Sort": sort, "Page": pageOf(r),
	}))
}

// P-303 GET /shop/p/{slug} — one product.
func (d *shopDeps) productDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// visibleOnly=true: 미노출 상품은 404 다. 숨김이 아니라 없음이어야 URL 을
	// 아는 사람도 존재를 확인하지 못한다.
	p, err := d.store.ProductBySlug(ctx, r.PathValue("slug"), true)
	if errors.Is(err, commerce.ErrNotFound) {
		d.notFound(w, r)
		return
	}
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	variants, err := d.store.Variants(ctx, p.ID, false)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	d.renderPage(w, r, "shop/product.html", http.StatusOK, d.shopView(r, p.Name, map[string]any{
		"Product": p, "Variants": variants, "Options": commerce.OptionGroups(variants),
	}))
}

// P-304 GET /shop/p/{slug}/variant — htmx: which combination did they pick?
//
// 조각만 그린다. 전체 페이지를 다시 그리면 선택할 때마다 상품 설명과 이미지가
// 다시 내려간다 (FR-602).
func (d *shopDeps) variantPick(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := d.store.ProductBySlug(ctx, r.PathValue("slug"), true)
	if errors.Is(err, commerce.ErrNotFound) {
		d.notFound(w, r)
		return
	}
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	variants, err := d.store.Variants(ctx, p.ID, false)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	// 선택값은 폼에서 온다. 조합 ID 를 받지 않는 이유: ID 를 받으면 다른 상품의
	// 조합 ID 를 넣어 그 가격을 이 상품에 붙일 수 있다.
	picked := map[string]string{}
	for _, g := range commerce.OptionGroups(variants) {
		if v := r.URL.Query().Get("opt_" + g.Name); v != "" {
			picked[g.Name] = v
		}
	}
	match := commerce.MatchVariant(variants, picked)
	d.renderPage(w, r, "shop/variant.html", http.StatusOK, d.shopView(r, p.Name, map[string]any{
		"Product": p, "Variant": match, "Picked": picked,
	}))
}

// P-401 POST /cart/items — add.
func (d *shopDeps) cartAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	owner, err := d.owner(r)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	qty, _ := strconv.Atoi(r.PostFormValue("quantity"))
	if qty == 0 {
		qty = 1
	}
	err = d.store.AddToCart(r.Context(), owner, r.PostFormValue("variant_id"), qty)
	switch {
	case errors.Is(err, commerce.ErrOutOfStock), errors.Is(err, commerce.ErrNotSellable),
		errors.Is(err, commerce.ErrQuantityRange):
		d.renderPage(w, r, "shop/cart.html", http.StatusUnprocessableEntity,
			d.shopView(r, "장바구니", map[string]any{"Error": err.Error()}))
		return
	case errors.Is(err, commerce.ErrNotFound):
		d.notFound(w, r)
		return
	case err != nil:
		d.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/cart", http.StatusSeeOther)
}

// P-402 GET /cart — the cart.
func (d *shopDeps) cartView(w http.ResponseWriter, r *http.Request) {
	owner, err := d.owner(r)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	items, err := d.store.CartItems(r.Context(), owner)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	goods := 0
	for _, it := range items {
		goods += it.UnitPrice * it.Quantity
	}
	fee, _ := d.shipping().Fee(goods)
	d.renderPage(w, r, "shop/cart.html", http.StatusOK, d.shopView(r, "장바구니", map[string]any{
		"Items": items, "Goods": goods, "Fee": fee, "Total": goods + fee,
	}))
}

// P-403 PATCH /cart/items/{id} · P-404 DELETE — change or remove.
//
// 두 화면이 한 함수인 이유: 수량 0 이 삭제다. 나누면 "0 으로 바꾸기" 와
// "삭제" 가 서로 다른 소유권 검사를 갖게 된다.
func (d *shopDeps) cartUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	owner, err := d.owner(r)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	qty := 0
	if r.Method != http.MethodDelete {
		qty, _ = strconv.Atoi(r.PostFormValue("quantity"))
	}
	err = d.store.UpdateCartItem(r.Context(), owner, r.PathValue("id"), qty)
	switch {
	case errors.Is(err, commerce.ErrNotFound):
		// 남의 항목도 여기로 온다 (SC-3). "권한 없음" 이 아니라 404 인 이유는
		// 그 답이 항목의 존재를 알려주기 때문이다.
		d.notFound(w, r)
		return
	case errors.Is(err, commerce.ErrOutOfStock), errors.Is(err, commerce.ErrQuantityRange),
		errors.Is(err, commerce.ErrNotSellable):
		d.renderPage(w, r, "shop/cart.html", http.StatusUnprocessableEntity,
			d.shopView(r, "장바구니", map[string]any{"Error": err.Error()}))
		return
	case err != nil:
		d.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/cart", http.StatusSeeOther)
}

// shopView wraps the page data in the theme's view model.
func (d *shopDeps) shopView(r *http.Request, title string, data map[string]any) theme.View {
	v := d.view(r, title, "")
	v.Data = data
	return v
}

func pageOf(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if n < 1 {
		return 1
	}
	return n
}
