package admin

import (
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/emirue/ondolith/internal/commerce"
)

// stockError maps store errors to the codes A-514·A-515 share (D19).
//
// **형식 오류(422)와 없는 조합(404)을 구분한다.** `variant_id` 는 A-517 의 행이
// 싣는 값이라 형식이 깨진 것은 사람의 실수가 아니고, 없는 조합은 그 사이 삭제된
// 것이다.
func stockError(err error) (int, string) {
	switch {
	case errors.Is(err, commerce.ErrScanFormat):
		return http.StatusUnprocessableEntity, "잘못된 요청입니다."
	case errors.Is(err, commerce.ErrNotFound):
		return http.StatusNotFound, "조합을 찾을 수 없습니다."
	case errors.Is(err, commerce.ErrQuantityRange):
		return http.StatusUnprocessableEntity, "수량이 허용 범위를 벗어났습니다."
	case errors.Is(err, commerce.ErrStockLedger):
		return http.StatusConflict, "재고가 방금 바뀌었습니다. 다시 세어 실측을 넣으세요."
	case errors.Is(err, commerce.ErrOutOfStock):
		return http.StatusUnprocessableEntity, "재고가 0보다 작아집니다."
	default:
		return http.StatusInternalServerError, "일시적인 오류입니다."
	}
}

// stockPath 는 A-517 의 주소다. A-514·A-515 가 돌아갈 곳도 여기 하나다 —
// 경로가 고정이라 폼이 실어 온 값으로 열린 리다이렉트가 되지 않는다 (D19 A-514).
const stockPath = "/admin/stock"

// stockMemoMax 는 실사 메모의 상한이다 (D19 A-515). 작업 로그 요약
// (`operation_logs.summary` 500자) 안에 장부·실측·조정과 함께 들어가야 한다.
const stockMemoMax = 200

// variantLabel 은 「{상품} {조합}」이다 — 결과 문구와 작업 로그가 같이 쓴다.
// **어느 조합에 반영됐는지가 문구에 있어야** 잘못 스캔한 것을 그 자리에서 안다.
//
// 240자에서 자른다: 상품명 200자에 옵션 값이 길면 요약이 500자를 넘어 로그
// 기록이 통째로 실패한다.
func variantLabel(v *commerce.ScannedVariant) string {
	s := v.ProductName
	if o := commerce.OptionLabel(v.OptionValues); o != "" {
		s += " " + o
	}
	if r := []rune(s); len(r) > 240 {
		s = string(r[:240]) + "…"
	}
	return s
}

// stockCarry 는 행의 폼이 실어 온 목록 상태(`q`·`product`·`page`)다. 해석하지
// 않고 돌아갈 주소에 그대로 붙인다.
func stockCarry(r *http.Request) url.Values {
	v := url.Values{}
	for _, k := range []string{"q", "product", "page"} {
		if s := strings.TrimSpace(r.PostFormValue(k)); s != "" {
			v.Set(k, s)
		}
	}
	return v
}

// stockQuery 는 그 상태로 다시 그릴 목록이다 (오류 재표시).
func stockQuery(v url.Values) commerce.VariantQuery {
	page, _ := strconv.Atoi(v.Get("page"))
	return commerce.VariantQuery{Q: v.Get("q"), ProductID: v.Get("product"), Page: page}
}

// Stock is A-517 — 재고 목록이자 입고·실사의 출발점 (FR-624).
//
// **이 화면 자체는 어떤 상태도 바꾸지 않는다.** 그래서 GET 이고 권한이
// `product.view` 다. 상태를 바꾸는 것은 행의 폼이 보내는 A-514·A-515 다.
func (d *Deps) Stock(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "product.view")
	if !ok {
		return
	}
	q := r.URL.Query()
	data := map[string]any{}
	if msg := d.stockDone(r); msg != "" {
		data["Notice"] = msg
	}
	d.renderStock(w, r, c, http.StatusOK, commerce.VariantQuery{
		Q: q.Get("q"), ProductID: q.Get("product"), Page: pageOf(r)}, data)
}

