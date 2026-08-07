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
				ctx: r.Context(), auth: store, sm: sm,
				limiter: auth.NewLimiter(), limit: auth.DefaultLimits().ReauthAccount}
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

// **재인증에 계정당 5회/분 제한이 걸린다** (D15 4.3-2).
//
// 로그인과 같은 비밀번호 대조다. 없으면 훔친 세션 하나로 관리자 트리 상한
// (IP당 60회/분)까지 비밀번호를 시도하는 오라클이 된다 — 세션 안에서 로그인
// 제한을 그대로 우회한다.
func TestReauthIsRateLimitedPerAccount(t *testing.T) {
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

	late := func() time.Time { return time.Now().Add(2 * reauthWindow) }
	limiter := auth.NewLimiter()
	limit := auth.DefaultLimits().ReauthAccount

	// **D15 4.3-2 의 값을 코드에서 읽지 않고 직접 못박는다.** `limit.Burst` 로
	// 기대값을 만들면 상수를 바꿀 때 테스트가 함께 움직여 아무것도 지키지
	// 못한다 — 문서가 정한 숫자를 여기 적어야 그 문서가 지켜진다.
	const d15ReauthPerMinute = 5
	if limit.Burst != d15ReauthPerMinute || limit.Window != time.Minute {
		t.Fatalf("재인증 한도가 %d회/%v — D15 4.3-2 는 %d회/분이다",
			limit.Burst, limit.Window, d15ReauthPerMinute)
	}

	var wrongAccepted int
	var rightAfterBlock bool
	probe := sm.LoadAndSave(withActor(sm, store)(http.HandlerFunc(
		func(_ http.ResponseWriter, r *http.Request) {
			c := adminCaller{a: ActorFrom(r.Context()), now: late, ctx: r.Context(),
				auth: store, sm: sm, limiter: limiter, limit: limit}
			// 틀린 비밀번호를 한도 넘게 시도한다.
			for i := 1; i <= d15ReauthPerMinute+3; i++ {
				if c.ConfirmReauth("틀린 비밀번호") {
					wrongAccepted++
				}
			}
			// **한도를 넘기면 맞는 비밀번호도 통과하지 못한다.** 통과한다면
			// 제한이 시도 횟수를 세지 않는다는 뜻이다.
			rightAfterBlock = c.ConfirmReauth(password)
		})))

	rec := httptest.NewRecorder()
	sm.LoadAndSave(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		sm.Put(r.Context(), sessUserID, id)
		putTime(sm, r.Context(), sessAuthAt, time.Now())
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	probe.ServeHTTP(httptest.NewRecorder(), req)

	if wrongAccepted != 0 {
		t.Errorf("틀린 비밀번호가 %d번 통과했다", wrongAccepted)
	}
	if rightAfterBlock {
		t.Error("한도를 넘긴 뒤에도 맞는 비밀번호가 통과했다 — 제한이 시도를 세지 않는다")
	}

}

// **제한이 계정별이다** (D15 4.3-2). 전역 키면 한 사람이 모든 관리자의
// 재인증을 1분간 잠글 수 있다.
//
// 앞 검사와 **같은 limiter** 를 쓴다: 새 limiter 를 만들면 어느 키로 세든
// 통과하므로 스코프를 확인할 수 없다.
func TestReauthLimitIsScopedPerAccount(t *testing.T) {
	store, _, sm := authFixture(t)
	ctx := context.Background()
	const password = "correct horse battery"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	victim, err := store.CreateUser(ctx, "victim@example.com", hash, "피해자")
	if err != nil {
		t.Fatal(err)
	}
	attacker, err := store.CreateUser(ctx, "attacker@example.com", hash, "공격자")
	if err != nil {
		t.Fatal(err)
	}

	late := func() time.Time { return time.Now().Add(2 * reauthWindow) }
	limiter := auth.NewLimiter()
	limit := auth.DefaultLimits().ReauthAccount

	// 세션 하나를 만들고 그 안에서 두 계정을 번갈아 시험한다.
	run := func(userID string, fn func(c adminCaller)) {
		t.Helper()
		rec := httptest.NewRecorder()
		sm.LoadAndSave(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			sm.Put(r.Context(), sessUserID, userID)
			putTime(sm, r.Context(), sessAuthAt, time.Now())
		})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		for _, c := range rec.Result().Cookies() {
			req.AddCookie(c)
		}
		sm.LoadAndSave(withActor(sm, store)(http.HandlerFunc(
			func(_ http.ResponseWriter, r *http.Request) {
				fn(adminCaller{a: ActorFrom(r.Context()), now: late, ctx: r.Context(),
					auth: store, sm: sm, limiter: limiter, limit: limit})
			}))).ServeHTTP(httptest.NewRecorder(), req)
	}

	// 공격자가 자기 세션에서 한도를 다 쓴다.
	run(attacker, func(c adminCaller) {
		for range limit.Burst + 2 {
			c.ConfirmReauth("틀린 비밀번호")
		}
	})

	// **피해자는 영향받지 않아야 한다.** 전역 키면 여기서 막힌다.
	var victimOK bool
	run(victim, func(c adminCaller) { victimOK = c.ConfirmReauth(password) })
	if !victimOK {
		t.Error("다른 계정의 시도가 이 계정의 재인증을 막았다 — 제한이 계정별이 아니다")
	}

	// 공격자 자신은 여전히 막혀 있다 — 위 단언이 "제한이 아예 없다" 를 본
	// 것이 아니라는 것.
	var attackerOK bool
	run(attacker, func(c adminCaller) { attackerOK = c.ConfirmReauth(password) })
	if attackerOK {
		t.Error("한도를 다 쓴 계정이 통과했다")
	}
}
