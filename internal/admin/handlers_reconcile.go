package admin

import (
	"net/http"
	"strconv"
	"time"

	"github.com/emirue/ondolith/internal/commerce"
)

// reconcileDefaultDays·reconcileMaxDays 는 D50 의 값이다. 상한이 있는 이유는
// 조회가 건별이기 때문이다 — 기간이 넓으면 그만큼 PG 를 두드린다.
const (
	reconcileDefaultDays = 7
	reconcileMaxDays     = 31
)

// Reconcile is A-508 GET.
//
// **금액을 폼에서 받지 않는다** (D19 A-508 받지 않는 필드). 근거는 조회
// 결과이고, 폼에서 받으면 대사가 아니라 수기 조작이다.
//
// **자동으로 고치지 않는다.** 조회 결과를 우리 행에 그대로 쓰면 PG 의 일시적
// 응답 하나가 우리 장부를 바꾼다. 사람이 보고 A-506 으로 옮긴다 (D50).
func (d *Deps) Reconcile(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "payment.view"); !ok {
		return
	}
	days := reconcileDefaultDays
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > reconcileMaxDays {
			d.Render(w, r, "admin/reconcile.html", http.StatusUnprocessableEntity,
				map[string]any{"Error": "조회 기간은 1~31일입니다.", "Days": reconcileDefaultDays})
			return
		}
		days = n
	}

	until := time.Now()
	rows, err := d.Commerce.PaymentsToReconcile(r.Context(),
		until.AddDate(0, 0, -days), until.Add(time.Minute))
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	// 클로저가 nil 인 경우(배선 전)와 반환값이 nil 인 경우(PG 「사용 안 함」)를
	// 한 경로로 모은다. Reconcile 이 nil 을 「조회하지 않았다」로 표시한다.
	var gw commerce.Gateway
	if d.Gateway != nil {
		gw = d.Gateway()
	}
	rows = d.Commerce.Reconcile(r.Context(), gw, rows)
	mismatched := 0
	for _, row := range rows {
		if row.Diff != "" {
			mismatched++
		}
	}
	d.Render(w, r, "admin/reconcile.html", http.StatusOK, map[string]any{
		"Rows": rows, "Days": days, "Mismatched": mismatched})
}

// WebhookLog is A-603 — 수신 이력.
//
// **P-905 가 기록하고 여기서 본다.** 가상계좌 입금이 주문에 반영되지 않았을 때
// "웹훅이 오긴 왔는가" 를 확인할 유일한 곳이다 (D13 A-603). 원문에 결제 정보가
// 들어 있으므로 `payment.view` 를 요구한다.
func (d *Deps) WebhookLog(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "payment.view"); !ok {
		return
	}
	rows, err := d.Commerce.WebhookHistory(r.Context(), 100)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	unprocessed := 0
	for _, row := range rows {
		if row.Status == "수신" {
			unprocessed++
		}
	}
	data := map[string]any{"Rows": rows}
	if unprocessed > 0 {
		// 자동 재처리를 두지 않기로 했으므로 (D50) 사람이 이것을 봐야 한다.
		data["Warning"] = "처리되지 않은 웹훅 " + strconv.Itoa(unprocessed) + "건이 있습니다."
	}
	d.Render(w, r, "admin/webhooks.html", http.StatusOK, data)
}
