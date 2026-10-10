// Package backup makes the site backup — the database dump and the upload
// folder as one zip (FR-713). A-604 hands one to the superuser on demand, and
// the scheduler keeps a few on disk (FR-715). Both go through this package so
// the two never write different archives.
package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Mu lets one backup run at a time — 덤프는 DB 를 끝까지 읽고 업로드 폴더를 통째로
// 보낸다. 저사양 서버에서 두 번 겹치면 사이트가 같이 느려진다. A-604 와 자동 백업이
// 같은 자물쇠를 본다.
// ponytail: 프로세스 하나 기준이다. 인스턴스가 여럿이면 각자 하나씩이다(수평 확장은 비목표, D00).
var Mu sync.Mutex

// Target splits the DSN for pg_dump: the password goes to the child's
// environment (PGPASSWORD), not its argv — /proc/<pid>/cmdline 는 같은 서버의
// 모든 계정이 읽는다.
func Target(dsn string) (dbname, password string) {
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

// Dump returns the function that writes `pg_dump --format=custom` of the site
// database to w (FR-713).
//
// **세션 표의 데이터는 뺀다.** 백업 파일이 새면 그 안의 로그인 세션으로 지금 사이트에
// 바로 들어갈 수 있다 — 복원한 사이트는 모두 다시 로그인할 뿐이다.
func Dump(dsn string) func(context.Context, io.Writer) error {
	return func(ctx context.Context, w io.Writer) error {
		dbname, password := Target(dsn)
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

// Write streams ondolith.dump and uploads/… as one zip.
//
// **압축하지 않는다(Store).** 덤프(custom 형식)는 이미 압축돼 있고 사진은 JPEG·PNG 다 —
// 다시 deflate 하면 크기는 거의 그대로이고 CPU 만 쓴다. 업로드 폴더는 os.Root 로 걷는다:
// 폴더 밖을 가리키는 심볼릭 링크를 따라 나가지 않는다.
func Write(w io.Writer, dump io.Reader, now time.Time, uploads string) error {
	zw := zip.NewWriter(w)
	add := func(name string, mod time.Time, r io.Reader) error {
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store, Modified: mod})
		if err != nil {
			return err
		}
		_, err = io.Copy(fw, r)
		return err
	}
	if err := add("ondolith.dump", now, dump); err != nil {
		return err
	}
	root, err := os.OpenRoot(uploads)
	if err != nil {
		return err
	}
	defer root.Close()
	err = fs.WalkDir(root.FS(), ".", func(p string, de fs.DirEntry, err error) error {
		if err != nil || !de.Type().IsRegular() {
			return err
		}
		info, err := de.Info()
		if err != nil {
			return err
		}
		f, err := root.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		return add("uploads/"+p, info.ModTime(), f)
	})
	if err != nil {
		return err
	}
	return zw.Close()
}
