// Package siteext is the core side of a site binary's extension ([D42](../../docs/42-site-extension.md)).
//
// 사이트 저장소는 공개 패키지 site 만 본다. site 가 이 타입으로 바꿔 넘기고, 코어의 내부 패키지는 이것만 읽는다 —
// 내부 패키지가 site 를 가져오면 site → boot → app → site 로 순환한다.
package siteext

import (
	"fmt"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
)

// APIPrefix is where every site API route lives. 코어 주소를 덮지 못하게 고정한다 (D42 지배 원칙 3).
const APIPrefix = "/api/site"

// AdminPrefix is where every site admin page lives (D42 7절). 관리자 트리 안이라 문(admin.access)·교차 출처 보호·
// 요청 상한이 그대로 붙는다.
const AdminPrefix = "/admin/site"

// Extension is what a site binary adds to the core. The zero value is the core's own binary.
type Extension struct {
	// Migrations holds the site's goose SQL files at its root; nil means none.
	Migrations fs.FS
	// API is mounted under APIPrefix.
	API []Route
	// Permissions are put into the permission table at boot, so the role editor can grant them.
	Permissions []Permission
	// Admin pages are mounted under AdminPrefix, in this order in the sidebar's 「사이트」 group.
	Admin []AdminPage
}

// Permission is a site permission (D42 3절). Key is "site.이름"; Name is what the role editor shows.
type Permission struct {
	Key  string
	Name string
}

// AdminPage is one site admin screen. Path follows AdminPrefix; one Handler takes GET and POST.
// Menu 가 비면 사이드바에 없다 — 목록에서 눌러 들어가는 상세 화면이다.
type AdminPage struct {
	Menu       string
	Path       string
	Permission string
	Handler    http.HandlerFunc
}

var siteKey = regexp.MustCompile(`^site\.[a-z][a-z0-9_]*$`)

// ValidKey reports whether k is a site permission key or op-log action: "site." and one more
// segment. DB 가 두 마디만 받는다(permissions.key·op_log.action 의 CHECK) — 여기서 같은 모양을 요구한다.
func ValidKey(k string) bool { return siteKey.MatchString(k) }

// Route is one site API endpoint. Path follows APIPrefix and uses http.ServeMux's wildcards.
type Route struct {
	Method  string
	Path    string
	Auth    Auth
	Handler http.HandlerFunc
}

// Auth is what a route requires besides the switch (D42 5절).
//
// **빈 값은 없다 — 부팅 검사가 거부한다.** 빈 값이 「키만」이면, 사람마다 다른 데이터를 내는 라우트에 사람 인증을
// 빠뜨린 것이 조용히 열린다. 키는 비밀이 아니므로(D42 지배 원칙 5) 열린 쪽이 기본값이 되면 안 된다.
type Auth uint8

const (
	// AuthApp is the app key alone: 「어느 앱인지」. 사람을 가리지 않는 기능에만 쓴다.
	AuthApp Auth = 1
)

// reservedSeg is the first path segment the core keeps for itself — 문자 인증 로그인 (D42 8절).
const reservedSeg = "auth"

var methods = map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}

var (
	literalSeg = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)
	wildSeg    = regexp.MustCompile(`^\{[A-Za-z_][A-Za-z0-9_]*(\.\.\.)?\}$`)
)

