package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/emirue/ondolith/internal/commerce"
)

// ProductForm is A-502 GET. id 가 비면 새 상품이다.
func (d *Deps) ProductForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "product.manage"); !ok {
		return
	}
	id := r.PathValue("id")
	if isCreate(id) {
		d.renderProduct(w, r, &commerce.Product{Visible: true}, http.StatusOK, "")
		return
	}
	p, err := d.Commerce.ProductByID(r.Context(), id)
	if d.fail(w, r, err) {
		return
	}
	if p.CategoryIDs, err = d.Commerce.ProductCategoryIDs(r.Context(), id); d.fail(w, r, err) {
		return
	}
	d.renderProduct(w, r, p, http.StatusOK, "")
}

// categoryChoice 는 A-502 의 카테고리 체크박스 하나다. 관리자 템플릿에는
// 함수맵이 없으므로 「골랐는가」를 여기서 계산해 넘긴다.
type categoryChoice struct {
	ID, Name string
	Checked  bool
}

// ProductSave is A-502 POST.
//
// **가격은 정수 minor unit 이다** (D50). 소수·문자열은 422 — 부동소수점으로
// 받으면 12000.00 과 11999.999… 가 같은 값이 되고, 그 차이가 환불에서 드러난다.
func (d *Deps) ProductSave(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "product.manage")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}

	// **재고·SKU·조합은 받지 않는다** (D19 A-502 받지 않는 필드). 실려 오면
	// 조용히 무시하지 않고 거부한다 — 무시는 오조작을 감춘다.
	for _, k := range []string{"stock", "sku", "variant_id"} {
		if r.PostForm.Has(k) {
			d.renderProduct(w, r, nil, http.StatusUnprocessableEntity,
				"재고·SKU·조합은 이 화면에서 바꾸지 않습니다. 옵션·재고 편집기를 쓰세요.")
			return
		}
	}

	p := &commerce.Product{
		ID:          r.PathValue("id"),
		Slug:        strings.TrimSpace(r.PostFormValue("slug")),
		Name:        strings.TrimSpace(r.PostFormValue("name")),
		Description: r.PostFormValue("description"),
		Visible:     r.PostFormValue("is_visible") != "",
		// **보낸 집합이 곧 저장되는 집합이다** (FR-615). 하나도 안 고르면
		// 미분류가 된다 — 카테고리는 선택 입력이다 (D19 A-502).
		CategoryIDs: r.PostForm["category_id"],
	}
	if p.Name == "" || p.Slug == "" {
		d.renderProduct(w, r, p, http.StatusUnprocessableEntity, "이름과 주소를 입력하세요.")
		return
	}
	// 상한(commerce.MaxAmount)은 저장 계층이 본다 — 아래 두 switch 가 그 오류를
	// 같은 422 로 옮긴다. 여기서 한 번 더 재면 같은 규칙이 두 곳에 생긴다.
	price, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("base_price")))
	if err != nil || price < 0 {
		d.renderProduct(w, r, p, http.StatusUnprocessableEntity, priceRangeMessage)
		return
	}
	p.BasePrice = price

	if isCreate(p.ID) {
		id, err := d.Commerce.CreateProduct(r.Context(), *p)
		switch {
		case errors.Is(err, commerce.ErrSlugTaken):
			d.renderProduct(w, r, p, http.StatusConflict, "이미 쓰이는 주소입니다.")
			return
		case errors.Is(err, commerce.ErrPriceNegative), errors.Is(err, commerce.ErrAmountTooBig):
			d.renderProduct(w, r, p, http.StatusUnprocessableEntity, priceRangeMessage)
			return
		case errors.Is(err, commerce.ErrCategoryMissing):
			d.renderProduct(w, r, p, http.StatusUnprocessableEntity, categoryMissingMessage)
			return
		case err != nil:
			http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
			return
		}
		d.log(r, c, "product.manage", "product", id, "상품 '"+p.Name+"' 생성")
		http.Redirect(w, r, "/admin/products/"+id, http.StatusSeeOther)
		return
	}

	err = d.Commerce.UpdateProduct(r.Context(), *p)
	switch {
	case errors.Is(err, commerce.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, commerce.ErrSlugTaken):
		d.renderProduct(w, r, p, http.StatusConflict, "이미 쓰이는 주소입니다.")
	case errors.Is(err, commerce.ErrPriceNegative), errors.Is(err, commerce.ErrAmountTooBig):
		d.renderProduct(w, r, p, http.StatusUnprocessableEntity, priceRangeMessage)
	case errors.Is(err, commerce.ErrCategoryMissing):
		d.renderProduct(w, r, p, http.StatusUnprocessableEntity, categoryMissingMessage)
	case err != nil:
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
	default:
		// D15 7절: 가격은 P-406 금액 계산의 입력이다.
		d.log(r, c, "product.manage", "product", p.ID, "상품 '"+p.Name+"' 수정")
		http.Redirect(w, r, "/admin/products/"+p.ID, http.StatusSeeOther)
	}
}

// priceRangeMessage 는 A-502 의 가격 범위 오류다 (0 ~ commerce.MaxAmount).
const priceRangeMessage = "가격은 0 이상 20억 이하의 정수여야 합니다."

