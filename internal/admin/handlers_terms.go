package admin

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/emirue/ondolith/internal/commerce"
)

// TermsList is A-207 GET.
func (d *Deps) TermsList(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "settings.update"); !ok {
		return
	}
	d.renderTerms(w, r, http.StatusOK, "")
}

// TermsAdd is A-207 POST — **새 버전만 추가한다.**
//
// 기존 버전을 고치는 경로가 없다. `order_agreements` 가 가리키는 본문이 바뀌면
// 동의 이력이 거짓이 되고, FR-619 가 약속한 "나중에 재현된다" 가 깨진다.
func (d *Deps) TermsAdd(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "settings.update")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}

	// **기존 버전 수정 요청을 조용히 무시하지 않고 거부한다.** 무시하면
	// 운영자는 본문이 고쳐졌다고 믿는다.
	if r.PostForm.Has("id") || r.PostForm.Has("terms_id") {
		d.renderTerms(w, r, http.StatusUnprocessableEntity,
			"배포된 약관은 수정하지 않습니다. 새 버전을 추가하세요.")
		return
	}

	effective, err := time.Parse("2006-01-02", strings.TrimSpace(r.PostFormValue("effective_at")))
	if err != nil {
		d.renderTerms(w, r, http.StatusUnprocessableEntity,
			"시행일을 YYYY-MM-DD 로 입력하세요.")
		return
	}
	t := commerce.Terms{
		Kind:        strings.TrimSpace(r.PostFormValue("kind")),
		Version:     strings.TrimSpace(r.PostFormValue("version")),
		Body:        r.PostFormValue("body"),
		EffectiveAt: effective,
		Required:    r.PostFormValue("is_required") != "",
	}

	_, err = d.Commerce.AddTerms(r.Context(), t, time.Now())
	switch {
	case errors.Is(err, commerce.ErrTermsVersionTaken):
		d.renderTerms(w, r, http.StatusConflict, "이미 있는 종류·버전입니다.")
	case errors.Is(err, commerce.ErrTermsBackdated):
		d.renderTerms(w, r, http.StatusUnprocessableEntity,
			"시행일은 오늘 이후여야 합니다. 소급 시행은 동의 이력을 거짓으로 만듭니다.")
	case err != nil:
		d.renderTerms(w, r, http.StatusUnprocessableEntity, "저장하지 못했습니다: "+err.Error())
	default:
		d.log(r, c, "settings.update", "terms", t.Kind+" "+t.Version, "약관 버전 추가")
		http.Redirect(w, r, "/admin/terms", http.StatusSeeOther)
	}
}

func (d *Deps) renderTerms(w http.ResponseWriter, r *http.Request, code int, msg string) {
	list, err := d.Commerce.ListTerms(r.Context())
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	data := map[string]any{"Terms": list, "Error": msg}
	// 필수 약관이 0건인 것은 **거부 사유가 아니라 경고다** — 어떤 약관이
	// 법적으로 필수인지 우리가 알 수 없고, 설치 직후는 항상 0건이다.
	required := 0
	for _, t := range list {
		if t.Required {
			required++
		}
	}
	if required == 0 {
		data["Warning"] = "필수 약관이 없습니다. 결제 화면에서 받을 동의가 없습니다."
	}
	d.Render(w, r, "admin/terms.html", code, data)
}

// BusinessForm is A-208 GET.
func (d *Deps) BusinessForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "settings.update"); !ok {
		return
	}
	d.renderBusiness(w, r, http.StatusOK, "", nil)
}

// BusinessSave is A-208 POST.
//
// **미입력을 저장 거부 사유로 삼지 않는다** (D19 A-208). 설치 직후는 항상
// 비어 있고, 거부하면 여덟 항목을 다 채우기 전에는 아무것도 저장할 수 없다.
// `shop` 모드의 미입력은 경고다 (FR-711).
func (d *Deps) BusinessSave(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "settings.update")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	kv := map[string]string{}
	for _, k := range commerce.BusinessKeys {
		kv[k] = strings.TrimSpace(r.PostFormValue(k))
	}
	if err := d.Content.PutSettings(r.Context(), kv); err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.log(r, c, "settings.update", "settings", "business", "사업자 정보 수정")
	http.Redirect(w, r, "/admin/business", http.StatusSeeOther)
}

func (d *Deps) renderBusiness(w http.ResponseWriter, r *http.Request, code int,
	msg string, form map[string]string) {

	kv := form
	if kv == nil {
		stored, err := d.Content.Settings(r.Context(), commerce.BusinessKeys...)
		if err != nil {
			http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
			return
		}
		kv = stored
	}
	data := map[string]any{"Business": kv, "Keys": commerce.BusinessKeys,
		"Labels": commerce.BusinessLabels, "Error": msg}
	if missing := commerce.MissingBusinessKeys(kv); len(missing) > 0 {
		data["Warning"] = "shop 모드에서 표시 의무 항목이 비어 있습니다: " +
			strings.Join(missing, ", ")
	}
	d.Render(w, r, "admin/business.html", code, data)
}

