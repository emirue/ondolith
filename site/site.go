// Package site lets a site repository build its own ondolith binary — its own tables and the API its mobile
// app calls — without changing the core ([D42](https://github.com/emirue/ondolith/blob/main/docs/42-site-extension.md)).
//
//	//go:embed migrations/*.sql
//	var migrationFiles embed.FS
//
//	func main() {
//		migrations, _ := fs.Sub(migrationFiles, "migrations")
//		site.Main(site.Extension{
//			Migrations: migrations,
//			API: []site.Route{{Method: "GET", Path: "/v1/notes", Auth: site.App, Handler: listNotes}},
//		})
//	}
//
// 코어의 나머지는 internal/ 이라 사이트가 기댈 수 없다. 기댈 수 있는 것은 이 패키지뿐이고, 이 패키지를 비호환으로
// 바꾸면 CHANGELOG 에 「⚠️ 파괴적 변경」으로 적는다 (D42 지배 원칙 6).
package site

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"

	"github.com/emirue/ondolith/internal/applogin"
	"github.com/emirue/ondolith/internal/boot"
	"github.com/emirue/ondolith/internal/siteext"
)

// Extension is what a site adds to the core.
type Extension struct {
	// Migrations holds goose SQL files (`-- +goose Up` / `-- +goose Down`) at its root — fs.Sub an
	// embed.FS. They run after the core's, recorded in their own version table. Every table they
	// create, alter or drop must start with site_.
	Migrations fs.FS
	// API is mounted under /api/site. 관리자가 A-213 에서 켜고 앱 키를 발급해야 열린다.
	API []Route
	// Permissions are added to the permission table at boot. 코드에서 빼도 표에서 지우지 않는다 — 이미 준 역할이
	// 있을 수 있다. 부팅이 「어떤 라우트도 쓰지 않는 권한」으로 알린다.
	Permissions []Permission
	// Admin pages are mounted under /admin/site, in this order under the sidebar's 「사이트」.
	Admin []AdminPage
	// Phone turns on phone login for the app (/api/site/auth/code·verify·logout) and site.Person routes.
	Phone PhoneLogin
	// SMS sends the login code, replacing the SENS settings in A-215 — 다른 업체나 개발용 로그 발송.
	// 받은 ctx 는 요청과 떨어져 있고 시한이 있다. 실패는 코어가 작업 로그에 남긴다.
	SMS func(ctx context.Context, to, text string) error
}

// PhoneLogin is who may log in by phone.
type PhoneLogin struct {
	// Lookup gets a normalized number (digits only: "01012345678") and returns the subject that logs in —
	// the site's own id for that person. ok=false refuses — 응답은 허락한 번호와 같아서 앱은 등록 여부를 모른다.
	// 그만둔 사람은 false 를 돌려주고, 이미 받은 토큰은 EndSessions 로 끊는다.
	Lookup func(ctx context.Context, phone string) (subject string, ok bool, err error)
}

// Route is one endpoint of the site's app API.
type Route struct {
	// Method is GET, POST, PUT, PATCH or DELETE.
	Method string
	// Path follows /api/site and may use http.ServeMux wildcards: "/v1/notes/{id}". 옛 앱이 남으므로
	// 판(/v1/)을 넣기를 권한다 (D42 12절). 첫 마디 auth 는 코어의 문자 인증 로그인 몫이라 쓸 수 없다.
	Path string
	// Auth is what the route requires. **반드시 적는다** — 빈 값은 부팅 검사가 거부한다. 빈 값이 「앱 키만」이면
	// 사람마다 다른 데이터를 내는 라우트에 사람 인증을 빠뜨린 것이 조용히 열린다.
	Auth Auth
	// Handler runs after the core checked the switch, the app key, the rate limits and the body size.
	Handler http.HandlerFunc
}

// Auth is what a route requires besides the switch in A-213.
type Auth uint8

