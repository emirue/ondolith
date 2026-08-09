// Package httpsec holds the response headers that hold for **every** tree.
//
// 설치 트리와 운영 트리는 별개다 (D20) — 하나의 라우터에 `if installed` 를 넣지
// 않는다. 그러나 「프레임에 들어가지 않는다」는 양쪽 모두에 해당하고, **두 벌로
// 적으면 한쪽만 고쳐진다.** 설치 화면은 DB 비밀번호를 받는 폼이라 클릭재킹의
// 대가가 더 크지, 덜하지 않다.
package httpsec

import "net/http"

// Policy is the Content-Security-Policy every response carries.
//
// **테마가 인라인 스타일과 `onsubmit` 확인창을 쓴다.** 그것을 막으려면 테마
// 작성자마다 nonce 를 다루게 해야 하고, 그 부담은 테마를 재컴파일 없이 갈아
// 끼운다는 전제와 맞지 않는다 (DEC-3.1). 대신 **값이 확실한 것부터 잠근다**:
//
//   - `frame-ancestors 'none'` — 클릭재킹. CSRF 검사로는 막히지 않는다
//   - `object-src 'none'` — 플러그인 자체가 없다
//   - `base-uri 'self'` — `<base>` 주입으로 상대 주소를 통째로 돌리는 수법
//   - `form-action 'self'` — 폼이 남의 서버로 제출되지 않는다
//
// `default-src 'self'` 는 두지 않는다. 테마가 외부 폰트나 이미지를 쓰는 것은
// 정상 사용이고, 여기서 막으면 테마가 깨진 채로 배포된다.
const Policy = "frame-ancestors 'none'; object-src 'none'; " +
	"base-uri 'self'; form-action 'self'"

// Headers wraps h so that every response carries the policy above.
//
// **CSRF 검사만으로는 클릭재킹이 남는다.** `CrossOriginProtection` 은 요청이
// 어디서 왔는지를 보는데, 프레임 안에서 사용자가 진짜 버튼을 누르면 그 요청은
// 프레임 자신에게서 나온다 — same-origin 이라 그대로 통과한다. 프레임에 들어
// 가지 않는 것이 유일한 방어다.
func Headers(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		head := w.Header()
		// CSP 를 모르는 브라우저를 위해 함께 둔다 — 둘은 같은 것을 말한다.
		head.Set("X-Frame-Options", "DENY")
		head.Set("X-Content-Type-Options", "nosniff")
		// 재설정 토큰이 주소에 있다 (`/password/reset/{토큰}`). 교차 출처로는
		// 출처만 보내고 경로는 보내지 않는다.
		head.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		head.Set("Content-Security-Policy", Policy)
		h.ServeHTTP(w, r)
	})
}
