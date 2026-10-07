package admin

import (
	"archive/zip"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
)

// backupMu lets one backup run at a time — 덤프는 DB 를 끝까지 읽고 업로드 폴더를 통째로
// 보낸다. 저사양 서버에서 두 번 겹치면 사이트가 같이 느려진다.
// ponytail: 프로세스 하나 기준이다. 인스턴스가 여럿이면 각자 하나씩이다(수평 확장은 비목표, D00).
var backupMu sync.Mutex

// BackupForm is A-604 GET (FR-713).
func (d *Deps) BackupForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "settings.update"); !ok {
		return
	}
	d.Render(w, r, "admin/backup.html", http.StatusOK, map[string]any{})
}

// BackupDownload is A-604 POST: the database dump and the upload folder as one zip.
//
// **최고 관리자 + 비밀번호 재확인** (FR-713). 덤프에는 모든 회원의 비밀번호 해시와 봉인된
// 자격증명이 있다 — 사이트를 통째로 들고 나가는 단추다. 받은 사실은 작업 로그에 남는다.
func (d *Deps) BackupDownload(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "settings.update")
	if !ok {
		return
	}
	fail := func(code int, msg string) {
		d.Render(w, r, "admin/backup.html", code, map[string]any{"Error": msg})
	}
	if !c.IsSuperuser() {
		fail(http.StatusForbidden, "백업은 최고 관리자만 내려받을 수 있습니다.")
		return
	}
	if !reauthOK(c, r) {
		fail(http.StatusForbidden, "비밀번호를 다시 입력하세요.")
		return
	}
	if !backupMu.TryLock() {
		fail(http.StatusConflict, "다른 백업이 진행 중입니다. 끝난 뒤 다시 시도하세요.")
		return
	}
	defer backupMu.Unlock()

	// **덤프를 먼저 끝까지 받는다.** 바로 응답으로 흘리면 pg_dump 가 중간에 실패해도 200 과
	// 잘린 zip 이 나가고, 사람은 그것을 백업으로 믿는다.
	tmp, err := os.CreateTemp("", "ondolith-*.dump")
	if err != nil {
		d.backupError(w, r, err, "임시 파일을 만들 수 없습니다.")
		return
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := d.DumpDB(r.Context(), tmp); err != nil {
		msg := "DB 덤프에 실패했습니다. 서버 로그를 확인하세요 — pg_dump 가 DB 서버보다 낮은 버전이면 실패합니다."
		if errors.Is(err, exec.ErrNotFound) {
			msg = "서버에 pg_dump 가 없습니다. PostgreSQL 클라이언트(postgresql-client)를 설치하세요."
		}
		d.backupError(w, r, err, msg)
		return
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		d.backupError(w, r, err, "임시 파일을 읽을 수 없습니다.")
		return
	}

	d.log(r, c, "backup.download", "system", "backup", "백업 내려받기 (DB 덤프 + 업로드 파일)")
	now := time.Now()
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", `attachment; filename="ondolith-backup-`+now.Format("20060102-1504")+`.zip"`)
	h.Set("Cache-Control", "no-store")
	// 여기서부터는 응답이 나가는 중이라 오류 화면을 그릴 수 없다 — 로그만 남는다.
	if err := writeBackup(w, tmp, now, d.UploadDir); err != nil && d.Logger != nil {
		d.Logger.Error("백업 zip 쓰기 중단", "err", err)
	}
}

func (d *Deps) backupError(w http.ResponseWriter, r *http.Request, err error, msg string) {
	if d.Logger != nil {
		d.Logger.Error("백업 실패", "err", err)
	}
	d.Render(w, r, "admin/backup.html", http.StatusInternalServerError, map[string]any{"Error": msg})
}

// writeBackup streams ondolith.dump and uploads/… as one zip.
//
// **압축하지 않는다(Store).** 덤프(custom 형식)는 이미 압축돼 있고 사진은 JPEG·PNG 다 —
// 다시 deflate 하면 크기는 거의 그대로이고 CPU 만 쓴다. 업로드 폴더는 os.Root 로 걷는다:
// 폴더 밖을 가리키는 심볼릭 링크를 따라 나가지 않는다.
func writeBackup(w io.Writer, dump io.Reader, now time.Time, uploads string) error {
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
