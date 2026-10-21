package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/markbates/goth"
	"github.com/markbates/goth/providers/google"
	"golang.org/x/oauth2"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/content"
	"github.com/emirue/ondolith/internal/theme"
)

// fakeProvider stands in for a real OAuth2 provider.
//
// **네트워크를 타지 않는다.** 여기서 확인하려는 것은 프로바이더가 아니라 우리
// 분기다 — state 대조, 재사용 금지, 프로바이더 대조, 자동 연결 금지. 진짜
// 프로바이더를 부르면 그 분기들이 네트워크 상태에 가려진다.
type fakeProvider struct {
	name string
	uid  string
	// authorizeErr 는 프로바이더가 인가를 거부한 경우다.
	authorizeErr error
	// fetchErr 는 사용자 정보 조회가 실패한 경우다.
	fetchErr error
	// authorized 는 Authorize 가 몇 번 불렸는지다. state 검사가 그 앞에서
	// 막았는지 확인하는 데 쓴다.
	authorized *int
}

func (p fakeProvider) Name() string     { return p.name }
func (p fakeProvider) SetName(n string) {}
func (p fakeProvider) Debug(bool)       {}
func (p fakeProvider) BeginAuth(state string) (goth.Session, error) {
	return &fakeSession{state: state, provider: p}, nil
}
func (p fakeProvider) UnmarshalSession(s string) (goth.Session, error) {
	if s == "" {
		return nil, errors.New("빈 세션")
	}
	return &fakeSession{state: strings.TrimPrefix(s, "state="), provider: p}, nil
}
func (p fakeProvider) FetchUser(goth.Session) (goth.User, error) {
	if p.fetchErr != nil {
		return goth.User{}, p.fetchErr
	}
	return goth.User{Provider: p.name, UserID: p.uid, Email: p.uid + "@example.com"}, nil
}
func (p fakeProvider) RefreshTokenAvailable() bool { return false }
func (p fakeProvider) RefreshToken(string) (*oauth2.Token, error) {
	return nil, errors.New("미지원")
}

type fakeSession struct {
	state    string
	provider fakeProvider
}

func (s *fakeSession) GetAuthURL() (string, error) {
	return "https://provider.example.com/authorize?state=" + url.QueryEscape(s.state), nil
}
func (s *fakeSession) Marshal() string { return "state=" + s.state }
func (s *fakeSession) Authorize(goth.Provider, goth.Params) (string, error) {
	if s.provider.authorized != nil {
		*s.provider.authorized++
	}
	if s.provider.authorizeErr != nil {
		return "", s.provider.authorizeErr
	}
	return "access-token", nil
}

