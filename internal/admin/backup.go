package admin

import (
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/emirue/ondolith/internal/backup"
)

// backupKeys are A-604's automatic-backup settings (FR-715).
var backupKeys = []string{"backup.auto", "backup.hour", "backup.keep"}

// BackupForm is A-604 GET (FR-713, FR-715).
func (d *Deps) BackupForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "settings.update"); !ok {
		return
	}
	d.renderBackup(w, r, http.StatusOK, "")
}

// renderBackup draws A-604 — **정상 경로와 오류 경로가 같은 함수다** (renderSettings 와
// 같은 이유: 오류 경로가 화면이 색인하는 값을 빠뜨리면 422 대신 500 이 나간다).
func (d *Deps) renderBackup(w http.ResponseWriter, r *http.Request, code int, msg string) {
	kv, err := d.Content.Settings(r.Context(), backupKeys...)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	// 고른 적이 없으면 0시(사용이 가장 적은 자정)·7개다 — 화면이 빈 칸을 보이지 않게.
	// 스케줄러의 기본값(app.backupSchedule)과 같아야 한다.
	if kv["backup.hour"] == "" {
		kv["backup.hour"] = "0"
	}
	if kv["backup.keep"] == "" {
		kv["backup.keep"] = "7"
	}
	files, err := backup.List(d.BackupDir)
	if err != nil && d.Logger != nil {
		d.Logger.Error("백업 목록", "err", err)
	}
	d.Render(w, r, "admin/backup.html", code, map[string]any{
		"Settings": kv, "Files": files, "Error": msg,
		// 시각은 서버 시계로 돈다. Lightsail 은 보통 UTC 다 — 어느 시계인지 화면에 적는다.
		"Zone": time.Now().Format("MST -07:00"),
	})
}

// BackupDownload is A-604 POST. `action` picks one of three:
//
//   - (없음) 지금 백업을 만들어 내려받는다 (FR-713)
//   - settings 자동 백업을 켜고 끄고 시각·보관 개수를 정한다 (FR-715)
//   - file 저장된 자동 백업 하나를 내려받는다 (FR-715)
//
// **셋 다 최고 관리자 + 비밀번호 재확인이다.** 덤프에는 모든 회원의 비밀번호 해시와 봉인된
// 자격증명이 있다 — 사이트를 통째로 들고 나가는 단추이고, 저장된 파일도 같은 내용이다.
// 자동 백업을 끄는 것도 같은 사람만 한다. 셋 다 작업 로그에 남는다.
func (d *Deps) BackupDownload(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "settings.update")
	if !ok {
		return
	}
	if !c.IsSuperuser() {
		d.renderBackup(w, r, http.StatusForbidden, "백업은 최고 관리자만 다룰 수 있습니다.")
		return
	}
	if !reauthOK(c, r) {
		d.renderBackup(w, r, http.StatusForbidden, "비밀번호를 다시 입력하세요.")
		return
	}
	switch r.PostFormValue("action") {
	case "settings":
		d.saveBackupSettings(w, r, c)
		return
	case "file":
		d.sendStoredBackup(w, r, c)
		return
	}
	if !backup.Mu.TryLock() {
		d.renderBackup(w, r, http.StatusConflict, "다른 백업이 진행 중입니다. 끝난 뒤 다시 시도하세요.")
		return
	}
	defer backup.Mu.Unlock()

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
	if err := backup.Write(w, tmp, now, d.UploadDir); err != nil && d.Logger != nil {
		d.Logger.Error("백업 zip 쓰기 중단", "err", err)
	}
}

func (d *Deps) backupError(w http.ResponseWriter, r *http.Request, err error, msg string) {
	if d.Logger != nil {
		d.Logger.Error("백업 실패", "err", err)
	}
	d.renderBackup(w, r, http.StatusInternalServerError, msg)
}

// saveBackupSettings is A-604's action=settings (FR-715).
func (d *Deps) saveBackupSettings(w http.ResponseWriter, r *http.Request, c Caller) {
	hour, err := strconv.Atoi(r.PostFormValue("backup.hour"))
	if err != nil || hour < 0 || hour > 23 {
		d.renderBackup(w, r, http.StatusUnprocessableEntity, "자동 백업 시각은 0~23시 중 하나입니다.")
		return
	}
	// 상한은 디스크다 — 백업 하나가 DB 와 업로드 폴더 전체다.
	keep, err := strconv.Atoi(r.PostFormValue("backup.keep"))
	if err != nil || keep < 1 || keep > 30 {
		d.renderBackup(w, r, http.StatusUnprocessableEntity, "보관 개수는 1~30개입니다.")
		return
	}
	auto, state := "", "꺼짐"
	if r.PostFormValue("backup.auto") != "" {
		auto, state = "1", "켜짐"
	}
	if err := d.Content.PutSettings(r.Context(), map[string]string{
		"backup.auto": auto, "backup.hour": strconv.Itoa(hour), "backup.keep": strconv.Itoa(keep),
	}); err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.log(r, c, "backup.settings", "system", "backup",
		"자동 백업 "+state+" · 매일 "+strconv.Itoa(hour)+"시 · "+strconv.Itoa(keep)+"개 보관")
	http.Redirect(w, r, "/admin/backup", http.StatusSeeOther)
}

// sendStoredBackup is A-604's action=file: one stored automatic backup (FR-715).
// 이름은 backup.Open 이 모양을 보고 os.Root 로 연다 — 백업 폴더 밖으로 나가지 않는다.
func (d *Deps) sendStoredBackup(w http.ResponseWriter, r *http.Request, c Caller) {
	name := r.PostFormValue("name")
	f, err := backup.Open(d.BackupDir, name)
	if err != nil {
		d.renderBackup(w, r, http.StatusNotFound, "그 백업 파일이 없습니다.")
		return
	}
	defer f.Close()
	d.log(r, c, "backup.download", "system", "backup", "저장된 백업 내려받기 "+name)
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	h.Set("Cache-Control", "no-store")
	if _, err := io.Copy(w, f); err != nil && d.Logger != nil {
		d.Logger.Error("저장된 백업 보내기 중단", "err", err)
	}
}
