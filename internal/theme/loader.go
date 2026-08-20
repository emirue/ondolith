// Package theme loads and renders templates.
//
// Two sources, in order: the active theme's directory on disk, then the
// built-in theme embedded in the binary. Partial override is the normal way to
// use this — dropping one `board/view.html` on disk changes that screen and
// nothing else (FR-308).
//
// Only html/template is used. No code generation, no precompilation: a theme
// has to be replaceable without rebuilding (DEC-3.1).
package theme

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

var (
	ErrNotFound  = errors.New("theme: 템플릿이 없습니다")
	ErrOutside   = errors.New("theme: 테마 디렉터리 밖의 경로입니다")
	ErrNoBase    = errors.New("theme: base.html 이 없습니다")
	ErrBadName   = errors.New("theme: 템플릿 이름 형식이 올바르지 않습니다")
	ErrNoBuiltin = errors.New("theme: 내장 테마에 없는 이름입니다 (코어 버그)")
)

// Loader resolves a template name to parsed templates.
type Loader struct {
	// builtin is the theme compiled into the binary. It is the floor: a name
	// missing from it is a core bug, not a theme problem.
	builtin fs.FS
	// dir is the active theme's directory, or "" for built-in only.
	dir string
	// dev re-parses on every request (FR-306). Production parses once at boot
	// and serves from cache (NFR-104) — a site on a 1-vCPU box cannot afford to
	// re-read templates per request.
	dev   bool
	funcs template.FuncMap

	mu     sync.RWMutex
	cache  map[string]*template.Template
	assets *assetHasher
}

// New returns a loader over the built-in FS, optionally overlaid with dir.
func New(builtin fs.FS, dir string, dev bool, funcs template.FuncMap) *Loader {
	l := &Loader{
		builtin: builtin,
		dir:     filepath.Clean(dir),
		dev:     dev,
		funcs:   funcs,
		cache:   make(map[string]*template.Template),
	}
	if dir == "" {
		l.dir = ""
	}
	l.assets = newAssetHasher(builtin, l.dir)
	return l
}

// validName rejects anything that is not a plain relative template path.
//
// For the disk path this is the earlier and cheaper of two checks — withinDir
// below is what actually holds, and removing this one alone does not open a
// hole. It is kept because it is the ONLY check on the paths that never reach
// withinDir: HasBuiltin and the asset hasher. Layering at a trust boundary is
// not redundancy worth deleting.
func validName(name string) error {
	if name == "" || strings.ContainsAny(name, "\\\x00") {
		return ErrBadName
	}
	if path.IsAbs(name) || strings.HasPrefix(name, "/") {
		return ErrOutside
	}
	clean := path.Clean(name)
	if clean != name || clean == "." || strings.HasPrefix(clean, "..") {
		return ErrOutside
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".." || seg == "" {
			return ErrOutside
		}
	}
	return nil
}

// resolve reads a template's source, disk first.
func (l *Loader) resolve(name string) (string, bool, error) {
	if err := validName(name); err != nil {
		return "", false, err
	}
	if l.dir != "" {
		p := filepath.Join(l.dir, filepath.FromSlash(name))
		// Join already cleans, but the result is re-checked against the root:
		// a symlink inside the theme could still point outside it.
		if !withinDir(l.dir, p) {
			return "", false, ErrOutside
		}
		// Compare canonical paths on both sides. The theme root itself may sit
		// behind a symlink — on macOS /var is one — and comparing a resolved
		// file against an unresolved root rejects every legitimate read.
		if real, err := filepath.EvalSymlinks(p); err == nil && !withinDir(l.root(), real) {
			return "", false, ErrOutside
		}
		if b, err := os.ReadFile(p); err == nil {
			return string(b), true, nil
		}
	}
	b, err := fs.ReadFile(l.builtin, name)
	if err != nil {
		return "", false, ErrNotFound
	}
	return string(b), false, nil
}

// root returns the theme directory with symlinks resolved, falling back to the
// raw path when it cannot be resolved (the directory may not exist yet).
func (l *Loader) root() string {
	if r, err := filepath.EvalSymlinks(l.dir); err == nil {
		return r
	}
	return l.dir
}

func withinDir(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Template returns name parsed together with base.html, so that a page template
// can define blocks the layout fills.
func (l *Loader) Template(name string) (*template.Template, error) {
	if !l.dev {
		l.mu.RLock()
		t, ok := l.cache[name]
		l.mu.RUnlock()
		if ok {
			return t, nil
		}
	}

	baseSrc, _, err := l.resolve("base.html")
	if err != nil {
		return nil, ErrNoBase
	}
	src, _, err := l.resolve(name)
	if err != nil {
		return nil, err
	}

	t := template.New("base.html").Funcs(l.funcs)
	if _, err := t.Parse(baseSrc); err != nil {
		return nil, fmt.Errorf("theme: base.html 파싱: %w", err)
	}
	// Fragments come along. A layout that pulls in partials/header.html has no
	// way to say "load this too", so parsing only base + page leaves every
	// {{template}} call unresolved — which surfaces as a 500 on the first page
	// a visitor opens, not at boot.
	//
	// Each fragment resolves through the same disk-then-builtin path, so a theme
	// can override one fragment and inherit the rest.
	for _, frag := range l.fragments() {
		fsrc, _, ferr := l.resolve(frag)
		if ferr != nil {
			continue
		}
		if _, err := t.New(frag).Parse(fsrc); err != nil {
			return nil, fmt.Errorf("theme: %s 파싱: %w", frag, err)
		}
	}
	if _, err := t.New(name).Parse(src); err != nil {
		return nil, fmt.Errorf("theme: %s 파싱: %w", name, err)
	}

	if !l.dev {
		l.mu.Lock()
		l.cache[name] = t
		l.mu.Unlock()
	}
	return t, nil
}

// fragments lists the partials to parse alongside every page: the union of what
// the built-in theme ships and what the active theme adds. Listing them by
// convention (the partials/ directory) rather than by a hardcoded set means a
// theme can add its own fragment without a core change.
func (l *Loader) fragments() []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(name string) {
		if _, dup := seen[name]; dup {
			return
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	if entries, err := fs.ReadDir(l.builtin, "partials"); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".html") {
				add("partials/" + e.Name())
			}
		}
	}
	if l.dir != "" {
		if entries, err := os.ReadDir(filepath.Join(l.dir, "partials")); err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".html") {
					add("partials/" + e.Name())
				}
			}
		}
	}
	return out
}