// socialFixture builds socialDeps over a real store and session manager.
//
// **운영이 세우는 것과 같은 배선이다** — 다른 것은 프로바이더 하나뿐이다.
func socialFixture(t *testing.T, p goth.Provider, enabled bool) (*socialDeps, *auth.Store, *scs.SessionManager) {
	t.Helper()
	store, pool, sm := authFixture(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// 내장 테마로 그린다 — 운영과 같은 템플릿이라야 "안내가 화면에 나온다" 를
	// 확인할 수 있다.
	loader := theme.New(theme.Builtin(), t.TempDir(), false,
		theme.FuncMap(theme.Deps{}))
	pub := &publicDeps{
		// 운영과 같이 채운다 — 뷰 모델이 메뉴를 읽으므로 비우면 nil 로 터진다.
		content: content.NewStore(pool),
		loader:  func() *theme.Loader { return loader },
		log:     log,
		site:    func() theme.Site { return theme.Site{Name: "테스트", Type: "cms"} },
	}
	// 비밀번호 재확인은 운영과 같은 loginDeps.confirmPassword 다 — 제한기까지.
	lg := &loginDeps{sm: sm, store: store, limiter: auth.NewLimiter(), limits: auth.DefaultLimits()}
	d := &socialDeps{publicDeps: pub, sm: sm, store: store, confirm: lg.confirmPassword,
		provider: func(_ *http.Request, name string) (goth.Provider, error) {
			// **꺼진 프로바이더는 없는 것과 같다** (FR-709).
			if !enabled {
				return nil, fmt.Errorf("%s 는 활성화되지 않았습니다", name)
			}
			// **두 프로바이더를 다 서비스한다.** 하나만 주면 "다른 프로바이더의
			// 콜백" 검사가 프로바이더 조회 실패로 통과해 버려서, 핸들러의
			// 대조가 무는지 알 수 없다.
			switch name {
			case p.Name():
				return p, nil
			case "kakao":
				return fakeProvider{name: "kakao", uid: "kakao-uid"}, nil
			}
			return nil, fmt.Errorf("등록되지 않은 프로바이더: %s", name)
		},
		enabled: func() []auth.SocialConfig {
			if !enabled {
				return nil
			}
			return []auth.SocialConfig{{Provider: p.Name(), Label: "테스트", Enabled: true}}
		}}
	return d, store, sm
}

// socialRun drives one request through the session middleware, carrying cookies.
func socialRun(t *testing.T, sm *scs.SessionManager, store *auth.Store, h http.HandlerFunc,
	req *http.Request, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	sm.LoadAndSave(withActor(sm, store)(h)).ServeHTTP(rec, req)
	return rec
}

// socialFailure 는 콜백이 실패로 끝났는지와 그 안내를 돌려준다.
//
// **실패는 콜백 주소에서 그리지 않는다** (D60 「비밀이 든 주소」). 그 주소에는 아직
// 교환하지 않은 인가 code 가 있고, 화면에 붙은 스크립트(GA4 의 page_location)가
// 주소째 밖으로 보낸다. 안내는 세션에 담겨 `/login`(연결 중이면 `/me/connections`)이
// 그린다 — 여기서는 그 세션에서 안내를 꺼내 본다.
func socialFailure(t *testing.T, sm *scs.SessionManager, store *auth.Store,
	rec *httptest.ResponseRecorder, cookies []*http.Cookie) (string, bool) {
	t.Helper()
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || (loc != loginPath && loc != "/me/connections") {
		return "", false
	}
	if next := rec.Result().Cookies(); len(next) > 0 {
		cookies = next
	}
	var msg string
	req := httptest.NewRequest(http.MethodGet, loc, nil)
	socialRun(t, sm, store, func(_ http.ResponseWriter, r *http.Request) {
		msg = sm.PopString(r.Context(), sessSocialError)
	}, req, cookies)
	return msg, msg != ""
}

// beginAndGetState starts P-106 and returns the session cookies plus the state
// the provider was handed.
func beginAndGetState(t *testing.T, d *socialDeps, sm *scs.SessionManager, store *auth.Store,
	provider string, cookies []*http.Cookie, form ...url.Values) ([]*http.Cookie, string) {
	t.Helper()
	var body url.Values
	if len(form) > 0 {
		body = form[0]
	}
	req := httptest.NewRequest(http.MethodPost, "/auth/"+provider, strings.NewReader(body.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("provider", provider)
	rec := socialRun(t, sm, store, d.socialBegin, req, cookies)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("P-106 = HTTP %d (%q)", rec.Code, rec.Body.String())
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	state := loc.Query().Get("state")
	if state == "" {
		t.Fatal("인가 URL 에 state 가 없다")
	}
	out := rec.Result().Cookies()
	if len(out) == 0 {
		out = cookies
	}
	return out, state
}

// **꺼진 프로바이더는 없는 것과 같다** (FR-709). 자격증명이 남아 있어도
// 시작할 수 없다 — 화면에서만 꺼지고 경로는 열려 있는 상태를 만들지 않는다.
func TestSocialBeginRefusesDisabledProvider(t *testing.T) {
	d, store, sm := socialFixture(t, fakeProvider{name: "google", uid: "u1"}, false)
	req := httptest.NewRequest(http.MethodPost, "/auth/google", nil)
	req.SetPathValue("provider", "google")
	rec := socialRun(t, sm, store, d.socialBegin, req, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("꺼진 프로바이더 = HTTP %d, want 404", rec.Code)
	}
}

// **state 가 없거나 다르면 즉시 중단한다.** P-107 은 GET 이라 CSRF 미들웨어가
// 통과시키므로, 이 대조가 방어의 전부다 (D12 P-107).
func TestSocialCallbackRefusesBadState(t *testing.T) {
	authorized := 0
	p := fakeProvider{name: "google", uid: "u1", authorized: &authorized}
	d, store, sm := socialFixture(t, p, true)

	ctx := context.Background()
	// **연결을 미리 만들어 둔다.** 없으면 state 를 통과해도 "연결된 계정
	// 없음" 으로 막혀서, state 검사가 무는지 알 수 없다.
	hash, err := auth.HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	uid, err := store.CreateUser(ctx, "a@example.com", hash, "A")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.LinkSocial(ctx, uid, "google", "u1"); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		// mangle 은 시작해서 받은 state 를 어떻게 망가뜨릴지다.
		mangle func(state string) (string, bool) // 값, 쿼리에 넣을지
	}{
		{"state 없음", func(string) (string, bool) { return "", false }},
		{"빈 state", func(string) (string, bool) { return "", true }},
		{"다른 state", func(s string) (string, bool) { return s + "tampered", true }},
		{"앞부분만 같은 state", func(s string) (string, bool) { return s[:len(s)-2], true }},
		{"뒷부분만 같은 state", func(s string) (string, bool) { return s[2:], true }},
	} {
		// **케이스마다 새로 시작한다.** 콜백 한 번이 세션의 state 를 지우므로,
		// 같은 세션을 재사용하면 두 번째부터는 「state 없음」만 시험하게 된다.
		cookies, state := beginAndGetState(t, d, sm, store, "google", nil)
		got, put := tc.mangle(state)
		target := "/auth/google/callback?code=x"
		if put {
			target += "&state=" + url.QueryEscape(got)
		}
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.SetPathValue("provider", "google")
		rec := socialRun(t, sm, store, d.socialCallback, req, cookies)
		msg, failed := socialFailure(t, sm, store, rec, cookies)
		if !failed {
			t.Errorf("%s 인데 실패로 끝나지 않았다: HTTP %d Location %q", tc.name, rec.Code, rec.Header().Get("Location"))
		}
		// **무엇이 틀렸는지 알려주지 않는다** — 알려주는 것이 곧 맞히는
		// 방법을 알려주는 것이다.
		if strings.Contains(msg+rec.Body.String(), "state") {
			t.Errorf("%s: 응답이 state 를 언급했다", tc.name)
		}
	}
	// **인가 교환까지 가지 않았다.** state 검사가 그 앞에 있어야 한다.
	if authorized != 0 {
		t.Errorf("state 가 틀렸는데 인가를 %d번 교환했다", authorized)
	}
}

// **한 번 쓴 state 는 버린다.** 남겨 두면 같은 콜백을 두 번 끝낼 수 있다.
func TestSocialCallbackStateIsSingleUse(t *testing.T) {
	p := fakeProvider{name: "google", uid: "u1"}
	d, store, sm := socialFixture(t, p, true)
	ctx := context.Background()

	hash, err := auth.HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	uid, err := store.CreateUser(ctx, "a@example.com", hash, "A")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.LinkSocial(ctx, uid, "google", "u1"); err != nil {
		t.Fatal(err)
	}

	cookies, state := beginAndGetState(t, d, sm, store, "google", nil)
	call := func(cs []*http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet,
			"/auth/google/callback?code=x&state="+url.QueryEscape(state), nil)
		req.SetPathValue("provider", "google")
		return socialRun(t, sm, store, d.socialCallback, req, cs)
	}

	first := call(cookies)
	if first.Code != http.StatusSeeOther {
		t.Fatalf("첫 콜백 = HTTP %d (%q)", first.Code, first.Body.String())
	}
	next := first.Result().Cookies()
	if len(next) == 0 {
		next = cookies
	}
	if _, failed := socialFailure(t, sm, store, call(next), next); !failed {
		t.Error("같은 state 로 두 번 로그인했다 — 한 번 쓴 state 는 버려야 한다")
	}
}

// 시작한 프로바이더와 끝내는 프로바이더가 같아야 한다. 다르면 한 프로바이더의
// 인가로 다른 프로바이더의 흐름을 마칠 수 있다.
func TestSocialCallbackRefusesDifferentProvider(t *testing.T) {
	p := fakeProvider{name: "google", uid: "u1"}
	d, store, sm := socialFixture(t, p, true)
	ctx := context.Background()

	// **카카오 쪽 연결을 미리 만들어 둔다.** 없으면 "연결된 계정 없음" 으로
	// 막혀서, 프로바이더 대조가 무는지 알 수 없다 — 가드를 지워도 통과한다.
	hash, err := auth.HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	uid, err := store.CreateUser(ctx, "k@example.com", hash, "K")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.LinkSocial(ctx, uid, "kakao", "kakao-uid"); err != nil {
		t.Fatal(err)
	}

	cookies, state := beginAndGetState(t, d, sm, store, "google", nil)
	req := httptest.NewRequest(http.MethodGet,
		"/auth/kakao/callback?code=x&state="+url.QueryEscape(state), nil)
	req.SetPathValue("provider", "kakao")
	rec := socialRun(t, sm, store, d.socialCallback, req, cookies)
	if _, failed := socialFailure(t, sm, store, rec, cookies); !failed {
		t.Error("구글로 시작한 흐름이 카카오 콜백으로 끝났다 — 프로바이더 대조가 없다")
	}
}

// **연결이 없으면 로그인되지 않는다** — 같은 이메일이어도 (D18 닫은 결정).
func TestSocialCallbackRefusesUnlinkedIdentity(t *testing.T) {
	p := fakeProvider{name: "google", uid: "u1"}
	d, store, sm := socialFixture(t, p, true)
	ctx := context.Background()

	// 프로바이더가 주는 이메일과 **같은 이메일**의 로컬 계정이 있다.
	hash, _ := auth.HashPassword("correct horse battery")
	if _, err := store.CreateUser(ctx, "u1@example.com", hash, "같은 이메일"); err != nil {
		t.Fatal(err)
	}

	cookies, state := beginAndGetState(t, d, sm, store, "google", nil)
	req := httptest.NewRequest(http.MethodGet,
		"/auth/google/callback?code=x&state="+url.QueryEscape(state), nil)
	req.SetPathValue("provider", "google")
	rec := socialRun(t, sm, store, d.socialCallback, req, cookies)

	msg, failed := socialFailure(t, sm, store, rec, cookies)
	if !failed {
		t.Fatal("같은 이메일의 로컬 계정에 자동으로 붙었다")
	}
	if !strings.Contains(msg, "연결된 계정이 없습니다") {
		t.Errorf("안내가 없다: %q", msg)
	}
}

// 정지된 계정은 소셜로도 들어올 수 없다.
func TestSocialCallbackRefusesDeactivatedAccount(t *testing.T) {
	p := fakeProvider{name: "google", uid: "u1"}
	d, store, sm := socialFixture(t, p, true)
	ctx := context.Background()
	hash, _ := auth.HashPassword("correct horse battery")
	uid, err := store.CreateUser(ctx, "off@example.com", hash, "정지")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.LinkSocial(ctx, uid, "google", "u1"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetActive(ctx, uid, false); err != nil {
		t.Fatal(err)
	}

	cookies, state := beginAndGetState(t, d, sm, store, "google", nil)
	req := httptest.NewRequest(http.MethodGet,
		"/auth/google/callback?code=x&state="+url.QueryEscape(state), nil)
	req.SetPathValue("provider", "google")
	if _, failed := socialFailure(t, sm, store, socialRun(t, sm, store, d.socialCallback, req, cookies), cookies); !failed {
		t.Error("정지된 계정이 소셜로 로그인했다")
	}
}

// 인증을 켠 사이트에서는 소셜 로그인도 미인증 계정을 들이지 않는다 (FR-214). 이 규칙
// 전에 미인증인 채로 연결해 둔 계정이 소셜로 인증을 우회하면 안 된다.
func TestSocialCallbackRefusesUnverifiedAccount(t *testing.T) {
	p := fakeProvider{name: "google", uid: "u1"}
	d, store, sm := socialFixture(t, p, true)
	d.unverified = func(_ context.Context, u *auth.User) (bool, bool) { return u.EmailVerifiedAt == nil, true }
	ctx := context.Background()
	hash, _ := auth.HashPassword("correct horse battery")
	uid, err := store.CreateUser(ctx, "nov@example.com", hash, "미인증")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.LinkSocial(ctx, uid, "google", "u1"); err != nil {
		t.Fatal(err)
	}
	callback := func() (int, bool) {
		cookies, state := beginAndGetState(t, d, sm, store, "google", nil)
		req := httptest.NewRequest(http.MethodGet,
			"/auth/google/callback?code=x&state="+url.QueryEscape(state), nil)
		req.SetPathValue("provider", "google")
		rec := socialRun(t, sm, store, d.socialCallback, req, cookies)
		_, failed := socialFailure(t, sm, store, rec, cookies)
		return rec.Code, failed
	}
	if _, failed := callback(); !failed {
		t.Error("미인증 계정이 소셜로 로그인했다")
	}
	if err := store.MarkEmailVerified(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if code, failed := callback(); code != http.StatusSeeOther || failed {
		t.Errorf("인증한 뒤 소셜 로그인 = HTTP %d 실패=%v, want 303 — 위 단언이 「늘 거부」였다", code, failed)
	}
}

// 프로바이더가 인가를 거부하거나 사용자 조회가 실패하면 로그인되지 않는다.
func TestSocialCallbackRefusesProviderFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    fakeProvider
	}{
		{"인가 거부", fakeProvider{name: "google", uid: "u1", authorizeErr: errors.New("denied")}},
		{"조회 실패", fakeProvider{name: "google", uid: "u1", fetchErr: errors.New("boom")}},
		{"uid 없음", fakeProvider{name: "google", uid: ""}},
	} {
		d, store, sm := socialFixture(t, tc.p, true)
		ctx := context.Background()
		// **빈 uid 로 연결된 행을 심어 둔다.** 프로바이더 조회가 실패하면
		// goth.User 가 제로값이라 uid 가 "" 인데, 그 값을 그대로 조회에 쓰면
		// 이 행과 맞아 **아무나 로그인된다.** 가드가 없으면 여기서 드러난다.
		hash, err := auth.HashPassword("correct horse battery")
		if err != nil {
			t.Fatal(err)
		}
		uid, err := store.CreateUser(ctx, "empty@example.com", hash, "빈 uid")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.LinkSocial(ctx, uid, "google", ""); err != nil {
			t.Fatal(err)
		}

		cookies, state := beginAndGetState(t, d, sm, store, "google", nil)
		req := httptest.NewRequest(http.MethodGet,
			"/auth/google/callback?code=x&state="+url.QueryEscape(state), nil)
		req.SetPathValue("provider", "google")
		if _, failed := socialFailure(t, sm, store, socialRun(t, sm, store, d.socialCallback, req, cookies), cookies); !failed {
			t.Errorf("%s 인데 로그인됐다", tc.name)
		}
	}
}

// **로그인한 사람이 시작하면 「연결」이다** (P-111). 같은 콜백을 쓰되 끝이 다르다.
func TestSocialCallbackLinksWhenStartedWhileLoggedIn(t *testing.T) {
	p := fakeProvider{name: "google", uid: "u1"}
	d, store, sm := socialFixture(t, p, true)
	ctx := context.Background()
	hash, _ := auth.HashPassword("correct horse battery")
	uid, err := store.CreateUser(ctx, "me@example.com", hash, "나")
	if err != nil {
		t.Fatal(err)
	}

	// 로그인 세션을 만든다.
	rec := httptest.NewRecorder()
	sm.LoadAndSave(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		sm.Put(r.Context(), sessUserID, uid)
		stampAuthAt(sm, r.Context(), loginStamp(t, store))
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	cookies := rec.Result().Cookies()

	cookies2, state := beginAndGetState(t, d, sm, store, "google", cookies,
		url.Values{"password": {"correct horse battery"}})
	req := httptest.NewRequest(http.MethodGet,
		"/auth/google/callback?code=x&state="+url.QueryEscape(state), nil)
	req.SetPathValue("provider", "google")
	got := socialRun(t, sm, store, d.socialCallback, req, append(cookies, cookies2...))
	if got.Code != http.StatusSeeOther {
		t.Fatalf("연결 = HTTP %d (%q)", got.Code, got.Body.String())
	}
	if loc := got.Header().Get("Location"); loc != "/me/connections" {
		t.Errorf("연결 뒤 이동 %q, want /me/connections", loc)
	}
	links, err := store.SocialAccounts(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].Provider != "google" {
		t.Errorf("연결이 만들어지지 않았다: %+v", links)
	}
}

// P-111 의 오류 매핑: 마지막 로그인 수단은 422, 연결 없음은 404.
func TestConnectionsActionMapsErrors(t *testing.T) {
	p := fakeProvider{name: "google", uid: "u1"}
	d, store, sm := socialFixture(t, p, true)
	ctx := context.Background()
	hash, _ := auth.HashPassword("correct horse battery")
	uid, err := store.CreateUser(ctx, "me@example.com", hash, "나")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.LinkSocial(ctx, uid, "google", "u1"); err != nil {
		t.Fatal(err)
	}

	login := httptest.NewRecorder()
	sm.LoadAndSave(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		sm.Put(r.Context(), sessUserID, uid)
		stampAuthAt(sm, r.Context(), loginStamp(t, store))
	})).ServeHTTP(login, httptest.NewRequest(http.MethodGet, "/", nil))
	cookies := login.Result().Cookies()

	post := func(provider string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/me/connections",
			strings.NewReader(url.Values{"provider": {provider}, "password": {"correct horse battery"}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return socialRun(t, sm, store, d.connectionsAction, req, cookies)
	}

	// 연결되지 않은 프로바이더 → 404.
	if rec := post("kakao"); rec.Code != http.StatusNotFound {
		t.Errorf("없는 연결 = HTTP %d, want 404", rec.Code)
	}
	// 비밀번호가 있으므로 마지막 소셜도 뗄 수 있다.
	if rec := post("google"); rec.Code != http.StatusSeeOther {
		t.Errorf("해제 = HTTP %d, want 303 (%q)", rec.Code, rec.Body.String())
	}
}

// 로그인하지 않았으면 연결 관리 화면이 없다 (SC-3).
func TestConnectionsNeedsLogin(t *testing.T) {
	p := fakeProvider{name: "google", uid: "u1"}
	d, store, sm := socialFixture(t, p, true)
	for _, h := range []http.HandlerFunc{d.connectionsPage, d.connectionsAction} {
		req := httptest.NewRequest(http.MethodGet, "/me/connections", nil)
		if rec := socialRun(t, sm, store, h, req, nil); rec.Code != http.StatusNotFound {
			t.Errorf("비로그인 = HTTP %d, want 404", rec.Code)
		}
	}
}

// 실제 goth 프로바이더도 우리 인터페이스를 만족한다 — 페이크만 통과하는
// 검사가 되지 않도록 한 번 확인한다.
func TestRealProviderSatisfiesTheInterface(t *testing.T) {
	var p goth.Provider = google.New("id", "secret", "https://x/auth/google/callback", "email")
	if p.Name() != "google" {
		t.Errorf("Name() = %q", p.Name())
	}
	sess, err := p.BeginAuth("state-123")
	if err != nil {
		t.Fatal(err)
	}
	u, err := sess.GetAuthURL()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u, "state-123") {
		t.Errorf("인가 URL 에 state 가 없다: %s", u)
	}
}

// 콜백이 세션에 담은 안내는 `/login` 이 **한 번** 그린다 — 다시 열면 사라진다.
func TestLoginShowsTheSocialFailureOnce(t *testing.T) {
	d, h, _, _ := accountFixture(t, false)
	seed := httptest.NewRecorder()
	d.sm.LoadAndSave(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		d.sm.Put(r.Context(), sessSocialError, "연결된 계정이 없습니다.")
	})).ServeHTTP(seed, httptest.NewRequest(http.MethodGet, "/", nil))
	c := sessionCookie(seed)
	if c == nil {
		t.Fatal("세션 쿠키가 없다")
	}
	open := func() string {
		req := httptest.NewRequest(http.MethodGet, loginPath, nil)
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("/login HTTP %d", rec.Code)
		}
		return rec.Body.String()
	}
	if got := open(); !strings.Contains(got, "|연결된 계정이 없습니다.") {
		t.Errorf("안내가 그려지지 않았다: %q", got)
	}
	if got := open(); strings.Contains(got, "|") {
		t.Errorf("안내가 두 번 그려졌다: %q", got)
	}
}

