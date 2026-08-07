package admin

import (
	"net/http"
	"strings"

	"github.com/emirue/ondolith/internal/auth"
)

// SocialSettingsForm is A-206 GET.
func (d *Deps) SocialSettingsForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "settings.update"); !ok {
		return
	}
	d.renderSocial(w, r, http.StatusOK, "")
}

// SocialSettingsSave is A-206 POST.
//
// **콜백 URL 을 받지 않는다** (D19 A-206 받지 않는 필드). 서버가 계산해 표시만
// 한다 — 입력받으면 인가 코드를 공격자 서버로 돌리는 경로가 된다. 필드가 실려
// 오면 무시하지 않고 거부한다: 무시는 조용해서 공격 시도를 놓친다.
func (d *Deps) SocialSettingsSave(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "settings.update")
	if !ok {
		return
	}
	// 자격증명 교체는 로그인 경로를 통째로 가른다 — 남의 프로바이더 앱으로
	// 돌리면 그 앱이 우리 사용자의 로그인을 받는다 (D15 5.3-1).
	if !reauthOK(c, r) {
		d.renderSocial(w, r, http.StatusForbidden, "비밀번호를 다시 입력하세요.")
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	for _, k := range []string{"callback_url", "callback", "redirect_uri",
		"authorize_url", "token_url", "scope"} {
		if r.PostForm.Has(k) {
			d.renderSocial(w, r, http.StatusUnprocessableEntity,
				"콜백 URL·엔드포인트·scope 는 서버가 정합니다. 입력할 수 없습니다.")
			return
		}
	}

	provider := strings.TrimSpace(r.PostFormValue("provider"))
	if _, known := auth.SocialProviders[provider]; !known {
		d.renderSocial(w, r, http.StatusUnprocessableEntity,
			"지원하지 않는 프로바이더입니다.")
		return
	}

	idKey := auth.SocialSettingKey(provider, "client_id")
	secretKey := auth.SocialSettingKey(provider, "client_secret")
	enabledKey := auth.SocialSettingKey(provider, "enabled")

	stored, err := d.Content.Settings(r.Context(), idKey, secretKey)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}

	clientID := strings.TrimSpace(r.PostFormValue("client_id"))
	secret := strings.TrimSpace(r.PostFormValue("client_secret"))
	enabled := r.PostFormValue("enabled") != ""

	if len(clientID) > 400 || len(secret) > 400 {
		d.renderSocial(w, r, http.StatusUnprocessableEntity, "키가 너무 깁니다 (400자 이내).")
		return
	}

	kv := map[string]string{idKey: clientID}
	if secret != "" {
		// 빈 칸은 「그대로 두라」다. 화면이 현재 값을 보여줄 수 없으므로 빈
		// 칸이 정상 상태이고, 지우면 다른 항목을 고치러 온 관리자가 로그인을
		// 꺼뜨린다 (D19 A-206).
		kv[secretKey] = secret
	}

	// **자격증명 없이 활성화할 수 없다** (D19 A-206 거부 조건). 켜면 P-106
	// 버튼이 즉시 깨지고, 사용자는 로그인이 고장 난 것으로 읽는다.
	haveSecret := secret != "" || stored[secretKey] != ""
	if enabled && (clientID == "" || !haveSecret) {
		d.renderSocial(w, r, http.StatusUnprocessableEntity,
			"키를 먼저 입력하세요. 자격증명 없이 활성화할 수 없습니다.")
		return
	}
	kv[enabledKey] = ""
	if enabled {
		kv[enabledKey] = "1"
	}

	if err := d.Content.PutSettings(r.Context(), kv); err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	// D15 7절: 프로바이더 키 변경은 작업 로그 대상이다. **secret 값은 넣지 않는다.**
	d.log(r, c, "settings.update", "settings", "social."+provider,
		"소셜 로그인 설정 변경: "+provider)
	http.Redirect(w, r, "/admin/settings/social", http.StatusSeeOther)
}

func (d *Deps) renderSocial(w http.ResponseWriter, r *http.Request, code int, msg string) {
	var keys []string
	for _, p := range auth.SocialProviderKeys() {
		keys = append(keys,
			auth.SocialSettingKey(p, "client_id"),
			auth.SocialSettingKey(p, "client_secret"),
			auth.SocialSettingKey(p, "enabled"))
	}
	stored, err := d.Content.Settings(r.Context(), keys...)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}

	base := ""
	if d.BaseURL != nil {
		base = d.BaseURL(r)
	}
	configs := make([]auth.SocialConfig, 0, len(auth.SocialProviders))
	for _, p := range auth.SocialProviderKeys() {
		configs = append(configs, auth.SocialConfig{
			Provider: p,
			Label:    auth.SocialProviders[p],
			ClientID: stored[auth.SocialSettingKey(p, "client_id")],
			// **값이 아니라 있는지 여부만.** 시크릿은 저장 후 어떤 화면에도
			// 다시 오지 않는다.
			HasSecret: stored[auth.SocialSettingKey(p, "client_secret")] != "",
			Enabled:   stored[auth.SocialSettingKey(p, "enabled")] != "",
		})
	}
	// 콜백 URL 은 **표시만** 한다. 관리자가 이 값을 프로바이더 콘솔에 붙여넣는
	// 것이 연동의 전부다.
	callbacks := map[string]string{}
	for _, p := range auth.SocialProviderKeys() {
		callbacks[p] = auth.SocialCallbackURL(base, p)
	}
	d.Render(w, r, "admin/social.html", code, map[string]any{
		"Providers": configs, "Callbacks": callbacks, "Error": msg,
	})
}
