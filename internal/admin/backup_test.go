package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// **백업은 최고 관리자가 비밀번호를 다시 넣고 받는다** (FR-713, A-604). 덤프에는 모든
// 회원의 비밀번호 해시와 봉인된 자격증명이 들어 있다. 받은 zip 에는 DB 덤프와 업로드
// 파일이 함께 있고, 받은 사실은 작업 로그에 남는다.
func TestBackupIsSuperuserOnlyReauthedLoggedAndComplete(t *testing.T) {
	op := &fakeCaller{perms: map[string]bool{"settings.update": true}, email: "op@example.com"}
	d, pool := fixture(t, op)
	ctx := context.Background()
	d.UploadDir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(d.UploadDir, "media", "2026", "10"), 0o755); err != nil {
		t.Fatal(err)
	}
	photo := []byte("\xff\xd8\xff 사진")
	if err := os.WriteFile(filepath.Join(d.UploadDir, "media", "2026", "10", "abc"), photo, 0o644); err != nil {
		t.Fatal(err)
	}
	dumped := 0
	d.DumpDB = func(_ context.Context, w io.Writer) error {
		dumped++
		_, err := w.Write([]byte("PGDMP 가짜 덤프"))
		return err
	}
	logged := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM operation_logs WHERE action = 'backup.download'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if rec := post(d.BackupDownload, "/admin/backup", url.Values{}); rec.Code != http.StatusForbidden || dumped != 0 {
		t.Fatalf("settings.update 만 있는 운영자 = HTTP %d, 덤프 %d번, want 403·0번", rec.Code, dumped)
	}
	su := &fakeCaller{superuser: true, reauth: true, email: "su@example.com", password: "correct horse battery"}
	d.Caller = func(*http.Request) Caller { return su }
	if rec := post(d.BackupDownload, "/admin/backup", url.Values{}); rec.Code != http.StatusForbidden || dumped != 0 {
		t.Fatalf("재인증 없이 = HTTP %d, 덤프 %d번, want 403·0번", rec.Code, dumped)
	}

	rec := post(d.BackupDownload, "/admin/backup", url.Values{"password": {"correct horse battery"}})
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("재인증 뒤 = HTTP %d %q (%s)", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); !bytes.Contains([]byte(cd), []byte("attachment")) {
		t.Errorf("Content-Disposition = %q, want attachment", cd)
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("받은 파일이 zip 이 아니다: %v", err)
	}
	got := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		got[f.Name] = string(b)
	}
	if got["ondolith.dump"] != "PGDMP 가짜 덤프" || got["uploads/media/2026/10/abc"] != string(photo) || len(got) != 2 {
		t.Errorf("zip 내용 = %v, want ondolith.dump 와 uploads/media/2026/10/abc", got)
	}
	if n := logged(); n != 1 {
		t.Errorf("백업 작업 로그 %d건, want 1", n)
	}

	// 덤프가 실패하면 zip 을 내지 않는다 — 반쯤 쓴 파일을 백업으로 믿게 하지 않는다.
	d.DumpDB = func(context.Context, io.Writer) error { return errors.New("pg_dump: server version mismatch") }
	rec = post(d.BackupDownload, "/admin/backup", url.Values{})
	if rec.Code != http.StatusInternalServerError || rec.Header().Get("Content-Type") == "application/zip" {
		t.Errorf("덤프 실패 = HTTP %d %q, want 500 이고 zip 아님", rec.Code, rec.Header().Get("Content-Type"))
	}
	if n := logged(); n != 1 {
		t.Errorf("실패한 백업이 작업 로그에 「내려받기」 로 남았다: %d건", n)
	}
}
