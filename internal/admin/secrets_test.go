package admin

import (
	"maps"
	"testing"
)

// 빈 비밀칸은 「그대로 두라」다 — 비밀이 아닌 빈 칸은 「비우라」 그대로다 (D19 A-205·A-206).
func TestDropBlankSecrets(t *testing.T) {
	kv := map[string]string{
		"mail.smtp_password": "", "social.kakao.client_secret": "",
		"pg.secret_key": "new", "mail.smtp_host": "",
	}
	dropBlankSecrets(kv)
	want := map[string]string{"pg.secret_key": "new", "mail.smtp_host": ""}
	if !maps.Equal(kv, want) {
		t.Errorf("= %v, want %v", kv, want)
	}
}

// 설정 화면에 비밀값은 설정됐는지만 나간다 — 값은 브라우저에 다시 가지 않는다.
func TestFormSettingsHideSecrets(t *testing.T) {
	shown, saved := formSettings(map[string]string{
		"pg.client_key": "ck", "pg.secret_key": "sk", "mail.smtp_password": "",
	})
	if !maps.Equal(shown, map[string]string{"pg.client_key": "ck"}) {
		t.Errorf("보이는 값 %v", shown)
	}
	if !maps.Equal(saved, map[string]bool{"pg.secret_key": true, "mail.smtp_password": false}) {
		t.Errorf("저장 여부 %v", saved)
	}
}
