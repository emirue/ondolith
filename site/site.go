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
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"

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
	// 라우트에는 쓰지 않는다. 사람 인증(문자 인증 로그인)은 다음 단계에서 더한다 (D42 8절).
	App Auth = Auth(siteext.AuthApp)
)

// Context is what the core already checked about a site API request.
type Context struct {
	// DB is the site database through database/sql — 코어가 쓰는 pgx 판이 바뀌어도 사이트 코드가 깨지지 않게.
	// **그 요청 안에서만 쓴다.** 전역 변수나 고루틴에 담아 두면, 관리자가 사이트 유형을 바꿔 코어가 다시 조립될 때
	// 옛 연결이 닫혀 `sql: database is closed` 가 난다.
	DB *sql.DB
	// App is the name of the app key the request carried (A-213).
	App string
	// Log already carries the app name.
	Log *slog.Logger
}

// From returns what the core checked about r. Outside a site API handler it is the zero Context.
func From(r *http.Request) Context {
	c, _ := siteext.CallerFrom(r.Context())
	return Context{DB: c.DB, App: c.App, Log: c.Log}
}

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
	return siteext.Extension{Migrations: e.Migrations, API: routes}
}
