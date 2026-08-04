package theme

import (
	"bytes"
	"errors"
	"html/template"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
)

func builtinFS() fstest.MapFS {
	return fstest.MapFS{
		"base.html":       {Data: []byte(`<html>{{block "body" .}}내장 본문{{end}}</html>`)},
		"page.html":       {Data: []byte(`{{define "body"}}내장 페이지{{end}}`)},
		"board/view.html": {Data: []byte(`{{define "body"}}내장 게시판{{end}}`)},
		"css/style.css":   {Data: []byte("body{}")},
	}
}

func render(t *testing.T, l *Loader, name string) string {
	t.Helper()
	var b bytes.Buffer
	if err := l.Render(&b, name, nil); err != nil {
		t.Fatalf("render %s: %v", name, err)
	}
	return b.String()
}

// FR-308: dropping one file on disk overrides that screen and nothing else.
// Partial override is the normal way to author a theme, not an edge case.
func TestPartialOverride(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "base.html", `<html>{{block "body" .}}x{{end}}</html>`)
	write(t, dir, "page.html", `{{define "body"}}디스크 페이지{{end}}`)

	l := New(builtinFS(), dir, false, nil)

	if got := render(t, l, "page.html"); !strings.Contains(got, "디스크 페이지") {
		t.Errorf("디스크 템플릿이 안 쓰였다: %s", got)
	}
	// board/view.html was not overridden, so it must fall back.
	if got := render(t, l, "board/view.html"); !strings.Contains(got, "내장 게시판") {
		t.Errorf("폴백이 안 됐다: %s", got)
	}
}

func TestBuiltinOnlyWhenNoDir(t *testing.T) {
	l := New(builtinFS(), "", false, nil)
	if got := render(t, l, "page.html"); !strings.Contains(got, "내장 페이지") {
		t.Errorf("내장 테마가 안 쓰였다: %s", got)
	}
}

// A name the built-in theme lacks is a core bug, not a theme error — there is
// no floor to fall back to.
func TestMissingEverywhereIsAnError(t *testing.T) {
	l := New(builtinFS(), "", false, nil)
	if _, err := l.Template("nope.html"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// The one place a caller-supplied name meets the filesystem. Traversal has to
// stop here or a theme name reads files the theme does not own.
func TestPathTraversalIsRefused(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "base.html", `<html>{{block "body" .}}x{{end}}</html>`)
	outside := filepath.Join(filepath.Dir(dir), "secret.html")
	if err := os.WriteFile(outside, []byte("비밀"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(outside) })

	l := New(builtinFS(), dir, false, nil)
	for _, name := range []string{
		"../secret.html",
		"../../etc/passwd",
		"/etc/passwd",
		"board/../../secret.html",
		"./page.html",
		"",
	} {
		if _, err := l.Template(name); err == nil {
			t.Errorf("%q 가 허용됐다", name)
		}
	}
}

// A symlink inside the theme still points outside it; Join alone would not
// notice, so the resolved path is re-checked.
func TestSymlinkOutOfThemeIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink")
	}
	dir := t.TempDir()
	write(t, dir, "base.html", `<html>{{block "body" .}}x{{end}}</html>`)
	outside := filepath.Join(t.TempDir(), "secret.html")
	if err := os.WriteFile(outside, []byte(`{{define "body"}}비밀{{end}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link.html")); err != nil {
		t.Skipf("symlink 불가: %v", err)
	}
	l := New(builtinFS(), dir, false, nil)
	if _, err := l.Template("link.html"); !errors.Is(err, ErrOutside) {
		t.Errorf("심볼릭 링크로 테마 밖을 읽었다: %v", err)
	}
}

// FR-306 / NFR-104: production parses once, development re-reads. A 1-vCPU box
// cannot re-parse per request, and an author cannot restart per edit.
func TestDevModeRereadsAndProductionCaches(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "base.html", `<html>{{block "body" .}}x{{end}}</html>`)
	write(t, dir, "page.html", `{{define "body"}}처음{{end}}`)

	prod := New(builtinFS(), dir, false, nil)
	if got := render(t, prod, "page.html"); !strings.Contains(got, "처음") {
		t.Fatal(got)
	}
	write(t, dir, "page.html", `{{define "body"}}수정됨{{end}}`)
	if got := render(t, prod, "page.html"); strings.Contains(got, "수정됨") {
		t.Error("운영 모드가 매 요청 재파싱하고 있다")
	}

	dev := New(builtinFS(), dir, true, nil)
	if got := render(t, dev, "page.html"); !strings.Contains(got, "수정됨") {
		t.Errorf("개발 모드가 재파싱하지 않았다: %s", got)
	}
	write(t, dir, "page.html", `{{define "body"}}또 수정{{end}}`)
	if got := render(t, dev, "page.html"); !strings.Contains(got, "또 수정") {
		t.Errorf("개발 모드가 두 번째 수정을 못 봤다: %s", got)
	}
}

// base.html is the one file a theme must provide (D17). Activating without it
// leaves every page unrenderable, so A-202 refuses before switching.
func TestValidateThemeDirRequiresBase(t *testing.T) {
	dir := t.TempDir()
	if err := ValidateThemeDir(dir); !errors.Is(err, ErrNoBase) {
		t.Errorf("base.html 없는 테마가 통과했다: %v", err)
	}
	write(t, dir, "base.html", "x")
	if err := ValidateThemeDir(dir); err != nil {
		t.Errorf("base.html 이 있는데 거부됐다: %v", err)
	}
	if err := ValidateThemeDir(""); err != nil {
		t.Errorf("내장 전용이 거부됐다: %v", err)
	}
}

func TestHasBuiltin(t *testing.T) {
	l := New(builtinFS(), "", false, nil)
	if !l.HasBuiltin("page.html") {
		t.Error("내장에 있는 이름을 없다고 한다")
	}
	if l.HasBuiltin("nope.html") || l.HasBuiltin("../x") {
		t.Error("없는 이름·탈출 경로를 있다고 한다")
	}
}

// The template escapes by default; a theme must not be able to inject markup
// through data.
func TestOutputIsEscaped(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "base.html", `{{block "body" .}}{{end}}`)
	write(t, dir, "page.html", `{{define "body"}}{{.}}{{end}}`)
	l := New(builtinFS(), dir, false, template.FuncMap{})
	var b bytes.Buffer
	if err := l.Render(&b, "page.html", "<script>alert(1)</script>"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "<script>") {
		t.Errorf("이스케이프되지 않았다: %s", b.String())
	}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