// categoryMissingMessage 는 A-502 가 없는 카테고리를 받았을 때의 문구다 (422).
// 그 사이 A-509 에서 지워진 카테고리도 여기로 온다.
const categoryMissingMessage = "존재하지 않는 카테고리입니다. 화면을 새로 고친 뒤 다시 고르세요."

// ProductDelete is A-502 DELETE (POST + action).
//
// 주문된 상품은 FK 가 막는다 (409). 판매 중단은 `is_visible = false` 이고,
// 소프트 삭제 컬럼을 따로 두지 않는다 — "안 보이는 상태" 가 둘이 되면
// 조회에서 한쪽을 빠뜨리는 순간 지운 상품이 되살아난다 (D30 3-1).
func (d *Deps) ProductDelete(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "product.manage")
	if !ok {
		return
	}
	id := r.PathValue("id")
	err := d.Commerce.DeleteProduct(r.Context(), id)
	switch {
	case errors.Is(err, commerce.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, commerce.ErrProductInUse):
		d.renderProduct(w, r, nil, http.StatusConflict,
			"주문 내역이 있어 삭제할 수 없습니다. 노출을 끄세요.")
	case err != nil:
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
	default:
		d.log(r, c, "product.manage", "product", id, "상품 삭제")
		http.Redirect(w, r, "/admin/products", http.StatusSeeOther)
	}
}

// renderProduct draws A-502. p 가 nil 이면 저장된 상품을 다시 읽는다 — 폼 값
// 없이 거부된 요청(삭제 409 등)이 그 경우다.
func (d *Deps) renderProduct(w http.ResponseWriter, r *http.Request,
	p *commerce.Product, code int, msg string) {

	ctx := r.Context()
	id := r.PathValue("id")
	if p == nil {
		if got, err := d.Commerce.ProductByID(ctx, id); err == nil {
			p = got
			p.CategoryIDs, _ = d.Commerce.ProductCategoryIDs(ctx, id)
		} else {
			p = &commerce.Product{}
		}
	}
	// 선택지는 A-509 가 만든 전부다. 거부된 폼은 **보낸 선택**을 그대로 다시
	// 그린다 — 저장된 값으로 되돌리면 고른 것이 사라진다.
	cats, err := d.Commerce.Categories(ctx)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	picked := map[string]bool{}
	for _, c := range p.CategoryIDs {
		picked[c] = true
	}
	choices := make([]categoryChoice, 0, len(cats))
	for _, c := range cats {
		choices = append(choices, categoryChoice{ID: c.ID, Name: c.Name, Checked: picked[c.ID]})
	}
	data := map[string]any{"Product": p, "Categories": choices, "New": isCreate(id)}
	if msg != "" {
		data["Error"] = msg
	}
	d.Render(w, r, "admin/product-edit.html", code, data)
}

// VariantForm is A-503 GET.
func (d *Deps) VariantForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "product.manage"); !ok {
		return
	}
	d.renderVariants(w, r, http.StatusOK, "")
}

// VariantSave is A-503 POST.
//
// **재고 절대값 필드는 없다** (D19 A-503 받지 않는 필드). 조정값(delta)만
// 받고, 실려 온 절대값은 조용히 무시하지 않고 거부한다 — 무시는 오조작을
// 감추고, 운영자는 재고가 자기 입력대로 됐다고 믿는다.
func (d *Deps) VariantSave(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "product.manage")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	productID := r.PathValue("id")

	if r.PostForm.Has("stock") || r.PostForm.Has("stock_absolute") {
		d.renderVariants(w, r, http.StatusUnprocessableEntity,
			"재고는 절대값이 아니라 증감으로 입력합니다. 동시 주문의 판매분이 지워지기 때문입니다.")
		return
	}

	var edits []commerce.VariantEdit
	for _, id := range r.PostForm["variant_id"] {
		delta, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("delta_" + id)))
		if err != nil {
			d.renderVariants(w, r, http.StatusUnprocessableEntity,
				"재고 증감을 정수로 입력하세요.")
			return
		}
		priceDelta, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("price_delta_" + id)))
		if err != nil {
			d.renderVariants(w, r, http.StatusUnprocessableEntity,
				"가격 차액을 정수로 입력하세요.")
			return
		}
		version := -1
		if v := r.PostFormValue("version_" + id); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				d.renderVariants(w, r, http.StatusUnprocessableEntity, "잘못된 요청입니다.")
				return
			}
			version = n
		}
		edits = append(edits, commerce.VariantEdit{ID: id, StockDelta: delta,
			PriceDelta: priceDelta, SKU: strings.TrimSpace(r.PostFormValue("sku_" + id)),
			// 앞뒤 공백을 자른다 (D19 A-503). 스캐너가 붙인 공백 하나로 같은
			// 바코드가 다른 값이 되면 유일 제약을 지나간다.
			Barcode: strings.TrimSpace(r.PostFormValue("barcode_" + id)),
			Version: version})
	}
	if len(edits) == 0 {
		d.renderVariants(w, r, http.StatusUnprocessableEntity, "바꿀 조합을 고르세요.")
		return
	}

	err := d.Commerce.EditVariants(r.Context(), productID, edits)
	switch {
	case errors.Is(err, commerce.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, commerce.ErrStockVersion):
		d.renderVariants(w, r, http.StatusConflict,
			"다른 사람이 먼저 바꿨습니다. 새로고침 후 다시 시도하세요.")
	case errors.Is(err, commerce.ErrSkuTaken):
		d.renderVariants(w, r, http.StatusConflict, "이미 쓰이는 SKU 입니다.")
	case errors.Is(err, commerce.ErrBarcodeTaken):
		d.renderVariants(w, r, http.StatusConflict, "이미 쓰이는 바코드입니다.")
	case errors.Is(err, commerce.ErrOutOfStock):
		d.renderVariants(w, r, http.StatusUnprocessableEntity,
			"재고가 0 보다 작아집니다. 백오더는 없습니다.")
	// 아래 둘은 저장 계층이 판정한다 (범위: commerce.MaxAmount, SKU: DB CHECK).
	// 예전에는 SKU 가 긴 것도 위의 「재고 부족」으로 나갔다.
	case errors.Is(err, commerce.ErrSkuLength):
		d.renderVariants(w, r, http.StatusUnprocessableEntity, "SKU 는 64자 이하여야 합니다.")
	case errors.Is(err, commerce.ErrBarcodeLength):
		d.renderVariants(w, r, http.StatusUnprocessableEntity, "바코드는 64자 이하여야 합니다.")
	case errors.Is(err, commerce.ErrAmountTooBig), errors.Is(err, commerce.ErrQuantityRange):
		d.renderVariants(w, r, http.StatusUnprocessableEntity,
			"재고 증감과 가격 차액은 ±20억 이내여야 합니다.")
	case err != nil:
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
	default:
		d.log(r, c, "product.manage", "product", productID, "옵션·재고 수정")
		http.Redirect(w, r, "/admin/products/"+productID+"/variants", http.StatusSeeOther)
	}
}

