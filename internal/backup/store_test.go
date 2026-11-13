package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeDump(calls *int, err error) func(context.Context, io.Writer) error {
	return func(_ context.Context, w io.Writer) error {
		*calls++
		if err != nil {
			return err
		}
		_, werr := w.Write([]byte("PGDMP"))
		return werr
	}
}

func uploadsWithOneFile(t *testing.T) string {
	t.Helper()
	up := t.TempDir()
	if err := os.WriteFile(filepath.Join(up, "a.jpg"), []byte("사진"), 0o644); err != nil {
		t.Fatal(err)
	}
	return up
}

// **저장한 백업은 최신 keep 개만 남고, 임시 파일은 남지 않는다** (FR-715). 파일은
// 회원 비밀번호 해시가 든 덤프라 주인만 읽는다(0600).
func TestSaveKeepsTheNewestAndLeavesNoTempFiles(t *testing.T) {
	dir, up := t.TempDir(), uploadsWithOneFile(t)
	n := 0
	base := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		if _, err := Save(context.Background(), fakeDump(&n, nil), up, dir, base.AddDate(0, 0, i), 2, nil); err != nil {
			t.Fatal(err)
		}
	}
	files, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Name != "auto-20261004-0300.zip" || files[1].Name != "auto-20261003-0300.zip" {
		t.Fatalf("남은 백업 = %+v, want 최신 둘(최신이 먼저)", files)
	}
	if all, _ := os.ReadDir(dir); len(all) != 2 {
		t.Errorf("폴더에 %d개 — 임시 파일이 남았다", len(all))
	}
	info, err := os.Stat(filepath.Join(dir, files[0].Name))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("백업 파일 권한 = %v (%v), want 0600", info.Mode().Perm(), err)
	}
	zr, err := zip.OpenReader(filepath.Join(dir, files[0].Name))
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	got := map[string]bool{}
	for _, f := range zr.File {
		got[f.Name] = true
	}
	if !got["ondolith.dump"] || !got["uploads/a.jpg"] {
		t.Errorf("zip 내용 = %v", got)
	}
}

// 덤프가 실패하면 파일을 남기지 않고, 있던 백업도 지우지 않는다 — 실패한 밤이
// 이전 백업을 밀어내면 남는 것이 없다.
func TestSaveFailureKeepsTheOldBackups(t *testing.T) {
	dir, up := t.TempDir(), uploadsWithOneFile(t)
	n := 0
	day := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	if _, err := Save(context.Background(), fakeDump(&n, nil), up, dir, day, 1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Save(context.Background(), fakeDump(&n, errors.New("pg_dump: 연결 거부")), up, dir, day.AddDate(0, 0, 1), 1, nil); err == nil {
		t.Fatal("덤프 실패가 오류로 돌아오지 않았다")
	}
	all, _ := os.ReadDir(dir)
	if len(all) != 1 || all[0].Name() != "auto-20261001-0300.zip" {
		t.Errorf("실패 뒤 폴더 = %v, want 어제 백업 하나", all)
	}
}

// 내려받을 이름은 우리가 만든 모양뿐이다 — 경로 조각이 섞이면 서버의 다른 파일이다.
func TestValidNameAcceptsOnlyOurBackups(t *testing.T) {
	for name, want := range map[string]bool{
		"auto-20261001-0300.zip":    true,
		"../auto-20261001-0300.zip": false,
		"auto-20261001-0300.zip/..": false,
		"auto-2026.zip":             false,
		".zip-123":                  false,
		"ondolith.json":             false,
	} {
		if ValidName(name) != want {
			t.Errorf("ValidName(%q) = %v, want %v", name, !want, want)
		}
	}
	if _, err := Open(t.TempDir(), "../../etc/passwd"); err == nil {
		t.Error("경로를 품은 이름이 열렸다")
	}
}

// **정한 시각에 하루 한 번** (FR-715). 꺼져 있거나 시각이 아니면 돌지 않고, 실패해도
// 그날은 다시 시도하지 않는다(매분 실패하는 pg_dump 가 로그를 덮는다). 수동 백업이
// 자물쇠를 쥐고 있으면 양보하고 다음 분에 돈다. 재시작해도 오늘 만든 것이 있으면 또 만들지 않는다.
func TestSchedulerRunsOncePerDayAtTheHour(t *testing.T) {
	dir, up := t.TempDir(), uploadsWithOneFile(t)
	n := 0
	var dumpErr error
	sch := Schedule{Enabled: false, Hour: 3, Keep: 7}
	var records []string
	s := &Scheduler{
		Settings: func() Schedule { return sch },
		Dump: func(ctx context.Context, w io.Writer) error {
			return fakeDump(&n, dumpErr)(ctx, w)
		},
		Uploads: up, Dir: dir,
		Record: func(summary string, err error) {
			records = append(records, summary)
		},
	}
	ctx := context.Background()
	at := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, time.UTC) }

	s.Tick(ctx, at(1, 3, 0))
	if n != 0 {
		t.Fatalf("꺼져 있는데 %d번 돌았다", n)
	}
	sch.Enabled = true
	s.Tick(ctx, at(1, 2, 59))
	s.Tick(ctx, at(1, 3, 0))
	s.Tick(ctx, at(1, 3, 1))
	if n != 1 {
		t.Fatalf("1일 3시 = %d번, want 1번", n)
	}

	dumpErr = errors.New("pg_dump: 연결 거부")
	s.Tick(ctx, at(2, 3, 0))
	s.Tick(ctx, at(2, 3, 5))
	if n != 2 || len(records) != 2 {
		t.Fatalf("실패한 날 = %d번 시도·기록 %d건, want 2·2 (그날 다시 시도하지 않는다)", n, len(records))
	}

	dumpErr = nil
	Mu.Lock()
	s.Tick(ctx, at(3, 3, 0))
	Mu.Unlock()
	if n != 2 {
		t.Fatalf("수동 백업이 도는 중에 돌았다")
	}
	s.Tick(ctx, at(3, 3, 1))
	if n != 3 {
		t.Fatalf("자물쇠가 풀린 다음 분에 돌지 않았다: %d번", n)
	}

	restarted := &Scheduler{Settings: s.Settings, Dump: s.Dump, Uploads: up, Dir: dir, Record: s.Record}
	restarted.Tick(ctx, at(3, 3, 30))
	if n != 3 {
		t.Errorf("재시작 뒤 오늘 백업이 있는데 또 만들었다")
	}
}

