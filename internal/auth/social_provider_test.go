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
