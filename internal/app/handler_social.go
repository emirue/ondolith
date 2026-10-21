package app

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/markbates/goth"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/theme"
)

// 소셜 로그인 흐름이 쓰는 세션 키.
//
// **`gothic` 을 쓰지 않는다.** 그 패키지는 gorilla/sessions 를 쓰는데 우리
// 세션은 scs 다 (NFR-204) — 두 세션 스토어를 한 요청에 두면 어느 쪽이 진짜
// 로그인 상태인지가 코드마다 달라진다. goth 코어만 쓰고 상태는 여기 둔다.
const (
	// sessOAuthState 는 CSRF 방어의 전부다. P-107 은 GET 이면서 상태를 바꾸고
	// `CrossOriginProtection` 이 GET 을 통과시키므로 (D12 P-107), 이 대조가
	// 실패하면 즉시 중단한다.
	sessOAuthState = "oauth_state"
	// sessOAuthSession 은 goth 가 요구하는 프로바이더 세션 문자열이다.
	sessOAuthSession = "oauth_session"
	// sessOAuthProvider 는 콜백이 어느 프로바이더의 것인지다. 경로에서만
	// 읽으면 다른 프로바이더의 콜백으로 시작 요청을 마칠 수 있다.
	sessOAuthProvider = "oauth_provider"
	// sessOAuthLink 는 「로그인」이 아니라 「연결」로 시작했다는 표시다
	// (P-111). 같은 콜백을 쓰되 끝이 다르다.
	sessOAuthLink = "oauth_link"
	// sessSocialError 는 콜백이 실패했을 때 다음 화면이 한 번 그릴 안내다. 콜백
	// 주소에서는 그리지 않는다 — 아직 교환하지 않은 인가 code 가 주소에 있고, 화면에
	// 붙은 스크립트(GA4 의 page_location)가 주소째 밖으로 보낸다 (D60 「비밀이 든 주소」).
	sessSocialError = "social_error"
)

// socialDeps serves P-106, P-107 and P-111.
type socialDeps struct {
	*publicDeps
	sm    *scs.SessionManager
	store *auth.Store
	// provider 는 요청마다 설정을 읽어 만든다 — A-206 이 키를 바꾸면 재시작
	// 없이 반영돼야 한다 (FR-303).
	provider func(r *http.Request, name string) (goth.Provider, error)
	// enabled 는 A-206 에서 켠 프로바이더다. 화면이 버튼을 그리는 데 쓴다.
	enabled func() []auth.SocialConfig
	// unverified 는 로그인과 같은 판정이다 (loginDeps.unverified, FR-214).
	unverified func(ctx context.Context, u *auth.User) (refused, sent bool)
	// confirm 은 P-111 의 비밀번호 재확인이다 (loginDeps.confirmPassword, D15 5.3-1).
	confirm func(ctx context.Context, a *Actor, password string) bool
	// now 는 비밀번호 없는 계정의 재인증 창을 잰다. nil 이면 time.Now.
	now func() time.Time
}

