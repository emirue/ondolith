package backup

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
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
		if _, err := Save(context.Background(), fakeDump(&n, nil), up, dir, base.AddDate(0, 0, i), 2); err != nil {
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
	if _, err := Save(context.Background(), fakeDump(&n, nil), up, dir, day, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := Save(context.Background(), fakeDump(&n, errors.New("pg_dump: 연결 거부")), up, dir, day.AddDate(0, 0, 1), 1); err == nil {
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
