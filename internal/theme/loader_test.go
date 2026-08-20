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

func fakeBuiltin() fstest.MapFS {
	return fstest.MapFS{
		"base.html":       {Data: []byte(`<html>{{block "body" .}}내장 본문{{end}}</html>`)},
		"page.html":       {Data: []byte(`{{define "body"}}내장 페이지{{end}}`)},
		"board/view.html": {Data: []byte(`{{define "body"}}내장 게시판{{end}}`)},
		// D17: 자산은 테마의 `static/` 아래 있다. 이 픽스처가 루트에 두는
		// 바람에 `/static/page.html` 이 템플릿 원문을 서빙하는데도 검사가
		// 초록이었다 — 픽스처가 실물과 다르면 통과는 아무 뜻이 없다.
		"static/css/style.css": {Data: []byte("body{}")},
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

	l := New(fakeBuiltin(), dir, false, nil)

	if got := render(t, l, "page.html"); !strings.Contains(got, "디스크 페이지") {
		t.Errorf("디스크 템플릿이 안 쓰였다: %s", got)
	}
	// board/view.html was not overridden, so it must fall back.
	if got := render(t, l, "board/view.html"); !strings.Contains(got, "내장 게시판") {
		t.Errorf("폴백이 안 됐다: %s", got)
	}
}

func TestBuiltinOnlyWhenNoDir(t *testing.T) {
	l := New(fakeBuiltin(), "", false, nil)
	if got := render(t, l, "page.html"); !strings.Contains(got, "내장 페이지") {
		t.Errorf("내장 테마가 안 쓰였다: %s", got)
	}
}

// A name the built-in theme lacks is a core bug, not a theme error — there is
// no floor to fall back to.
func TestMissingEverywhereIsAnError(t *testing.T) {
	l := New(fakeBuiltin(), "", false, nil)
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

	l := New(fakeBuiltin(), dir, false, nil)
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
	l := New(fakeBuiltin(), dir, false, nil)
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

	prod := New(fakeBuiltin(), dir, false, nil)
	if got := render(t, prod, "page.html"); !strings.Contains(got, "처음") {
		t.Fatal(got)
	}
	write(t, dir, "page.html", `{{define "body"}}수정됨{{end}}`)
	if got := render(t, prod, "page.html"); strings.Contains(got, "수정됨") {
		t.Error("운영 모드가 매 요청 재파싱하고 있다")
	}

	dev := New(fakeBuiltin(), dir, true, nil)
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
	if _, err := ValidateThemeDir(dir, "1.0.0"); !errors.Is(err, ErrNoBase) {
		t.Errorf("base.html 없는 테마가 통과했다: %v", err)
	}
	write(t, dir, "base.html", "x")
	if _, err := ValidateThemeDir(dir, "1.0.0"); err != nil {
		t.Errorf("base.html 이 있는데 거부됐다: %v", err)
	}
	if _, err := ValidateThemeDir("", "1.0.0"); err != nil {
		t.Errorf("내장 전용이 거부됐다: %v", err)
	}
}

func TestHasBuiltin(t *testing.T) {
	l := New(fakeBuiltin(), "", false, nil)
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
	l := New(fakeBuiltin(), dir, false, template.FuncMap{})
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

// D17 의 구조 약속과 템플릿 목록을 활성화 시점에 대조한다.
//
// **제3자 테마는 여기 말고 어디도 지나지 않는다.** 게이트는 내장 테마 하나만
// 띄우고 `make ui` 는 이 저장소의 테마만 잰다. 남이 만든 테마가 계약을 어겼는지
// 기계가 보는 자리는 활성화뿐이다.
func TestValidateThemeDirChecksTheContract(t *testing.T) {
	// 계약을 지킨 테마는 조용하다. 이것이 없으면 「전부 경고하는」 검사도
	// 아래 케이스들을 통과시킨다.
	good := t.TempDir()
	write(t, good, "base.html", `<header class="site-header"></header><footer class="site-footer"></footer>`)
	if warn, err := ValidateThemeDir(good, "1.0.0"); err != nil || warn != "" {
		t.Errorf("계약을 지킨 테마에 경고가 붙었다: warn=%q err=%v", warn, err)
	}

	// 세로축을 잴 띠가 없다 — 그만큼 make ui 의 규칙이 헛돈다.
	for _, missing := range []struct{ name, base, want string }{
		{"머리 띠 없음", `<footer class="site-footer"></footer>`, "site-header"},
		{"바닥 띠 없음", `<header class="site-header"></header>`, "site-footer"},
	} {
		t.Run(missing.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, "base.html", missing.base)
			warn, err := ValidateThemeDir(dir, "1.0.0")
			if err != nil {
				t.Fatalf("거부됐다 — 경고여야 한다 (D17 규칙 2 는 base.html 하나만 필수다): %v", err)
			}
			if !strings.Contains(warn, missing.want) {
				t.Errorf("경고에 %q 가 없다: %q", missing.want, warn)
			}
		})
	}

	// **코어가 찾지 않는 이름은 아무 화면도 바꾸지 않는다.** 오류가 아니라
	// 침묵이 문제다 — 「고쳤는데 반영이 안 되네」의 대부분이 이것이다.
	unknown := t.TempDir()
	write(t, unknown, "base.html", `<header class="site-header"></header><footer class="site-footer"></footer>`)
	write(t, unknown, "board/listing.html", "x") // 진짜 이름은 board/list.html 이다
	warn, err := ValidateThemeDir(unknown, "1.0.0")
	if err != nil {
		t.Fatalf("모르는 이름 때문에 거부됐다 — 경고여야 한다: %v", err)
	}
	if !strings.Contains(warn, "board/listing.html") {
		t.Errorf("코어가 찾지 않는 이름을 짚지 않았다: %q", warn)
	}

	// 코어가 아는 이름은 경고하지 않는다. 이것이 없으면 위 케이스는
	// 「모든 템플릿을 모르는 이름이라 부르는」 구현으로도 통과한다.
	known := t.TempDir()
	write(t, known, "base.html", `<header class="site-header"></header><footer class="site-footer"></footer>`)
	write(t, known, "board/list.html", "x")
	if warn, err := ValidateThemeDir(known, "1.0.0"); err != nil || warn != "" {
		t.Errorf("코어가 아는 이름에 경고가 붙었다: warn=%q err=%v", warn, err)
	}
}