// oauthState makes an unguessable state value.
func oauthState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// socialBegin is P-106 POST /auth/{provider}.
//
// **POST 다.** 시작이 상태를 바꾸므로(세션에 state 를 심는다) GET 이면 P5
// 위반이고, 링크 프리페치가 로그인 흐름을 시작시킨다.
func (d *socialDeps) socialBegin(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("provider")
	p, err := d.provider(r, name)
	if err != nil {
		// 설정되지 않은 프로바이더는 **404 다.** 어떤 프로바이더가 있는지
		// 알려주지 않는다 (D14 2절).
		d.notFound(w, r)
		return
	}

	// 로그인한 사람이 시작하면 「연결」이다 (P-111). 연결은 본인 확인을 먼저 한다 —
	// state 를 심기 전에.
	ctx := r.Context()
	link := ActorFrom(ctx).IsAuthenticated()
	if link && !d.ownerConfirmed(w, r) {
		return
	}

	state, err := oauthState()
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	sess, err := p.BeginAuth(state)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	authURL, err := sess.GetAuthURL()
	if err != nil {
		d.serverError(w, r, err)
		return
	}

	d.sm.Put(ctx, sessOAuthState, state)
	d.sm.Put(ctx, sessOAuthSession, sess.Marshal())
	d.sm.Put(ctx, sessOAuthProvider, name)
	d.sm.Put(ctx, sessOAuthLink, link)

	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

// socialCallback is P-107 GET /auth/{provider}/callback.
//
// **GET 이면서 상태를 바꾸는 지점이다.** `CrossOriginProtection` 이 GET 을
// 통과시키므로 방어가 전적으로 `state` 대조에 걸려 있다 (D12 P-107).
func (d *socialDeps) socialCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := r.PathValue("provider")

	want := d.sm.GetString(ctx, sessOAuthState)
	marshalled := d.sm.GetString(ctx, sessOAuthSession)
	started := d.sm.GetString(ctx, sessOAuthProvider)
	link := d.sm.GetBool(ctx, sessOAuthLink)
	// 한 번 쓰면 버린다. 남겨 두면 같은 state 로 두 번 끝낼 수 있다.
	d.clearOAuth(ctx)

	// **state 가 없거나 다르면 즉시 중단한다.** 이유를 화면에 적지 않는다 —
	// 무엇이 틀렸는지 알려주는 것이 곧 맞히는 방법을 알려주는 것이다.
	got := r.URL.Query().Get("state")
	if want == "" || got == "" || !subtleEqual(want, got) {
		d.socialFail(w, r, "로그인을 완료하지 못했습니다. 다시 시도해 주세요.")
		return
	}
	// 시작한 프로바이더와 끝내는 프로바이더가 같아야 한다.
	if started == "" || started != name {
		d.socialFail(w, r, "로그인을 완료하지 못했습니다. 다시 시도해 주세요.")
		return
	}

	p, err := d.provider(r, name)
	if err != nil {
		// 시작한 뒤 A-206 에서 꺼졌다. 404 화면을 여기서 그리면 아직 교환하지 않은
		// code 가 든 주소에서 그리게 된다 (sessSocialError).
		d.socialFail(w, r, "로그인을 완료하지 못했습니다. 다시 시도해 주세요.")
		return
	}
	sess, err := p.UnmarshalSession(marshalled)
	if err != nil {
		d.socialFail(w, r, "로그인을 완료하지 못했습니다. 다시 시도해 주세요.")
		return
	}
	if _, err := sess.Authorize(p, r.URL.Query()); err != nil {
		d.socialFail(w, r, "로그인을 완료하지 못했습니다. 다시 시도해 주세요.")
		return
	}
	gu, err := p.FetchUser(sess)
	if err != nil || gu.UserID == "" {
		d.socialFail(w, r, "로그인을 완료하지 못했습니다. 다시 시도해 주세요.")
		return
	}

	if link {
		d.finishLink(w, r, name, gu.UserID)
		return
	}
	d.finishSocialLogin(w, r, name, gu)
}

