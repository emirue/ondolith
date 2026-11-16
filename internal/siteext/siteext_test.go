package siteext

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
)

func ok(http.ResponseWriter, *http.Request) {}

// **부팅 검사: 사이트 라우트가 틀리면 서버가 뜨지 않는다** (D42 4절 5). 틀린 라우트로 뜨면 그 앱 기능이 조용히
// 없고, 겹치는 패턴은 http.ServeMux 가 부팅 중 패닉으로 알린다 — 무엇이 틀렸는지 한 번에 모아 말한다.
func TestValidateRefusesBadRoutes(t *testing.T) {
	good := []Route{
		{Method: "GET", Path: "/v1/notes", Auth: AuthApp, Handler: ok},
		{Method: "POST", Path: "/v1/notes", Auth: AuthApp, Handler: ok},
		{Method: "GET", Path: "/v1/notes/{id}", Auth: AuthApp, Handler: ok},
		{Method: "DELETE", Path: "/v1/files/{rest...}", Auth: AuthApp, Handler: ok},
		{Method: "PATCH", Path: "/v1/me.json", Auth: AuthApp, Handler: ok},
		{Method: "GET", Path: "/authors", Auth: AuthApp, Handler: ok}, // auth 로 시작해도 마디가 다르면 사이트 것
	}
	if err := (Extension{API: good}).Validate(); err != nil {
		t.Fatalf("맞는 라우트를 거부했다: %v", err)
	}
	for _, tc := range []struct {
		name  string
		route Route
		want  string
	}{
		{"빈 경로", Route{Method: "GET", Path: "", Auth: AuthApp, Handler: ok}, "경로"},
		{"/ 로 시작하지 않음", Route{Method: "GET", Path: "v1/x", Auth: AuthApp, Handler: ok}, "경로"},
		{"상위 폴더", Route{Method: "GET", Path: "/v1/../admin", Auth: AuthApp, Handler: ok}, "경로"},
		{"빈 마디", Route{Method: "GET", Path: "/v1//x", Auth: AuthApp, Handler: ok}, "경로"},
		{"공백", Route{Method: "GET", Path: "/v1/a b", Auth: AuthApp, Handler: ok}, "경로"},
		{"질의 문자열", Route{Method: "GET", Path: "/v1/x?y=1", Auth: AuthApp, Handler: ok}, "경로"},
		{"끝 표시", Route{Method: "GET", Path: "/v1/{$}", Auth: AuthApp, Handler: ok}, "경로"},
		{"모르는 메서드", Route{Method: "TRACE", Path: "/v1/x", Auth: AuthApp, Handler: ok}, "메서드"},
		{"소문자 메서드", Route{Method: "get", Path: "/v1/x", Auth: AuthApp, Handler: ok}, "메서드"},
		{"처리기 없음", Route{Method: "GET", Path: "/v1/x", Auth: AuthApp}, "처리기"},
		// 인증 방식을 정하지 않으면 거부한다 — 빈 값이 「키만」이면 사람 데이터 라우트에 site.Person 을 빠뜨린 것이
		// 조용히 열린다(키는 비밀이 아니다). 닫힌 쪽이 기본값이다.
		{"인증 방식 없음", Route{Method: "GET", Path: "/v1/x", Handler: ok}, "인증"},
		{"모르는 인증 방식", Route{Method: "GET", Path: "/v1/x", Auth: Auth(99), Handler: ok}, "인증"},
		// 첫 마디 auth 는 코어의 문자 인증 로그인 몫이다 (D42 8절). 지금 막지 않으면 코어를 올리는 순간 겹친다.
		{"코어 몫 주소", Route{Method: "POST", Path: "/auth/code", Auth: AuthApp, Handler: ok}, "코어"},
		{"코어 몫 주소 자체", Route{Method: "GET", Path: "/auth", Auth: AuthApp, Handler: ok}, "코어"},
		{"같은 것 두 번", Route{Method: "GET", Path: "/v1/notes", Auth: AuthApp, Handler: ok}, "겹친다"},
		{"패턴이 겹침", Route{Method: "GET", Path: "/v1/notes/{other}", Auth: AuthApp, Handler: ok}, "겹친다"},
	} {
		err := (Extension{API: append(append([]Route{}, good...), tc.route)}).Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want 「%s」 오류", tc.name, err, tc.want)
		}
	}
}