// stockDone 은 직전 입고·실사의 결과 문구다 (D13 A-517 「직전 결과 표시」).
//
// A-514·A-515 는 303 으로 돌아오므로 결과가 주소에 실려 온다. 상품·조합·현재
// 재고는 여기서 다시 읽는다.
// ponytail: 주소를 손으로 만들면 일어나지 않은 입고의 문구를 띄울 수 있다.
// 근거는 이 문구가 아니라 작업 로그(A-601)다. 문제가 되면 세션 플래시로 옮긴다.
func (d *Deps) stockDone(r *http.Request) string {
	q := r.URL.Query()
	done := q.Get("done")
	if done == "" {
		return ""
	}
	v, err := d.Commerce.ScanVariant(r.Context(), q.Get("v"))
	if err != nil {
		return ""
	}
	switch done {
	case "receive":
		if n, err := strconv.Atoi(q.Get("n")); err == nil && n > 0 {
			return variantLabel(v) + " 입고 +" + strconv.Itoa(n) +
				" → 현재 " + strconv.Itoa(v.Stock) + "개"
		}
	case "stocktake":
		ledger, lerr := strconv.Atoi(q.Get("ledger"))
		counted, cerr := strconv.Atoi(q.Get("counted"))
		if lerr == nil && cerr == nil {
			return stocktakeSummary(v, ledger, counted)
		}
	}
	return ""
}

// stocktakeSummary 는 실사 결과 한 줄이다. 화면과 작업 로그가 같은 문장을 쓴다.
// 문구는 「실사」가 아니라 「조사」다 — 운영자가 아는 말이다 (D13 A-515).
func stocktakeSummary(v *commerce.ScannedVariant, ledger, counted int) string {
	s := variantLabel(v) + " 재고 조사 장부 " + strconv.Itoa(ledger) +
		" · 실측 " + strconv.Itoa(counted) + " · 조정 " + strconv.Itoa(counted-ledger)
	if counted == ledger {
		s += " (차이 없음)"
	}
	return s
}

// renderStock draws A-517 for find, with data's Notice/Error on top.
//
// 행의 입고·실사 폼은 **`product.manage` 가 있는 사람에게만 그린다.** 재고를
// 확인만 하는 사람이 실수로 입고를 찍지 않게 하려는 것이고, 판정은 A-514·A-515
// 가 서버에서 다시 한다 (D15 4.3 — 숨기기는 UX 이지 보안이 아니다).
func (d *Deps) renderStock(w http.ResponseWriter, r *http.Request, c Caller, code int,
	find commerce.VariantQuery, data map[string]any) {

	find.Q = strings.TrimSpace(find.Q)
	found, err := d.Commerce.FindVariants(r.Context(), find)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	page := find.Page
	if page < 1 {
		page = 1
	}
	data["Rows"] = found.Rows
	data["Q"] = find.Q
	data["Product"] = find.ProductID
	data["CanManage"] = c.Can("product.manage")

	// **스캔 흐름**: 값이 식별 값으로 정확히 한 조합에 맞으면 그 행의 입고 칸에
	// 포커스가 가고, 행의 폼은 검색어를 싣지 않는다. 그래서 입고하고 돌아오면
	// 검색창이 비어 있고 포커스가 검색창에 있어 다음 스캔을 바로 받는다.
	// 그 밖의 경우 행의 폼은 목록 상태를 싣고, 돌아오면 같은 목록이 다시 나온다.
	data["Focus"] = ""
	if found.Exact && len(found.Rows) == 1 {
		data["Focus"] = found.Rows[0].ID
	} else {
		data["CarryQ"] = find.Q
		data["CarryProduct"] = find.ProductID
		data["CarryPage"] = page
	}
	pager(data, stockPath, url.Values{"q": {find.Q}, "product": {find.ProductID}}, page, found.More)
	d.Render(w, r, "admin/stock.html", code, data)
}