// loggedIn 은 uid 로 로그인한 세션 쿠키다.
func loggedIn(t *testing.T, sm *scs.SessionManager, store *auth.Store, uid string) []*http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	sm.LoadAndSave(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		sm.Put(r.Context(), sessUserID, uid)
		stampAuthAt(sm, r.Context(), loginStamp(t, store))
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec.Result().Cookies()
}

func formPost(target, provider string, form url.Values) *http.Request {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if provider != "" {
		req.SetPathValue("provider", provider)
	}
	return req
}

// **P-111 연결은 비밀번호를 다시 받는다** (D15 5.3-1). 열린 채 떠난 세션 하나로 자기
// 소셜 계정을 붙이면, 피해자가 비밀번호를 바꿔도 그 연결로 계속 들어온다 (2026-10-21 점검).
func TestSocialLinkAsksForThePassword(t *testing.T) {
	d, store, sm := socialFixture(t, fakeProvider{name: "google", uid: "u1"}, true)
	hash, _ := auth.HashPassword("correct horse battery")
	uid, err := store.CreateUser(context.Background(), "me@example.com", hash, "나")
	if err != nil {
		t.Fatal(err)
	}
	cookies := loggedIn(t, sm, store, uid)
	start := func(form url.Values) *httptest.ResponseRecorder {
		return socialRun(t, sm, store, d.socialBegin, formPost("/auth/google", "google", form), cookies)
	}
	for _, form := range []url.Values{{}, {"password": {"not the password"}}} {
		if rec := start(form); rec.Code != http.StatusBadRequest {
			t.Errorf("비밀번호 %q 로 연결 시작 = HTTP %d (Location %q), want 400",
				form.Get("password"), rec.Code, rec.Header().Get("Location"))
		}
	}
	rec := start(url.Values{"password": {"correct horse battery"}})
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "https://provider.example.com/") {
		t.Errorf("맞는 비밀번호 = HTTP %d Location %q", rec.Code, rec.Header().Get("Location"))
	}
}