// finishSocialLogin logs in an already-linked account, or explains why not.
//
// **같은 이메일의 로컬 계정에 자동으로 붙이지 않는다** (D18 닫은 결정).
// 프로바이더가 이메일 소유를 확인해 줬더라도 마찬가지다 — 자동 연결을
// 허용하면 프로바이더 계정 하나를 뚫는 것이 곧 우리 계정을 뚫는 것이 된다.
func (d *socialDeps) finishSocialLogin(w http.ResponseWriter, r *http.Request,
	provider string, gu goth.User) {

	ctx := r.Context()
	u, err := d.store.UserBySocial(ctx, provider, gu.UserID)
	if errors.Is(err, auth.ErrNoUser) {
		d.socialFail(w, r, "연결된 계정이 없습니다. 로그인 후 「연결된 계정」에서 연결하세요.")
		return
	}
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	// FR-214: 소셜로도 미인증 계정은 들이지 않는다 — 이 규칙 전에 미인증인 채로
	// 연결해 둔 계정이 소셜로 인증을 우회하면 안 된다.
	if d.unverified != nil {
		if refused, _ := d.unverified(ctx, u); refused {
			d.socialFail(w, r, "이메일 인증이 끝나지 않았습니다. 가입한 주소로 인증 메일을 보냈습니다. 메일의 링크를 누른 뒤 다시 로그인하세요.")
			return
		}
	}

	// FR-204 세션 고정: 도착할 때의 토큰으로 인증 상태를 만들지 않는다.
	if err := d.sm.RenewToken(ctx); err != nil {
		d.serverError(w, r, err)
		return
	}
	d.sm.Put(ctx, sessUserID, u.ID)
	// auth_at 은 연결과 같은 문장에서 읽은 기준 시각이다 (auth.User.Cutoff, P-101 과 같다).
	stampAuthAt(d.sm, ctx, u.Cutoff())
	putTime(d.sm, ctx, sessReauth, time.Now())
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// finishLink attaches the identity to the logged-in account (P-111).
func (d *socialDeps) finishLink(w http.ResponseWriter, r *http.Request, provider, uid string) {
	a := ActorFrom(r.Context())
	if !a.IsAuthenticated() {
		// 시작할 때는 로그인 상태였는데 콜백에서 아니다 — 세션이 만료됐다.
		d.socialFail(w, r, "로그인이 만료되었습니다. 다시 로그인한 뒤 연결하세요.")
		return
	}
	// 이 세션이 아직 유효할 때만 잇는다 — 프로바이더와 왕복하는 사이 A-402 가 회수했을 수 있다.
	err := d.store.LinkSocialInSession(r.Context(), a.User.ID, provider, uid, a.AuthAt)
	switch {
	case errors.Is(err, auth.ErrSessionRevoked):
		d.socialFail(w, r, "로그인이 만료되었습니다. 다시 로그인한 뒤 연결하세요.")
	case errors.Is(err, auth.ErrSocialTaken):
		d.socialFail(w, r, "이미 다른 계정에 연결된 소셜 계정입니다.")
	case errors.Is(err, auth.ErrSocialLinked):
		d.socialFail(w, r, "이미 연결된 프로바이더입니다.")
	case err != nil:
		d.serverError(w, r, err)
	default:
		http.Redirect(w, r, "/me/connections", http.StatusSeeOther)
	}
}

// ownerConfirmed is P-111's re-check that the person at the keyboard owns the
// account (D15 5.3-1), answering with the connections screen when it fails.
//
// An account with a password types it every time, like P-109 and P-110 — an
// unattended open session must not be enough to attach someone else's social
// identity, which would outlive a password change. An account without one has
// nothing to type, so it must have logged in within the re-auth window.
func (d *socialDeps) ownerConfirmed(w http.ResponseWriter, r *http.Request) bool {
	a := ActorFrom(r.Context())
	has, err := d.store.HasPassword(r.Context(), a.User.ID)
	if err != nil {
		d.serverError(w, r, err)
		return false
	}
	if !has {
		now := time.Now
		if d.now != nil {
			now = d.now
		}
		if NeedsReauth(a, now()) {
			d.renderConnections(w, r, http.StatusBadRequest, "보안을 위해 다시 로그인한 뒤 시도하세요.")
			return false
		}
		return true
	}
	if d.confirm == nil || !d.confirm(r.Context(), a, r.PostFormValue("password")) {
		d.renderConnections(w, r, http.StatusBadRequest, "비밀번호가 올바르지 않습니다.")
		return false
	}
	return true
}

// connectionsPage is P-111 GET /me/connections.
func (d *socialDeps) connectionsPage(w http.ResponseWriter, r *http.Request) {
	d.renderConnections(w, r, http.StatusOK, d.sm.PopString(r.Context(), sessSocialError))
}

// connectionsAction is P-111 POST — 연결 해제.
func (d *socialDeps) connectionsAction(w http.ResponseWriter, r *http.Request) {
	a := ActorFrom(r.Context())
	if !a.IsAuthenticated() {
		d.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	if !d.ownerConfirmed(w, r) {
		return
	}
	provider := strings.TrimSpace(r.PostFormValue("provider"))
	err := d.store.UnlinkSocial(r.Context(), a.User.ID, provider)
	switch {
	case errors.Is(err, auth.ErrLastLoginMethod):
		// **마지막 로그인 수단은 해제할 수 없다** (FR-213). 해제하면 그
		// 계정으로 들어올 방법이 사라지고, 되돌릴 화면은 로그인 뒤에 있다.
		d.renderConnections(w, r, http.StatusUnprocessableEntity,
			"마지막 로그인 수단입니다. 비밀번호를 먼저 설정하세요.")
	case errors.Is(err, auth.ErrNoUser):
		d.renderConnections(w, r, http.StatusNotFound, "연결되어 있지 않습니다.")
	case err != nil:
		d.serverError(w, r, err)
	default:
		http.Redirect(w, r, "/me/connections", http.StatusSeeOther)
	}
}

func (d *socialDeps) renderConnections(w http.ResponseWriter, r *http.Request, code int, msg string) {
	a := ActorFrom(r.Context())
	if !a.IsAuthenticated() {
		d.notFound(w, r)
		return
	}
	linked, err := d.store.SocialAccounts(r.Context(), a.User.ID)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	have := map[string]bool{}
	for _, l := range linked {
		have[l.Provider] = true
	}

	type row struct {
		Provider      string
		Label         string
		Linked        bool
		CanDisconnect bool
	}
	// 비밀번호가 없으면 마지막 연결은 뗄 수 없다. 화면이 그 사실을 미리
	// 보여준다 — 거부하는 것은 여전히 서버다 (D15 4.3: 숨기는 것은 보안이 아니다).
	hasPassword, err := d.store.HasPassword(r.Context(), a.User.ID)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	last := len(linked) <= 1 && !hasPassword
	var rows []row
	for _, cfg := range d.enabled() {
		rows = append(rows, row{
			Provider: cfg.Provider, Label: cfg.Label, Linked: have[cfg.Provider],
			CanDisconnect: have[cfg.Provider] && !last,
		})
	}
	d.renderPage(w, r, "account/connections.html", code,
		d.socialView(r, "연결된 계정", map[string]any{
			"Connections": rows, "Error": msg, "HasPassword": hasPassword,
		}))
}

// socialFail ends the callback without rendering at its URL (sessSocialError):
// the message rides the session to the login page, or to P-111 when the
// person was linking while logged in.
func (d *socialDeps) socialFail(w http.ResponseWriter, r *http.Request, msg string) {
	d.sm.Put(r.Context(), sessSocialError, msg)
	target := loginPath
	if ActorFrom(r.Context()).IsAuthenticated() {
		target = "/me/connections"
	}
	// **빈 조각(#)을 붙인다.** Location 에 조각이 없으면 브라우저가 요청 주소의 조각을
	// 이어 붙인다(RFC 9110 10.2.2) — 하이브리드 응답이 code 를 # 뒤에 실어 오면 그것이
	// 다음 화면의 주소로 옮겨 간다.
	http.Redirect(w, r, target+"#", http.StatusSeeOther)
}

func (d *socialDeps) clearOAuth(ctx context.Context) {
	for _, k := range []string{sessOAuthState, sessOAuthSession, sessOAuthProvider, sessOAuthLink} {
		d.sm.Remove(ctx, k)
	}
}

// subtleEqual compares in constant time.
//
// state 비교는 비밀 대조다. 길이·접두사에서 새는 시간 차이가 곧 맞히는
// 실마리가 된다.
func subtleEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// socialView is the view model these screens hand the theme.
func (d *socialDeps) socialView(r *http.Request, title string, data map[string]any) theme.View {
	v := d.view(r, title, "")
	v.Data = data
	return v
}
