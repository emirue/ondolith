package app

import (
	"context"
	"database/sql"
	"html/template"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/admin"
	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/content"
	"github.com/emirue/ondolith/internal/siteext"
)

// siteCaller is admin.Caller with the reauth window and notices under the test's control.
type siteCaller struct {
	needs    bool
	password string
	asked    []string
	notified [2]string
}

func (c *siteCaller) Can(string) bool                 { return true }
func (c *siteCaller) CanOn(string, auth.BoardID) bool { return true }
func (c *siteCaller) CanOnScreen(string, string) bool { return true }
func (c *siteCaller) UserID() string                  { return "u-1" }
func (c *siteCaller) Email() string                   { return "op@example.com" }
func (c *siteCaller) IsSuperuser() bool               { return false }
func (c *siteCaller) NeedsReauth() bool               { return c.needs }
func (c *siteCaller) Notify(to, msg string)           { c.notified = [2]string{to, msg} }
func (c *siteCaller) ConfirmReauth(p string) bool {
	c.asked = append(c.asked, p)
	return p == c.password
}

type rendered struct {
	name string
	code int
	data map[string]any
}

// **사이트 관리자 화면은 코어가 감싸서 연다** (D42 7절). 라우트의 Permission 칸은 선언일 뿐이다(tree.go) — 사이트
// 처리기는 권한을 보지 않으므로 감싸개가 먼저 거절해야 한다. 통과하면 처리기는 DB·관리자·코어의 틀·재확인·알림·작업
// 로그를 받는다. 재확인은 요청마다의 Caller 를 거친다 — 계정당 상한이 그 안에 있다.
func TestSiteAdminPageIsGuardedAndGetsTheCoreServices(t *testing.T) {
	var got rendered
	var entries []content.Entry
	caller := &siteCaller{needs: true, password: "pw"}
	db := &sql.DB{}
	d := siteAdmin{
		render: func(_ http.ResponseWriter, _ *http.Request, name string, code int, data any) {
			got = rendered{name, code, data.(map[string]any)}
		},
		caller:      func(*http.Request) admin.Caller { return caller },
		db:          db,
		log:         slog.New(slog.DiscardHandler),
		record:      func(_ context.Context, e content.Entry) error { entries = append(entries, e); return nil },
		reauthField: `<input name="password">`,
	}
	var seen struct {
		caller siteext.Caller
		admin  siteext.Admin
		id     string
	}
	h := d.page(siteext.AdminPage{Path: "/notes/{id}", Permission: "site.notes", Handler: func(w http.ResponseWriter, r *http.Request) {
		seen.caller, _ = siteext.CallerFrom(r.Context())
		seen.admin, _ = siteext.AdminFrom(r.Context())
		seen.id = r.PathValue("id")
		seen.admin.Render(w, r, http.StatusUnprocessableEntity, "메모", "<p>본문</p>")
	}})
	mux := http.NewServeMux()
	mux.Handle("/admin/site/notes/{id}", h)

	ask := func(perms *auth.Permissions) {
		got, seen.id = rendered{}, ""
		r := httptest.NewRequest("POST", "/admin/site/notes/7", strings.NewReader(url.Values{"password": {"pw"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		a := &Actor{Perms: perms, User: &auth.User{ID: "u-1", DisplayName: "운영자"}}
		mux.ServeHTTP(httptest.NewRecorder(), r.WithContext(withActorValue(r.Context(), a)))
	}

	// 관리자 문은 통과했지만 그 화면의 권한이 없다 — 처리기는 불리지 않고 틀 안의 403 이다.
	ask(auth.NewPermissions(false, []auth.Grant{{Permission: "admin.access", Board: auth.Global}}))
	if seen.id != "" || got.name != "admin/refusal.html" || got.code != http.StatusForbidden {
		t.Fatalf("권한 없음: 처리기 %q, 그린 것 %+v — 403 이고 처리기를 부르지 않아야 한다", seen.id, got)
	}

	ask(auth.NewPermissions(false, []auth.Grant{{Permission: "admin.access", Board: auth.Global}, {Permission: "site.notes", Board: auth.Global}}))
	if seen.id != "7" || seen.caller.DB != db || seen.caller.Actor != (siteext.Actor{ID: "u-1", Name: "운영자"}) || seen.caller.Log == nil {
		t.Fatalf("처리기가 받은 것: id %q, %+v", seen.id, seen.caller)
	}
	if got.name != "admin/site.html" || got.code != 422 || got.data["Title"] != "메모" || got.data["SiteBody"] != template.HTML("<p>본문</p>") {
		t.Errorf("코어 틀: %+v", got)
	}
	a := seen.admin
	if !a.NeedsReauth() || a.ConfirmReauth("bad") || !a.ConfirmReauth("pw") || len(caller.asked) != 2 || a.ReauthField != `<input name="password">` {
		t.Errorf("재확인이 Caller 를 거치지 않았다: needs=%v asked=%v field=%q", a.NeedsReauth(), caller.asked, a.ReauthField)
	}
	a.Notify("/admin/site/notes", "남겼습니다.")
	if caller.notified != [2]string{"/admin/site/notes", "남겼습니다."} {
		t.Errorf("알림 = %v", caller.notified)
	}
	if err := a.Record("site.note_add", "n-1", "메모 하나"); err != nil || len(entries) != 1 {
		t.Fatalf("Record = %v, %v", err, entries)
	}
	if e := entries[0]; e.ActorID != "u-1" || e.ActorEmail != "op@example.com" || e.Action != "site.note_add" ||
		e.TargetType != "site" || e.TargetID != "n-1" || e.Summary != "메모 하나" || e.IP == "" {
		t.Errorf("작업 로그 = %+v", e)
	}
}