// 해제도 비밀번호를 다시 받는다 (D15 5.3-1 「P-111 소셜 해제」).
func TestSocialUnlinkAsksForThePassword(t *testing.T) {
	d, store, sm := socialFixture(t, fakeProvider{name: "google", uid: "u1"}, true)
	ctx := context.Background()
	hash, _ := auth.HashPassword("correct horse battery")
	uid, err := store.CreateUser(ctx, "me@example.com", hash, "나")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.LinkSocial(ctx, uid, "google", "u1"); err != nil {
		t.Fatal(err)
	}
	cookies := loggedIn(t, sm, store, uid)
	unlink := func(form url.Values) int {
		return socialRun(t, sm, store, d.connectionsAction, formPost("/me/connections", "", form), cookies).Code
	}
	if code := unlink(url.Values{"provider": {"google"}}); code != http.StatusBadRequest {
		t.Errorf("비밀번호 없이 해제 = HTTP %d, want 400", code)
	}
	if links, _ := store.SocialAccounts(ctx, uid); len(links) != 1 {
		t.Fatalf("거부했는데 연결이 사라졌다: %+v", links)
	}
	if code := unlink(url.Values{"provider": {"google"}, "password": {"correct horse battery"}}); code != http.StatusSeeOther {
		t.Errorf("맞는 비밀번호로 해제 = HTTP %d, want 303", code)
	}
}

// 비밀번호가 없는 계정은 받을 비밀번호가 없다 — 방금(15분 안) 로그인했어야 한다.
func TestPasswordlessAccountLinksOnlyRightAfterLogin(t *testing.T) {
	d, store, sm := socialFixture(t, fakeProvider{name: "google", uid: "u1"}, true)
	ctx := context.Background()
	uid, err := store.CreateUser(ctx, "s@example.com", "", "소셜만")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.LinkSocial(ctx, uid, "kakao", "kakao-uid"); err != nil {
		t.Fatal(err)
	}
	cookies := loggedIn(t, sm, store, uid)
	start := func() int {
		return socialRun(t, sm, store, d.socialBegin, formPost("/auth/google", "google", url.Values{}), cookies).Code
	}
	if code := start(); code != http.StatusSeeOther {
		t.Errorf("방금 로그인한 계정 = HTTP %d, want 303", code)
	}
	d.now = func() time.Time { return time.Now().Add(20 * time.Minute) }
	if code := start(); code != http.StatusBadRequest {
		t.Errorf("로그인 20분 뒤 = HTTP %d, want 400 — 다시 로그인해야 한다", code)
	}
}
