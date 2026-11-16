package site

import (
	"bytes"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/siteext"
)

// fakeAdmin records what the site package asked the core for.
type fakeAdmin struct {
	status    int
	title     string
	body      template.HTML
	notified  [2]string
	needs     bool
	password  string // ConfirmReauth 가 맞다고 할 비밀번호
	asked     []string
	recorded  [][3]string
	recordErr error
}

func (f *fakeAdmin) request(method, target string, form url.Values) *http.Request {
	var r *http.Request
	if form != nil {
		r = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	a := siteext.Admin{
		Render: func(w http.ResponseWriter, _ *http.Request, status int, title string, body template.HTML) {
			f.status, f.title, f.body = status, title, body
			w.WriteHeader(status)
		},
		Notify:      func(to, msg string) { f.notified = [2]string{to, msg} },
		NeedsReauth: func() bool { return f.needs },
		ConfirmReauth: func(p string) bool { // 코어처럼 맞으면 창을 연다(세션에 찍는다)
			f.asked = append(f.asked, p)
			if p != "" && p == f.password {
				f.needs = false
				return true
			}
			return false
		},
		ReauthField: `<input name="password">`,
		Record: func(action, target, summary string) error {
			f.recorded = append(f.recorded, [3]string{action, target, summary})
			return f.recordErr
		},
	}
	ctx := siteext.WithAdmin(r.Context(), a)
	ctx = siteext.WithCaller(ctx, siteext.Caller{Log: slog.New(slog.DiscardHandler)})
	return r.WithContext(ctx)
}

// **사이트 템플릿의 content 블록이 코어 관리자 틀 안에 그려진다** (D42 7절). 사이트 값은 사이트 템플릿이 받고,
// html/template 의 이스케이프가 그대로 걸린다. 상태 코드와 제목은 넘긴 그대로다 — 422 로 폼을 다시 그릴 수 있어야 한다.
func TestRenderAdminDrawsTheSiteContentInsideTheCoreFrame(t *testing.T) {
	tpl := template.Must(template.New("w").Funcs(template.FuncMap{"up": strings.ToUpper}).Parse(
		`{{define "content"}}<p>{{up .Name}} {{.Note}}</p>{{end}}`))
	f := &fakeAdmin{}
	w := httptest.NewRecorder()
	RenderAdmin(w, f.request("GET", "/admin/site/workers", nil), http.StatusUnprocessableEntity, "인부",
		tpl, map[string]string{"Name": "kim", "Note": "<b>"})
	if f.status != 422 || f.title != "인부" || string(f.body) != "<p>KIM &lt;b&gt;</p>" || w.Code != 422 {
		t.Fatalf("RenderAdmin → status %d title %q body %q (응답 %d)", f.status, f.title, f.body, w.Code)
	}
}

// 사이트 템플릿이 틀리면 반쯤 그린 화면 대신 500 이다 — content 블록이 없거나 실행이 실패하는 경우.
func TestRenderAdminRefusesABrokenTemplate(t *testing.T) {
	for name, tpl := range map[string]*template.Template{
		"content 없음": template.Must(template.New("x").Parse(`{{define "main"}}x{{end}}`)),
		"실행 실패":      template.Must(template.New("x").Parse(`{{define "content"}}{{.Missing.Deeper}}{{end}}`)),
	} {
		f := &fakeAdmin{}
		w := httptest.NewRecorder()
		RenderAdmin(w, f.request("GET", "/admin/site/x", nil), 200, "x", tpl, map[string]any{"Missing": 1})
		if w.Code != 500 || f.status != 0 {
			t.Errorf("%s: 응답 %d, 코어 틀 %d — 500 이고 틀을 그리지 않아야 한다", name, w.Code, f.status)
		}
	}
	// 관리자 화면 밖(사이트 API 처리기 등)에서 부르면 그릴 틀이 없다.
	w := httptest.NewRecorder()
	RenderAdmin(w, httptest.NewRequest("GET", "/api/site/v1/x", nil), 200, "x",
		template.Must(template.New("x").Parse(`{{define "content"}}x{{end}}`)), nil)
	if w.Code != 500 {
		t.Errorf("관리자 화면 밖 = %d, want 500", w.Code)
	}
}

// **성공한 POST 의 끝은 알림 + 303 이다** (D19 C11). 돌아갈 곳은 관리자 트리 안이어야 한다 — 사이트 코드가 폼의
// 값을 그대로 넘기면 남의 사이트로 보내는 열린 리다이렉트가 된다.
func TestDoneNotifiesAndRedirectsInsideTheAdminTree(t *testing.T) {
	f := &fakeAdmin{}
	w := httptest.NewRecorder()
	Done(w, f.request("POST", "/admin/site/workers", url.Values{}), "/admin/site/workers?page=2", "저장했습니다.")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin/site/workers?page=2" ||
		f.notified != [2]string{"/admin/site/workers?page=2", "저장했습니다."} {
		t.Fatalf("Done → %d %q, 알림 %v", w.Code, w.Header().Get("Location"), f.notified)
	}
	// /admin/../logout 은 http.Redirect 가 정리해 /logout 으로 보낸다 — 같은 사이트지만 관리자 트리 밖이다.
	for _, to := range []string{"https://evil.example/admin/", "//evil.example/admin/", "/login", "", "/admin",
		"/admin/../logout", "/admin/site/%2e%2e/../logout", "/admin/./site", "/admin/site/x/.."} {
		f := &fakeAdmin{}
		w := httptest.NewRecorder()
		Done(w, f.request("POST", "/admin/site/workers", url.Values{}), to, "x")
		if w.Code != 500 || w.Header().Get("Location") != "" || f.notified != [2]string{} {
			t.Errorf("Done(%q) → %d %q — 관리자 트리 밖으로 보내면 안 된다", to, w.Code, w.Header().Get("Location"))
		}
	}
}

// **재확인: 창이 열려 있으면 비밀번호를 묻지 않고, 닫혀 있으면 폼의 password 를 코어에 확인시킨다** (D15 5.3-1).
// 창만 보고 false 를 주면 15분이 지난 관리자는 그 동작을 영원히 못 한다. 칸은 닫혀 있을 때만 그린다.
func TestReauthOpensTheWindowWithThePostedPassword(t *testing.T) {
	open := &fakeAdmin{needs: false}
	r := open.request("POST", "/admin/site/pay", url.Values{"password": {"wrong"}})
	if !Reauth(r) || len(open.asked) != 0 || ReauthField(r) != "" {
		t.Errorf("창이 열림: Reauth=%v, 물은 비밀번호 %v, 칸 %q", Reauth(r), open.asked, ReauthField(r))
	}
	closed := &fakeAdmin{needs: true, password: "pw"}
	if r := closed.request("POST", "/admin/site/pay", url.Values{"password": {"bad"}}); Reauth(r) {
		t.Error("틀린 비밀번호로 통과했다")
	}
	if r := closed.request("POST", "/admin/site/pay", url.Values{}); Reauth(r) {
		t.Error("비밀번호 없이 통과했다")
	}
	r = closed.request("POST", "/admin/site/pay", url.Values{"password": {"pw"}})
	if ReauthField(r) != `<input name="password">` {
		t.Errorf("창이 닫힘: 칸 %q", ReauthField(r))
	}
	if !Reauth(r) || closed.asked[len(closed.asked)-1] != "pw" {
		t.Errorf("맞는 비밀번호: 물은 것 %v", closed.asked)
	}
	// 같은 요청에서 창이 열렸다 — 다시 그리는 폼에 칸이 없고, 두 번째 Reauth 는 비밀번호를 다시 대조하지 않는다.
	n := len(closed.asked)
	if !Reauth(r) || len(closed.asked) != n || ReauthField(r) != "" {
		t.Errorf("확인한 뒤: 다시 물은 것 %v, 칸 %q — 창이 열렸으면 묻지도 그리지도 않는다", closed.asked[n:], ReauthField(r))
	}
	// 관리자 화면 밖에서는 통과시키지 않는다 — 닫힌 쪽이 기본이다.
	if bare := httptest.NewRequest("POST", "/api/site/v1/x", nil); Reauth(bare) || ReauthField(bare) != "" {
		t.Error("관리자 화면 밖에서 재확인을 통과했다")
	}
}

// **작업 로그: 동작은 site. 로 시작하는 두 마디다** (DB CHECK). 틀린 동작은 적지 않고 오류 로그를 남긴다 — 기록
// 실패가 요청을 망치지는 않는다(일은 이미 했다, D15 7절). 기록 오류도 오류 로그다.
func TestOpLogRecordsSiteActions(t *testing.T) {
	var logged bytes.Buffer
	f := &fakeAdmin{}
	r := f.request("POST", "/admin/site/pay", url.Values{})
	r = r.WithContext(siteext.WithCaller(r.Context(), siteext.Caller{Log: slog.New(slog.NewTextHandler(&logged, nil))}))
	OpLog(r, "site.worker_pay", "w-1", "김 50,000원")
	if len(f.recorded) != 1 || f.recorded[0] != [3]string{"site.worker_pay", "w-1", "김 50,000원"} {
		t.Fatalf("recorded = %v", f.recorded)
	}
	OpLog(r, "worker.pay", "w-1", "x")
	if len(f.recorded) != 1 || !strings.Contains(logged.String(), "worker.pay") {
		t.Errorf("틀린 동작을 적었거나 알리지 않았다: %v / %s", f.recorded, logged.String())
	}
	f.recordErr = errors.New("db down")
	OpLog(r, "site.worker_pay", "w-1", "x")
	if !strings.Contains(logged.String(), "db down") {
		t.Errorf("기록 실패를 알리지 않았다: %s", logged.String())
	}
}
