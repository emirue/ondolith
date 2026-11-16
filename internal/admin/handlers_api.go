package admin

import (
	"errors"
	"net/http"

	"github.com/emirue/ondolith/internal/apikeys"
)

// SettingAPIEnabled turns /api/site/ on (A-213, D42 6절). 끄면 사이트 API 전체가 404 다.
const SettingAPIEnabled = "api.enabled"

const apiScreen = "/admin/settings/api"

// APISettingsForm is A-213 GET: 켜짐, 이 실행 파일의 사이트 API, 앱 키 목록.
func (d *Deps) APISettingsForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "settings.update"); !ok {
		return
	}
	d.renderAPI(w, r, http.StatusOK, "", "")
}

// renderAPI draws A-213. issued is a key just made — 그 응답에만 실린다 (D42 6절).
func (d *Deps) renderAPI(w http.ResponseWriter, r *http.Request, code int, msg, issued string) {
	kv, err := d.Content.Settings(r.Context(), SettingAPIEnabled)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	keys, err := d.APIKeys.List(r.Context())
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	data := map[string]any{"Enabled": kv[SettingAPIEnabled] == "1", "Routes": d.SiteAPI, "Keys": keys, "Error": msg}
	if issued != "" {
		data["Issued"] = issued
	}
	d.Render(w, r, "admin/api.html", code, data)
}

// apiSuperuser is A-213's gate for every change. 키는 외부 앱이 코어 데이터에 닿는 문이라 메일·저장소
// 설정처럼 최고 관리자만 다룬다 (D15 5.1).
func (d *Deps) apiSuperuser(w http.ResponseWriter, r *http.Request) (Caller, bool) {
	c, ok := d.require(w, r, "settings.update")
	if !ok {
		return nil, false
	}
	if !c.IsSuperuser() {
		d.renderAPI(w, r, http.StatusForbidden, "커스텀 API 는 최고 관리자만 바꿀 수 있습니다.", "")
		return nil, false
	}
	return c, true
}

// APIToggle is A-213 POST: 켜기·끄기. 되돌릴 수 있어 비밀번호를 묻지 않는다 (FR-726).
func (d *Deps) APIToggle(w http.ResponseWriter, r *http.Request) {
	c, ok := d.apiSuperuser(w, r)
	if !ok {
		return
	}
	on := r.PostFormValue("on") == "1"
	v, state, notice := "", "꺼짐", "커스텀 API 를 껐습니다 — /api/site/ 는 모두 404 입니다."
	if on {
		v, state, notice = "1", "켜짐", "커스텀 API 를 켰습니다 — 발급한 앱 키로 /api/site/ 를 부를 수 있습니다."
	}
	if err := d.Content.PutSettings(r.Context(), map[string]string{SettingAPIEnabled: v}); err != nil {
		d.serverError(w, r, err)
		return
	}
	d.log(r, c, "settings.update", "settings", "api", "커스텀 API "+state)
	d.done(w, r, c, apiScreen, notice)
}

// APIKeyIssue is A-213 POST /keys: 키를 만들어 **이 응답에만** 보인다. 재확인 (D15 5.3-1 자격증명).
//
// done 으로 돌아가지 않는다(D19 C11 의 예외) — 알림은 세션(DB 의 sessions 표)에 평문으로 남고 그 표는 백업에도
// 실린다. 키 원문은 이 화면과 받아 적는 사람에게만 있어야 한다. 응답은 저장되지 않게 no-store.
func (d *Deps) APIKeyIssue(w http.ResponseWriter, r *http.Request) {
	c, ok := d.apiSuperuser(w, r)
	if !ok {
		return
	}
	if !reauthOK(c, r) {
		d.renderAPI(w, r, http.StatusForbidden, "비밀번호를 다시 입력하세요.", "")
		return
	}
	var by *string
	if id := c.UserID(); id != "" {
		by = &id
	}
	raw, k, err := d.APIKeys.Issue(r.Context(), r.PostFormValue("name"), by)
	if errors.Is(err, apikeys.ErrName) {
		d.renderAPI(w, r, http.StatusUnprocessableEntity, "키 이름은 1~60자로 넣으세요 (예: flutter-android).", "")
		return
	}
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	d.log(r, c, "settings.update", "api_key", k.ID, "앱 키 발급: "+k.Name)
	w.Header().Set("Cache-Control", "no-store")
	d.renderAPI(w, r, http.StatusOK, "", raw)
}

// APIKeyRevoke is A-213 POST /keys/revoke. 재확인 (D15 5.3-1). 폐기한 키로 오는 요청은 바로 401 이다.
func (d *Deps) APIKeyRevoke(w http.ResponseWriter, r *http.Request) {
	c, ok := d.apiSuperuser(w, r)
	if !ok {
		return
	}
	if !reauthOK(c, r) {
		d.renderAPI(w, r, http.StatusForbidden, "비밀번호를 다시 입력하세요.", "")
		return
	}
	id := r.PostFormValue("id")
	name, err := d.APIKeys.Revoke(r.Context(), id)
	if errors.Is(err, apikeys.ErrNotFound) {
		d.renderAPI(w, r, http.StatusNotFound, "그 키가 없거나 이미 폐기됐습니다.", "")
		return
	}
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	d.log(r, c, "settings.update", "api_key", id, "앱 키 폐기: "+name)
	d.done(w, r, c, apiScreen, "앱 키 「"+name+"」를 폐기했습니다 — 그 키로 오는 요청은 401 입니다.")
}
