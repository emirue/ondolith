package s3store

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/emirue/ondolith/internal/s3store/fakes3"
)

func newStore(t *testing.T) (*Store, *fakes3.Server) {
	t.Helper()
	srv := fakes3.New("site")
	t.Cleanup(srv.Close)
	return New(Config{Endpoint: srv.URL, Region: "us-east-1", Bucket: "site",
		AccessKey: "AK", SecretKey: "SK", PathStyle: true}, nil), srv
}

func TestPutOpenRemove(t *testing.T) {
	s, srv := newStore(t)
	ctx := context.Background()
	if err := s.Put(ctx, "2026/10/a", strings.NewReader("hello world"), 11); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if b, ok := srv.Object("2026/10/a"); !ok || string(b) != "hello world" {
		t.Fatalf("bucket holds %q, %v", b, ok)
	}
	f, err := s.Open(ctx, "2026/10/a")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	b, _ := io.ReadAll(f)
	f.Close()
	if string(b) != "hello world" {
		t.Fatalf("read %q", b)
	}
	if err := s.Remove(ctx, "2026/10/a"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok := srv.Object("2026/10/a"); ok {
		t.Fatal("object survived Remove")
	}
	// 지우려던 상태가 이미 참이다 — 로컬 RemoveUpload 와 같다.
	if err := s.Remove(ctx, "2026/10/a"); err != nil {
		t.Fatalf("Remove of a missing object: %v", err)
	}
}

// 없는 객체는 fs.ErrNotExist 여야 한다 — content.Files 가 그것을 보고 다른 쪽
// (로컬)을 본다. 다른 오류로 오면 옮기기 전 파일이 전부 깨진다.
func TestOpenMissingIsNotExist(t *testing.T) {
	s, _ := newStore(t)
	_, err := s.Open(context.Background(), "2026/10/none")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Open of a missing key: %v, want fs.ErrNotExist", err)
	}
}

// http.ServeContent 가 Range 를 처리하려면 Seek 가 돼야 한다. 처음 GET 의 본문을
// 그대로 쓰는 전체 읽기는 요청이 하나이고, Range 는 그 자리부터 다시 받는다.
func TestServeContentRange(t *testing.T) {
	s, srv := newStore(t)
	ctx := context.Background()
	if err := s.Put(ctx, "k", strings.NewReader("0123456789"), 10); err != nil {
		t.Fatal(err)
	}
	serve := func(rangeHeader string) *httptest.ResponseRecorder {
		f, err := s.Open(ctx, "k")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		req := httptest.NewRequest("GET", "/x", nil)
		if rangeHeader != "" {
			req.Header.Set("Range", rangeHeader)
		}
		w := httptest.NewRecorder()
		// 핸들러(P-213·P-306)처럼 타입을 먼저 정한다 — 없으면 ServeContent 가
		// 내용을 추측하려고 앞을 읽고 되감는다.
		w.Header().Set("Content-Type", "text/plain")
		http.ServeContent(w, req, "", time.Time{}, f)
		return w
	}

	before := srv.Gets()
	w := serve("")
	if w.Code != 200 || w.Body.String() != "0123456789" {
		t.Fatalf("full: %d %q", w.Code, w.Body.String())
	}
	if n := srv.Gets() - before; n != 1 {
		t.Fatalf("a full read made %d GETs, want 1", n)
	}

	w = serve("bytes=3-5")
	if w.Code != http.StatusPartialContent || w.Body.String() != "345" {
		t.Fatalf("range: %d %q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Range"); got != "bytes 3-5/10" {
		t.Fatalf("Content-Range %q", got)
	}
}

func TestCheck(t *testing.T) {
	s, srv := newStore(t)
	if err := s.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if keys := srv.Keys(); len(keys) != 0 {
		t.Fatalf("Check left %v in the bucket", keys)
	}

	srv.RejectCode = "InvalidAccessKeyId"
	err := s.Check(context.Background())
	if err == nil {
		t.Fatal("Check passed against a bucket that refuses the key")
	}
	if got := Code(err); got != "InvalidAccessKeyId" {
		t.Fatalf("Code(%v) = %q", err, got)
	}
}

func TestCheckWrongBucket(t *testing.T) {
	srv := fakes3.New("site")
	defer srv.Close()
	s := New(Config{Endpoint: srv.URL, Region: "us-east-1", Bucket: "other",
		AccessKey: "AK", SecretKey: "SK", PathStyle: true}, nil)
	if got := Code(s.Check(context.Background())); got != "NoSuchBucket" {
		t.Fatalf("Code = %q, want NoSuchBucket", got)
	}
}

// 접속 직전 판정(D60 「파일 저장소」)이 실제로 걸리는지 — 주입한 Control 이
// 거부하면 요청이 나가지 않는다. 서비스 응답이 아니므로 Code 는 비어 있다.
func TestDialControlIsUsed(t *testing.T) {
	srv := fakes3.New("site")
	defer srv.Close()
	refused := errors.New("refused by control")
	s := New(Config{Endpoint: srv.URL, Region: "us-east-1", Bucket: "site",
		AccessKey: "AK", SecretKey: "SK", PathStyle: true},
		func(string, string, syscall.RawConn) error { return refused })
	err := s.Check(context.Background())
	if !errors.Is(err, refused) {
		t.Fatalf("Check: %v, want the control's refusal", err)
	}
	if c := Code(err); c != "" {
		t.Fatalf("Code = %q for a dial refusal", c)
	}
	if len(srv.Keys()) != 0 || srv.Gets() != 0 {
		t.Fatal("a request reached the bucket past the control")
	}
}

// **없는 키를 「없다」로 답해야 한다.** AWS 는 키에 s3:ListBucket 이 없으면 없는 객체를
// 403 AccessDenied 로 답한다 — 그러면 행은 있고 파일이 없는 첨부(A-309 가 허용하는 상태)가
// 404 가 아니라 500 이 된다. 연결 확인이 그 정책을 저장 전에 거부한다.
func TestCheckNeedsMissingKeysToBeNotFound(t *testing.T) {
	s, srv := newStore(t)
	srv.MissingDenied = true
	err := s.Check(context.Background())
	if !errors.Is(err, ErrMissingNotReported) {
		t.Fatalf("Check: %v, want ErrMissingNotReported", err)
	}
	if keys := srv.Keys(); len(keys) != 0 {
		t.Fatalf("Check left %v in the bucket", keys)
	}
}

// http.ServeContent 는 여러 구간 요청을 고루틴으로 읽는다. 클라이언트가 끊기면 핸들러의
// Close 가 그 Read 와 겹친다 — 경쟁·nil 역참조가 없어야 한다 (go test -race).
func TestCloseDuringReadIsSafe(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	body := strings.Repeat("0123456789", 100000)
	if err := s.Put(ctx, "big", strings.NewReader(body), int64(len(body))); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		f, err := s.Open(ctx, "big")
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			buf := make([]byte, 4096)
			for j := 0; ; j++ {
				if _, err := f.Seek(int64(j*7919%len(body)), io.SeekStart); err != nil {
					return
				}
				if _, err := f.Read(buf); err != nil {
					return
				}
			}
		}()
		f.Close()
		<-done
	}
}
