package admin

import (
	"net/http"
	"strings"
	"time"

	"github.com/emirue/ondolith/internal/applogin"
	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/sms"
)

// A-215 「문자(SMS)」 (D42 9절) — 사이트 앱의 문자 인증 로그인이 인증번호를 보내는 네이버 클라우드 SENS 설정.
// 메일(A-205)과 같은 모양이다: 저장은 최고 관리자 + 재확인, 시크릿은 봉인되고 다시 보이지 않는다.
const (
	smsAccessKey = "sms.sens.access_key"
	smsSecretKey = "sms.sens.secret_key" // content.IsSecretSetting — 봉인
	smsServiceID = "sms.sens.service_id"
	smsFrom      = "sms.sens.from"
	smsScreen    = "/admin/settings/sms"
)

// SMSSettingKeys are A-215's settings.
var SMSSettingKeys = []string{smsAccessKey, smsSecretKey, smsServiceID, smsFrom}

// smsTestPerHour is A-215's test send per account — 넣은 번호로 보내는 길이라 메일 시험 발송과 같은 선이다.
var smsTestPerHour = auth.Limit{Burst: 5, Window: time.Hour}

// SMSConfigured reports whether every SENS field is filled.
func SMSConfigured(kv map[string]string) bool {
	for _, k := range SMSSettingKeys {
		if strings.TrimSpace(kv[k]) == "" {
			return false
		}
	}
	return true
}

// SENSFrom is the sender the saved settings describe.
func SENSFrom(kv map[string]string) sms.SENS {
	return sms.SENS{AccessKey: kv[smsAccessKey], SecretKey: kv[smsSecretKey], ServiceID: kv[smsServiceID], From: kv[smsFrom]}
}

// smsSettingsProblem is D19 A-215's format check; 빈 값은 받는다(설정을 지우는 저장).
func smsSettingsProblem(kv map[string]string) string {
	if v := kv[smsServiceID]; v != "" && !sms.ValidServiceID(v) {
		return "서비스 ID 는 SENS 콘솔의 값 그대로 넣으세요 (ncp:sms:kr:…:…)."
	}
	if v := kv[smsFrom]; v != "" && !sms.ValidNumber(v) {
		return "발신번호는 숫자만 넣으세요 — SENS 콘솔에 등록한 번호입니다."
	}
	return ""
}

func (d *Deps) renderSMS(w http.ResponseWriter, r *http.Request, code int, msg, notice string) {
	d.renderSettingsData(w, r, "admin/sms.html", SMSSettingKeys, code, msg, notice, func(kv map[string]string, data map[string]any) {
		data["PhoneLogin"], data["CustomSender"] = d.SMSPhoneLogin, d.SMSCustom
		// 문자 로그인을 켠 실행 파일인데 보낼 길이 없다 — 앱의 인증번호 요청은 늘 받았다고 답하므로 여기가 아니면 모른다.
		if d.SMSPhoneLogin && !d.SMSCustom && !SMSConfigured(kv) {
			w, _ := data["Warning"].(string)
			data["Warning"] = strings.TrimSpace(w + " 설정이 비어 있어 앱의 인증번호 문자가 나가지 않습니다.")
		}
	})
}

// SMSSettingsForm is A-215's GET.
func (d *Deps) SMSSettingsForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "settings.update"); !ok {
		return
	}
	d.renderSMS(w, r, http.StatusOK, "", "")
}

// SMSSettingsSave is A-215's write — 최고 관리자 + 재확인 (D15 5.3-1). 이후 모든 인증번호 문자의 경로다.
func (d *Deps) SMSSettingsSave(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "settings.update")
	if !ok {
		return
	}
	if !c.IsSuperuser() {
		d.renderSMS(w, r, http.StatusForbidden, "문자(SMS) 설정은 최고 관리자만 바꿀 수 있습니다.", "")
		return
	}
	if !reauthOK(c, r) {
		d.renderSMS(w, r, http.StatusForbidden, "비밀번호를 다시 입력하세요.", "")
		return
	}
	kv := map[string]string{}
	for _, k := range SMSSettingKeys {
		kv[k] = strings.TrimSpace(r.PostFormValue(k))
	}
	dropBlankSecrets(kv)
	if msg := smsSettingsProblem(kv); msg != "" {
		d.renderSMS(w, r, http.StatusUnprocessableEntity, msg, "")
		return
	}
	if err := d.Content.PutSettings(r.Context(), kv); err != nil {
		d.serverError(w, r, err)
		return
	}
	// 값은 남기지 않는다 — 키가 섞여 있고 로그는 지워지지 않는다 (D15 7절).
	d.log(r, c, "settings.update", "settings", "sms", "문자(SMS) 설정 변경")
	d.done(w, r, c, smsScreen, "문자(SMS) 설정을 저장했습니다.")
}

// SMSTest sends one test message to the number the superuser typed and shows SENS's answer.
// 재확인은 묻지 않는다 — 저장된 설정으로, 정한 문구 한 통, 계정당 시간 5통이다 (메일 시험 발송과 같다).
func (d *Deps) SMSTest(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "settings.update")
	if !ok {
		return
	}
	if !c.IsSuperuser() {
		d.renderSMS(w, r, http.StatusForbidden, "문자(SMS) 설정은 최고 관리자만 바꿀 수 있습니다.", "")
		return
	}
	if d.SendSMSTest == nil {
		d.refuse(w, r, http.StatusNotFound, "시험 발송을 쓸 수 없습니다.")
		return
	}
	phone, ok := applogin.NormalizePhone(r.PostFormValue("phone"))
	if !ok {
		d.renderSMS(w, r, http.StatusUnprocessableEntity, "휴대폰 번호를 넣으세요.", "")
		return
	}
	if d.Limiter != nil && !d.Limiter.Allow("sms-test:acct:"+c.Email(), smsTestPerHour) {
		w.Header().Set("Retry-After", "3600")
		d.renderSMS(w, r, http.StatusTooManyRequests, "시험 발송은 한 시간에 5번까지입니다. 잠시 후 다시 시도하세요.", "")
		return
	}
	if err := d.SendSMSTest(r.Context(), phone, "[문자 설정 시험] 관리자 화면에서 보낸 시험 문자입니다."); err != nil {
		reason := err.Error()
		if len([]rune(reason)) > 200 {
			reason = string([]rune(reason)[:200]) + "…"
		}
		d.renderSMS(w, r, http.StatusOK, "발송에 실패했습니다: "+reason, "")
		return
	}
	d.renderSMS(w, r, http.StatusOK, "", sms.Mask(phone)+" 로 시험 문자를 보냈습니다 — SENS 가 접수했습니다. 도착은 휴대폰에서 확인하세요.")
}
