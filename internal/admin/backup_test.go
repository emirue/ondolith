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
	"strings"
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

// **자동 백업은 A-604 에서 켜고, 저장된 백업은 같은 화면에서 받는다** (FR-715). 둘 다
// 최고 관리자 + 재확인이다 — 저장된 파일도 지금 받는 백업과 같은 내용이다. 시각은 0~23시,
// 보관은 1~30개만 받는다. 받을 이름은 우리가 만든 모양뿐이다.
func TestAutoBackupSettingsAndStoredDownloads(t *testing.T) {
	op := &fakeCaller{perms: map[string]bool{"settings.update": true}, email: "op@example.com"}
	d, pool := fixture(t, op)
	ctx := context.Background()
	d.BackupDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(d.BackupDir, "auto-20261001-0300.zip"), []byte("저장된 zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	on := url.Values{"action": {"settings"}, "backup.auto": {"1"}, "backup.hour": {"3"}, "backup.keep": {"7"}}
	saved := func() map[string]string {
		kv, err := d.Content.Settings(ctx, "backup.auto", "backup.hour", "backup.keep")
		if err != nil {
			t.Fatal(err)
		}
		return kv
	}
	logged := func(action string) int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM operation_logs WHERE action = $1`, action).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if rec := post(d.BackupDownload, "/admin/backup", on); rec.Code != http.StatusForbidden || saved()["backup.auto"] != "" {
		t.Fatalf("운영자의 자동 백업 설정 = HTTP %d, want 403", rec.Code)
	}
	su := &fakeCaller{superuser: true, reauth: true, email: "su@example.com", password: "correct horse battery"}
	d.Caller = func(*http.Request) Caller { return su }
	if rec := post(d.BackupDownload, "/admin/backup", on); rec.Code != http.StatusForbidden {
		t.Fatalf("재인증 없이 = HTTP %d, want 403", rec.Code)
	}
	withPW := url.Values{"password": {"correct horse battery"}}
	for k, v := range on {
		withPW[k] = v
	}
	if rec := post(d.BackupDownload, "/admin/backup", withPW); rec.Code != http.StatusSeeOther {
		t.Fatalf("설정 저장 = HTTP %d (%s)", rec.Code, rec.Body.String())
	}
	if kv := saved(); kv["backup.auto"] != "1" || kv["backup.hour"] != "3" || kv["backup.keep"] != "7" {
		t.Errorf("저장된 설정 = %v", kv)
	}
	if n := logged("backup.settings"); n != 1 {
		t.Errorf("설정 변경 작업 로그 %d건, want 1", n)
	}
	for _, bad := range []url.Values{
		{"action": {"settings"}, "backup.auto": {"1"}, "backup.hour": {"24"}, "backup.keep": {"7"}},
		{"action": {"settings"}, "backup.auto": {"1"}, "backup.hour": {"새벽"}, "backup.keep": {"7"}},
		{"action": {"settings"}, "backup.auto": {"1"}, "backup.hour": {"3"}, "backup.keep": {"0"}},
		{"action": {"settings"}, "backup.auto": {"1"}, "backup.hour": {"3"}, "backup.keep": {"31"}},
	} {
		if rec := post(d.BackupDownload, "/admin/backup", bad); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%v = HTTP %d, want 422", bad, rec.Code)
		}
	}
	if kv := saved(); kv["backup.hour"] != "3" || kv["backup.keep"] != "7" {
		t.Errorf("거부한 값이 저장됐다: %v", kv)
	}
	// 끄기: 체크하지 않은 칸은 오지 않는다.
	post(d.BackupDownload, "/admin/backup", url.Values{"action": {"settings"}, "backup.hour": {"3"}, "backup.keep": {"7"}})
	if kv := saved(); kv["backup.auto"] != "" {
		t.Errorf("끌 수 없다: %v", kv)
	}

	rec := post(d.BackupDownload, "/admin/backup", url.Values{"action": {"file"}, "name": {"auto-20261001-0300.zip"}})
	if rec.Code != http.StatusOK || rec.Body.String() != "저장된 zip" ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), `attachment; filename="auto-20261001-0300.zip"`) {
		t.Fatalf("저장된 백업 받기 = HTTP %d %q %q", rec.Code, rec.Header().Get("Content-Disposition"), rec.Body.String())
	}
	if n := logged("backup.download"); n != 1 {
		t.Errorf("저장된 백업 받기 작업 로그 %d건, want 1", n)
	}
	for _, name := range []string{"../../etc/passwd", "auto-20261002-0300.zip", ""} {
		if rec := post(d.BackupDownload, "/admin/backup", url.Values{"action": {"file"}, "name": {name}}); rec.Code != http.StatusNotFound {
			t.Errorf("%q = HTTP %d, want 404", name, rec.Code)
		}
	}
}