// validPath accepts "/마디/…" whose segments are plain characters or {name}, with {name...} only last.
// 빈 마디(`//`·끝의 `/`)·`.`·`..`·질의 문자열·`{$}` 는 받지 않는다.
func validPath(p string) bool {
	if !strings.HasPrefix(p, "/") {
		return false
	}
	segs := strings.Split(p[1:], "/")
	for i, s := range segs {
		switch {
		case s == "." || s == "..":
			return false
		case literalSeg.MatchString(s):
		case wildSeg.MatchString(s):
			if strings.HasSuffix(s, "...}") && i != len(segs)-1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// Validate is the boot check on the routes (D42 4절 5): every problem at once, so a site author
// fixes them in one pass instead of one per restart.
func (e Extension) Validate() error {
	var errs []string
	mux := http.NewServeMux()
	for _, r := range e.API {
		where := r.Method + " " + APIPrefix + r.Path
		bad := false
		if !methods[r.Method] {
			errs = append(errs, where+": 메서드는 GET·POST·PUT·PATCH·DELETE 중 하나다")
			bad = true
		}
		if !validPath(r.Path) {
			errs = append(errs, where+": 경로는 / 로 시작하고, 마디는 글자·숫자·._~- 이거나 {이름}·맨 끝의 {이름...} 이다")
			bad = true
		}
		if r.Handler == nil {
			errs = append(errs, where+": 처리기가 없다")
			bad = true
		}
		if r.Auth != AuthApp {
			errs = append(errs, where+": 인증 방식을 정하지 않았다 — site.App(앱 키)을 적는다. 빈 값은 받지 않는다")
			bad = true
		}
		if first, _, _ := strings.Cut(strings.TrimPrefix(r.Path, "/"), "/"); first == reservedSeg {
			errs = append(errs, where+": "+APIPrefix+"/"+reservedSeg+"/ 는 코어의 문자 인증 로그인 몫이다")
			bad = true
		}
		if !bad {
			if err := register(mux, r.Method+" "+APIPrefix+r.Path); err != nil {
				errs = append(errs, where+": 다른 라우트와 겹친다 — "+err.Error())
			}
		}
	}
	errs = append(errs, e.validateAdmin()...)
	if len(errs) > 0 {
		return fmt.Errorf("사이트 확장 라우트가 틀렸다:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// validateAdmin checks Permissions and Admin (D42 7절).
func (e Extension) validateAdmin() []string {
	var errs []string
	declared := map[string]bool{}
	for _, p := range e.Permissions {
		where := "권한 " + p.Key
		switch n := len([]rune(strings.TrimSpace(p.Name))); {
		case !ValidKey(p.Key):
			errs = append(errs, where+": 키는 site. 다음에 소문자로 시작하는 마디 하나다 (예: site.workers)")
		case n == 0 || n > 60:
			errs = append(errs, where+": 이름은 1~60자다 — 역할 편집기에 보인다")
		case declared[p.Key]:
			errs = append(errs, where+": 두 번 적었다")
		}
		declared[p.Key] = true
	}
	mux := http.NewServeMux()
	for _, a := range e.Admin {
		where := AdminPrefix + a.Path
		bad := false
		if !validPath(a.Path) {
			errs = append(errs, where+": 경로는 / 로 시작하고, 마디는 글자·숫자·._~- 이거나 {이름}·맨 끝의 {이름...} 이다")
			bad = true
		}
		if a.Handler == nil {
			errs = append(errs, where+": 처리기가 없다")
			bad = true
		}
		if !declared[a.Permission] {
			errs = append(errs, where+": 권한 "+a.Permission+"을 Permissions 에 선언하지 않았다")
		}
		if strings.Contains(a.Path, "{") && a.Menu != "" {
			errs = append(errs, where+": 변수가 든 주소는 메뉴에 걸 수 없다 — Menu 를 비운다")
		}
		if len([]rune(a.Menu)) > 30 {
			errs = append(errs, where+": 메뉴 글자는 30자까지다")
		}
		if !bad {
			if err := register(mux, where); err != nil {
				errs = append(errs, where+": 다른 화면과 겹친다 — "+err.Error())
			}
		}
	}
	return errs
}

// register asks the real ServeMux, which panics on a duplicate or ambiguous pattern — the
// verdict is then exactly the mux's own.
func register(mux *http.ServeMux, pattern string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	mux.HandleFunc(pattern, func(http.ResponseWriter, *http.Request) {})
	return nil
}

var (
	tableStmt  = regexp.MustCompile(`(?i)\b(create|alter|drop)\s+table\s+`)
	tableSkip  = regexp.MustCompile(`(?i)^(if\s+not\s+exists|if\s+exists|only)\s+`)
	tableIdent = regexp.MustCompile(`^(?:"[^"]+"|[A-Za-z_][A-Za-z0-9_$]*)(?:\.(?:"[^"]+"|[A-Za-z_][A-Za-z0-9_$]*))?`)
	lineNote   = regexp.MustCompile(`--[^\n]*`)
)

// CheckMigrations refuses a site migration that creates, alters or drops a table whose name does not
// start with site_ (D42 지배 원칙 3).
//
// ponytail: 정규식으로 보는 실수 방지다 — 보안 경계가 아니다. 사이트 코드는 DB 를 그대로 쓰고, SQL 을 끝까지 읽지
// 않으므로 동적 SQL·DO 블록은 지나간다. 코어를 올릴 때 코어 표와 부딪히는 흔한 실수만 막는다.
func CheckMigrations(fsys fs.FS) error {
	if fsys == nil {
		return nil
	}
	files, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return err
	}
	if len(files) == 0 {
		// embed.FS 를 그대로 넘기면 파일이 migrations/ 아래에 있다. 여기서 막지 않으면 설치를 마친 뒤에야 goose 가
		// 「마이그레이션 없음」으로 운영 전환을 멈춘다.
		return fmt.Errorf("사이트 마이그레이션의 맨 위에 .sql 파일이 없다 — embed.FS 를 fs.Sub(…, \"migrations\") 로 넘겼는지 본다")
	}
	var errs []string
	for _, f := range files {
		b, err := fs.ReadFile(fsys, f)
		if err != nil {
			return err
		}
		for _, t := range tablesIn(lineNote.ReplaceAllString(string(b), "")) {
			if !siteTable(t) {
				errs = append(errs, f+": 표 "+t+" — 사이트 표는 site_ 로 시작한다")
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("사이트 마이그레이션이 코어 표를 건드린다:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// tablesIn lists the table names after CREATE/ALTER/DROP TABLE; DROP may name several.
func tablesIn(sql string) []string {
	var names []string
	for _, loc := range tableStmt.FindAllStringSubmatchIndex(sql, -1) {
		verb := strings.ToLower(sql[loc[2]:loc[3]])
		rest := sql[loc[1]:]
		for m := tableSkip.FindString(rest); m != ""; m = tableSkip.FindString(rest) {
			rest = rest[len(m):]
		}
		for {
			id := tableIdent.FindString(rest)
			if id == "" {
				break
			}
			names = append(names, id)
			rest = strings.TrimLeft(rest[len(id):], " \t\r\n")
			if verb != "drop" || !strings.HasPrefix(rest, ",") {
				break
			}
			rest = strings.TrimLeft(rest[1:], " \t\r\n")
		}
	}
	return names
}

// siteTable looks at the table part of [schema.]table, quotes removed.
func siteTable(name string) bool {
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return strings.HasPrefix(strings.ToLower(strings.Trim(name, `"`)), "site_")
}
