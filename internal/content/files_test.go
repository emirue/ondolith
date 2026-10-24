package content

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// memRemote is a bucket in a map — the Files rules, not the S3 protocol, are
// under test here (the protocol is s3store's).
type memRemote struct {
	mu         sync.Mutex
	objects    map[string][]byte
	failPut    error
	failOpen   error
	failRemove error
	onPut      func(key string)
}

func newMemRemote() *memRemote { return &memRemote{objects: map[string][]byte{}} }

func (m *memRemote) Put(_ context.Context, key string, body io.ReadSeeker, size int64) error {
	if m.failPut != nil {
		return m.failPut
	}
	if m.onPut != nil {
		m.onPut(key)
	}
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if int64(len(b)) != size {
		return fmt.Errorf("size %d, body %d", size, len(b))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = b
	return nil
}

type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }

func (m *memRemote) Open(_ context.Context, key string) (io.ReadSeekCloser, error) {
	if m.failOpen != nil {
		return nil, m.failOpen
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objects[key]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return nopCloser{bytes.NewReader(b)}, nil
}

func (m *memRemote) Remove(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.failRemove != nil {
		return m.failRemove
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}

func (m *memRemote) keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.objects {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func remoteOf(r Remote, write bool) RemoteFunc {
	return func(context.Context) (Remote, bool, error) { return r, write, nil }
}

func readAll(t *testing.T, f *Files, rel string) string {
	t.Helper()
	o, err := f.Open(context.Background(), rel)
	if err != nil {
		t.Fatalf("Open(%s): %v", rel, err)
	}
	defer o.Close()
	b, err := io.ReadAll(o)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func localExists(t *testing.T, root, rel string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

// age makes a local upload look settled — Migrate leaves files written in the
// last minute alone (they may still be mid-upload).
func age(t *testing.T, root string, rels ...string) {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	for _, rel := range rels {
		if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(rel)), old, old); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLocalFilesStoreOpenRemove(t *testing.T) {
	root := t.TempDir()
	f := LocalFiles(root)
	ctx := context.Background()
	st, err := f.Store(ctx, "a.pdf", bytes.NewReader(pdfBytes), uploadAt, DefaultUploadLimits())
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, f, st.StoredPath); got != string(pdfBytes) {
		t.Fatalf("read back %q", got)
	}
	if err := f.Remove(ctx, st.StoredPath); err != nil {
		t.Fatal(err)
	}
	if localExists(t, root, st.StoredPath) {
		t.Fatal("file survived Remove")
	}
}

// S3 로 저장하면 로컬에 남지 않는다 (FR-717). 검증은 로컬을 지나는 그대로다.
func TestRemoteWriteLeavesNothingLocal(t *testing.T) {
	root, rem := t.TempDir(), newMemRemote()
	f := NewFiles(root, remoteOf(rem, true))
	ctx := context.Background()
	st, err := f.Store(ctx, "a.pdf", bytes.NewReader(pdfBytes), uploadAt, DefaultUploadLimits())
	if err != nil {
		t.Fatal(err)
	}
	if localExists(t, root, st.StoredPath) {
		t.Fatal("an upload to the bucket stayed in upload_dir")
	}
	if got := rem.keys(); len(got) != 1 || got[0] != st.StoredPath {
		t.Fatalf("bucket keys %v, want [%s]", got, st.StoredPath)
	}
	if got := readAll(t, f, st.StoredPath); got != string(pdfBytes) {
		t.Fatalf("read back %q", got)
	}

	// 사진(Write)도 같은 곳으로 간다.
	if err := f.Write(ctx, "media/2026/10/x", []byte("img")); err != nil {
		t.Fatal(err)
	}
	if localExists(t, root, "media/2026/10/x") {
		t.Fatal("Write to the bucket stayed in upload_dir")
	}
	if got := readAll(t, f, "media/2026/10/x"); got != "img" {
		t.Fatalf("read back %q", got)
	}

	// 검증은 그대로 걸린다 — 버킷 경로가 네 겹을 비켜 가지 않는다.
	if _, err := f.Store(ctx, "x.php", bytes.NewReader(phpBytes), uploadAt, DefaultUploadLimits()); !errors.Is(err, ErrUploadExt) {
		t.Fatalf("Store of a .php: %v", err)
	}
}

// 버킷에 못 올리면 업로드는 실패하고, 로컬에 반쪽이 남지 않는다 — 그 파일을
// 가리키는 행이 생기지 않으므로 남은 파일은 아무도 찾지 못한다.
func TestRemotePutFailureFailsTheUpload(t *testing.T) {
	root, rem := t.TempDir(), newMemRemote()
	rem.failPut = errors.New("bucket down")
	f := NewFiles(root, remoteOf(rem, true))
	_, err := f.Store(context.Background(), "a.pdf", bytes.NewReader(pdfBytes), uploadAt, DefaultUploadLimits())
	if err == nil {
		t.Fatal("Store succeeded while the bucket refused")
	}
	var left []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, _ error) error {
		if d != nil && !d.IsDir() {
			left = append(left, p)
		}
		return nil
	})
	if len(left) != 0 {
		t.Fatalf("files left behind: %v", left)
	}
}

// 설정을 못 읽으면 조용히 로컬로 쓰지 않는다 — 운영자는 S3 로 간다고 믿는다.
func TestUnresolvableRemoteFailsTheUpload(t *testing.T) {
	broken := errors.New("secret does not open")
	f := NewFiles(t.TempDir(), func(context.Context) (Remote, bool, error) { return nil, false, broken })
	_, err := f.Store(context.Background(), "a.pdf", bytes.NewReader(pdfBytes), uploadAt, DefaultUploadLimits())
	if !errors.Is(err, broken) {
		t.Fatalf("Store: %v, want the resolver's error", err)
	}
}

// 바꾸기 전 로컬 파일은 S3 모드에서도 보이고, 로컬로 되돌려도 버킷의 파일이 보인다.
func TestOpenFindsTheFileOnEitherSide(t *testing.T) {
	root, rem := t.TempDir(), newMemRemote()
	ctx := context.Background()
	old, err := LocalFiles(root).Store(ctx, "old.pdf", bytes.NewReader(pdfBytes), uploadAt, DefaultUploadLimits())
	if err != nil {
		t.Fatal(err)
	}
	s3 := NewFiles(root, remoteOf(rem, true))
	if got := readAll(t, s3, old.StoredPath); got != string(pdfBytes) {
		t.Fatalf("S3 mode lost a local file: %q", got)
	}
	inBucket, err := s3.Store(ctx, "new.pdf", bytes.NewReader(pdfBytes), uploadAt, DefaultUploadLimits())
	if err != nil {
		t.Fatal(err)
	}
	back := NewFiles(root, remoteOf(rem, false))
	if got := readAll(t, back, inBucket.StoredPath); got != string(pdfBytes) {
		t.Fatalf("local mode lost a bucket file: %q", got)
	}
	// 어느 쪽에도 없으면 「없다」다 — 핸들러가 404 로 그린다.
	if _, err := back.Open(ctx, "2026/10/00000000-0000-4000-8000-000000000000"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Open of nothing: %v", err)
	}
	// 버킷이 없는 사이트는 버킷을 보지 않는다.
	if _, err := LocalFiles(root).Open(ctx, inBucket.StoredPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("local-only Open found a bucket file: %v", err)
	}
}

// 지우기는 양쪽이다 — 한쪽에 남으면 읽기가 다른 쪽을 보고 지운 파일을 되살린다.
func TestRemoveClearsBothSides(t *testing.T) {
	root, rem := t.TempDir(), newMemRemote()
	ctx := context.Background()
	st, err := LocalFiles(root).Store(ctx, "a.pdf", bytes.NewReader(pdfBytes), uploadAt, DefaultUploadLimits())
	if err != nil {
		t.Fatal(err)
	}
	rem.objects[st.StoredPath] = pdfBytes
	for _, write := range []bool{true, false} {
		f := NewFiles(root, remoteOf(rem, write))
		if err := f.Remove(ctx, st.StoredPath); err != nil {
			t.Fatal(err)
		}
		if localExists(t, root, st.StoredPath) || len(rem.keys()) != 0 {
			t.Fatalf("write=%v: Remove left a copy (local %v, bucket %v)", write,
				localExists(t, root, st.StoredPath), rem.keys())
		}
	}
}

func TestMigrateMovesUploadsOnly(t *testing.T) {
	root, rem := t.TempDir(), newMemRemote()
	ctx := context.Background()
	local := LocalFiles(root)
	att, err := local.Store(ctx, "a.pdf", bytes.NewReader(pdfBytes), uploadAt, DefaultUploadLimits())
	if err != nil {
		t.Fatal(err)
	}
	const pic = "media/2026/10/11111111-1111-4111-8111-111111111111"
	for _, rel := range []string{pic, pic + "-thumb"} {
		if err := local.Write(ctx, rel, []byte(rel)); err != nil {
			t.Fatal(err)
		}
	}
	// 업로드 모양이 아닌 파일과 폴더 밖을 가리키는 링크는 보내지 않는다 (D60 「파일 저장소」).
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("server file"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := "2026/10/22222222-2222-4222-8222-222222222222"
	if err := os.MkdirAll(filepath.Join(root, "2026", "10"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, filepath.FromSlash(link))); err != nil {
		t.Fatal(err)
	}

	age(t, root, att.StoredPath, pic, pic+"-thumb", link)
	f := NewFiles(root, remoteOf(rem, true))
	res, err := f.Migrate(ctx, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if res != (MigrateResult{Moved: 3}) {
		t.Fatalf("Migrate = %+v, want 3 moved", res)
	}
	want := []string{att.StoredPath, pic, pic + "-thumb"}
	sort.Strings(want)
	if got := rem.keys(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("bucket %v, want %v", got, want)
	}
	for _, rel := range want {
		if localExists(t, root, rel) {
			t.Fatalf("%s stayed local after Migrate", rel)
		}
	}
	if !localExists(t, root, "notes.txt") {
		t.Fatal("Migrate touched a file that is not an upload")
	}
	// 옮긴 뒤에도 같은 경로로 읽힌다.
	if got := readAll(t, f, pic); got != pic {
		t.Fatalf("read back %q", got)
	}
}

// 시한이 지나면 더 옮기지 않고 남은 수를 센다 — 다시 누르면 이어진다.
func TestMigrateStopsAtTheDeadline(t *testing.T) {
	root, rem := t.TempDir(), newMemRemote()
	ctx := context.Background()
	st, err := LocalFiles(root).Store(ctx, "a.pdf", bytes.NewReader(pdfBytes), uploadAt, DefaultUploadLimits())
	if err != nil {
		t.Fatal(err)
	}
	age(t, root, st.StoredPath)
	f := NewFiles(root, remoteOf(rem, true))
	res, err := f.Migrate(ctx, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if res != (MigrateResult{Left: 1}) || len(rem.keys()) != 0 {
		t.Fatalf("past the deadline: %+v, bucket %v", res, rem.keys())
	}
	if res, _ = f.Migrate(ctx, time.Now().Add(time.Minute)); res != (MigrateResult{Moved: 1}) {
		t.Fatalf("second press: %+v", res)
	}
}

func TestMigrateFailureKeepsTheLocalFile(t *testing.T) {
	root, rem := t.TempDir(), newMemRemote()
	ctx := context.Background()
	st, err := LocalFiles(root).Store(ctx, "a.pdf", bytes.NewReader(pdfBytes), uploadAt, DefaultUploadLimits())
	if err != nil {
		t.Fatal(err)
	}
	age(t, root, st.StoredPath)
	rem.failPut = errors.New("bucket down")
	res, err := NewFiles(root, remoteOf(rem, true)).Migrate(ctx, time.Now().Add(time.Minute))
	if err == nil || res != (MigrateResult{Failed: 1}) {
		t.Fatalf("Migrate = %+v, %v", res, err)
	}
	if !localExists(t, root, st.StoredPath) {
		t.Fatal("a file that did not reach the bucket was deleted")
	}
}

func TestMigrateNeedsS3(t *testing.T) {
	for _, f := range []*Files{LocalFiles(t.TempDir()), NewFiles(t.TempDir(), remoteOf(newMemRemote(), false))} {
		if _, err := f.Migrate(context.Background(), time.Now().Add(time.Minute)); !errors.Is(err, ErrStorageLocal) {
			t.Fatalf("Migrate without S3 writes: %v", err)
		}
	}
}

// S3 시크릿 키는 봉인하고 다시 보이지 않는다 — 목록은 IsSecretSetting 한 곳이다 (D60 ②).
// 액세스 키는 시크릿 없이는 쓸 수 없어 화면에 다시 보인다 (D19 A-210).
func TestStorageSecretIsASecretSetting(t *testing.T) {
	if !IsSecretSetting(SettingStorageSecretKey) {
		t.Errorf("%s 가 시크릿 목록에 없다", SettingStorageSecretKey)
	}
	for _, k := range StorageSettingKeys {
		if k != SettingStorageSecretKey && IsSecretSetting(k) {
			t.Errorf("%s 가 시크릿으로 잡혔다", k)
		}
	}
}

// **로컬을 먼저 본다.** 버킷이 없는 키를 403 으로 답하든(AWS, ListBucket 없음) 불통이든
// 로컬 파일은 열린다 — 옮기기 전 파일이 버킷 사정으로 깨지지 않는다.
func TestLocalFileOpensWhateverTheBucketSays(t *testing.T) {
	root, rem := t.TempDir(), newMemRemote()
	ctx := context.Background()
	st, err := LocalFiles(root).Store(ctx, "a.pdf", bytes.NewReader(pdfBytes), uploadAt, DefaultUploadLimits())
	if err != nil {
		t.Fatal(err)
	}
	rem.failOpen = errors.New("api error AccessDenied")
	if got := readAll(t, NewFiles(root, remoteOf(rem, true)), st.StoredPath); got != string(pdfBytes) {
		t.Fatalf("read back %q", got)
	}
}

// 버킷이 있어도 저장 위치가 로컬이면 새 업로드는 로컬에 간다 — 되돌린 뒤 버킷에 쓰면
// 운영자는 버킷을 지워도 된다고 믿는다.
func TestLocalModeWritesLocallyEvenWithABucket(t *testing.T) {
	root, rem := t.TempDir(), newMemRemote()
	ctx := context.Background()
	f := NewFiles(root, remoteOf(rem, false))
	st, err := f.Store(ctx, "a.pdf", bytes.NewReader(pdfBytes), uploadAt, DefaultUploadLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Write(ctx, "media/2026/10/x", []byte("img")); err != nil {
		t.Fatal(err)
	}
	if !localExists(t, root, st.StoredPath) || !localExists(t, root, "media/2026/10/x") || len(rem.keys()) != 0 {
		t.Fatalf("local mode: local %v/%v, bucket %v", localExists(t, root, st.StoredPath),
			localExists(t, root, "media/2026/10/x"), rem.keys())
	}
}

// 지우기는 요청이 끊겨도 끝까지 한다 — 행은 이미 갔고, 버킷에 남은 객체는 아무도 찾지 못한다.
func TestRemoveSurvivesACanceledRequest(t *testing.T) {
	rem := newMemRemote()
	rem.objects["2026/10/x"] = []byte("x")
	f := NewFiles(t.TempDir(), func(ctx context.Context) (Remote, bool, error) {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		return rem, true, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.Remove(ctx, "2026/10/x"); err != nil {
		t.Fatalf("Remove on a canceled request: %v", err)
	}
	if len(rem.keys()) != 0 {
		t.Fatal("the bucket object survived")
	}
}

// 방금 쓰인 파일은 옮기지 않는다 — 아직 쓰는 중일 수 있다. 남은 것으로 센다.
func TestMigrateLeavesFilesBeingWritten(t *testing.T) {
	root, rem := t.TempDir(), newMemRemote()
	ctx := context.Background()
	st, err := LocalFiles(root).Store(ctx, "a.pdf", bytes.NewReader(pdfBytes), uploadAt, DefaultUploadLimits())
	if err != nil {
		t.Fatal(err)
	}
	res, err := NewFiles(root, remoteOf(rem, true)).Migrate(ctx, time.Now().Add(time.Minute))
	if err != nil || res != (MigrateResult{Left: 1}) || len(rem.keys()) != 0 || !localExists(t, root, st.StoredPath) {
		t.Fatalf("Migrate of a fresh file = %+v, %v; bucket %v", res, err, rem.keys())
	}
}

// 걷는 사이 사라진 파일(삭제·직접 올라감)은 실패가 아니다 — 거짓 502 를 만들지 않는다.
func TestMigrateSkipsFilesThatVanished(t *testing.T) {
	root, rem := t.TempDir(), newMemRemote()
	ctx := context.Background()
	a := "2026/10/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	b := "2026/10/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	for _, rel := range []string{a, b} {
		if err := LocalFiles(root).Write(ctx, rel, []byte(rel)); err != nil {
			t.Fatal(err)
		}
	}
	age(t, root, a, b)
	rem.onPut = func(key string) {
		if key == a {
			_ = os.Remove(filepath.Join(root, filepath.FromSlash(b)))
		}
	}
	res, err := NewFiles(root, remoteOf(rem, true)).Migrate(ctx, time.Now().Add(time.Minute))
	if err != nil || res != (MigrateResult{Moved: 1}) {
		t.Fatalf("Migrate = %+v, %v — a vanished file is not a failure", res, err)
	}
}

// 읽지 못한 폴더가 있으면 그 사실을 센다. 나머지는 계속 옮긴다 — 「0개 실패」로 끝나면
// 운영자는 다 옮긴 줄 알고 업로드 폴더를 정리한다.
func TestMigrateCountsFoldersItCouldNotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 는 권한 없는 폴더도 읽는다")
	}
	root, rem := t.TempDir(), newMemRemote()
	ctx := context.Background()
	ok := "2026/10/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	hidden := "2026/09/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	for _, rel := range []string{ok, hidden} {
		if err := LocalFiles(root).Write(ctx, rel, []byte(rel)); err != nil {
			t.Fatal(err)
		}
	}
	age(t, root, ok, hidden)
	locked := filepath.Join(root, "2026", "09")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	res, err := NewFiles(root, remoteOf(rem, true)).Migrate(ctx, time.Now().Add(time.Minute))
	if err == nil || res != (MigrateResult{Moved: 1, Unreadable: 1}) {
		t.Fatalf("Migrate = %+v, %v; want 1 moved, 1 unreadable folder, an error", res, err)
	}
}

// pickyRemote refuses to delete one key — 버킷이 그 객체 하나만 거부하는 경우.
type pickyRemote struct {
	*memRemote
	refuse string
}

func (p pickyRemote) Remove(ctx context.Context, key string) error {
	if key == p.refuse {
		return errors.New("AccessDenied")
	}
	return p.memRemote.Remove(ctx, key)
}

// 묶음 삭제는 하나가 실패해도 나머지를 지운다 — 앞에서 멈추면 뒤의 파일은 영영 남는다.
// 실패는 ErrFilesLeft 이고, 어느 파일인지 오류에 있다.
func TestRemoveAllTriesEveryPath(t *testing.T) {
	mem := newMemRemote()
	keys := []string{"2026/10/a", "2026/10/b", "2026/10/c"}
	for _, k := range keys {
		mem.objects[k] = []byte("x")
	}
	f := NewFiles(t.TempDir(), remoteOf(pickyRemote{mem, keys[1]}, true))
	err := f.RemoveAll(context.Background(), keys)
	if !errors.Is(err, ErrFilesLeft) || !strings.Contains(err.Error(), keys[1]) {
		t.Errorf("= %v, want ErrFilesLeft naming %s", err, keys[1])
	}
	if got := mem.keys(); len(got) != 1 || got[0] != keys[1] {
		t.Errorf("남은 객체 %v, want [%s]", got, keys[1])
	}
	if err := f.RemoveAll(context.Background(), []string{keys[0], keys[2]}); err != nil {
		t.Errorf("다 지워지면 nil 이어야 한다: %v", err)
	}
}
