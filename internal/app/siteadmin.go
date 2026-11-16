package app

import (
	"context"
	"database/sql"
	"errors"
	"html/template"
	"log/slog"
	"net/http"

	"github.com/emirue/ondolith/internal/admin"
	"github.com/emirue/ondolith/internal/content"
	"github.com/emirue/ondolith/internal/siteext"
)

// siteAdmin wraps a site binary's admin pages (D42 7절, A-214). 관리자 트리 안에 등록되므로 문(admin.access)·교차
// 출처 보호·요청 상한은 이미 지났다. 여기서는 그 화면의 권한을 보고, 사이트 처리기가 코어에 부탁할 것을 붙인다.
type siteAdmin struct {
	render func(w http.ResponseWriter, r *http.Request, name string, code int, data any)
	caller func(*http.Request) admin.Caller
	// needsReauth is adminRenderer.needsReauth — **세션을 다시 읽는다.** Caller.NeedsReauth 는 요청이 들어올 때의
	// Actor 를 보므로, 같은 요청에서 방금 확인한 비밀번호를 모른다. 그러면 422 로 다시 그린 폼에 칸이 또 나오고,
	// 사이트가 Reauth 를 두 번 부르면 비밀번호를 두 번 대조한다.
	needsReauth func(*http.Request, *Actor) bool
	db          *sql.DB
	log         *slog.Logger
	record      func(context.Context, content.Entry) error
	// endSubject ends a subject's app tokens (site.EndSessions, D42 8절).
	endSubject func(context.Context, string) (int64, error)
	// reauthField is the layout's reauth-field drawn open — 사이트 템플릿은 코어 템플릿 묶음 밖이라 그대로 못 부른다.
	reauthField template.HTML
}

// page is the handler the route table gets for p.
//
// **권한은 여기서 본다.** 라우트의 Permission 칸은 부팅 검사용 선언이고 거절하지 않는다(tree.go). 코어 처리기는
// 저마다 d.require 를 부르지만 사이트 처리기는 그러지 않는다 — 여기서 막지 않으면 admin.access 만 있는 관리자가
// 사이트 화면을 연다.
func (d siteAdmin) page(p siteext.AdminPage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor := ActorFrom(r.Context())
		if !actor.Can(p.Permission) {
			d.render(w, r, "admin/refusal.html", http.StatusForbidden, map[string]any{"Error": "권한이 없습니다."})
			return
		}
		c := d.caller(r)
		ctx := siteext.WithCaller(r.Context(), siteext.Caller{DB: d.db, Log: d.log,
			Actor: siteext.Actor{ID: c.UserID(), Name: actorName(actor)}})
		ctx = siteext.WithAdmin(ctx, siteext.Admin{
			Render: func(w http.ResponseWriter, r *http.Request, status int, title string, body template.HTML) {
				d.render(w, r, "admin/site.html", status, map[string]any{"Title": title, "SiteBody": body})
			},
			Notify:        c.Notify,
			NeedsReauth:   func() bool { return d.needsReauth(r, actor) },
			ConfirmReauth: c.ConfirmReauth,
			ReauthField:   d.reauthField,
			EndSessions: func(ctx context.Context, subject string) error {
				if d.endSubject == nil {
					return errors.New("app: 문자 로그인 저장소가 없다")
				}
				_, err := d.endSubject(ctx, subject)
				return err
			},
			Record: func(action, target, summary string) error {
				// 요청이 끊겨도 남긴다 — 일은 이미 했다 (D15 7절). 코어 화면의 d.log 와 같은 모양이다.
				return d.record(context.WithoutCancel(r.Context()), content.Entry{
					ActorID: c.UserID(), ActorEmail: c.Email(), Action: action,
					TargetType: "site", TargetID: target, Summary: summary, IP: r.RemoteAddr})
			},
		})
		p.Handler(w, r.WithContext(ctx))
	}
}
