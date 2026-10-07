package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

// pgDumpTarget splits the DSN for pg_dump: the password goes to the child's
// environment (PGPASSWORD), not its argv — /proc/<pid>/cmdline 는 같은 서버의
// 모든 계정이 읽는다.
func pgDumpTarget(dsn string) (dbname, password string) {
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		// ponytail: key=value DSN 은 그대로 넘긴다(비밀번호가 있으면 인자에 보인다).
		// 설치 마법사가 쓰는 형식은 URL 이다.
		return dsn, ""
	}
	if u.User != nil {
		password, _ = u.User.Password()
		u.User = url.User(u.User.Username())
	}
	if q := u.Query(); q.Has("password") {
		password = q.Get("password")
		q.Del("password")
		u.RawQuery = q.Encode()
	}
	return u.String(), password
}

// pgDump is A-604's DumpDB: `pg_dump --format=custom` of the site database (FR-713).
//
// **세션 표의 데이터는 뺀다.** 백업 파일이 새면 그 안의 로그인 세션으로 지금 사이트에
// 바로 들어갈 수 있다 — 복원한 사이트는 모두 다시 로그인할 뿐이다.
func pgDump(dsn string) func(context.Context, io.Writer) error {
	return func(ctx context.Context, w io.Writer) error {
		dbname, password := pgDumpTarget(dsn)
		cmd := exec.CommandContext(ctx, "pg_dump", "--format=custom",
			"--exclude-table-data=public.sessions", "--dbname="+dbname)
		cmd.Env = os.Environ()
		if password != "" {
			cmd.Env = append(cmd.Env, "PGPASSWORD="+password)
		}
		var stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = w, &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("pg_dump: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return nil
	}
}