// **사이트 표는 site_ 로 시작한다** (D42 지배 원칙 3). 코어를 올릴 때 코어 표와 부딪히지 않게 하는 실수 방지다 —
// 보안 경계가 아니다(사이트 코드는 DB 를 그대로 쓴다). 만들기·바꾸기·지우기의 표 이름을 본다.
func TestCheckMigrationsWantsSitePrefixedTables(t *testing.T) {
	good := fstest.MapFS{
		"00001_workers.sql": {Data: []byte("-- +goose Up\nCREATE TABLE site_workers (id bigserial PRIMARY KEY);\n" +
			"create table if not exists \"site_days\" (id int);\nALTER TABLE site_workers ADD COLUMN phone text;\n" +
			"CREATE INDEX ON site_workers (phone);\n-- 주석의 DROP TABLE users 는 문장이 아니다\n-- +goose Down\nDROP TABLE IF EXISTS site_days, site_workers;\n")},
		"README.md": {Data: []byte("CREATE TABLE users — 문서는 보지 않는다")},
	}
	if err := CheckMigrations(good); err != nil {
		t.Fatalf("맞는 마이그레이션을 거부했다: %v", err)
	}
	// embed.FS 를 fs.Sub 하지 않고 넘기면 파일이 migrations/ 아래에 있어 루트에는 .sql 이 없다. 그대로 두면 검사는
	// 0개를 보고 통과하고, 설치를 마친 뒤에야 goose 가 「마이그레이션 없음」으로 운영 전환을 멈춘다 (D42 4절 5).
	nested := fstest.MapFS{"migrations/00001_workers.sql": {Data: []byte("-- +goose Up\nCREATE TABLE site_w (id int);\n")}}
	if err := CheckMigrations(nested); err == nil || !strings.Contains(err.Error(), "fs.Sub") {
		t.Errorf("루트에 .sql 이 없는 마이그레이션: %v, want fs.Sub 를 짚는 거부", err)
	}
	for _, sql := range []string{
		"CREATE TABLE workers (id int);",
		"create table if not exists users (id int);",
		"ALTER TABLE users ADD COLUMN x int;",
		"alter table only public.posts add column x int;",
		"DROP TABLE sessions;",
		"DROP TABLE IF EXISTS site_a, settings;",
		"CREATE TABLE \"orders\" (id int);",
	} {
		bad := fstest.MapFS{"00002_bad.sql": {Data: []byte("-- +goose Up\n" + sql + "\n")}}
		if err := CheckMigrations(bad); err == nil || !strings.Contains(err.Error(), "00002_bad.sql") {
			t.Errorf("%q: %v, want 그 파일을 짚는 거부", sql, err)
		}
	}
}