// memBucket is a Remote that keeps objects in memory. fail, when set, refuses every Put.
type memBucket struct {
	objects map[string][]byte
	fail    error
}

func (b *memBucket) Put(_ context.Context, key string, body io.ReadSeeker, size int64) error {
	if b.fail != nil {
		return b.fail
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return fmt.Errorf("크기 %d 라더니 %d 바이트", size, len(data))
	}
	if b.objects == nil {
		b.objects = map[string][]byte{}
	}
	b.objects[key] = data
	return nil
}

// **파일 저장소가 S3 면 자동 백업은 버킷의 backups/ 로 가고 이 서버에는 남지 않는다** (FR-722). 서버를 잃어도
// 백업이 남는 것이 목적이다 — 로컬에도 두면 디스크만 쓴다.
func TestSaveToTheBucketLeavesNothingLocal(t *testing.T) {
	dir, up := t.TempDir(), uploadsWithOneFile(t)
	n := 0
	b := &memBucket{}
	at := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	saved, err := Save(context.Background(), fakeDump(&n, nil), up, dir, at, 7, b)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Where != "s3" || saved.Name != "auto-20261114-0000.zip" || saved.Size == 0 {
		t.Errorf("Saved = %+v", saved)
	}
	data, ok := b.objects["backups/auto-20261114-0000.zip"]
	if !ok {
		t.Fatalf("버킷에 backups/auto-20261114-0000.zip 이 없다: %v", b.objects)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("올린 것이 zip 이 아니다: %v", err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["ondolith.dump"] || !names["uploads/a.jpg"] {
		t.Errorf("zip 안 = %v, want ondolith.dump·uploads/a.jpg", names)
	}
	if all, _ := os.ReadDir(dir); len(all) != 0 {
		t.Errorf("버킷에 올렸는데 로컬 폴더에 %d개가 남았다", len(all))
	}
}

// **버킷에 올리지 못하면 만든 백업을 로컬에 남긴다** — 그날 백업이 아예 없는 것보다 낫다. 오류는 돌려줘서
// 작업 로그에 실패로 남는다(조용히 로컬로 가면 운영자는 버킷에 있다고 믿는다). 덤프는 한 번만 뜬다.
func TestSaveKeepsTheBackupLocallyWhenTheBucketRefuses(t *testing.T) {
	dir, up := t.TempDir(), uploadsWithOneFile(t)
	n := 0
	b := &memBucket{fail: errors.New("AccessDenied")}
	at := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	saved, err := Save(context.Background(), fakeDump(&n, nil), up, dir, at, 7, b)
	if !errors.Is(err, ErrRemote) {
		t.Fatalf("err = %v, want ErrRemote", err)
	}
	if saved.Where != "local" || saved.Name != "auto-20261114-0000.zip" {
		t.Errorf("Saved = %+v, want 로컬의 auto-20261114-0000.zip", saved)
	}
	if files, _ := List(dir); len(files) != 1 {
		t.Errorf("로컬 백업 %d개, want 1", len(files))
	}
	if n != 1 {
		t.Errorf("덤프를 %d번 떴다 — 올리기 실패로 다시 뜨지 않는다", n)
	}
	if all, _ := os.ReadDir(dir); len(all) != 1 {
		t.Errorf("폴더에 %d개 — 임시 파일이 남았다", len(all))
	}
}

// 스케줄러: 버킷을 고르는 쪽이 오류(예: 봉인이 풀리지 않는 시크릿)여도 로컬에 백업을 남기고 실패로 기록한다.
// 성공한 날은 Done 이 받아 저장한다 — 재시작한 같은 날 다시 만들지 않는 근거가 그 값이다(버킷 백업은 로컬
// 폴더에 흔적이 없다).
func TestSchedulerSendsToTheBucketAndRemembersTheDay(t *testing.T) {
	dir, up := t.TempDir(), uploadsWithOneFile(t)
	n := 0
	b := &memBucket{}
	var remoteErr error
	sch := Schedule{Enabled: true, Hour: 0, Keep: 7}
	var records []string
	var errs []error
	var done []Saved
	s := &Scheduler{
		Settings: func() Schedule { return sch },
		Dump:     fakeDump(&n, nil), Uploads: up, Dir: dir,
		Remote: func(context.Context) (Remote, error) {
			if remoteErr != nil {
				return nil, remoteErr
			}
			return b, nil
		},
		Record: func(summary string, err error) { records = append(records, summary); errs = append(errs, err) },
		Done:   func(sv Saved) { done = append(done, sv) },
	}
	ctx := context.Background()
	day := func(d int) time.Time { return time.Date(2026, 11, d, 0, 0, 0, 0, time.Local) }

	s.Tick(ctx, day(14))
	if _, ok := b.objects["backups/auto-20261114-0000.zip"]; !ok || len(done) != 1 || done[0].Where != "s3" {
		t.Fatalf("버킷 = %v · Done = %+v", b.objects, done)
	}
	if errs[0] != nil || !strings.Contains(records[0], "버킷") {
		t.Errorf("성공 기록 = %q (%v), want 버킷에 올렸다는 말", records[0], errs[0])
	}

	// 재시작: 로컬 폴더는 비어 있다. 저장된 마지막 백업 시각이 오늘이면 다시 만들지 않는다.
	sch.Last = done[0].Time
	restarted := &Scheduler{Settings: s.Settings, Dump: s.Dump, Uploads: up, Dir: dir, Remote: s.Remote, Record: s.Record, Done: s.Done}
	restarted.Tick(ctx, day(14).Add(30*time.Second))
	if n != 1 {
		t.Errorf("재시작한 같은 날 또 만들었다 (덤프 %d번)", n)
	}

	// 버킷을 고르지 못한 날: 로컬에 남기고 실패로 기록한다.
	remoteErr = errors.New("시크릿 키를 열 수 없다")
	s.Tick(ctx, day(15))
	if files, _ := List(dir); len(files) != 1 || files[0].Name != "auto-20261115-0000.zip" {
		t.Errorf("버킷을 못 고른 날 로컬 백업 = %+v", files)
	}
	if last := errs[len(errs)-1]; !errors.Is(last, ErrRemote) {
		t.Errorf("버킷을 못 고른 날의 기록 오류 = %v, want ErrRemote", last)
	}
	if len(done) != 2 || done[1].Where != "local" {
		t.Errorf("로컬에 남긴 것도 마지막 백업이다: Done = %+v", done)
	}
}
