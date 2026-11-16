package siteext

import (
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
		{Method: "GET", Path: "/v1/notes", Handler: ok},
		{Method: "POST", Path: "/v1/notes", Handler: ok},
		{Method: "GET", Path: "/v1/notes/{id}", Handler: ok},
		{Method: "DELETE", Path: "/v1/files/{rest...}", Handler: ok},
		{Method: "PATCH", Path: "/v1/me.json", Handler: ok},
	}
	if err := (Extension{API: good}).Validate(); err != nil {
		t.Fatalf("맞는 라우트를 거부했다: %v", err)
	}
	for _, tc := range []struct {
		name  string
		route Route
		want  string
	}{
		{"빈 경로", Route{Method: "GET", Path: "", Handler: ok}, "경로"},
		{"/ 로 시작하지 않음", Route{Method: "GET", Path: "v1/x", Handler: ok}, "경로"},
		{"상위 폴더", Route{Method: "GET", Path: "/v1/../admin", Handler: ok}, "경로"},
		{"빈 마디", Route{Method: "GET", Path: "/v1//x", Handler: ok}, "경로"},
		{"공백", Route{Method: "GET", Path: "/v1/a b", Handler: ok}, "경로"},
		{"질의 문자열", Route{Method: "GET", Path: "/v1/x?y=1", Handler: ok}, "경로"},
		{"끝 표시", Route{Method: "GET", Path: "/v1/{$}", Handler: ok}, "경로"},
		{"모르는 메서드", Route{Method: "TRACE", Path: "/v1/x", Handler: ok}, "메서드"},
		{"소문자 메서드", Route{Method: "get", Path: "/v1/x", Handler: ok}, "메서드"},
		{"처리기 없음", Route{Method: "GET", Path: "/v1/x"}, "처리기"},
		{"같은 것 두 번", Route{Method: "GET", Path: "/v1/notes", Handler: ok}, "겹친다"},
		{"패턴이 겹침", Route{Method: "GET", Path: "/v1/notes/{other}", Handler: ok}, "겹친다"},
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
