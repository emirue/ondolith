package admin

import (
	"net/http"
	"strconv"

	"github.com/emirue/ondolith/internal/commerce"
)

// 커머스 정책 키 (A-512). 값의 단일 출처는 `settings` 이고, 읽는 쪽은
// 그때그때 읽는다 — 부팅 시점에 잡으면 "바꾸려면 재시작" 이 된다.
const (
	settingReturnDays  = "order.return_days"
	settingConfirmDays = "order.confirm_days"
)

// policyDefaults are D19 A-512's defaults.
var policyDefaults = map[string]string{
	settingReturnDays:               "7",
	settingConfirmDays:              "8",
	commerce.SettingReturnFeePolicy: commerce.FeePolicyDeduct,
	commerce.SettingReturnFeeAmount: "0",
	// 0 원·기준 없음 — 이 화면이 생기기 전 모든 사이트가 그렇게 계산해 왔다.
	commerce.SettingShippingFlatFee:       "0",
	commerce.SettingShippingFreeThreshold: "0",
}

var policyKeys = []string{settingReturnDays, settingConfirmDays,
	commerce.SettingReturnFeePolicy, commerce.SettingReturnFeeAmount,
	commerce.SettingShippingFlatFee, commerce.SettingShippingFreeThreshold}

// PolicyForm is A-512 GET.
func (d *Deps) PolicyForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "settings.update"); !ok {
		return
	}
	d.renderPolicy(w, r, http.StatusOK, "", nil)
}

// PolicySave is A-512 POST.
//
// **소급하지 않는다.** 이미 접수된 반품 건의 환불액은 달라지지 않는다 —
// 부담 방식·금액은 A-511 의 수거 확인 시점에 `returns` 로 복사되기 때문이다
// (D19 A-512 「성공 후」). 참조로 뒀다면 정책 변경이 곧 과거 환불액 변경이다.
func (d *Deps) PolicySave(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "settings.update")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		d.refuse(w, r, http.StatusBadRequest, "잘못된 요청입니다.")
		return
	}

	kv := map[string]string{}
	for _, k := range policyKeys {
		kv[k] = r.PostFormValue(k)
	}

	returnDays, err := strconv.Atoi(kv[settingReturnDays])
	if err != nil || returnDays < 1 || returnDays > 365 {
		d.renderPolicy(w, r, http.StatusUnprocessableEntity,
			"반품 기간은 1~365일입니다.", kv)
		return
	}
	confirmDays, err := strconv.Atoi(kv[settingConfirmDays])
	if err != nil || confirmDays < 1 {
		d.renderPolicy(w, r, http.StatusUnprocessableEntity,
			"구매확정 기간은 1일 이상입니다.", kv)
		return
	}
	// **구매확정이 반품보다 길어야 한다.** 같거나 짧으면 구매확정이 먼저
	// 닫혀서, 반품 기간이 남아 있는데 반품을 걸 수 없는 주문이 생긴다 —
	// 상태머신은 `배송완료` 에서만 반품을 받는다 (FR-604, FR-617).
	if confirmDays <= returnDays {
		d.renderPolicy(w, r, http.StatusUnprocessableEntity,
			"구매확정 기간은 반품 기간보다 길어야 합니다.", kv)
		return
	}
	if p := kv[commerce.SettingReturnFeePolicy]; p != commerce.FeePolicyDeduct &&
		p != commerce.FeePolicySeparate {
		d.renderPolicy(w, r, http.StatusUnprocessableEntity,
			"배송비 부담 방식은 차감 또는 별도청구입니다.", kv)
		return
	}
	fee, err := strconv.Atoi(kv[commerce.SettingReturnFeeAmount])
	if err != nil || fee < 0 {
		d.renderPolicy(w, r, http.StatusUnprocessableEntity,
			"배송비는 0 이상의 정수입니다.", kv)
		return
	}
	// 주문 배송비 (D50). 음수가 새면 Shipping.Fee 가 모든 주문을 거부하고, 금액 상한을
	// 넘으면 그 뒤 모든 주문이 상한에 걸린다 — 저장 단계에서 막는다.
	if n, err := strconv.Atoi(kv[commerce.SettingShippingFlatFee]); err != nil || n < 0 || n > commerce.MaxAmount {
		d.renderPolicy(w, r, http.StatusUnprocessableEntity,
			"주문 배송비는 0원부터 20억 원까지의 정수입니다.", kv)
		return
	}
	if n, err := strconv.Atoi(kv[commerce.SettingShippingFreeThreshold]); err != nil || n < 0 || n > commerce.MaxAmount {
		d.renderPolicy(w, r, http.StatusUnprocessableEntity,
			"무료배송 기준액은 0원부터 20억 원까지의 정수입니다.", kv)
		return
	}

	if err := d.Content.PutSettings(r.Context(), kv); err != nil {
		d.refuse(w, r, http.StatusInternalServerError, "일시적인 오류입니다.")
		return
	}
	// D15 7절: 정책 변경은 이후 모든 반품의 환불액을 가른다.
	d.log(r, c, "settings.update", "settings", "commerce.policy", "커머스 정책 변경")
	http.Redirect(w, r, "/admin/commerce/policy", http.StatusSeeOther)
}

// renderPolicy draws the form. 거부했을 때는 **보낸 값을 그대로 되돌려준다** —
// 저장된 값으로 다시 그리면 운영자가 무엇을 고쳐야 하는지 볼 수 없다.
func (d *Deps) renderPolicy(w http.ResponseWriter, r *http.Request, code int,
	msg string, form map[string]string) {

	kv := form
	if kv == nil {
		stored, err := d.Content.Settings(r.Context(), policyKeys...)
		if err != nil {
			d.refuse(w, r, http.StatusInternalServerError, "일시적인 오류입니다.")
			return
		}
		kv = map[string]string{}
		for _, k := range policyKeys {
			if v := stored[k]; v != "" {
				kv[k] = v
			} else {
				kv[k] = policyDefaults[k]
			}
		}
	}
	data := map[string]any{"Policy": kv}
	if msg != "" {
		data["Error"] = msg
	}
	d.Render(w, r, "admin/policy.html", code, data)
}
