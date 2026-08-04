package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emirue/ondolith/internal/config"
)

func body(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	b, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func handlerSaying(s string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, s)
	})
}

// The install tree is replaced by the operating tree in place, with no
// restart. This is the mechanism behind FR-106.
func TestRootSwapsHandlerInPlace(t *testing.T) {
	rt := &root{}
	rt.swap(handlerSaying("install"), nil)
	if got := body(t, rt); got != "install" {
		t.Fatalf("before swap = %q, want install", got)
	}

	rt.swap(handlerSaying("operating"), nil)
	if got := body(t, rt); got != "operating" {
		t.Fatalf("after swap = %q, want operating", got)
	}
}

// Every other test swaps a tree in first, so the empty-root path would never
// be exercised — and a nil dereference there is a panic on every request.
func TestServeHTTPBeforeAnySwap(t *testing.T) {
	rt := &root{}

	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("HTTP %d, want 503", rec.Code)
	}
}

func TestCloseBeforeAnySwap(t *testing.T) {
	rt := &root{}
	rt.close() // must not panic
}

// Swapping in a new tree must release what the previous one held — otherwise
// re-initialising leaks a connection pool.
func TestSwapReleasesPreviousTree(t *testing.T) {
	rt := &root{}

	var released atomic.Int32
	rt.swap(handlerSaying("first"), func() { released.Add(1) })
	if got := released.Load(); got != 0 {
		t.Fatalf("교체 전에 정리가 실행됐다: %d", got)
	}

	rt.swap(handlerSaying("second"), nil)
	if got := released.Load(); got != 1 {
		t.Errorf("이전 트리 정리 횟수 = %d, want 1", got)
	}
	if got := body(t, rt); got != "second" {
		t.Errorf("교체 후 응답 = %q, want second", got)
	}
}

func TestCloseRunsCleanupExactlyOnce(t *testing.T) {
	rt := &root{}
	var closed atomic.Int32
	rt.swap(handlerSaying("operating"), func() { closed.Add(1) })

	rt.close()
	rt.close()
	rt.close()

	if got := closed.Load(); got != 1 {
		t.Errorf("정리 횟수 = %d, want 1", got)
	}
}

// Regression: the cleanup used to live in a plain variable captured by run(),
// written by the install handler goroutine and read by the main goroutine at
// shutdown, with nothing synchronising them. srv.Shutdown returns early when
// its deadline expires, so an in-flight install really can overlap shutdown.
//
// Meaningful under -race, which `make test` enables.
func TestCloseIsSafeAgainstConcurrentSwap(t *testing.T) {
	rt := &root{}
	var doubleRun atomic.Bool

	// Each tree gets a cleanup that reports if it is ever run twice.
	cleanup := func() func() {
		var ran atomic.Bool
		return func() {
			if ran.Swap(true) {
				doubleRun.Store(true)
			}
		}
	}

	rt.swap(handlerSaying("install"), cleanup())

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			rt.swap(handlerSaying("operating"), cleanup())
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			rt.close()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		}
	}()
	wg.Wait()

	if doubleRun.Load() {
		t.Error("정리 함수가 두 번 실행됐다 — 커넥션 풀 이중 해제")
	}
}

// run() releases the live tree right after srv.Shutdown, which returns early on
// its deadline while handlers may still be running. These pin what a request
// sees in that window: the tree stays installed and dispatch still happens, so
// the handler runs and fails on its own closed resources rather than the router
// panicking on everyone. Whether that is acceptable is a documented tradeoff in
// run(); what must not happen is a nil dereference.
func TestServeHTTPAfterCloseStillDispatches(t *testing.T) {
	rt := &root{}
	var cleaned atomic.Int32
	rt.swap(handlerSaying("live"), func() { cleaned.Add(1) })

	rt.close()

	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil)) // must not panic

	if got := rec.Body.String(); got != "live" {
		t.Errorf("응답 = %q, want live — 종료 중에도 라우터는 디스패치해야 한다", got)
	}
	if got := cleaned.Load(); got != 1 {
		t.Errorf("정리 횟수 = %d, want 1", got)
	}
}

// A tree installed after close() is released immediately, yet it is also the
// live tree until the process exits. Requests in that window must be dispatched,
// not met with a nil handler.
func TestServeHTTPOnTreeSwappedInAfterClose(t *testing.T) {
	rt := &root{}
	rt.swap(handlerSaying("first"), nil)
	rt.close()

	rt.swap(handlerSaying("late"), func() {})

	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil)) // must not panic

	if got := rec.Body.String(); got != "late" {
		t.Errorf("응답 = %q, want late", got)
	}
}

