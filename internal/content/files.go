package content

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"time"
)

// A-210 의 설정 키 (D30 `storage.*`). 화면(admin)과 저장소를 고르는 쪽(app)이 같은 이름을 쓴다.
const (
	SettingStorageBackend   = "storage.backend" // 빈 값 = 로컬, "s3"
	SettingStorageEndpoint  = "storage.s3.endpoint"
	SettingStorageRegion    = "storage.s3.region"
	SettingStorageBucket    = "storage.s3.bucket"
	SettingStorageAccessKey = "storage.s3.access_key"
	SettingStorageSecretKey = "storage.s3.secret_key"
	SettingStoragePathStyle = "storage.s3.path_style"
)

// StorageSettingKeys are every key A-210 writes.
var StorageSettingKeys = []string{
	SettingStorageBackend, SettingStorageEndpoint, SettingStorageRegion, SettingStorageBucket,
	SettingStorageAccessKey, SettingStorageSecretKey, SettingStoragePathStyle,
}

// Remote is an S3-compatible bucket that keeps uploads instead of upload_dir
// (FR-717, A-210). s3store implements it; content does not import the SDK.
type Remote interface {
	// Put stores body under key. size is its exact length.
	Put(ctx context.Context, key string, body io.ReadSeeker, size int64) error
	// Open reads key. A missing object is fs.ErrNotExist.
	Open(ctx context.Context, key string) (io.ReadSeekCloser, error)
	// Remove deletes key. A missing object is success.
	Remove(ctx context.Context, key string) error
}

// RemoteFunc resolves the bucket for one operation — A-210 changes it while
// the server runs, and the next request must see it (A-209 와 같은 이유).
// r is nil when no bucket is configured; write reports whether new uploads go
// to it (`storage.backend = s3`).
type RemoteFunc func(ctx context.Context) (r Remote, write bool, err error)

// Files is where uploads live: upload_dir, and the bucket when A-210 set one.
//
// **읽기는 양쪽을 본다** — 로컬을 먼저, 없으면 버킷. 행에 저장소를 적지 않으므로(D30)
// 바꾸기 전 로컬 파일이 옮기기 전에도 보이고, 로컬로 되돌려도 버킷의 파일이 보인다. 로컬이
// 먼저인 것은 버킷의 답에 기대지 않으려는 것이다: 없는 키를 403 으로 답하는 버킷(AWS,
// ListBucket 없음)이나 불통인 버킷이 로컬 파일까지 깨뜨리지 않는다. 키가 UUID 라 순서는
// 결과를 바꾸지 않고, 로컬에 없을 때 드는 것은 stat 한 번이다.
// **지우기도 양쪽이다**: 한쪽에 남으면 읽기가 그것을 되살린다.
type Files struct {
	root   string
	remote RemoteFunc
}

// NewFiles binds the upload directory and the bucket resolver.
func NewFiles(root string, remote RemoteFunc) *Files { return &Files{root: root, remote: remote} }

// LocalFiles is upload_dir alone — 버킷이 없는 사이트와 시험이 쓴다.
func LocalFiles(root string) *Files { return NewFiles(root, nil) }

// ErrFilesLeft means the rows went but some of their files did not — 글·상품을 지운 뒤
// 디스크나 버킷의 파일 삭제가 실패했다. 행은 되돌리지 않는다(A-309 와 같은 결정). 호출자는
// 이것을 실패가 아니라 경고로 다룬다: 500 을 보이면 운영자는 다시 누르고, 그때는 404 다.
var ErrFilesLeft = errors.New("content: 행은 지웠고 파일 일부가 남았다")

// ErrStorageLocal means the operation needs `storage.backend = s3` (A-210 옮기기).
var ErrStorageLocal = errors.New("content: 파일 저장소가 S3 가 아닙니다")

func (f *Files) resolve(ctx context.Context) (Remote, bool, error) {
	if f.remote == nil {
		return nil, false, nil
	}
	return f.remote(ctx)
}

