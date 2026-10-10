package backup

import "testing"

// **비밀번호는 pg_dump 의 인자가 아니라 환경으로 넘긴다** (FR-713). 인자는
// /proc/<pid>/cmdline 으로 같은 서버의 모든 계정에 보인다.
func TestPgDumpTargetKeepsThePasswordOutOfArgv(t *testing.T) {
	for _, tc := range []struct{ name, dsn, db, pw string }{
		{"URL 사용자 정보", "postgres://u:p%40ss@127.0.0.1:5432/site?sslmode=disable",
			"postgres://u@127.0.0.1:5432/site?sslmode=disable", "p@ss"},
		{"쿼리의 password", "postgresql://u@h/site?password=x&sslmode=require",
			"postgresql://u@h/site?sslmode=require", "x"},
		{"비밀번호 없음", "postgres://u@h/site", "postgres://u@h/site", ""},
		{"key=value 는 그대로", "host=h user=u dbname=site", "host=h user=u dbname=site", ""},
	} {
		db, pw := Target(tc.dsn)
		if db != tc.db || pw != tc.pw {
			t.Errorf("%s: (%q, %q), want (%q, %q)", tc.name, db, pw, tc.db, tc.pw)
		}
	}
}
