package admin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/auth"
)

// **A-215 문자(SMS)** (D42 9절) — 메일(A-205)과 같은 모양이다:
//   - 보기는 settings.update, 저장은 최고 관리자 + 재확인(자격증명 — 이후 모든 인증번호 문자의 경로다)
//   - 시크릿 키는 화면에 다시 나오지 않고, 비워 저장하면 그대로 남는다. 작업 로그에 값이 없다
//   - 서비스 ID·발신번호는 형식을 본다(서비스 ID 는 API 경로에 들어간다)
//   - 시험 발송은 최고 관리자가 넣은 번호로 한 통, 계정당 시간 5통. 결과를 그 화면에 그린다
func TestSMSSettingsScreen(t *testing.T) {
	op := &fakeCaller{perms: map[string]bool{"settings.update": true}, email: "op@example.com"}
	d, pool := fixture(t, op)
	ctx := context.Background()
	var got map[string]any
	var code int
	d.Render = func(w http.ResponseWriter, _ *http.Request, _ string, c int, data any) {
		got, code = data.(map[string]any), c
		w.WriteHeader(c)
	}
	var sent [][2]string
	var sendErr error
	d.SendSMSTest = func(_ context.Context, to, text string) error {
		sent = append(sent, [2]string{to, text})
		return sendErr
	}
	d.SMSPhoneLogin = true
	d.Limiter = auth.NewLimiter()
	saved := func() map[string]string {
		kv, err := d.Content.Settings(ctx, SMSSettingKeys...)
		if err != nil {
			t.Fatal(err)
		}
		return kv
	}
	good := url.Values{"sms.sens.access_key": {"AKEY"}, "sms.sens.secret_key": {"SKEY-zzz"},
		"sms.sens.service_id": {"ncp:sms:kr:123456789012:ondo"}, "sms.sens.from": {"0212345678"}}

	// 문자 로그인을 쓰는 실행 파일인데 설정이 비었다 — 화면이 말한다.
	if rec := get(d.SMSSettingsForm, "/admin/settings/sms"); rec.Code != 200 || !strings.Contains(got["Warning"].(string), "인증번호") {
		t.Fatalf("빈 설정의 보기 = HTTP %d, 경고 %q", rec.Code, got["Warning"])
	}
	// 운영자는 보기만.
	if rec := post(d.SMSSettingsSave, "/admin/settings/sms", good); rec.Code != http.StatusForbidden || saved()["sms.sens.access_key"] != "" {
		t.Fatalf("운영자의 저장 = HTTP %d", rec.Code)
	}
	if rec := post(d.SMSTest, "/admin/settings/sms/test", url.Values{"phone": {"01012345678"}}); rec.Code != http.StatusForbidden || len(sent) != 0 {
		t.Fatalf("운영자의 시험 발송 = HTTP %d", rec.Code)
	}

	su := &fakeCaller{superuser: true, reauth: true, email: "su@example.com", password: "correct horse battery"}
	d.Caller = func(*http.Request) Caller { return su }
	if rec := post(d.SMSSettingsSave, "/admin/settings/sms", good); rec.Code != http.StatusForbidden {
		t.Fatalf("재확인 없는 저장 = HTTP %d, want 403", rec.Code)
	}
	withPW := func(v url.Values) url.Values {
		out := url.Values{"password": {"correct horse battery"}}
		for k, vs := range v {
			out[k] = vs
		}
		return out
	}
	for name, bad := range map[string][2]string{
		"서비스 ID 에 /": {"sms.sens.service_id", "ncp:sms:kr:1:a/../x"},
		"발신번호에 글자":   {"sms.sens.from", "02-1234-5678"},
	} {
		v := withPW(good)
		v.Set(bad[0], bad[1])
		su.reauth = true
		if rec := post(d.SMSSettingsSave, "/admin/settings/sms", v); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s = HTTP %d, want 422", name, rec.Code)
		}
	}
	su.reauth = true
	if rec := post(d.SMSSettingsSave, "/admin/settings/sms", withPW(good)); rec.Code != http.StatusSeeOther {
		t.Fatalf("저장 = HTTP %d", rec.Code)
	}
	wantNotice(t, d, "문자(SMS) 설정을 저장했습니다.")
	if kv := saved(); kv["sms.sens.secret_key"] != "SKEY-zzz" || kv["sms.sens.service_id"] != "ncp:sms:kr:123456789012:ondo" {
		t.Fatalf("저장된 값 = %v", kv)
	}
	// 시크릿은 비워 저장하면 남는다.
	v := withPW(good)
	v.Set("sms.sens.secret_key", "")
	v.Set("sms.sens.from", "0311112222")
	if rec := post(d.SMSSettingsSave, "/admin/settings/sms", v); rec.Code != http.StatusSeeOther || saved()["sms.sens.secret_key"] != "SKEY-zzz" {
		t.Fatalf("시크릿을 비운 저장 = HTTP %d, 시크릿 %q", rec.Code, saved()["sms.sens.secret_key"])
	}
	takeNotice(t, d)
	if rec := get(d.SMSSettingsForm, "/admin/settings/sms"); rec.Code != 200 {
		t.Fatalf("보기 = HTTP %d", rec.Code)
	}
	if shown := got["Settings"].(map[string]string); shown["sms.sens.secret_key"] != "" || shown["sms.sens.access_key"] != "AKEY" {
		t.Errorf("화면 값 = %v — 시크릿은 다시 나오지 않는다", shown)
	}
	if w, _ := got["Warning"].(string); w != "" {
		t.Errorf("설정이 찼는데 경고 %q", w)
	}
	var leaked int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operation_logs WHERE summary LIKE '%SKEY%' OR summary LIKE '%AKEY%'`).Scan(&leaked); err != nil || leaked != 0 {
		t.Errorf("작업 로그에 키 값이 있다 (%d)", leaked)
	}

	// 시험 발송: 번호를 보고, 보내고, 결과를 그 화면에 그린다. 재확인은 묻지 않는다(저장된 설정으로 한 통).
	su.reauth = true
	if rec := post(d.SMSTest, "/admin/settings/sms/test", url.Values{"phone": {"02-123-4567"}}); rec.Code != http.StatusUnprocessableEntity || len(sent) != 0 {
		t.Errorf("휴대폰 번호가 아님 = HTTP %d", rec.Code)
	}
	if rec := post(d.SMSTest, "/admin/settings/sms/test", url.Values{"phone": {"010-1234-5678"}}); rec.Code != 200 ||
		len(sent) != 1 || sent[0][0] != "01012345678" || !strings.Contains(got["Notice"].(string), "010****5678") {
		t.Fatalf("시험 발송 = HTTP %d, 보낸 것 %v, 알림 %v", rec.Code, sent, got["Notice"])
	}
	sendErr = errors.New("sms: SENS 가 접수하지 않았다 — HTTP 401")
	if rec := post(d.SMSTest, "/admin/settings/sms/test", url.Values{"phone": {"01012345678"}}); rec.Code != 200 ||
		!strings.Contains(got["Error"].(string), "HTTP 401") {
		t.Errorf("실패한 시험 발송 = HTTP %d, 오류 %v", rec.Code, got["Error"])
	}
	sendErr = nil
	for i := 0; i < 3; i++ {
		post(d.SMSTest, "/admin/settings/sms/test", url.Values{"phone": {"01012345678"}})
	}
	if rec := post(d.SMSTest, "/admin/settings/sms/test", url.Values{"phone": {"01012345678"}}); rec.Code != http.StatusTooManyRequests {
		t.Errorf("시간당 6번째 = HTTP %d, want 429", rec.Code)
	}
	if len(sent) != 5 {
		t.Errorf("보낸 문자 %d통, want 5", len(sent))
	}
	_ = code
}