// Store validates and stores one upload (D60 네 겹은 StoreUpload 그대로다).
//
// 버킷에 쓸 때도 **로컬에서 검증을 다 마친 뒤** 올리고 로컬 사본을 지운다 — 검증기가
// 하나로 남는다. 버킷에 못 올리면 업로드는 실패하고 로컬에도 남지 않는다.
func (f *Files) Store(ctx context.Context, name string, r io.Reader, now time.Time, limits UploadLimits) (StoredUpload, error) {
	rem, write, err := f.resolve(ctx)
	if err != nil {
		// 설정을 못 읽었다. 조용히 로컬에 쓰면 운영자는 S3 로 간다고 믿는다.
		return StoredUpload{}, err
	}
	st, err := StoreUpload(f.root, name, r, now, limits)
	if err != nil || !write {
		return st, err
	}
	rt, err := os.OpenRoot(f.root)
	if err != nil {
		_ = RemoveUpload(f.root, st.StoredPath)
		return StoredUpload{}, err
	}
	defer rt.Close()
	if err := push(ctx, rt, rem, st.StoredPath); err != nil {
		_ = rt.Remove(filepath.FromSlash(st.StoredPath))
		return StoredUpload{}, err
	}
	return st, nil
}

// Write puts already-processed bytes at rel (본문 사진, FR-514). 로컬에는 첨부와 같이
// os.Root 로(탈출 방지, NFR-201) 앱 계정만 읽게 쓴다 (StoreUpload, D60 3절).
func (f *Files) Write(ctx context.Context, rel string, b []byte) error {
	rem, write, err := f.resolve(ctx)
	if err != nil {
		return err
	}
	if write {
		return rem.Put(ctx, rel, bytes.NewReader(b), int64(len(b)))
	}
	rt, err := os.OpenRoot(f.root)
	if err != nil {
		return err
	}
	defer rt.Close()
	if err := rt.MkdirAll(filepath.FromSlash(path.Dir(rel)), 0o700); err != nil {
		return err
	}
	w, err := rt.OpenFile(filepath.FromSlash(rel), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := w.Write(b)
	if cerr := w.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = rt.Remove(filepath.FromSlash(rel))
	}
	return werr
}

// Open reads rel — upload_dir first, then the bucket. Nothing on either side
// is fs.ErrNotExist (핸들러가 404 로 그린다).
func (f *Files) Open(ctx context.Context, rel string) (io.ReadSeekCloser, error) {
	local, err := OpenUpload(f.root, rel)
	if err == nil {
		return local, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	rem, _, rerr := f.resolve(ctx)
	if rerr != nil {
		// 로컬에 없고 버킷 설정을 못 읽었다 — 「없다」가 아니라 모르는 것이다.
		return nil, rerr
	}
	if rem == nil {
		return nil, err
	}
	return rem.Open(ctx, rel)
}

// removeBudget bounds one Remove. 요청이 끊겨도 지우기는 끝까지 한다 — 행은 이미 갔고,
// 남은 객체는 아무도 찾지 못한다. 그렇다고 답하지 않는 버킷에 영원히 매달리지 않는다.
const removeBudget = 30 * time.Second

// Remove deletes rel on both sides. 이미 없으면 성공이다.
func (f *Files) Remove(ctx context.Context, rel string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), removeBudget)
	defer cancel()
	rem, _, rerr := f.resolve(ctx)
	return f.removeFrom(ctx, rem, rerr, rel)
}

// removeFrom deletes rel locally and from the already-resolved bucket.
func (f *Files) removeFrom(ctx context.Context, rem Remote, rerr error, rel string) error {
	err := RemoveUpload(f.root, rel)
	if rerr != nil {
		err = errors.Join(err, rerr)
	} else if rem != nil {
		err = errors.Join(err, rem.Remove(ctx, rel))
	}
	if err != nil {
		// 어느 파일인지 남긴다 — 버킷의 오류에는 키가 없어 로그만으로는 찾지 못한다.
		return fmt.Errorf("%s: %w", rel, err)
	}
	return nil
}

// removeAllBudget bounds one RemoveAll — 한 요청 안에서 끝나야 운영자가 빈 응답을 보지
// 않는다(WriteTimeout 60초, admin 의 migrateBudget 과 같은 이유). 시험이 줄인다.
var removeAllBudget = 40 * time.Second