// The precise failure, without concurrency: a tree installed after shutdown
// has nobody left to release it. close() already ran and released the previous
// tree; swap() also releases only the previous one. The new pool stays open.
func TestSwapAfterCloseReleasesImmediately(t *testing.T) {
	rt := &root{}

	var first, second atomic.Int32
	rt.swap(handlerSaying("install"), func() { first.Add(1) })

	rt.close()
	if got := first.Load(); got != 1 {
		t.Fatalf("close 후 첫 트리 정리 횟수 = %d, want 1", got)
	}

	rt.swap(handlerSaying("operating"), func() { second.Add(1) })
	if got := second.Load(); got != 1 {
		t.Fatalf("종료 후 들어온 트리의 정리 횟수 = %d, want 1 — 아무도 닫지 않는다", got)
	}
}

// The stronger guarantee: not just "never twice" but "every tree exactly once".
//
// A swap landing after close() used to leave its tree open — close() released
// the old tree and swap() also released only the old one, so the newly
// installed pool was never freed. Counting per tree catches that; counting
// double-runs does not.
func TestEveryTreeIsClosedExactlyOnce(t *testing.T) {
	const swaps = 300

	var mu sync.Mutex
	counts := make([]int, 0, swaps+1)

	// newCounted returns a cleanup that records into its own slot.
	newCounted := func() func() {
		mu.Lock()
		i := len(counts)
		counts = append(counts, 0)
		mu.Unlock()
		return func() {
			mu.Lock()
			counts[i]++
			mu.Unlock()
		}
	}

	rt := &root{}
	rt.swap(handlerSaying("install"), newCounted())

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < swaps; i++ {
			rt.swap(handlerSaying("operating"), newCounted())
		}
	}()
	go func() {
		defer wg.Done()
		rt.close()
	}()
	wg.Wait()

	// No trailing rt.close() on purpose. Calling it here would release the last
	// tree and hide exactly the leak this test exists to catch.

	mu.Lock()
	defer mu.Unlock()
	for i, n := range counts {
		if n != 1 {
			t.Fatalf("tree %d 의 정리 횟수 = %d, want 1 (전체 %d개)", i, n, len(counts))
		}
	}
	if len(counts) != swaps+1 {
		t.Fatalf("생성된 tree = %d, want %d", len(counts), swaps+1)
	}
}

// The swap happens while the server is serving, so it must be safe against
// concurrent requests. Meaningful under -race, which `make test` enables.
func TestRootSwapIsConcurrencySafe(t *testing.T) {
	rt := &root{}
	rt.swap(handlerSaying("install"), nil)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				rec := httptest.NewRecorder()
				rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
				if s := rec.Body.String(); s != "install" && s != "operating" {
					t.Errorf("served neither tree: %q", s)
					return
				}
			}
		}()
	}

	for i := 0; i < 200; i++ {
		rt.swap(handlerSaying("operating"), nil)
		rt.swap(handlerSaying("install"), nil)
	}
	close(stop)
	wg.Wait()
}