const (
	// App is the app key alone — 「어느 앱인지」. 앱 키는 앱을 풀면 꺼낼 수 있으므로 사람마다 다른 데이터를 내는
	// 라우트에는 쓰지 않는다.
	App Auth = Auth(siteext.AuthApp)
	// Person is the app key and a phone-login token in X-Ondolith-Session — 「누구인지」. From(r).Subject 가 그
	// 사람이다. 사람마다 다른 데이터는 이것으로만 연다 (D42 8절). Extension.Phone 이 있어야 한다.
	Person Auth = Auth(siteext.AuthPerson)
)

// Context is what the core already checked about a site API request or site admin page.
type Context struct {
	// DB is the site database through database/sql — 코어가 쓰는 pgx 판이 바뀌어도 사이트 코드가 깨지지 않게.
	// **그 요청 안에서만 쓴다.** 전역 변수나 고루틴에 담아 두면, 관리자가 사이트 유형을 바꿔 코어가 다시 조립될 때
	// 옛 연결이 닫혀 `sql: database is closed` 가 난다.
	DB *sql.DB
	// App is the name of the app key the request carried (A-213).
	App string
	// Log already carries the app name.
	Log *slog.Logger
	// Actor is the administrator on a site admin page; zero on the app API.
	Actor Actor
	// Subject is who logged in by phone, on a site.Person route — Lookup 이 돌려준 값.
	Subject string
}

// From returns what the core checked about r. Outside a site handler it is the zero Context.
func From(r *http.Request) Context { return FromContext(r.Context()) }

// FromContext is From for code that gets only a context — Phone.Lookup 이 그렇다. 코어는 Lookup 을 그 앱 요청의
// 문맥으로 부르므로 DB 가 들어 있다.
func FromContext(ctx context.Context) Context {
	c, _ := siteext.CallerFrom(ctx)
	return Context{DB: c.DB, App: c.App, Log: c.Log, Actor: Actor(c.Actor), Subject: c.Subject}
}

// NormalizePhone is the shape Phone.Lookup receives: a Korean mobile number, digits only —
// "+82 10-1234-5678" → "01012345678". 인부 번호를 저장할 때 이것을 거쳐야 Lookup 에서 찾는다.
func NormalizePhone(s string) (string, bool) { return applogin.NormalizePhone(s) }

// JSON answers with v as JSON, not stored by shared caches.
func JSON(w http.ResponseWriter, status int, v any) { siteext.WriteJSON(w, status, v) }

// Error answers with the site API's one error shape: {"error": msg}.
func Error(w http.ResponseWriter, status int, msg string) { siteext.WriteError(w, status, msg) }

// Main runs the whole core — install wizard, admin, themes, shop — with the extension added, and
// exits when the server stops. 확장이 틀렸으면(라우트·마이그레이션 표 이름) 설치 화면도 띄우지 않고 끝난다.
func Main(ext Extension) {
	if err := boot.Run(boot.Options{Ext: ext.internal()}); err != nil {
		fmt.Fprintln(os.Stderr, "ondolith:", err)
		os.Exit(1)
	}
}

func (e Extension) internal() siteext.Extension {
	routes := make([]siteext.Route, 0, len(e.API))
	for _, r := range e.API {
		routes = append(routes, siteext.Route{Method: r.Method, Path: r.Path, Auth: siteext.Auth(r.Auth), Handler: r.Handler})
	}
	perms := make([]siteext.Permission, 0, len(e.Permissions))
	for _, p := range e.Permissions {
		perms = append(perms, siteext.Permission(p))
	}
	pages := make([]siteext.AdminPage, 0, len(e.Admin))
	for _, p := range e.Admin {
		pages = append(pages, siteext.AdminPage(p))
	}
	return siteext.Extension{Migrations: e.Migrations, API: routes, Permissions: perms, Admin: pages,
		Phone: siteext.PhoneLogin(e.Phone), SMS: e.SMS}
}