// StockReceive is A-514 — A-517 행의 폼이 보내는 입고 (FR-621).
//
// **조합을 다시 찾지 않는다.** 식별은 A-517 의 검색이 끝냈고 여기는 고른 조합의
// `variant_id` 만 받는다 — 여기서 스캔 값을 다시 해석하면 식별 규칙이 두 곳이
// 되어 갈라진다 (D19 A-514 받지 않는 필드).
func (d *Deps) StockReceive(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "product.manage")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	carry := stockCarry(r)
	refuse := func(code int, msg string) {
		d.renderStock(w, r, c, code, stockQuery(carry), map[string]any{"Error": msg})
	}

	id := strings.TrimSpace(r.PostFormValue("variant_id"))
	v, err := d.Commerce.ScanVariant(r.Context(), id)
	if err != nil {
		refuse(stockError(err))
		return
	}
	qty, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("quantity")))
	if err != nil || qty < 1 {
		refuse(http.StatusUnprocessableEntity, "입고 수량은 1 이상의 정수입니다.")
		return
	}

	after, err := d.Commerce.ReceiveStock(r.Context(), id, qty)
	if err != nil {
		refuse(stockError(err))
		return
	}
	// D15 7절 「재고 수기 변경」: 조합·수량·입고 전후 재고.
	d.log(r, c, "product.manage", "variant", id, variantLabel(v)+" 입고 +"+strconv.Itoa(qty)+
		" (재고 "+strconv.Itoa(after-qty)+" → "+strconv.Itoa(after)+")")
	carry.Set("done", "receive")
	carry.Set("v", id)
	carry.Set("n", strconv.Itoa(qty))
	http.Redirect(w, r, stockPath+"?"+carry.Encode(), http.StatusSeeOther)
}

// Stocktake is A-515 — A-517 행의 폼이 보내는 재고 조사 (FR-622).
//
// **조정값을 받지 않는다** (D19 A-515 받지 않는 필드). 서버가 `실측 - 장부` 로
// 계산한다 — 클라이언트가 조정값을 주면 실사가 임의 재고 조작 창구가 된다.
//
// **장부 수량은 행이 실어 온 숨은 값이다.** 사람이 타이핑하지 않는다 — 잠금용
// 값을 사람이 만들면 잠금이 아니다.
func (d *Deps) Stocktake(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "product.manage")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	carry := stockCarry(r)
	refuse := func(code int, msg string) {
		d.renderStock(w, r, c, code, stockQuery(carry), map[string]any{"Error": msg})
	}
	// 조정값이 실려 오면 조용히 무시하지 않고 거부한다. 무시하면 운영자는
	// 자기가 보낸 조정값이 적용됐다고 믿는다.
	if r.PostForm.Has("delta") || r.PostForm.Has("adjustment") {
		refuse(http.StatusUnprocessableEntity, "조정값은 받지 않습니다. 실측 수량만 입력하세요.")
		return
	}

	id := strings.TrimSpace(r.PostFormValue("variant_id"))
	v, err := d.Commerce.ScanVariant(r.Context(), id)
	if err != nil {
		refuse(stockError(err))
		return
	}
	counted, cerr := strconv.Atoi(strings.TrimSpace(r.PostFormValue("counted")))
	if cerr != nil || counted < 0 {
		refuse(http.StatusUnprocessableEntity, "실측 수량은 0 이상이어야 합니다.")
		return
	}
	ledger, lerr := strconv.Atoi(strings.TrimSpace(r.PostFormValue("ledger")))
	if lerr != nil {
		refuse(http.StatusUnprocessableEntity, "잘못된 요청입니다.")
		return
	}
	memo := strings.TrimSpace(r.PostFormValue("memo"))
	if utf8.RuneCountInString(memo) > stockMemoMax {
		refuse(http.StatusUnprocessableEntity, "메모는 200자 이하여야 합니다.")
		return
	}

	res, err := d.Commerce.Stocktake(r.Context(), id, counted, ledger)
	if err != nil {
		code, msg := stockError(err)
		if code == http.StatusConflict {
			// **그 조합 한 행으로 현재 재고와 함께 다시 그린다.** 사용자는 다시
			// 세어 실측만 넣으면 된다 — 새 장부 값은 행이 다시 싣는다.
			d.renderStock(w, r, c, code, commerce.VariantQuery{Q: id}, map[string]any{"Error": msg})
			return
		}
		refuse(code, msg)
		return
	}
	// **장부·실측·조정 셋을 모두 남긴다** (D15 7절). 하나라도 빠지면 나중에
	// 무엇을 근거로 재고가 바뀌었는지 재구성할 수 없다. 메모는 왜 틀렸었는지다.
	summary := stocktakeSummary(v, res.Ledger, res.Counted)
	if memo != "" {
		summary += " · 메모: " + memo
	}
	d.log(r, c, "product.manage", "variant", id, summary)
	carry.Set("done", "stocktake")
	carry.Set("v", id)
	carry.Set("ledger", strconv.Itoa(res.Ledger))
	carry.Set("counted", strconv.Itoa(res.Counted))
	http.Redirect(w, r, stockPath+"?"+carry.Encode(), http.StatusSeeOther)
}

