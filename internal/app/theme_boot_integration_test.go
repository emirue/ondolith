package app

import (
	"bytes"
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emirue/ondolith/internal/config"
	"github.com/emirue/ondolith/internal/theme"
)

// lockedLog is the boot log a test reads while the server's goroutines may still write.
type lockedLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// logLines is every line of the log that contains needle.
func logLines(l *lockedLog, needle string) []string {
	var out []string
	for _, line := range strings.Split(l.String(), "\n") {
		if strings.Contains(line, needle) {
			out = append(out, line)
		}
	}
	return out
}

// writeTheme puts one disk theme under root: the built-in base.html plus the given files.
func writeTheme(t *testing.T, root, name string, files map[string]string) {
	t.Helper()
	base, err := fs.ReadFile(theme.Builtin(), "base.html")
	if err != nil {
		t.Fatal(err)
	}
	files["base.html"] = string(base)
	for rel, body := range files {
		p := filepath.Join(root, name, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// bootTheme activates name in the database and starts the app again on the same schema with
// the given version — the restart a deploy does — returning the server and its log.
func bootTheme(t *testing.T, root, name, version string) (*httptest.Server, *pgxpool.Pool, *lockedLog) {
	t.Helper()
	_, pool := liveSite(t)
	if _, err := pool.Exec(context.Background(), `INSERT INTO settings (key, value) VALUES ('theme.active', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, name); err != nil {
		t.Fatal(err)
	}
	logs := &lockedLog{}
	cfg := &config.Config{DatabaseURL: os.Getenv(dsnEnv), SiteName: "테스트 사이트", UploadDir: t.TempDir(), ThemeDir: root}
	h, cleanup, err := New(context.Background(), cfg, version, slog.New(slog.NewTextHandler(logs, nil)), nil)
	if err != nil {
		t.Fatalf("재기동 실패: %v", err)
	}
	t.Cleanup(cleanup)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, pool, logs
}

// FR-312 을 실제 조립으로: 테마 템플릿이 없는 필드를 읽으면 500 이고 본문에 테마 조각이 없다.
// 같은 테마의 다른 화면과 /healthz 는 그대로다.
func TestBrokenThemeTemplateServesA500NotHalfAPage(t *testing.T) {
	root := t.TempDir()
	writeTheme(t, root, "broken", map[string]string{
		"home.html": `{{define "body"}}<p id="theme-fragment">테마 조각</p>{{.Nope}}{{end}}`,
	})
	srv, _, logs := bootTheme(t, root, "broken", "1.0.0")

	code, home := mustGet(t, client(), srv.URL+"/")
	if code != http.StatusInternalServerError || strings.Contains(home, "theme-fragment") ||
		!strings.Contains(home, "<h1>일시적인 오류</h1>") {
		t.Errorf("깨진 홈: HTTP %d, 테마 조각 없이 내장 오류 화면이어야 한다\n%s", code, home)
	}
	if !strings.Contains(logs.String(), "template=home.html") {
		t.Errorf("ERROR 로그에 템플릿 이름이 없다:\n%s", logs.String())
	}
	if code, _ := mustGet(t, client(), srv.URL+"/login"); code != http.StatusOK {
		t.Errorf("깨지지 않은 화면 /login = HTTP %d", code)
	}
	if code, _ := mustGet(t, client(), srv.URL+"/healthz"); code != http.StatusOK {
		t.Errorf("/healthz = HTTP %d", code)
	}
}

// FR-313: 켜져 있는 테마의 requires 가 코어보다 높은 채 재시작하면 사이트를 내리지 않고 내장 테마로
// 뜬다 — ERROR 한 줄, 공개 화면 200(내장), /healthz 200, A-602 경고. 다른 테마를 켜면 경고가 걷힌다.
func TestBootServesTheBuiltinThemeWhenTheActiveOneNeedsANewerCore(t *testing.T) {
	root := t.TempDir()
	writeTheme(t, root, "future", map[string]string{
		"theme.json": `{"name": "미래", "requires": "9.0.0"}`,
		"home.html":  `{{define "body"}}<p id="future-home">미래 테마</p>{{end}}`,
	})
	srv, pool, logs := bootTheme(t, root, "future", "v1.2.3")

	if errs := logLines(logs, "level=ERROR"); len(errs) != 1 || !strings.Contains(errs[0], "9.0.0") || !strings.Contains(errs[0], "future") {
		t.Errorf("부팅 로그의 ERROR 는 테마 이름·요구 버전이 든 한 줄이어야 한다: %q", errs)
	}
	code, home := mustGet(t, client(), srv.URL+"/")
	if code != http.StatusOK || strings.Contains(home, "future-home") {
		t.Errorf("홈: HTTP %d, 내장 테마여야 한다 (미래 테마의 홈이 그려졌는가 %v)", code, strings.Contains(home, "future-home"))
	}
	if code, _ := mustGet(t, client(), srv.URL+"/healthz"); code != http.StatusOK {
		t.Errorf("/healthz = HTTP %d", code)
	}

	c, post := adminSession(t, srv, pool)
	_, sys := mustGet(t, c, srv.URL+"/admin/system")
	if !strings.Contains(sys, "adm-toast--warn") || !strings.Contains(sys, "9.0.0") || !strings.Contains(sys, "v1.2.3") {
		t.Errorf("A-602 에 경고가 없다 (요구 버전·현재 버전)\n%s", sys)
	}
	// A-202 도 같다 — 고른 테마는 「선택됨」, 경고에 까닭.
	if _, themes := mustGet(t, c, srv.URL+"/admin/themes"); !strings.Contains(themes, "adm-toast--warn") ||
		!strings.Contains(themes, "9.0.0") || !strings.Contains(themes, "선택됨") {
		t.Errorf("A-202 이 내장 테마로 뜬 것을 말하지 않는다\n%s", themes)
	}

	// 운영자가 다른 테마(내장)를 켜면 내장으로 띄운 까닭이 사라진다 — 경고도 걷힌다.
	resp := post("/admin/themes", url.Values{"theme": {""}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("내장 테마 켜기 = HTTP %d", resp.StatusCode)
	}
	if _, sys := mustGet(t, c, srv.URL+"/admin/system"); strings.Contains(sys, "9.0.0") {
		t.Errorf("다른 테마를 켠 뒤에도 A-602 경고가 남았다")
	}
}

// 개발 빌드처럼 버전을 해석할 수 없으면 지금처럼 경고만 한다 — 테마는 그대로 뜬다 (D17).
func TestBootOnlyWarnsAboutRequiresOnADevBuild(t *testing.T) {
	root := t.TempDir()
	writeTheme(t, root, "future", map[string]string{
		"theme.json": `{"name": "미래", "requires": "9.0.0"}`,
		"home.html":  `{{define "body"}}<p id="future-home">미래 테마</p>{{end}}`,
	})
	srv, _, logs := bootTheme(t, root, "future", "dev")

	if errs, req := logLines(logs, "level=ERROR"), logLines(logs, "9.0.0"); len(errs) != 0 || len(req) != 1 ||
		!strings.Contains(req[0], "level=WARN") {
		t.Errorf("개발 빌드: ERROR 없이 요구 버전 WARN 한 줄이어야 한다 — ERROR %q, 요구 버전 %q", errs, req)
	}
	if code, home := mustGet(t, client(), srv.URL+"/"); code != http.StatusOK || !strings.Contains(home, "future-home") {
		t.Errorf("개발 빌드에서는 테마가 그대로 떠야 한다: HTTP %d", code)
	}
}

// requires 가 major.minor.patch 가 아니면 비교할 수 없다 — 이미 켜진 테마는 경고만 하고 그대로 뜬다.
// 비교할 수 없는 값 하나로 사이트의 테마를 내리지 않는다. 같은 값은 활성화(A-202)에서 거부된다.
func TestBootOnlyWarnsAboutAMalformedRequires(t *testing.T) {
	root := t.TempDir()
	writeTheme(t, root, "future", map[string]string{
		"theme.json": `{"name": "미래", "requires": "nine"}`,
		"home.html":  `{{define "body"}}<p id="future-home">미래 테마</p>{{end}}`,
	})
	srv, _, logs := bootTheme(t, root, "future", "v1.2.3")

	if errs, req := logLines(logs, "level=ERROR"), logLines(logs, "nine"); len(errs) != 0 || len(req) != 1 ||
		!strings.Contains(req[0], "level=WARN") {
		t.Errorf("깨진 requires: ERROR 없이 WARN 한 줄이어야 한다 — ERROR %q, requires %q", errs, req)
	}
	if code, home := mustGet(t, client(), srv.URL+"/"); code != http.StatusOK || !strings.Contains(home, "future-home") {
		t.Errorf("깨진 requires 에서도 테마가 그대로 떠야 한다: HTTP %d", code)
	}
}
