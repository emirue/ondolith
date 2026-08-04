// Command ondolith is the whole product: front-end, admin and API in one
// binary. On boot it either serves the install wizard or the site.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/emirue/ondolith/internal/app"
	"github.com/emirue/ondolith/internal/config"
	"github.com/emirue/ondolith/internal/install"
)

// version is stamped at release time with -ldflags "-X main.version=v1.2.3".
var version = "dev"

// tree is a route tree together with the resources it owns. The two travel as
// one value so that swapping trees cannot leave a handler live with its
// cleanup already run, or a cleanup orphaned with no owner.
type tree struct {
	handler http.Handler
	cleanup func() // nil for the install tree, which owns nothing
	once    sync.Once
}

func (t *tree) close() {
	if t != nil && t.cleanup != nil {
		t.once.Do(t.cleanup)
	}
}

// root is the live handler. Completing the install swaps the install tree for
// the operating tree in place, so the operator never has to restart by hand.
//
// The swap runs on an HTTP handler goroutine while shutdown runs on the main
// one, so every access goes through the atomic pointer — a plain field beside
// it would be a data race between "install finished" and SIGTERM.
type root struct {
	t atomic.Pointer[tree]
	// closed is set before close() reads the live tree, so a swap that lands
	// after it can tell that nobody is coming back to release the tree it is
	// installing. Without this, a swap racing shutdown leaves the new tree's
	// pool open: close() released the old tree, and swap() also releases only
	// the old one.
	closed atomic.Bool
}

// swap makes h live and releases whatever the previous tree owned.
func (r *root) swap(h http.Handler, cleanup func()) {
	next := &tree{handler: h, cleanup: cleanup}
	r.t.Swap(next).close()
	if r.closed.Load() {
		next.close()
	}
}

// close releases the live tree. Safe to call concurrently with swap: every
// cleanup goes through sync.Once, so each tree is released exactly once no
// matter which side reaches it.
func (r *root) close() {
	r.closed.Store(true)
	r.t.Load().close()
}

func (r *root) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// run() always swaps a tree in before it listens, but that is call order,
	// not something the type enforces. Serving 503 instead of dereferencing nil
	// keeps a future reordering from turning into a panic on every request.
	t := r.t.Load()
	if t == nil {
		http.Error(w, "서버가 아직 준비되지 않았습니다.", http.StatusServiceUnavailable)
		return
	}
	t.handler.ServeHTTP(w, req)
}

// bootDecision is what the config on disk says the server should do. Split out
// of run() because run() parses flags and binds a socket, so the branch that
// decides whether a broken config may restart the install wizard — the FR-110
// hole that lets a passer-by re-point the site — would otherwise be untestable.
type bootDecision int

const (
	bootInstall bootDecision = iota // no config yet: serve the wizard
	bootOperate                     // config loads: serve the site
	bootAbort                       // config exists but is unusable: stop
)

func decideBoot(loadErr error) bootDecision {
	switch {
	case errors.Is(loadErr, config.ErrNotInstalled):
		return bootInstall
	case loadErr != nil:
		// Never fall back to install mode here (FR-110).
		return bootAbort
	default:
		return bootOperate
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ondolith:", err)
		os.Exit(1)
	}
}

// newServer builds the listener with every timeout set.
//
// A slow client that dribbles a request body, or reads a response one byte at
// a time, otherwise holds a connection for as long as it likes. On a
// 1 vCPU / 512MB instance (NFR-101) that is the cheapest denial of service
// there is, and ReadHeaderTimeout alone only covers the headers.
//
// WriteTimeout bounds a whole response, so it is the ceiling on the largest
// theme asset or attachment we intend to serve. IdleTimeout bounds a kept-alive
// connection that has gone quiet; without it, idle connections accumulate.
func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

func run() error {
	var (
		addr        = flag.String("addr", ":8080", "listen address")
		configPath  = flag.String("config", "ondolith.json", "path to the config file")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("ondolith", versionString())
		return nil
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rt := &root{}

	// startOperating builds the operating tree and makes it live.
	startOperating := func(cfg *config.Config) error {
		h, cleanup, err := app.New(ctx, cfg, log)
		if err != nil {
			return err
		}
		rt.swap(h, cleanup)
		return nil
	}

	cfg, err := config.Load(*configPath)
	switch decideBoot(err) {
	case bootInstall:
		h, err := install.New(*configPath, log, startOperating)
		if err != nil {
			return err
		}
		rt.swap(h, nil)
		// The listen address is logged as given: it may be ":8080" or bound to
		// a public interface, and printing a made-up "localhost" host would
		// tell the operator the wrong thing about who can reach this.
		log.Warn("설치되지 않았습니다. 설치를 마치기 전까지는 이 주소에 접근할 수 있는 누구나 사이트를 점유할 수 있습니다",
			"addr", *addr, "path", "/install")
	case bootAbort:
		return err
	case bootOperate:
		if err := startOperating(cfg); err != nil {
			return err
		}
		log.Info("운영 모드", "site", cfg.SiteName, "version", versionString())
	}

	srv := newServer(*addr, rt)

	errc := make(chan error, 1)
	go func() {
		log.Info("수신 대기", "addr", *addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	log.Info("종료 중")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Shutdown returns early if the deadline expires, so handlers — including
	// one finishing an install — may still be running here. rt.close() is safe
	// against a concurrent swap, but a handler still in flight will find its
	// pool closed. Say so rather than letting it look like a clean stop.
	err = srv.Shutdown(shutdownCtx)
	if err != nil {
		log.Warn("종료 대기 시간을 넘겨 진행 중인 요청이 끊겼을 수 있습니다", "err", err)
	}
	rt.close()
	return err
}

// versionString prefers the ldflags value and falls back to the VCS revision
// recorded by the Go toolchain, so an unreleased build still identifies itself.
func versionString() string { return formatVersion(version, vcsRevision()) }

// formatVersion is the decision on its own, split out because `go test` builds
// record no VCS revision — a test driving versionString() directly cannot tell
// the stamped and unstamped paths apart and silently passes either way.
func formatVersion(v, rev string) string {
	if v != "dev" {
		return v
	}
	if len(rev) >= 12 {
		return v + "+" + rev[:12]
	}
	return v
}

func vcsRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			return s.Value
		}
	}
	return ""
}