// PickCheck is A-516 (GET 목록 · POST 스캔 대조).
//
// **이 화면은 재고도 주문 상태도 건드리지 않는다** (FR-623). 건드리면 재고는
// P-406 에서 이미 차감됐으므로 이중 차감이 되고, 상태는 A-506 이 옮기는
// 것이라 유령 전이가 생긴다.
func (d *Deps) PickCheck(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "order.update")
	if !ok {
		return
	}
	orderNo := r.PathValue("no")
	lines, err := d.Commerce.PickList(r.Context(), orderNo)
	if d.fail(w, r, err) {
		return
	}
	if r.Method == http.MethodGet {
		// **`Scanned` 를 빈 map 으로라도 넘긴다.** 없으면 템플릿의
		// `index $s .VariantID` 가 nil 을 색인해 오류로 끝나고, 화면을 여는
		// 것만으로 500 이 났다 — 아직 아무것도 스캔하지 않은 것이 이 화면의
		// 정상 출발 상태인데도.
		d.Render(w, r, "admin/pick.html", http.StatusOK, map[string]any{
			"OrderNo": orderNo, "Lines": lines, "Scanned": map[string]int{}})
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}

	// 대조 상태는 폼이 실어 나른다 — 서버에 세션 상태를 두면 두 사람이 같은
	// 주문을 대조할 때 서로의 진행을 덮어쓴다.
	scanned := map[string]int{}
	for _, l := range lines {
		if n, err := strconv.Atoi(r.PostFormValue("count_" + l.VariantID)); err == nil && n > 0 {
			scanned[l.VariantID] = n
		}
	}
	data := map[string]any{"OrderNo": orderNo, "Lines": lines, "Scanned": scanned}

	if value := strings.TrimSpace(r.PostFormValue("scanned")); value != "" {
		// **수량은 기본값 1 이다** — 스캔하고 Enter 만 치면 1개로 센다.
		qty := 1
		if s := strings.TrimSpace(r.PostFormValue("quantity")); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 {
				data["Error"] = "수량은 1 이상의 정수입니다."
				d.Render(w, r, "admin/pick.html", http.StatusUnprocessableEntity, data)
				return
			}
			qty = n
		}
		// 식별은 A-517 과 같은 함수가 한다 (FR-627): QR·SKU·바코드.
		found, err := d.Commerce.FindVariants(r.Context(), commerce.VariantQuery{Q: value})
		if err != nil {
			http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
			return
		}
		v, err := found.One()
		if err == nil {
			err = commerce.CheckPick(lines, scanned, v.ID, qty)
		}
		if err != nil {
			msg := pickMessage(err, lines, v)
			// **거부도 작업 로그에 남는다** (FR-623) — 잘못된 스캔이 반복되면
			// 그것 자체가 라벨이나 피킹 절차의 문제 신호다.
			d.log(r, c, "order.update", "order", orderNo, "피킹 대조 거부: "+msg)
			data["Error"] = msg
			d.Render(w, r, "admin/pick.html", http.StatusUnprocessableEntity, data)
			return
		}
		scanned[v.ID] += qty
	}

	if commerce.PickComplete(lines, scanned) {
		d.log(r, c, "order.update", "order", orderNo, "피킹 전 품목 대조 완료")
		data["Notice"] = "전 품목 대조 완료."
	}
	d.Render(w, r, "admin/pick.html", http.StatusOK, data)
}

