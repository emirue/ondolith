package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// **프록시 헤더는 루프백에서 온 연결에서만 믿는다** (config.ProxyIPHeader, D72).
//
// 같은 서버의 nginx 가 클라이언트 주소를 헤더에 덮어써 넘기면, 앱은 그 값을
// 클라이언트로 본다 — 그러지 않으면 모든 방문자가 127.0.0.1 이라 IP 별 제한이
// 사이트 전체 하나가 된다. 반대로 밖에서 곧장 들어온 요청이 그 헤더를 보내면
// 무시해야 한다: 믿으면 요청자가 자기 속도 제한 버킷을 고른다.
func TestProxyClientIPIsTrustedOnlyFromLoopback(t *testing.T) {
	cases := []struct {
		name, header, remote, value, want string
	}{
		{"프록시 뒤: 헤더를 클라이언트로", "X-Real-IP", "127.0.0.1:40000", "203.0.113.7", "203.0.113.7:0"},
		{"IPv6 루프백도 같다", "X-Real-IP", "[::1]:40000", "2001:db8::7", "[2001:db8::7]:0"},
		{"밖에서 직접: 헤더 무시", "X-Real-IP", "198.51.100.9:5555", "203.0.113.7", "198.51.100.9:5555"},
		{"설정 없음: 헤더 무시", "", "127.0.0.1:40000", "203.0.113.7", "127.0.0.1:40000"},
		{"값이 IP 가 아님: 무시", "X-Real-IP", "127.0.0.1:40000", "203.0.113.7, 10.0.0.1", "127.0.0.1:40000"},
		{"헤더 없음: 그대로", "X-Real-IP", "127.0.0.1:40000", "", "127.0.0.1:40000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got string
			h := withProxyClientIP(c.header)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got = r.RemoteAddr
			}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = c.remote
			if c.value != "" {
				req.Header.Set("X-Real-IP", c.value)
			}
			h.ServeHTTP(httptest.NewRecorder(), req)
			if got != c.want {
				t.Errorf("RemoteAddr = %q, want %q", got, c.want)
			}
		})
	}
}
