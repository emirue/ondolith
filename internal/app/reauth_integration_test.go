package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/emirue/ondolith/internal/auth"
)

// **재인증 안내는 실제로 만족될 수 있어야 한다** (D15 5.3-1).
//
// D19 C7 은 별도 재인증 화면을 두지 않고 "대상 화면의 폼에 비밀번호 칸이
// 나타난다"고 했다. 그 칸을 읽는 곳이 없으면 안내는 장식이고, 로그인 후 15분이
// 지난 운영자는 칸을 채워도 계속 403 을 받는다 — 로그아웃했다 다시 들어오기
// 전에는 환불이 불가능해진다.
//
// admin 패키지의 stub 이 아니라 **운영이 세우는 adminCaller** 로 확인한다.
// stub 에서 재인증 플래그를 Go 코드로 내리는 것은, 운영 코드가 만들 수 없는
// 상태를 시험하는 것이다.
func TestAdminReauthWindowCanBeReopenedWithThePassword(t *testing.T) {
	store, _, sm := authFixture(t)
	ctx := context.Background()
	const password = "correct horse battery"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.CreateUser(ctx, "op@example.com", hash, "운영자")
	if err != nil {
		t.Fatal(err)
	}

	// 로그인은 지금 하고, 판정 시각만 창 밖으로 민다.
	late := func() time.Time { return time.Now().Add(2 * reauthWindow) }

	var needBefore, wrongAccepted, rightAccepted bool
	probe := sm.LoadAndSave(withActor(sm, store)(http.HandlerFunc(
		func(_ http.ResponseWriter, r *http.Request) {
			c := adminCaller{a: ActorFrom(r.Context()), now: late,
				ctx: r.Context(), auth: store, sm: sm}
			needBefore = c.NeedsReauth()
			wrongAccepted = c.ConfirmReauth("틀린 비밀번호")
			rightAccepted = c.ConfirmReauth(password)
		})))

	// 세션을 만든다 (로그인과 같은 값).
	rec := httptest.NewRecorder()
	sm.LoadAndSave(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		sm.Put(r.Context(), sessUserID, id)
		putTime(sm, r.Context(), sessAuthAt, time.Now())
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	cookies := rec.Result().Cookies()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	out := httptest.NewRecorder()
	probe.ServeHTTP(out, req)

	if !needBefore {
		t.Fatal("15분이 지났는데 재인증을 요구하지 않았다")
	}
	if wrongAccepted {
		t.Error("틀린 비밀번호가 재인증을 통과시켰다")
	}
	if !rightAccepted {
		t.Fatal("맞는 비밀번호인데 재인증이 거부됐다 — 안내가 장식이다")
	}

	// **도장이 세션에 남는다.** 남지 않으면 다음 요청에서 또 물어보고,
	// 한 화면에서 두 번 연속 처리하려면 매번 비밀번호를 친다.
	var needAfter bool
	after := sm.LoadAndSave(withActor(sm, store)(http.HandlerFunc(
		func(_ http.ResponseWriter, r *http.Request) {
			needAfter = adminCaller{a: ActorFrom(r.Context()), now: late}.NeedsReauth()
		})))
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range out.Result().Cookies() {
		req2.AddCookie(c)
	}
	for _, c := range cookies {
		req2.AddCookie(c)
	}
	after.ServeHTTP(httptest.NewRecorder(), req2)
	if needAfter {
		t.Error("재인증에 성공했는데 다음 요청에서 또 요구한다 — 도장이 남지 않았다")
	}
}
