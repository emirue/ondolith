package auth

import (
	"net/http"
	"testing"
	"time"
)

func TestSocialProvidersBoundOutboundRequests(t *testing.T) {
	for _, name := range SocialProviderKeys() {
		p, err := NewSocialProvider(name, "id", "secret", "https://example.com/callback")
		if err != nil {
			t.Fatal(err)
		}
		client := p.(interface{ Client() *http.Client }).Client()
		if client.Timeout <= 0 || client.Timeout > 15*time.Second {
			t.Errorf("%s 의 외부 요청 시한 = %v — 응답하지 않는 서버가 로그인 요청을 붙든다", name, client.Timeout)
		}
	}
}

// 로그인 화면(켠 프로바이더)과 A-206 이 같은 값을 읽는다 — 두 곳이 따로 읽으면 「버튼은
// 있는데 눌러도 404」 가 된다. 시크릿은 값이 아니라 있는지만 나온다.
func TestSocialConfigFrom(t *testing.T) {
	kv := map[string]string{
		SocialSettingKey("kakao", "client_id"):     "kid",
		SocialSettingKey("kakao", "client_secret"): "s3cr3t",
		SocialSettingKey("kakao", "enabled"):       "1",
	}
	got := SocialConfigFrom("kakao", kv)
	want := SocialConfig{Provider: "kakao", Label: "카카오", ClientID: "kid", HasSecret: true, Enabled: true}
	if got != want {
		t.Errorf("= %+v, want %+v", got, want)
	}
	if got := SocialConfigFrom("google", nil); got != (SocialConfig{Provider: "google", Label: "구글"}) {
		t.Errorf("빈 설정 = %+v", got)
	}
}
