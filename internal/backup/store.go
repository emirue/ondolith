package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// namePattern is the only shape a stored backup has: auto-YYYYMMDD-HHMM.zip.
// 이름이 시각이라 이름순이 곧 시간순이다.
var namePattern = regexp.MustCompile(`^auto-\d{8}-\d{4}\.zip$`)

// ValidName reports whether name is a stored backup's name — 내려받기가 받는
// 이름은 이 모양뿐이다. 경로 조각이 섞이면 서버의 다른 파일이다.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// File is one stored backup.
type File struct {
	Name string
	Size int64
	Time time.Time
}

// MB is the size as the A-604 list shows it.
func (f File) MB() string { return fmt.Sprintf("%.1f MB", float64(f.Size)/(1<<20)) }

// List returns the stored backups in dir, newest first. A missing dir is an
// empty list — 자동 백업을 켠 적이 없는 사이트다.
func List(dir string) ([]File, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []File
	for _, e := range entries {
		if !e.Type().IsRegular() || !ValidName(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		out = append(out, File{Name: e.Name(), Size: info.Size(), Time: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}

// rootFile closes the os.Root it was opened through together with the file.
type rootFile struct {
	*os.File
	root *os.Root
}

func (f rootFile) Close() error {
	err := f.File.Close()
	f.root.Close()
	return err
}

// Open opens a stored backup for download, through os.Root so a name can never
// leave dir.
func Open(dir, name string) (io.ReadCloser, error) {
	if !ValidName(name) {
		return nil, fs.ErrNotExist
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	f, err := root.Open(name)
	if err != nil {
		root.Close()
		return nil, err
	}
	return rootFile{f, root}, nil
}

// Save makes one stored backup in dir and keeps only the newest keep (FR-715).
//
// 덤프와 zip 을 임시 파일(이름이 `.` 로 시작해 List 가 보지 않는다)에 다 쓴 뒤에만
// 이름을 붙인다 — 도중에 실패하면 잘린 백업이 목록에 오르지 않고, 이전 백업도 지우지
// 않는다. 폴더는 0700, 파일은 CreateTemp 의 0600 이다: 회원 비밀번호 해시가 든다.
// ponytail: 프로세스가 도중에 죽으면 임시 파일이 남는다. 쌓이면 이름이 `.dump-`·`.zip-` 인 것을 지운다.
func Save(ctx context.Context, dump func(context.Context, io.Writer) error,
	uploads, dir string, now time.Time, keep int,
) (File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return File{}, err
	}
	d, err := os.CreateTemp(dir, ".dump-*")
	if err != nil {
		return File{}, err
	}
	defer os.Remove(d.Name())
	defer d.Close()
	if err := dump(ctx, d); err != nil {
		return File{}, err
	}
	if _, err := d.Seek(0, io.SeekStart); err != nil {
		return File{}, err
	}
	z, err := os.CreateTemp(dir, ".zip-*")
	if err != nil {
		return File{}, err
	}
	defer os.Remove(z.Name()) // 이름을 붙인 뒤에는 없는 파일이라 아무 일도 없다
	if err := Write(z, d, now, uploads); err != nil {
		z.Close()
		return File{}, err
	}
	if err := z.Close(); err != nil {
		return File{}, err
	}
	name := "auto-" + now.Format("20060102-1504") + ".zip"
	path := filepath.Join(dir, name)
	if err := os.Rename(z.Name(), path); err != nil {
		return File{}, err
	}
	if err := prune(dir, keep); err != nil {
		return File{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return File{}, err
	}
	return File{Name: name, Size: info.Size(), Time: info.ModTime()}, nil
}

func prune(dir string, keep int) error {
	files, err := List(dir)
	if err != nil {
		return err
	}
	for i := keep; i < len(files); i++ {
		if err := os.Remove(filepath.Join(dir, files[i].Name)); err != nil {
			return err
		}
	}
	return nil
}

// Schedule is A-604's automatic-backup setting (FR-715): every day at Hour
// (server local time), keeping the newest Keep.
type Schedule struct {
	Enabled bool
	Hour    int
	Keep    int
}

// Scheduler runs Schedule. The app calls Tick once a minute.
type Scheduler struct {
	Settings func() Schedule
	Dump     func(context.Context, io.Writer) error
	Uploads  string
	Dir      string
	// Record reports each run — err is nil on success. 작업 로그와 서버 로그가 받는다.
	Record func(summary string, err error)
	// last is the day (YYYYMMDD) already attempted. 실패한 날도 다시 시도하지 않는다 —
	// 그 시각 내내 매분 실패하는 pg_dump 가 로그를 덮는다.
	last string
}

// Tick runs the day's backup if now is its hour and it has not run today.
func (s *Scheduler) Tick(ctx context.Context, now time.Time) {
	sch := s.Settings()
	day := now.Format("20060102")
	if !sch.Enabled || now.Hour() != sch.Hour || s.last == day {
		return
	}
	// 재시작한 프로세스는 last 를 모른다 — 오늘 만든 것이 이미 있으면 그날은 끝났다.
	if files, err := List(s.Dir); err == nil {
		for _, f := range files {
			if strings.HasPrefix(f.Name, "auto-"+day) {
				s.last = day
				return
			}
		}
	}
	// 수동 백업(A-604)이 도는 중이면 양보한다 — 날짜를 적지 않으므로 다음 분에 다시 본다.
	if !Mu.TryLock() {
		return
	}
	defer Mu.Unlock()
	s.last = day
	f, err := Save(ctx, s.Dump, s.Uploads, s.Dir, now, sch.Keep)
	if err != nil {
		s.Record("자동 백업 실패", err)
		return
	}
	s.Record(fmt.Sprintf("자동 백업 %s (%d MB)", f.Name, (f.Size+(1<<20)-1)>>20), nil)
}