func (d *Deps) renderVariants(w http.ResponseWriter, r *http.Request, code int, msg string) {
	productID := r.PathValue("id")
	p, err := d.Commerce.ProductByID(r.Context(), productID)
	if d.fail(w, r, err) {
		return
	}
	// sellableOnly=false — 관리자는 품절 조합도 봐야 재고를 채운다.
	variants, err := d.Commerce.Variants(r.Context(), productID, false)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	opts, err := d.Commerce.Options(r.Context(), productID)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	// 값을 미리 이어서 넘긴다. 관리자 템플릿에는 함수맵이 없고, `join` 하나를
	// 위해 함수맵을 들이면 다음 화면부터 무엇이 쓸 수 있는지가 흐려진다.
	type optView struct {
		Name   string
		Joined string
	}
	views := make([]optView, 0, len(opts))
	for _, o := range opts {
		views = append(views, optView{Name: o.Name, Joined: strings.Join(o.Values, ", ")})
	}
	d.Render(w, r, "admin/product-variants.html", code,
		map[string]any{"Product": p, "Variants": variants, "Options": views, "Error": msg})
}

// OptionsSave is A-503's option half: 그룹과 값을 받아 **조합을 만든다**.
//
// 값은 쉼표로 나눈다. 자바스크립트로 행을 늘리는 편집기가 아니라 텍스트
// 한 칸인 이유는, 옵션이 몇 개 안 되고 그 정도를 위해 클라이언트 상태를
// 만들면 그것부터 테마와 어긋나기 시작하기 때문이다 (DEC-3.1 결).
func (d *Deps) OptionsSave(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "product.manage")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	names := r.PostForm["option_name"]
	values := r.PostForm["option_values"]
	if len(names) != len(values) {
		d.renderVariants(w, r, http.StatusBadRequest, "잘못된 요청입니다.")
		return
	}
	var opts []commerce.Option
	for i, n := range names {
		// 빈 줄은 「추가하지 않음」이다. 화면이 늘 빈 줄을 두 개 두므로 이것을
		// 거부하면 아무것도 저장할 수 없다.
		if strings.TrimSpace(n) == "" && strings.TrimSpace(values[i]) == "" {
			continue
		}
		opts = append(opts, commerce.Option{
			Name: n, Values: strings.Split(values[i], ","),
		})
	}
	if len(opts) == 0 {
		d.renderVariants(w, r, http.StatusUnprocessableEntity,
			"옵션 그룹과 값을 하나 이상 입력하세요.")
		return
	}

	productID := r.PathValue("id")
	err := d.Commerce.SetOptions(r.Context(), productID, opts)
	switch {
	case errors.Is(err, commerce.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, commerce.ErrOptionDuplicate):
		d.renderVariants(w, r, http.StatusUnprocessableEntity,
			"그룹 이름과 값을 확인하세요. 빈 값이나 그룹 안의 중복은 저장하지 않습니다.")
	case err != nil:
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
	default:
		d.log(r, c, "product.manage", "product", productID, "옵션 저장·조합 생성")
		http.Redirect(w, r, "/admin/products/"+productID+"/variants", http.StatusSeeOther)
	}
}
