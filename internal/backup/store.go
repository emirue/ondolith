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

// Remote is where an automatic backup goes when the site keeps its files in S3 (FR-722) — 파일 저장소(A-210)의
// 버킷과 키를 그대로 쓴다. content.Remote 가 이것을 채운다.
type Remote interface {
	Put(ctx context.Context, key string, body io.ReadSeeker, size int64) error
}

// RemotePrefix is the backups' place in the bucket. 사이트 파일(YYYY/MM/…)·연결 확인(ondolith-check/)과 겹치지
// 않는다 — 보관 기간을 정하는 버킷 수명 주기 규칙은 **이 접두사에만** 건다. 버킷 전체에 걸면 사이트 사진이 지워진다
// (D70 「백업」).
const RemotePrefix = "backups/"

// ErrRemote means the backup could not reach the bucket and was kept locally instead.
var ErrRemote = errors.New("backup: 버킷에 올리지 못해 이 서버에 남겼다")

// Saved is where one automatic backup ended up: Where 가 "s3" 면 버킷의 RemotePrefix+Name, "local" 이면 백업 폴더.
type Saved struct {
	File
	Where string
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

// Save makes one stored backup (FR-715). remote 가 없으면 dir 에 두고 최신 keep 개만 남긴다. remote 가 있으면
// 버킷의 RemotePrefix 에 올리고 이 서버에는 남기지 않는다(FR-722) — 올리지 못하면 만든 것을 dir 에 두고
// ErrRemote 를 돌려준다. 그날 백업이 아예 없는 것보다 낫고, 오류라서 작업 로그에 실패로 남는다.
//
// 덤프와 zip 을 임시 파일(이름이 `.` 로 시작해 List 가 보지 않는다)에 다 쓴 뒤에만
// 이름을 붙인다 — 도중에 실패하면 잘린 백업이 목록에 오르지 않고, 이전 백업도 지우지
// 않는다. 폴더는 0700, 파일은 CreateTemp 의 0600 이다: 회원 비밀번호 해시가 든다.
// ponytail: 프로세스가 도중에 죽으면 임시 파일이 남는다. 쌓이면 이름이 `.dump-`·`.zip-` 인 것을 지운다.
func Save(ctx context.Context, dump func(context.Context, io.Writer) error,
	uploads, dir string, now time.Time, keep int, remote Remote,
) (Saved, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Saved{}, err
	}
	d, err := os.CreateTemp(dir, ".dump-*")
	if err != nil {
		return Saved{}, err
	}
	defer os.Remove(d.Name())
	defer d.Close()
	if err := dump(ctx, d); err != nil {
		return Saved{}, err
	}
	if _, err := d.Seek(0, io.SeekStart); err != nil {
		return Saved{}, err
	}
	z, err := os.CreateTemp(dir, ".zip-*")
	if err != nil {
		return Saved{}, err
	}
	defer os.Remove(z.Name()) // 이름을 붙인 뒤에는 없는 파일이라 아무 일도 없다
	if err := Write(z, d, now, uploads); err != nil {
		z.Close()
		return Saved{}, err
	}
	name := "auto-" + now.Format("20060102-1504") + ".zip"
	var remoteErr error
	if remote != nil {
		size, err := z.Seek(0, io.SeekEnd)
		if err == nil {
			_, err = z.Seek(0, io.SeekStart)
		}
		if err == nil {
			err = remote.Put(ctx, RemotePrefix+name, z, size)
		}
		if err == nil {
			z.Close()
			return Saved{File: File{Name: name, Size: size, Time: now}, Where: "s3"}, nil
		}
		remoteErr = fmt.Errorf("%w: %w", ErrRemote, err)
	}
	if err := z.Close(); err != nil {
		return Saved{}, err
	}
	path := filepath.Join(dir, name)
	if err := os.Rename(z.Name(), path); err != nil {
		return Saved{}, err
	}
	if err := prune(dir, keep); err != nil {
		return Saved{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Saved{}, err
	}
	return Saved{File: File{Name: name, Size: info.Size(), Time: info.ModTime()}, Where: "local"}, remoteErr
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

// A-604 를 한 번도 저장하지 않은 사이트의 자동 백업 시각·보관 개수 — 사용이 가장 적은
// 자정, 7개. 화면(admin)과 스케줄러(app)가 같은 값을 쓴다.
const (
	DefaultHour = 0
	DefaultKeep = 7
)

// Schedule is A-604's automatic-backup setting (FR-715): every day at Hour
// (server local time), keeping the newest Keep. Last is when the last automatic
// backup was made (FR-722) — 버킷으로 간 백업은 로컬 폴더에 흔적이 없어, 재시작한 같은 날
// 다시 만들지 않는 근거가 이 값이다.
type Schedule struct {
	Enabled bool
	Hour    int
	Keep    int
	Last    time.Time
}

// Scheduler runs Schedule. The app calls Tick once a minute.
type Scheduler struct {
	Settings func() Schedule
	Dump     func(context.Context, io.Writer) error
	Uploads  string
	Dir      string
	// Remote picks the bucket for this run (FR-722): nil 이면 로컬. 오류(시크릿을 열 수 없다 등)면 그날은
	// 로컬에 남기고 실패로 기록한다. nil 함수는 늘 로컬이다.
	Remote func(context.Context) (Remote, error)
	// Record reports each run — err is nil on success. 작업 로그와 서버 로그가 받는다.
	Record func(summary string, err error)
	// Done receives every backup that was made, 버킷이든 로컬이든 — 마지막 백업 시각으로 저장된다.
	Done func(Saved)
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
	if !sch.Last.IsZero() && sch.Last.In(now.Location()).Format("20060102") == day {
		s.last = day
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
	var remote Remote
	var pickErr error
	if s.Remote != nil {
		remote, pickErr = s.Remote(ctx)
	}
	saved, err := Save(ctx, s.Dump, s.Uploads, s.Dir, now, sch.Keep, remote)
	if err == nil && pickErr != nil {
		err = fmt.Errorf("%w: %w", ErrRemote, pickErr)
	}
	switch {
	case saved.Name == "":
		s.Record("자동 백업 실패", err)
		return
	case err != nil:
		s.Record("자동 백업을 버킷에 올리지 못해 이 서버에 남겼다: "+saved.Name+" ("+saved.MB()+")", err)
	case saved.Where == "s3":
		s.Record("자동 백업 → 버킷 "+RemotePrefix+saved.Name+" ("+saved.MB()+")", nil)
	default:
		s.Record("자동 백업 "+saved.Name+" ("+saved.MB()+")", nil)
	}
	if s.Done != nil {
		s.Done(saved)
	}
}