// ---- A-209 결제 설정 -----------------------------------------------------------

// paymentSettingKeys are what A-209 writes.
//
// **PG 어댑터가 여럿이라는 것이 FR-605 의 전제**이지만 지금 등록된 것은 토스
// 하나다. 키를 `pg.` 접두사로 두는 이유가 그것이다 — 두 번째 어댑터가 붙으면
// `pg.provider` 가 어느 쪽을 쓸지 정하고, 키 이름은 그대로다.
var paymentSettingKeys = []string{
	"pg.provider", "pg.client_key", "pg.secret_key",
}

// pgProviders is the allow-list. 자유 문자열을 그대로 어댑터 선택에 쓰지
// 않는다 — 목록 밖 값은 "결제가 조용히 안 되는" 사이트를 만든다.
var pgProviders = map[string]string{"toss": "토스페이먼츠"}

// PaymentSettingsForm is A-209 GET.
func (d *Deps) PaymentSettingsForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "settings.update"); !ok {
		return
	}
	d.renderPayment(w, r, http.StatusOK, "")
}

// PaymentSettingsSave is A-209 POST.
//
// **시크릿 키는 화면으로 돌아오지 않는다** (D19 A-205 와 같은 규칙): 값을
// 다시 보내면 관리자 화면을 여는 것 자체가 자격증명 노출이 되고, "화면에서
// 가렸다" 는 "보낸 적 없다" 와 다르다.
func (d *Deps) PaymentSettingsSave(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "settings.update")
	if !ok {
		return
	}
	// **PG 자격증명은 결제를 통째로 가른다.** 바꿔치기하면 이후 모든 결제가
	// 공격자의 상점으로 간다 — 돈이 나가는 것은 아니지만 들어오는 돈이
	// 사라진다 (D15 5.3-1 이 재인증을 요구하는 것과 같은 종류의 위험이다).
	if !reauthOK(c, r) {
		d.renderPayment(w, r, http.StatusForbidden, "비밀번호를 다시 입력하세요.")
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}

	kv := map[string]string{}
	for _, k := range paymentSettingKeys {
		v := strings.TrimSpace(r.PostFormValue(k))
		// **길이 상한을 서버가 건다** (D19 A-209: 200자). `settings.value` 는
		// 무제한 text 라, HTML 의 maxlength 만으로는 스크립트 POST 를 막지
		// 못한다 — 100KB 짜리 키가 저장되고 이후 매 GET 마다 폼 속성으로
		// 다시 나간다.
		if len(v) > 200 {
			d.renderPayment(w, r, http.StatusUnprocessableEntity,
				"키가 너무 깁니다 (200자 이내).")
			return
		}
		if secretKeys[k] && v == "" {
			// 빈 시크릿은 "그대로 두라" 이지 "지우라" 가 아니다. 화면이 현재
			// 값을 보여줄 수 없으므로 빈 칸이 정상 상태다.
			continue
		}
		kv[k] = v
	}
	if p, ok := kv["pg.provider"]; ok && p != "" {
		if _, known := pgProviders[p]; !known {
			d.renderPayment(w, r, http.StatusUnprocessableEntity,
				"등록된 결제사가 아닙니다.")
			return
		}
	}
	if err := d.Content.PutSettings(r.Context(), kv); err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	// D15 7절: 자격증명 변경은 분쟁의 근거가 된다. **값은 남기지 않는다.**
	d.log(r, c, "settings.update", "settings", "payment", "결제 설정 변경")
	http.Redirect(w, r, "/admin/settings/payment", http.StatusSeeOther)
}

func (d *Deps) renderPayment(w http.ResponseWriter, r *http.Request, code int, msg string) {
	kv, err := d.Content.Settings(r.Context(), paymentSettingKeys...)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	shown := map[string]string{}
	saved := map[string]bool{}
	for k, v := range kv {
		if secretKeys[k] {
			// 설정 여부만. 값은 아니다.
			saved[k] = v != ""
			continue
		}
		shown[k] = v
	}
	data := map[string]any{"Settings": shown, "SecretSaved": saved,
		"Providers": pgProviders, "Error": msg}
	// 클라이언트 키만 있고 시크릿이 없으면 결제창은 뜨는데 승인이 실패한다 —
	// 구매자가 카드를 넣은 뒤에 실패하는 가장 나쁜 순서다.
	if shown["pg.client_key"] != "" && !saved["pg.secret_key"] {
		data["Warning"] = "시크릿 키가 없습니다. 결제창은 열리지만 승인이 실패합니다."
	}
	d.Render(w, r, "admin/payment.html", code, data)
}
