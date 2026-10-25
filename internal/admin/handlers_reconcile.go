package admin

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/emirue/ondolith/internal/commerce"
	"github.com/emirue/ondolith/internal/content"
)

// reconcileMaxDays 는 D50 의 값이다: 종료일 − 시작일의 상한. 상한이 있는 이유는
// 조회가 건별이기 때문이다 — 기간이 넓으면 그만큼 PG 를 두드린다. 1개월 버튼은
// 차이가 최대 31일이라 언제나 이 안에 든다.
const reconcileMaxDays = 31

// Reconcile is A-508 GET.
//
// **금액을 폼에서 받지 않는다** (D19 A-508 받지 않는 필드). 근거는 조회
// 결과이고, 폼에서 받으면 대사가 아니라 수기 조작이다.
//
// **자동으로 고치지 않는다.** 조회 결과를 우리 행에 그대로 쓰면 PG 의 일시적
// 응답 하나가 우리 장부를 바꾼다. 사람이 보고 그 줄의 「PG 결과로 반영」을
// 누른다 (ReconcileResolve, D50).
func (d *Deps) Reconcile(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "payment.view")
	if !ok {
		return
	}
	q := r.URL.Query()
	d.reconcilePage(w, r, c, q.Get("from"), q.Get("to"), http.StatusOK, "")
}

// ReconcileResolve is A-508 POST — 「PG 결과로 반영」 (D19 A-508 「대사 결과로 상태
// 변경」). 승인 결과를 모른 채 '대기' 로 남은 주문결제를 조회 API 가 답한 대로
// 닫는다. 근거는 조회 결과뿐이고 폼은 결제 id 만 준다 — 금액·상태를 받지 않는다.
func (d *Deps) ReconcileResolve(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "payment.view")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		d.refuse(w, r, http.StatusBadRequest, "잘못된 요청입니다.")
		return
	}
	id, from, to := r.PostFormValue("payment_id"), r.PostFormValue("from"), r.PostFormValue("to")
	// uuid 컬럼과 비교하면 22P02 로 500 이 된다 — 없는 것과 같게 404 다.
	if !content.IsUUID(id) {
		http.NotFound(w, r)
		return
	}
	// D13: 대사 결과로 상태를 바꾸는 것은 payment.view + order.update 를 함께
	// 요구한다. 조회 권한만으로 주문을 바꾸면 대사가 우회 경로가 된다.
	if !c.Can("order.update") {
		d.log(r, c, "payment.reconcile", "payment", id, "PG 결과 반영 거부 — order.update 없음")
		d.refuse(w, r, http.StatusForbidden, "주문 변경 권한이 필요합니다.")
		return
	}
	to2, err := d.Commerce.ResolvePendingPayment(r.Context(), d.pgGateway(), id)
	switch {
	case err == nil:
		summary := "PG 실패 반영 — 결제 실패"
		if to2 != "" {
			summary = "PG 승인 반영 — 주문 " + string(to2)
		}
		d.log(r, c, "payment.reconcile", "payment", id, summary)
		back := url.Values{}
		if from != "" || to != "" {
			back.Set("from", from)
			back.Set("to", to)
		}
		target := "/admin/reconcile"
		if len(back) > 0 {
			target += "?" + back.Encode()
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	case errors.Is(err, commerce.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, commerce.ErrNothingToResolve):
		d.reconcilePage(w, r, c, from, to, http.StatusConflict,
			"반영할 것이 없습니다 — 결제사도 아직 끝내지 않았거나 이미 반영된 결제입니다.")
	case errors.Is(err, commerce.ErrTransitionNotAllowed), errors.Is(err, commerce.ErrActorNotAllowed),
		errors.Is(err, commerce.ErrAmountMismatch):
		// 예: 10분 만료로 이미 결제실패가 된 주문을 PG 가 승인이라 한다 — 돈은
		// 나갔고 주문은 끝났다. 결제사 화면에서 취소할 일이다.
		d.log(r, c, "payment.reconcile", "payment", id, "PG 결과 반영 거부 — "+err.Error())
		d.reconcilePage(w, r, c, from, to, http.StatusConflict, "이 상태로 변경할 수 없습니다.")
	default:
		d.Logger.Error("PG 결과 반영", "payment", id, "err", err)
		d.log(r, c, "payment.reconcile", "payment", id, "PG 조회 실패 — 반영하지 않음")
		d.reconcilePage(w, r, c, from, to, http.StatusBadGateway, "결제사 조회에 실패했습니다.")
	}
}