// RemoveAll deletes every path, carrying on past a failure: 앞에서 멈추면 뒤의
// 파일은 행이 사라진 뒤라 아무도 찾지 못한다. 버킷 설정은 한 번 읽는다. 하나라도
// 남으면 — 시한을 넘겨 손대지 못한 것까지 — 몇 개인지와 첫 오류를 실은 ErrFilesLeft
// 다. 행을 먼저 지운 호출자는 그것을 경고로 다룬다(A-309 와 같은 결정).
func (f *Files) RemoveAll(ctx context.Context, paths []string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), removeAllBudget)
	defer cancel()
	rem, _, rerr := f.resolve(ctx)
	left := 0
	var first error
	for i, p := range paths {
		if ctx.Err() != nil {
			left += len(paths) - i
			first = cmp.Or(first, ctx.Err())
			break
		}
		if err := f.removeFrom(ctx, rem, rerr, p); err != nil {
			left++
			first = cmp.Or(first, err)
		}
	}
	if left > 0 {
		return fmt.Errorf("%w: %d 개 중 %d 개 (첫 오류: %w)", ErrFilesLeft, len(paths), left, first)
	}
	return nil
}

// uploadPathRe is what Migrate may send: the shapes the stores write
// (첨부·상품 이미지 `YYYY/MM/<uuid>`, 본문 사진 `media/…` 와 `-thumb`).
var uploadPathRe = regexp.MustCompile(`^(media/)?[0-9]{4}/[0-9]{2}/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}(-thumb)?$`)

// MigrateResult is what one press of A-210 「옮기기」 did. Unreadable counts the
// folders it could not read — their files were neither moved nor counted.
type MigrateResult struct {
	Moved, Failed, Left, Unreadable int
}

// migrateSettle is how old a local file must be before Migrate sends it. 막 쓰인
// 파일은 아직 쓰는 중일 수 있다 — 반쯤 쓴 파일을 올리고 지우면 버킷에 잘린 사진이 남는다.
// 업로드 한 건은 서버 시한(ReadTimeout 30초) 안에 끝난다.
const migrateSettle = time.Minute

// Migrate moves local uploads into the bucket until the deadline (A-210).
//
// 폴더는 os.Root 로 걷는다 — 폴더 밖을 가리키는 링크를 따라 서버의 다른 파일을 버킷에
// 올리지 않는다(백업과 같은 기준, D60). 업로드 모양인 일반 파일만 보낸다. 올린 뒤에야
// 로컬 사본을 지우므로, 실패한 파일은 로컬에 그대로 남아 계속 보인다.
// 시한이 지나면 남은 개수만 센다 — 한 요청이 WriteTimeout(D60)을 넘지 않게.
func (f *Files) Migrate(ctx context.Context, until time.Time) (MigrateResult, error) {
	var res MigrateResult
	rem, write, err := f.resolve(ctx)
	if err != nil {
		return res, err
	}
	if rem == nil || !write {
		return res, ErrStorageLocal
	}
	rt, err := os.OpenRoot(f.root)
	if err != nil {
		return res, err
	}
	defer rt.Close()
	var firstErr error
	keep := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	walkErr := fs.WalkDir(rt.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() && p != "." {
				// 읽지 못한 폴더는 세고 건너뛴다 — 나머지는 계속 옮긴다.
				res.Unreadable++
				keep(fmt.Errorf("%s: %w", p, err))
				return fs.SkipDir
			}
			return err
		}
		if !d.Type().IsRegular() || !uploadPathRe.MatchString(p) {
			return nil
		}
		info, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil // 걷는 사이 사라졌다
		}
		if err != nil {
			res.Failed++
			keep(fmt.Errorf("%s: %w", p, err))
			return nil
		}
		if ctx.Err() != nil || !time.Now().Before(until) || time.Since(info.ModTime()) < migrateSettle {
			res.Left++
			return nil
		}
		err = push(ctx, rt, rem, p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// 걷는 사이 지워졌거나 업로드가 직접 올리고 지웠다 — 실패가 아니다.
		case err != nil:
			res.Failed++
			keep(fmt.Errorf("%s: %w", p, err))
		default:
			res.Moved++
		}
		return nil
	})
	return res, errors.Join(walkErr, firstErr)
}

// push uploads one local file to the bucket and then deletes the local copy.
func push(ctx context.Context, rt *os.Root, rem Remote, rel string) error {
	name := filepath.FromSlash(rel)
	src, err := rt.Open(name)
	if err != nil {
		return err
	}
	info, err := src.Stat()
	if err == nil {
		err = rem.Put(ctx, rel, src, info.Size())
	}
	if cerr := src.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return rt.Remove(name)
}