// Render executes name into w. The page template is executed first so its
// {{define}} blocks are registered, then base.html draws the page.
func (l *Loader) Render(w io.Writer, name string, data any) error {
	t, err := l.Template(name)
	if err != nil {
		return err
	}
	return t.ExecuteTemplate(w, "base.html", data)
}

// RenderPartial draws only the screen's own block, without the page chrome.
//
// **조각은 조각이어야 한다.** P-304 는 htmx 가 상품 화면의 한 조각만 갈아
// 끼우려고 부르는데, `Render` 로 그리면 `<!doctype html>` 부터 머리글·바닥글까지
// 통째로 돌아와 그 조각 자리에 페이지가 통째로 박힌다. 아무도 그것을 부르지
// 않아서 지금까지 드러나지 않았을 뿐이다.
func (l *Loader) RenderPartial(w io.Writer, name string, data any) error {
	t, err := l.Template(name)
	if err != nil {
		return err
	}
	return t.ExecuteTemplate(w, "body", data)
}

// HasBuiltin reports whether the built-in theme carries name. A-202 uses this
// before activating a theme: a name the built-in lacks can never be a fallback,
// so requesting it is a core bug rather than a theme error.
func (l *Loader) HasBuiltin(name string) bool {
	if err := validName(name); err != nil {
		return false
	}
	_, err := fs.ReadFile(l.builtin, name)
	return err == nil
}

// ValidateThemeDir checks a candidate directory before it is activated.
//
// base.html is the one required file (D17): everything else falls back. The
// `requires` floor is checked here too, because activating a theme the binary
// is too old for breaks every page — including the screen that would switch
// back. The returned warning covers the cases where the version comparison
// cannot be made; it is advice, not a refusal.
func ValidateThemeDir(dir, current string) (warn string, err error) {
	if dir == "" {
		return "", nil // built-in only
	}
	if _, err := os.Stat(filepath.Join(dir, "base.html")); err != nil {
		return "", ErrNoBase
	}
	m, err := ReadManifest(dir)
	if err != nil {
		return "", err
	}
	w, err := CheckRequires(m.Requires, current)
	if err != nil {
		return "", err
	}
	return joinWarnings(w, contractWarnings(dir)), nil
}

// contractWarnings reports where a candidate theme leaves D17 unmet.
//
// **제3자 테마는 여기 말고 어디도 지나지 않는다.** 게이트는 내장 테마 하나만
// 띄우고, `make ui` 는 이 저장소의 테마만 잰다. 남이 만든 테마가 계약을
// 어겼는지 기계가 보는 자리는 활성화 시점뿐이다.
//
// **거부하지 않고 경고한다.** D17 규칙 2 가 `base.html` 하나만 필수로 정했다 —
// 검사가 그 계약을 조용히 좁히면, 계약을 읽고 만든 테마가 활성화되지 않는다.
func contractWarnings(dir string) []string {
	var out []string

	// ① base.html 이 세로축을 잴 띠를 내는가 (D17 구조 약속).
	//
	// 이 두 클래스는 오래 `ui-audit.mjs` 만 알고 있었다 — 도구가 전제하는
	// 것을 계약이 약속하지 않으면 그 전제는 언제든 깨진다.
	if b, err := os.ReadFile(filepath.Join(dir, "base.html")); err == nil {
		for _, want := range []string{"site-header", "site-footer"} {
			if !bytes.Contains(b, []byte(want)) {
				out = append(out, "base.html 에 "+want+" 가 없다 — 머리·바닥 정렬 검사가 그만큼 헛돈다 (D17 구조 약속)")
			}
		}
	}

	// ② 코어가 찾지 않는 이름의 템플릿.
	//
	// **오류가 아니라 침묵이 문제다.** 그런 파일은 아무 화면도 바꾸지 않고
	// 아무 말도 하지 않는다 — 「고쳤는데 반영이 안 되네」의 대부분이 이것이다.
	known := map[string]bool{}
	_ = fs.WalkDir(Builtin(), ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".html") {
			known[p] = true
		}
		return nil
	})
	// 내장 목록을 못 읽었으면 대조하지 않는다 — 빈 목록으로 대조하면 모든
	// 템플릿이 「모르는 이름」이 되어 오탐이 쏟아진다.
	if len(known) == 0 {
		return out
	}
	var unknown []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".html") {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return nil
		}
		if !known[filepath.ToSlash(rel)] {
			unknown = append(unknown, filepath.ToSlash(rel))
		}
		return nil
	})
	if len(unknown) > 0 {
		sort.Strings(unknown)
		out = append(out, "코어가 찾지 않는 템플릿 이름이라 아무 화면도 바뀌지 않는다: "+strings.Join(unknown, ", "))
	}
	return out
}

// joinWarnings folds the version warning and the contract warnings into the
// one string A-202 renders. Empty pieces drop out so the screen never shows a
// stray separator.
func joinWarnings(first string, rest []string) string {
	all := rest
	if first != "" {
		all = append([]string{first}, rest...)
	}
	return strings.Join(all, " · ")
}