// reconcilePage draws A-508 for a period. GET 과 POST 의 오류 화면이 같은 것을 그린다.
func (d *Deps) reconcilePage(w http.ResponseWriter, r *http.Request, c Caller,
	from, to string, code int, msg string) {

	// 기간 조회 (D19 0.6): 결제 생성 시각 기준. **기본은 1주일 버튼과 같은 범위다.**
	// 한쪽만 비면 그쪽을 채운다 — 끝이 열리면 상한을 잴 수 없고, 기간 안의
	// 결제마다 PG 를 한 번씩 부르는 화면에서 열린 범위는 받지 않는다.
	today := time.Now()
	week := quickRanges(today, false)[0]
	if to == "" {
		to = week.To
	}
	p, err := parsePeriod(from, to, time.Local)
	if err == nil && from == "" {
		// 종료일만 왔다: 그날까지의 1주일.
		end, _ := time.ParseInLocation(dateLayout, to, time.Local)
		p, err = parsePeriod(end.AddDate(0, 0, -7).Format(dateLayout), to, time.Local)
	}
	if err == nil && p.days > reconcileMaxDays {
		err = errPeriodSpan
	}
	data := map[string]any{"From": p.From, "To": p.To,
		// 3개월 버튼이 없다 — 상한이 31일이다.
		"Quick": quickLinks("/admin/reconcile", nil, today, false),
		// 「PG 결과로 반영」은 order.update 가 있을 때만 그린다 (D13).
		"CanResolve": c.Can("order.update"),
		// 저장값(toss)이 아니라 결제 설정 화면과 같은 이름으로 부른다.
		"PGNames": pgProviders}
	if msg != "" {
		data["Error"] = msg
	}
	if err != nil {
		data["Error"] = err.Error()
		d.Render(w, r, "admin/reconcile.html", http.StatusUnprocessableEntity, data)
		return
	}

	rows, err := d.Commerce.PaymentsToReconcile(r.Context(), *p.Since, *p.Until)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	// 클로저가 nil 인 경우(배선 전)와 반환값이 nil 인 경우(PG 「사용 안 함」)를
	// 한 경로로 모은다. Reconcile 이 nil 을 「조회하지 않았다」로 표시한다.
	rows = d.Commerce.Reconcile(r.Context(), d.pgGateway(), rows)
	mismatched := 0
	for _, row := range rows {
		if row.Diff != "" {
			mismatched++
		}
	}
	data["Rows"] = rows
	data["Mismatched"] = mismatched
	d.Render(w, r, "admin/reconcile.html", code, data)
}

// webhookPageSize 는 A-603 한 쪽의 행 수다.
const webhookPageSize = 100

// WebhookLog is A-603 — 수신 이력.
//
// **P-905 가 기록하고 여기서 본다.** 가상계좌 입금이 주문에 반영되지 않았을 때
// "웹훅이 오긴 왔는가" 를 확인할 유일한 곳이다 (D13 A-603). 원문에 결제 정보가
// 들어 있으므로 `payment.view` 를 요구한다.
func (d *Deps) WebhookLog(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "payment.view"); !ok {
		return
	}
	// 기간 조회 (D19 0.6): 수신 시각 기준. 기본은 기간 조건 없음.
	q := r.URL.Query()
	page := pageOf(r)
	p, perr := parsePeriod(q.Get("from"), q.Get("to"), time.Local)
	data := map[string]any{"From": p.From, "To": p.To,
		"Quick": quickLinks("/admin/webhooks", nil, time.Now(), true), "PGNames": pgProviders}
	if perr != nil {
		data["Error"] = perr.Error()
		d.Render(w, r, "admin/webhooks.html", http.StatusUnprocessableEntity, data)
		return
	}
	// 한 행 더 읽어 다음 쪽이 있는지 본다. 앞 판은 최신 100행에서 말없이 잘랐다 —
	// 기간을 넓히면 그 뒤가 보이지 않았다.
	rows, err := d.Commerce.WebhookHistory(r.Context(), p.Since, p.Until,
		webhookPageSize+1, (page-1)*webhookPageSize)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	more := len(rows) > webhookPageSize
	if more {
		rows = rows[:webhookPageSize]
	}
	pager(data, "/admin/webhooks", withPeriod(nil, p), page, more)
	unprocessed := 0
	for _, row := range rows {
		if row.Status == "수신" {
			unprocessed++
		}
	}
	data["Rows"] = rows
	if unprocessed > 0 {
		// 자동 재처리를 두지 않기로 했으므로 (D50) 사람이 이것을 봐야 한다.
		data["Warning"] = "처리되지 않은 웹훅 " + strconv.Itoa(unprocessed) + "건이 있습니다."
	}
	d.Render(w, r, "admin/webhooks.html", http.StatusOK, data)
}
