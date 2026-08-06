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
