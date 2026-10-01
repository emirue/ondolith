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
	write(t, dir, "static/css/style.css", "body{color:red}")
	l := New(fakeBuiltin(), dir, false, nil)
	h := l.StaticHandler("/static")

	if got := get(t, h, "/static/css/style.css").Body.String(); !strings.Contains(got, "red") {
		t.Errorf("디스크 자산이 안 쓰였다: %q", got)
	}
	// D17 은 `static/` 만 서빙한다. 템플릿은 자산이 아니다 — 원문이 나가면
	// 테마 작성자가 무엇을 어떻게 그리는지가 그대로 공개된다.
	if rec := get(t, h, "/static/page.html"); rec.Code != http.StatusNotFound {
		t.Errorf("템플릿이 /static 으로 서빙됐다: HTTP %d", rec.Code)
	}
}

// SC-7: a request path reaching the filesystem. Every refusal is 404, never
// 403 — which paths exist is itself information (D15 SC-1 4항).
func TestStaticRefusesEscape(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "static/css/style.css", "ok")
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
	write(t, dir, "static/css/style.css", "ok")
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "secret.txt")
	if err := os.WriteFile(secret, []byte("비밀"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "static/leak.txt")); err != nil {
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

func TestStaticCachePolicyMatchesContentAndMode(t *testing.T) {
	l := New(fakeBuiltin(), "", false, nil)
	for _, tc := range []struct{ target, want string }{
		{l.AssetURL("css/style.css"), "public, max-age=31536000, immutable"},
		{"/static/css/style.css", "public, max-age=3600"},
		{"/static/css/style.css?v=old-theme", "no-store"},
		{"/static/missing.css?v=old-theme", ""},
	} {
		if got := get(t, l.StaticHandler("/static/"), tc.target).Header().Get("Cache-Control"); got != tc.want {
			t.Errorf("%s: Cache-Control=%q, want %q", tc.target, got, tc.want)
		}
	}
	dev := New(fakeBuiltin(), "", true, nil)
	if got := get(t, dev.StaticHandler("/static/"), dev.AssetURL("css/style.css")).Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("개발 모드 Cache-Control=%q, want no-store", got)
	}
}

func TestStaticOnlyAcceptsReadMethods(t *testing.T) {
	l := New(fakeBuiltin(), "", false, nil)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		l.StaticHandler("/static/").ServeHTTP(rec, httptest.NewRequest(method, "/static/css/style.css", nil))
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s: HTTP %d, Allow=%q", method, rec.Code, rec.Header().Get("Allow"))
		}
	}
	rec := httptest.NewRecorder()
	l.StaticHandler("/static/").ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/static/css/style.css", nil))
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD: HTTP %d, body=%q", rec.Code, rec.Body.String())
	}
}