// FR-110 lives here: the one branch that must never map a broken config back
// to "not installed", because that hands the site to whoever reaches it first.
func TestDecideBoot(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bootDecision
	}{
		{"설정 없음 → 설치 모드", config.ErrNotInstalled, bootInstall},
		{"감싼 ErrNotInstalled 도 설치 모드", fmt.Errorf("load: %w", config.ErrNotInstalled), bootInstall},
		{"정상 → 운영 모드", nil, bootOperate},
		{"파싱 실패 → 기동 중단", errors.New("parse ondolith.json: unexpected character"), bootAbort},
		{"권한 오류 → 기동 중단", fs.ErrPermission, bootAbort},
		{"database_url 없음 → 기동 중단", errors.New("config: ondolith.json has no database_url"), bootAbort},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decideBoot(tt.err); got != tt.want {
				t.Errorf("decideBoot(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

// Spelled out separately because it is the security property, not a case in a
// table: no error other than ErrNotInstalled may ever reach the wizard.
func TestDecideBootNeverStartsWizardOnBrokenConfig(t *testing.T) {
	for _, err := range []error{
		errors.New("invalid character 'x'"),
		fs.ErrPermission,
		fmt.Errorf("wrapped: %w", errors.New("truncated json")),
		errors.New(""),
	} {
		if got := decideBoot(err); got == bootInstall {
			t.Errorf("decideBoot(%q) 이 설치 마법사를 열었다 — 사이트 재점유 경로", err)
		}
	}
}

// `go test` builds record no vcs.revision, so these cases drive the decision
// directly rather than through versionString(); otherwise both the stamped and
// unstamped branches return the same thing and the test proves nothing.
func TestFormatVersion(t *testing.T) {
	const rev = "80813d3fffaa1234"

	tests := []struct {
		name    string
		version string
		rev     string
		want    string
	}{
		{"릴리즈 스탬프가 우선", "v1.2.3", rev, "v1.2.3"},
		{"스탬프가 있으면 리비전을 붙이지 않는다", "v1.2.3", "", "v1.2.3"},
		{"미스탬프 + 리비전 → 12자로 자른다", "dev", rev, "dev+80813d3fffaa"},
		{"미스탬프 + 리비전 없음", "dev", "", "dev"},
		{"미스탬프 + 짧은 리비전은 무시", "dev", "80813d3", "dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatVersion(tt.version, tt.rev); got != tt.want {
				t.Errorf("formatVersion(%q, %q) = %q, want %q", tt.version, tt.rev, got, tt.want)
			}
		})
	}
}

// The wiring, separate from the decision: whatever the build recorded, the
// binary must never report an empty version.
func TestVersionStringIsNeverEmpty(t *testing.T) {
	orig := version
	t.Cleanup(func() { version = orig })

	for _, v := range []string{"dev", "v9.9.9"} {
		version = v
		got := versionString()
		if got == "" {
			t.Errorf("version=%q → versionString() 이 비어 있다", v)
		}
		if !strings.HasPrefix(got, v) {
			t.Errorf("version=%q → versionString() = %q, %q 로 시작해야 한다", v, got, v)
		}
	}
}

// TestServerTimeoutsAllSet pins every timeout on the listener.
//
// ReadHeaderTimeout alone leaves a slow client able to hold a connection while
// it dribbles a request body or reads a response one byte at a time. On the
// single small instance this product targets (NFR-101) that is the cheapest
// denial of service available, and it is invisible in a passing test suite
// because nothing else in the process notices.
func TestServerTimeoutsAllSet(t *testing.T) {
	srv := newServer(":0", http.NotFoundHandler())

	for _, tc := range []struct {
		name string
		got  time.Duration
	}{
		{"ReadHeaderTimeout", srv.ReadHeaderTimeout},
		{"ReadTimeout", srv.ReadTimeout},
		{"WriteTimeout", srv.WriteTimeout},
		{"IdleTimeout", srv.IdleTimeout},
	} {
		if tc.got <= 0 {
			t.Errorf("%s = %v, 0 이면 무제한이다 — 슬로우 클라이언트가 연결을 무한 점유한다", tc.name, tc.got)
		}
	}

	// Ordering, not magnitudes. Headers arrive before the body, the body before
	// the response is done, and a kept-alive connection outlives one exchange.
	// A later edit that sets ReadTimeout below ReadHeaderTimeout would make the
	// header budget unreachable, and the values-are-nonzero check above would
	// still pass.
	if srv.ReadTimeout < srv.ReadHeaderTimeout {
		t.Errorf("ReadTimeout(%v) < ReadHeaderTimeout(%v): 헤더 예산에 도달할 수 없다",
			srv.ReadTimeout, srv.ReadHeaderTimeout)
	}
	if srv.WriteTimeout < srv.ReadTimeout {
		t.Errorf("WriteTimeout(%v) < ReadTimeout(%v): 다 읽기도 전에 응답이 끊긴다",
			srv.WriteTimeout, srv.ReadTimeout)
	}
	if srv.IdleTimeout < srv.WriteTimeout {
		t.Errorf("IdleTimeout(%v) < WriteTimeout(%v): keep-alive 가 한 번의 요청보다 짧다",
			srv.IdleTimeout, srv.WriteTimeout)
	}
}

// TestServerReadTimeoutActuallyDisconnects proves the field is enforced, not
// merely set. A test that only reads struct fields restates the constants; this
// one holds a connection open past the deadline and requires the server to
// close it.
func TestServerReadTimeoutActuallyDisconnects(t *testing.T) {
	srv := newServer(":0", http.NotFoundHandler())
	srv.ReadTimeout = 150 * time.Millisecond
	srv.ReadHeaderTimeout = 150 * time.Millisecond

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go srv.Serve(ln)
	defer srv.Close()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// A request that never finishes: no blank line, so the server keeps reading.
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	buf := make([]byte, 1)
	// Either an explicit close (io.EOF / reset) or a 408 body counts: both mean
	// the server refused to hold the connection. A test timeout means it did.
	if _, err := conn.Read(buf); err != nil && !errors.Is(err, io.EOF) {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			t.Fatal("읽기 시한을 넘겼는데 서버가 연결을 끊지 않았다 — ReadTimeout 이 적용되지 않는다")
		}
	}
}
