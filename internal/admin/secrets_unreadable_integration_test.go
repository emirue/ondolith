package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/secretbox"
)

func newBox(t *testing.T) *secretbox.Box {
	t.Helper()
	k, err := secretbox.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := secretbox.New(k)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// **열쇠가 바뀌어 저장된 비밀값을 열 수 없어도, 그 값을 다시 넣을 화면은 열린다**
// (D60 「저장된 자격증명」).
//
// D60 은 secret_key 를 잃으면 저장된 자격증명을 다시 입력하라고 한다. 그런데 네
// 설정 화면이 「저장됐는지」를 그리려고 값을 열다 실패해 500 이었다 — 다시 입력할
// 길이 막혀 있었다 (2026-10-20 개발 서버). 기존 테스트는 「열리지 않으면 오류」까지만
// 봤고 그 오류가 화면에 무엇을 하는지는 보지 않았다.
func TestUnreadableSecretStillLetsTheOperatorRetypeIt(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"settings.update": true},
		email: "op@example.com", superuser: true}
	d, _ := fixture(t, caller)
	ctx := context.Background()

	d.Content.UseSealer(newBox(t))
	if err := d.Content.PutSettings(ctx, map[string]string{
		"pg.provider": "toss", "pg.client_key": "test_ck_public", "pg.secret_key": "test_sk_OLD",
		"mail.smtp_password": "old-pw",
		"turnstile.site_key": "ts-site", "turnstile.secret_key": "ts-old",
		"social.naver.client_id": "cid", "social.naver.client_secret": "naver-old",
	}); err != nil {
		t.Fatal(err)
	}
	// 다른 secret_key 로 부팅한 것과 같다.
	d.Content.UseSealer(newBox(t))

	for _, tc := range []struct {
		screen string
		h      http.HandlerFunc
		path   string
	}{
		{"A-209", d.PaymentSettingsForm, "/admin/settings/payment"},
		{"A-205", d.MailSettingsForm, "/admin/settings/mail"},
		{"A-201", d.SettingsForm, "/admin/settings"},
		{"A-206", d.SocialSettingsForm, "/admin/settings/social"},
	} {
		var name, warning string
		d.Render = func(_ http.ResponseWriter, _ *http.Request, n string, _ int, data any) {
			name = n
			m, _ := data.(map[string]any)
			warning, _ = m["Warning"].(string)
		}
		tc.h(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, tc.path, nil))
		if name == "" || name == "admin/refusal.html" {
			t.Errorf("%s: 설정 화면 대신 %q — 다시 입력할 길이 막혔다", tc.screen, name)
			continue
		}
		if !strings.Contains(warning, "다시 입력") {
			t.Errorf("%s: 경고 %q — 왜 다시 넣어야 하는지 말하지 않는다", tc.screen, warning)
		}
	}

	// 다시 넣으면 새 열쇠로 봉인되어 읽힌다.
	d.Render = func(w http.ResponseWriter, _ *http.Request, _ string, code int, _ any) { w.WriteHeader(code) }
	rec := postAdmin(t, d.PaymentSettingsSave, "/admin/settings/payment", nil, url.Values{
		"pg.provider": {"toss"}, "pg.client_key": {"test_ck_public"}, "pg.secret_key": {"test_sk_NEW"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("다시 넣기 = HTTP %d", rec.Code)
	}
	kv, err := d.Content.Settings(ctx, "pg.secret_key")
	if err != nil || kv["pg.secret_key"] != "test_sk_NEW" {
		t.Fatalf("다시 넣은 시크릿 = %q, %v", kv["pg.secret_key"], err)
	}

	// **열 수 없는 값은 「저장됨」이 아니다.** 새 시크릿 없이 켜면 P-106 버튼이
	// 깨진다 — 저장된 시크릿이 없을 때와 같이 거부한다 (D19 A-206).
	rec = postAdmin(t, d.SocialSettingsSave, "/admin/settings/social", nil, url.Values{
		"provider": {"naver"}, "client_id": {"cid"}, "enabled": {"1"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("열 수 없는 시크릿으로 활성화 = HTTP %d, want 422", rec.Code)
	}
	rec = postAdmin(t, d.SocialSettingsSave, "/admin/settings/social", nil, url.Values{
		"provider": {"naver"}, "client_id": {"cid"}, "client_secret": {"naver-new"}, "enabled": {"1"}})
	if rec.Code != http.StatusSeeOther {
		t.Errorf("새 시크릿으로 활성화 = HTTP %d, want 303", rec.Code)
	}
}
