package httpsec

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// **값을 통째로 못 박는다.** 이 패키지의 값은 전부 보안 통제이고, 각각 왜 그
// 값이어야 하는지가 headers.go 주석에 한 문단씩 적혀 있다. 그런데 여기 검사가
// 없어서 `app` 쪽 단언이 대신 봐 왔고, 그 단언은 「비어 있지 않다」·「이 문자열이
// 들어 있다」 수준이라 `Referrer-Policy: unsafe-url` 도 `form-action` 삭제도
// 전부 통과시켰다 (실제로 지워 보고 확인했다). 완화는 값을 바꾸는 것으로만
// 일어나므로, 값을 그대로 적어 두면 완화가 반드시 이 파일을 지난다.
func TestHeadersAreExact(t *testing.T) {
	want := map[string]string{
		"X-Frame-Options":         "DENY",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "strict-origin-when-cross-origin",
		"Content-Security-Policy": Policy,
	}

	rec := httptest.NewRecorder()
	Headers(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, 기대 %q", k, got, v)
		}
	}
	// 새 헤더가 늘면 이 검사도 같이 늘어야 한다 — 그러지 않으면 새 값은
	// 아무도 안 본 채로 나간다.
	if n := len(rec.Header()); n != len(want) {
		t.Errorf("헤더 %d 개인데 검사는 %d 개만 안다: %v", n, len(want), rec.Header())
	}
}

// **지시어를 하나씩 센다.** Policy 를 통째로 비교하면 위 검사가 잡지만, 어느
// 지시어가 왜 있는지는 실패 메시지에 남지 않는다. 여기서 하나씩 이름을 부르면
// 지운 사람이 무엇을 지웠는지 그 자리에서 읽는다.
func TestPolicyKeepsEveryDirective(t *testing.T) {
	for _, d := range []struct{ directive, why string }{
		{"frame-ancestors 'none'", "클릭재킹 — CSRF 검사로는 막히지 않는다"},
		{"object-src 'none'", "플러그인 자체가 없다"},
		{"base-uri 'self'", "<base> 주입으로 상대 주소를 통째로 돌리는 수법"},
		{"form-action 'self'", "폼이 남의 서버로 제출되지 않는다"},
	} {
		if !strings.Contains(Policy, d.directive) {
			t.Errorf("CSP 에 %q 가 없다 — %s", d.directive, d.why)
		}
	}
}

// **미들웨어가 감싼 핸들러를 부르지 않으면 헤더만 붙고 사이트는 빈 화면이다.**
// 위 두 검사는 헤더만 보므로 h 를 호출하지 않아도 전부 통과한다.
func TestHeadersCallsWrappedHandler(t *testing.T) {
	called := false
	rec := httptest.NewRecorder()
	Headers(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		_, _ = w.Write([]byte("본문"))
	})).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if !called {
		t.Fatal("감싼 핸들러가 불리지 않았다")
	}
	if rec.Body.String() != "본문" {
		t.Errorf("본문 = %q, 기대 %q", rec.Body.String(), "본문")
	}
}
