package app

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/alexedwards/scs/v2/memstore"
)

// 저장 성공 알림 (FR-724, D19 C11). 처리기는 adminCaller.Notify 로 한 줄을 세션에 싣고
// 303 으로 돌아가고, 돌아간 화면을 그리는 Render 가 그것을 꺼낸다.
//
// 진짜 세션 관리자(LoadAndSave)와 진짜 관리자 렌더러를 잇는다 — 세션이 언제 저장되는지
// (WriteHeader 때)와 렌더러가 언제 꺼내는지가 맞물려야 동작하므로 둘 중 하나를 가짜로
// 바꾸면 시험이 그 맞물림을 보지 못한다.
const noticeLine = "자동 백업 설정을 저장했습니다 — 켜짐 · 매일 4시 · 7개 보관"

// noticeClient 는 POST /save 가 알림을 싣고 303 으로 /screen 에 돌아가는 서버와,
// 쿠키를 들고 다니는 브라우저다. /screen 은 ?code= 로 상태 코드를, ?own= 으로 화면이
// 스스로 그리는 알림을 고른다.
func noticeClient(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	sm := newSessionManager(memstore.New(), false)
	ui, err := newAdminRenderer(func() string { return "s" }, nil, "", false, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ui.sm = sm
	mux := http.NewServeMux()
	mux.HandleFunc("/save", func(w http.ResponseWriter, r *http.Request) {
		// ?to= 는 처리기가 돌아가겠다고 한 곳이다. 브라우저는 늘 /screen 으로 간다.
		to := "/screen?page=2"
		if t := r.URL.Query().Get("to"); t != "" {
			to = t
		}
		adminCaller{ctx: r.Context(), sm: sm}.Notify(to, noticeLine)
		http.Redirect(w, r, "/screen", http.StatusSeeOther)
	})
	screen := func(w http.ResponseWriter, r *http.Request) {
		code := http.StatusOK
		if c := r.URL.Query().Get("code"); c != "" {
			code, _ = strconv.Atoi(c)
		}
		data := map[string]any{}
		if own := r.URL.Query().Get("own"); own != "" {
			data["Notice"] = own
		}
		// 레이아웃의 알림 자리만 본다 — 내용이 비어도 그려지는 화면이면 된다.
		ui.Render(w, r, "admin/refusal.html", code, data)
	}
	mux.HandleFunc("/screen", screen)
	mux.HandleFunc("/other", screen)
	srv := httptest.NewServer(sm.LoadAndSave(mux))
	t.Cleanup(srv.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return srv, &http.Client{Jar: jar}
}

func noticeFetch(t *testing.T, c *http.Client, method, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(b)
}

const statusNotice = `<p class="adm-toast" role="status">` + noticeLine + `</p>`

// **저장 뒤 돌아간 화면에 한 번 보이고, 새로 고침하면 없다.** 운영자가 같은 저장을 17초에
// 네 번 누른 것이 이것이 없어서였다 (A-604).
func TestAdminNoticeShowsOnceAfterTheRedirect(t *testing.T) {
	srv, c := noticeClient(t)

	code, body := noticeFetch(t, c, http.MethodPost, srv.URL+"/save")
	if code != http.StatusOK {
		t.Fatalf("저장 후 돌아간 화면 = HTTP %d", code)
	}
	if !strings.Contains(body, statusNotice) {
		t.Fatalf("돌아간 화면에 role=status 알림이 없다:\n%s", body)
	}

	if _, again := noticeFetch(t, c, http.MethodGet, srv.URL+"/screen"); strings.Contains(again, noticeLine) {
		t.Error("새로 고침했는데 알림이 다시 떴다")
	}
}

// **422 로 다시 그린 화면은 알림을 쓰지 않는다.** 검증에 걸린 화면이 먹으면 고쳐 저장한
// 뒤 돌아간 화면에는 없다. 거절 화면(4xx)·POST 가 바로 그린 화면도 같다.
func TestAdminNoticeIsKeptPastScreensThatAreNotTheReturn(t *testing.T) {
	srv, c := noticeClient(t)

	// 알림만 싣고 리다이렉트는 따라가지 않는다.
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if code, _ := noticeFetch(t, c, http.MethodPost, srv.URL+"/save"); code != http.StatusSeeOther {
		t.Fatalf("저장 = HTTP %d, want 303", code)
	}
	c.CheckRedirect = nil

	for _, tc := range []struct{ method, query string }{
		{http.MethodGet, "?code=422"},
		{http.MethodGet, "?code=403"},
		{http.MethodPost, ""},
	} {
		if _, body := noticeFetch(t, c, tc.method, srv.URL+"/screen"+tc.query); strings.Contains(body, noticeLine) {
			t.Errorf("%s /screen%s 가 알림을 그렸다 — 돌아간 화면이 아니다", tc.method, tc.query)
		}
	}
	if _, body := noticeFetch(t, c, http.MethodGet, srv.URL+"/screen"); !strings.Contains(body, statusNotice) {
		t.Errorf("앞의 화면들이 알림을 먹었다 — 돌아간 화면에 없다:\n%s", body)
	}
}

// **화면이 스스로 그리는 알림이 이긴다.** 실린 알림은 그 화면에서 버려진다 — 남겨 두면
// 엉뚱한 다음 화면에 「저장했습니다」가 뜬다.
func TestAdminNoticeYieldsToTheScreensOwnNotice(t *testing.T) {
	srv, c := noticeClient(t)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	noticeFetch(t, c, http.MethodPost, srv.URL+"/save")
	c.CheckRedirect = nil

	_, body := noticeFetch(t, c, http.MethodGet, srv.URL+"/screen?own="+url.QueryEscape("전 품목 대조 완료."))
	if !strings.Contains(body, `<p class="adm-toast" role="status">전 품목 대조 완료.</p>`) {
		t.Errorf("화면의 알림이 없다:\n%s", body)
	}
	if strings.Contains(body, noticeLine) {
		t.Error("화면이 제 알림을 그렸는데 실린 알림까지 그렸다")
	}
	if _, next := noticeFetch(t, c, http.MethodGet, srv.URL+"/screen"); strings.Contains(next, noticeLine) {
		t.Error("버려야 할 알림이 다음 화면에 떴다")
	}
}

// **알림은 처리기가 돌아가겠다고 한 화면에서만 보인다.** 돌아간 화면이 403 으로 그려지면(자기 역할에서
// role.view 를 뺀 운영자) 알림은 꺼내지지 않은 채 세션에 남았다가, 나중에 여는 엉뚱한 화면에 뜬다.
// 다른 화면이 먼저 꺼내면 버린다. 쿼리(?page=)는 가리지 않는다 — 같은 화면의 다른 쪽이다.
func TestAdminNoticeIsOnlyForTheScreenItReturnsTo(t *testing.T) {
	srv, c := noticeClient(t)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	noticeFetch(t, c, http.MethodPost, srv.URL+"/save?to="+url.QueryEscape("/screen"))
	c.CheckRedirect = nil

	if _, body := noticeFetch(t, c, http.MethodGet, srv.URL+"/other"); strings.Contains(body, noticeLine) {
		t.Error("돌아갈 화면이 아닌 /other 에 알림이 떴다")
	}
	if _, body := noticeFetch(t, c, http.MethodGet, srv.URL+"/screen"); strings.Contains(body, noticeLine) {
		t.Error("다른 화면이 꺼낸 알림이 남아 있다가 떴다 — 버려야 한다")
	}
}