// **사이트 관리자 화면과 권한도 부팅 전에 거른다** (D42 7절). 권한 키·작업 로그 동작은 DB 가 「두 마디」만 받으므로
// 여기서 같은 모양을 요구한다 — 아니면 설치를 마친 뒤 INSERT 가 실패하고서야 안다. 변수가 든 주소는 메뉴에 걸 수
// 없다(눌러 갈 주소가 없다). 화면이 쓰는 권한은 선언한 것이어야 한다 — 선언하지 않은 키는 권한 표에 없다.
func TestValidateRefusesBadAdminPages(t *testing.T) {
	perms := []Permission{{Key: "site.workers", Name: "인부 관리"}, {Key: "site.pay_2", Name: "입금 기록"}}
	good := []AdminPage{
		{Menu: "인부", Path: "/workers", Permission: "site.workers", Handler: ok},
		{Path: "/workers/{id}", Permission: "site.workers", Handler: ok}, // 메뉴 없는 상세 화면
		{Menu: "입금", Path: "/pay", Permission: "site.pay_2", Handler: ok},
	}
	if err := (Extension{Permissions: perms, Admin: good}).Validate(); err != nil {
		t.Fatalf("맞는 화면을 거부했다: %v", err)
	}
	for _, tc := range []struct {
		name string
		perm *Permission
		page *AdminPage
		want string
	}{
		{"권한 키 접두사", &Permission{Key: "workers.view", Name: "x"}, nil, "site."},
		{"권한 키 세 마디", &Permission{Key: "site.workers.view", Name: "x"}, nil, "site."},
		{"권한 키 대문자", &Permission{Key: "site.Workers", Name: "x"}, nil, "site."},
		{"권한 키 숫자로 시작", &Permission{Key: "site.1x", Name: "x"}, nil, "site."},
		{"권한 이름 없음", &Permission{Key: "site.x", Name: "  "}, nil, "이름"},
		{"권한 이름 61자", &Permission{Key: "site.x", Name: strings.Repeat("가", 61)}, nil, "이름"},
		{"권한 두 번", &Permission{Key: "site.workers", Name: "또"}, nil, "두 번"},
		{"화면 경로", nil, &AdminPage{Menu: "x", Path: "/a/../b", Permission: "site.workers", Handler: ok}, "경로"},
		{"화면 처리기 없음", nil, &AdminPage{Menu: "x", Path: "/x", Permission: "site.workers"}, "처리기"},
		{"선언하지 않은 권한", nil, &AdminPage{Menu: "x", Path: "/x", Permission: "site.other", Handler: ok}, "선언"},
		{"권한 없음", nil, &AdminPage{Menu: "x", Path: "/x", Handler: ok}, "선언"},
		{"변수 주소에 메뉴", nil, &AdminPage{Menu: "x", Path: "/x/{id}", Permission: "site.workers", Handler: ok}, "메뉴"},
		{"메뉴 31자", nil, &AdminPage{Menu: strings.Repeat("가", 31), Path: "/x", Permission: "site.workers", Handler: ok}, "메뉴"},
		{"같은 주소 두 번", nil, &AdminPage{Menu: "또", Path: "/workers", Permission: "site.workers", Handler: ok}, "겹친다"},
		{"패턴이 겹침", nil, &AdminPage{Path: "/workers/{other}", Permission: "site.workers", Handler: ok}, "겹친다"},
	} {
		e := Extension{Permissions: append([]Permission{}, perms...), Admin: append([]AdminPage{}, good...)}
		if tc.perm != nil {
			e.Permissions = append(e.Permissions, *tc.perm)
		}
		if tc.page != nil {
			e.Admin = append(e.Admin, *tc.page)
		}
		if err := e.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want 「%s」 오류", tc.name, err, tc.want)
		}
	}
}

// 작업 로그 동작도 권한과 같은 모양이다 — DB 가 두 마디만 받는다. site. 로 시작해야 코어 동작과 섞이지 않는다.
func TestValidKey(t *testing.T) {
	for k, want := range map[string]bool{
		"site.worker_pay": true, "site.a": true, "site.a1_b": true,
		"": false, "site.": false, "site": false, "worker.pay": false, "site.a.b": false,
		"site.Pay": false, "site.1a": false, "site.a-b": false, " site.a": false,
	} {
		if got := ValidKey(k); got != want {
			t.Errorf("ValidKey(%q) = %v, want %v", k, got, want)
		}
	}
}

// **사람 인증 라우트는 문자 로그인이 있어야 한다** (D42 4절 5). Lookup 없이 site.Person 을 적으면 토큰을 받을 길이
// 없어 그 라우트는 영영 401 이다 — 부팅에서 알린다.
func TestPersonRoutesNeedPhoneLogin(t *testing.T) {
	person := Route{Method: "GET", Path: "/v1/me", Auth: AuthPerson, Handler: ok}
	if err := (Extension{API: []Route{person}}).Validate(); err == nil || !strings.Contains(err.Error(), "Phone.Lookup") {
		t.Errorf("Lookup 없는 site.Person = %v, want Phone.Lookup 오류", err)
	}
	lookup := func(context.Context, string) (string, bool, error) { return "", false, nil }
	if err := (Extension{API: []Route{person}, Phone: PhoneLogin{Lookup: lookup}}).Validate(); err != nil {
		t.Errorf("Lookup 있는 site.Person = %v", err)
	}
	if err := (Extension{API: []Route{{Method: "GET", Path: "/v1/x", Auth: Auth(3), Handler: ok}}}).Validate(); err == nil {
		t.Error("모르는 인증 방식을 받았다")
	}
}
