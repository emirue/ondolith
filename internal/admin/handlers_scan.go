package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/emirue/ondolith/internal/commerce"
)

// scanError maps store errors to the two codes A-514~A-517 share.
//
// **형식 오류(422)와 없는 조합(404)을 구분한다.** 고치는 사람이 다르다 —
// 형식은 스캐너 설정이고, 없는 조합은 라벨이 오래된 것이다.
func scanError(err error) (int, string) {
	switch {
	case errors.Is(err, commerce.ErrScanFormat):
		return http.StatusUnprocessableEntity, "스캔 값이 조합 식별자가 아닙니다. 스캐너 설정을 확인하세요."
	case errors.Is(err, commerce.ErrNotFound):
		return http.StatusNotFound, "그런 조합이 없습니다. 라벨이 오래된 것일 수 있습니다."
	case errors.Is(err, commerce.ErrQuantityRange):
		return http.StatusUnprocessableEntity, "수량이 허용 범위를 벗어났습니다."
	case errors.Is(err, commerce.ErrStockLedger):
		return http.StatusConflict, "재고가 방금 바뀌었습니다. 다시 확인하세요."
	case errors.Is(err, commerce.ErrOutOfStock):
		return http.StatusUnprocessableEntity, "재고가 0보다 작아집니다."
	default:
		return http.StatusInternalServerError, "일시적인 오류입니다."
	}
}

// ScanReceive is A-514 (GET 폼 · POST 입고).
func (d *Deps) ScanReceive(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "product.manage")
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		d.Render(w, r, "admin/scan-receive.html", http.StatusOK, map[string]any{})
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	scanned := strings.TrimSpace(r.PostFormValue("scanned"))
	qty, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("quantity")))
	if err != nil || qty < 1 {
		d.Render(w, r, "admin/scan-receive.html", http.StatusUnprocessableEntity,
			map[string]any{"Error": "수량은 1 이상의 정수입니다."})
		return
	}

	after, err := d.Commerce.ReceiveStock(r.Context(), scanned, qty)
	if err != nil {
		code, msg := scanError(err)
		d.Render(w, r, "admin/scan-receive.html", code, map[string]any{"Error": msg})
		return
	}
	d.log(r, c, "product.manage", "variant", scanned,
		"스캔 입고 +"+strconv.Itoa(qty)+" (재고 "+strconv.Itoa(after)+")")
	d.Render(w, r, "admin/scan-receive.html", http.StatusOK, map[string]any{
		"Notice": "입고 완료. 현재 재고 " + strconv.Itoa(after) + "개."})
}

// Stocktake is A-515 (GET 폼 · POST 실사).
//
// **조정값을 받지 않는다** (D19 A-515 받지 않는 필드). 서버가 `실측 - 장부` 로
// 계산한다 — 클라이언트가 조정값을 주면 실사가 임의 재고 조작 창구가 된다.
func (d *Deps) Stocktake(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "product.manage")
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		d.Render(w, r, "admin/stocktake.html", http.StatusOK, map[string]any{})
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	// 조정값이 실려 오면 조용히 무시하지 않고 거부한다. 무시하면 운영자는
	// 자기가 보낸 조정값이 적용됐다고 믿는다.
	if r.PostForm.Has("delta") || r.PostForm.Has("adjustment") {
		d.Render(w, r, "admin/stocktake.html", http.StatusUnprocessableEntity,
			map[string]any{"Error": "조정값은 받지 않습니다. 실측 수량만 입력하세요."})
		return
	}

	scanned := strings.TrimSpace(r.PostFormValue("scanned"))
	counted, cerr := strconv.Atoi(strings.TrimSpace(r.PostFormValue("counted")))
	ledger, lerr := strconv.Atoi(strings.TrimSpace(r.PostFormValue("ledger")))
	if cerr != nil || counted < 0 {
		d.Render(w, r, "admin/stocktake.html", http.StatusUnprocessableEntity,
			map[string]any{"Error": "실측 수량은 0 이상이어야 합니다."})
		return
	}
	if lerr != nil {
		d.Render(w, r, "admin/stocktake.html", http.StatusUnprocessableEntity,
			map[string]any{"Error": "장부 수량이 없습니다. 조합을 다시 스캔하세요."})
		return
	}

	res, err := d.Commerce.Stocktake(r.Context(), scanned, counted, ledger)
	if err != nil {
		code, msg := scanError(err)
		data := map[string]any{"Error": msg}
		if code == http.StatusConflict {
			// 현재 값을 함께 보여준다 — 다시 세라는 말만으로는 부족하다.
			if v, verr := d.Commerce.ScanVariant(r.Context(), scanned); verr == nil {
				data["Variant"] = v
			}
		}
		d.Render(w, r, "admin/stocktake.html", code, data)
		return
	}
	// **장부·실측·조정 셋을 모두 남긴다** (D15 7절). 하나라도 빠지면 나중에
	// 무엇을 근거로 재고가 바뀌었는지 재구성할 수 없다.
	d.log(r, c, "product.manage", "variant", scanned,
		"재고 실사 장부 "+strconv.Itoa(res.Ledger)+
			" · 실측 "+strconv.Itoa(res.Counted)+
			" · 조정 "+strconv.Itoa(res.Delta))
	notice := "차이 없음."
	if res.Delta != 0 {
		notice = "조정 " + strconv.Itoa(res.Delta) + "개 반영. 현재 재고 " +
			strconv.Itoa(res.Counted) + "개."
	}
	d.Render(w, r, "admin/stocktake.html", http.StatusOK, map[string]any{"Notice": notice})
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
	if errors.Is(err, commerce.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	if r.Method == http.MethodGet {
		d.Render(w, r, "admin/pick.html", http.StatusOK,
			map[string]any{"OrderNo": orderNo, "Lines": lines})
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

	if v := strings.TrimSpace(r.PostFormValue("scanned")); v != "" {
		if err := commerce.CheckPick(lines, scanned, v); err != nil {
			// **거부도 작업 로그에 남는다** (FR-623) — 잘못된 스캔이 반복되면
			// 그것 자체가 라벨이나 피킹 절차의 문제 신호다.
			d.log(r, c, "order.update", "order", orderNo, "피킹 대조 거부: "+err.Error())
			data["Error"] = err.Error()
			d.Render(w, r, "admin/pick.html", http.StatusUnprocessableEntity, data)
			return
		}
		scanned[v]++
		data["Scanned"] = scanned
	}

	if commerce.PickComplete(lines, scanned) {
		d.log(r, c, "order.update", "order", orderNo, "피킹 전 품목 대조 완료")
		data["Notice"] = "전 품목 대조 완료."
	}
	d.Render(w, r, "admin/pick.html", http.StatusOK, data)
}

// ScanLookup is A-517 — 조회만 한다. 권한이 `product.view` 인 이유가 그것이다.
func (d *Deps) ScanLookup(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "product.view"); !ok {
		return
	}
	data := map[string]any{}
	if v := strings.TrimSpace(r.URL.Query().Get("scanned")); v != "" {
		got, err := d.Commerce.ScanVariant(r.Context(), v)
		if err != nil {
			code, msg := scanError(err)
			d.Render(w, r, "admin/scan-lookup.html", code, map[string]any{"Error": msg})
			return
		}
		data["Variant"] = got
	}
	d.Render(w, r, "admin/scan-lookup.html", http.StatusOK, data)
}
