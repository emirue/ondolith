package theme

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestStaticServesBuiltin(t *testing.T) {
	l := New(fakeBuiltin(), "", false, nil)
	rec := get(t, l.StaticHandler("/static"), "/static/css/style.css")
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "body{}") {
		t.Errorf("본문 = %q", rec.Body.String())
	}
}

// Disk overrides the built-in, same order as templates: a theme that ships one
// stylesheet still gets the built-in images.
func TestStaticDiskOverridesBuiltin(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "css/style.css", "body{color:red}")
	l := New(fakeBuiltin(), dir, false, nil)
	h := l.StaticHandler("/static")

	if got := get(t, h, "/static/css/style.css").Body.String(); !strings.Contains(got, "red") {
		t.Errorf("디스크 자산이 안 쓰였다: %q", got)
	}
	// Not overridden → built-in.
	if rec := get(t, h, "/static/page.html"); rec.Code != http.StatusOK {
		t.Errorf("내장 폴백이 안 됐다: HTTP %d", rec.Code)
	}
}

// SC-7: a request path reaching the filesystem. Every refusal is 404, never
// 403 — which paths exist is itself information (D15 SC-1 4항).
func TestStaticRefusesEscape(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "css/style.css", "ok")
	outside := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(outside, []byte("비밀"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(outside) })

	l := New(fakeBuiltin(), dir, false, nil)
	h := l.StaticHandler("/static")

	for _, target := range []string{
		"/static/../secret.txt",
		"/static/css/../../secret.txt",
		"/static//etc/passwd",
		"/static/",
		"/static/css",     // directory: a listing would enumerate the theme
		"/static/css/",    // ditto
		"/static/./style", // non-canonical
	} {
		rec := get(t, h, target)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s → HTTP %d, want 404 (본문 %q)", target, rec.Code, rec.Body.String())
		}
	}
}

func TestStaticRefusesSymlinkOutOfTheme(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink")
	}
	dir := t.TempDir()
	write(t, dir, "css/style.css", "ok")
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "secret.txt")
	if err := os.WriteFile(secret, []byte("비밀"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "leak.txt")); err != nil {
		t.Skipf("symlink 불가: %v", err)
	}

	l := New(fakeBuiltin(), dir, false, nil)
	rec := get(t, l.StaticHandler("/static"), "/static/leak.txt")
	if rec.Code != http.StatusNotFound {
		t.Errorf("심볼릭 링크로 테마 밖 파일을 서빙했다: HTTP %d, %q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "비밀") {
		t.Error("테마 밖 내용이 응답에 실렸다")
	}
}

func TestStaticMissingIs404(t *testing.T) {
	l := New(fakeBuiltin(), "", false, nil)
	if rec := get(t, l.StaticHandler("/static"), "/static/none.css"); rec.Code != http.StatusNotFound {
		t.Errorf("HTTP %d, want 404", rec.Code)
	}
}