// pickMessage 는 A-516 의 거부 문구다 (D19 A-516 거부 조건).
//
// v 는 스캔 값이 가리킨 조합이다 — 식별되지 않았으면 nil 이다. 주문에 없는
// 상품이면 그 이름을 함께 보여야 사람이 무엇을 집었는지 안다.
func pickMessage(err error, lines []commerce.PickLine, v *commerce.ScannedVariant) string {
	switch {
	case errors.Is(err, commerce.ErrPickAmbiguous):
		return "여러 상품에 해당하는 코드입니다. QR로 다시 스캔하세요."
	case errors.Is(err, commerce.ErrPickOverCount):
		for _, l := range lines {
			if v != nil && l.VariantID == v.ID {
				return "주문 수량(" + strconv.Itoa(l.Ordered) + "개)을 넘었습니다: " + l.ProductName
			}
		}
		return "주문 수량을 넘었습니다."
	default:
		if v != nil {
			return "이 주문에 없는 상품입니다: " + v.ProductName
		}
		return "이 주문에 없는 상품입니다."
	}
}

// QRLabel is A-513 — 라벨 인쇄 시트.
//
// **QR 이 담는 값은 `product_variants.id` 다** (FR-620). SKU 가 아니다: SKU 는
// 외부 시스템이 정하고 바뀔 수 있어서, 바뀌는 순간 이미 붙은 스티커가 다른
// 조합을 가리키거나 아무것도 가리키지 않게 된다.
//
// **상태를 바꾸지 않으므로 GET 만 있다.** 인쇄는 브라우저가 한다.
func (d *Deps) QRLabel(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "product.view"); !ok {
		return
	}
	productID := r.PathValue("id")
	p, err := d.Commerce.ProductByID(r.Context(), productID)
	if d.fail(w, r, err) {
		return
	}
	// sellableOnly=false — 품절 조합에도 라벨은 필요하다. 라벨이 없으면
	// 그 조합은 입고 스캔을 할 수 없어 영원히 품절로 남는다.
	variants, err := d.Commerce.Variants(r.Context(), productID, false)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}

	labels := make([]map[string]any, 0, len(variants))
	for _, v := range variants {
		svg, err := qrSVG(v.ID, 128)
		if err != nil {
			http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
			return
		}
		labels = append(labels, map[string]any{
			// **우리가 만든 SVG 다** — 사용자 입력이 아니라 위의 qrSVG 가
			// 좌표만 찍은 문자열이라 template.HTML 로 낸다. 담기는 값은
			// uuid 이고 그것도 속성이 아니라 도형 좌표로만 쓰인다.
			"SVG":     template.HTML(svg), //nolint:gosec // 생성원이 qrSVG 하나다
			"Variant": v,
		})
	}
	d.Render(w, r, "admin/qr-labels.html", http.StatusOK,
		map[string]any{"Product": p, "Labels": labels})
}
