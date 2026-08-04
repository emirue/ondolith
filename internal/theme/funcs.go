package theme

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FuncNames is D17's function map, as a list.
//
// The list is the contract: "여기 없는 것은 템플릿에서 쓸 수 없다". Keeping it
// separate from the construction lets a test assert both directions — every
// documented name is registered, and nothing else is. A quietly added helper
// becomes API that themes depend on and that we then cannot remove.
var FuncNames = []string{
	// URL
	"url", "asset",
	// 포맷
	"date", "dateAgo", "money", "number", "filesize",
	// 문자열·구조
	"truncate", "nl2br", "field", "fields", "pages",
}

// Forbidden names, spelled out so the test can assert their absence rather than
// relying on nobody adding them. `raw`/`safeHTML` would let a theme turn
// escaping off, which is a stored-XSS path (NFR-203); template.HTML stays a
// core-only tool.
var ForbiddenFuncNames = []string{"raw", "safeHTML", "html", "js", "query", "exec", "readFile"}

// Deps are the request-scoped values the functions close over. Passing them in
// keeps this package free of the http and database imports — a theme function
// that could reach the database would let one theme take the site down (FR-305).
// Deps holds only values that are the same for every request.
//
// `isCurrent` and `can` used to live here and were removed: both change per
// request, but a func map is bound when a template is parsed and the parse is
// cached (NFR-104). A closure over a per-request value is either frozen at the
// first request or raced between concurrent ones. The view model already
// carries `.Path` and `.Can` — see D17.
type Deps struct {
	// AssetURL returns the hashed URL for a theme asset.
	AssetURL func(name string) string
	// URLFor builds an application URL by kind.
	URLFor func(kind string, args ...string) string
	// Now is injectable so dateAgo is testable.
	Now func() time.Time
}

// FuncMap builds the map. Every entry here must appear in FuncNames.
func FuncMap(d Deps) template.FuncMap {
	if d.Now == nil {
		d.Now = time.Now
	}
	return template.FuncMap{
		"url": func(kind string, args ...string) string {
			if d.URLFor == nil {
				return ""
			}
			return d.URLFor(kind, args...)
		},
		"asset": func(name string) string {
			if d.AssetURL == nil {
				return ""
			}
			return d.AssetURL(name)
		},
		"date":    func(t time.Time, layout string) string { return t.Format(layout) },
		"dateAgo": func(t time.Time) string { return ago(d.Now().Sub(t)) },
		// money takes an integer minor unit and never a float: D30 keeps money
		// as integers precisely so that no rounding happens on the way to the
		// screen.
		"money":    func(minor int64) string { return formatMoney(minor) },
		"number":   func(n int64) string { return group(n) },
		"filesize": filesize,

		"truncate": truncate,
		// Escape first, then turn newlines into <br>. The other order would let
		// the input close a tag.
		"nl2br": func(s string) template.HTML {
			esc := template.HTMLEscapeString(s)
			return template.HTML(strings.ReplaceAll(esc, "\n", "<br>")) // #nosec G203 -- escaped above; this is the only place theme output is marked safe
		},
		"field":  func(m map[string]any, key string) any { return m[key] },
		"fields": func(m map[string]any) map[string]any { return m },
		"pages":  pageNumbers,
	}
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "방금"
	case d < time.Hour:
		return fmt.Sprintf("%d분 전", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d시간 전", int(d.Hours()))
	default:
		return fmt.Sprintf("%d일 전", int(d.Hours()/24))
	}
}

func group(n int64) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func formatMoney(minor int64) string { return group(minor) + "원" }

func filesize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 0 {
		return ""
	}
	return string(r[:n]) + "…"
}

// pageNumbers returns the page numbers to show around cur.
func pageNumbers(cur, total int) []int {
	if total < 1 {
		return []int{}
	}
	const span = 2
	lo, hi := cur-span, cur+span
	if lo < 1 {
		lo = 1
	}
	if hi > total {
		hi = total
	}
	out := make([]int, 0, hi-lo+1)
	for i := lo; i <= hi; i++ {
		out = append(out, i)
	}
	return out
}

// assetHasher turns a theme asset path into a URL carrying a content hash.
//
// The hash is of the file's contents, not the theme version. Editing a file
// without bumping the version is the most common thing a theme author does, and
// a version-based cache key does not break for it (D17).
type assetHasher struct {
	builtin fs.FS
	dir     string

	mu   sync.RWMutex
	seen map[string]string
}

func newAssetHasher(builtin fs.FS, dir string) *assetHasher {
	return &assetHasher{builtin: builtin, dir: dir, seen: make(map[string]string)}
}

// URL returns /static/<name>?v=<hash8>, or /static/<name> when the file cannot
// be read — a missing asset must not stop the page from rendering.
func (a *assetHasher) URL(name string) string {
	if err := validName(name); err != nil {
		return ""
	}
	a.mu.RLock()
	h, ok := a.seen[name]
	a.mu.RUnlock()
	if !ok {
		h = a.hash(name)
		a.mu.Lock()
		a.seen[name] = h
		a.mu.Unlock()
	}
	if h == "" {
		return "/static/" + name
	}
	return "/static/" + name + "?v=" + h
}

// Forget drops a cached hash so dev mode picks up an edit.
func (a *assetHasher) Forget(name string) {
	a.mu.Lock()
	delete(a.seen, name)
	a.mu.Unlock()
}

func (a *assetHasher) hash(name string) string {
	// Same mapping the handler uses: the URL says `css/style.css`, the file
	// lives at `static/css/style.css` (D17). Hashing a path the handler would
	// not serve produces a URL that 404s with a version string on it.
	name = path.Join("static", name)
	var b []byte
	if a.dir != "" {
		p := filepath.Join(a.dir, filepath.FromSlash(name))
		if withinDir(a.dir, p) {
			if data, err := os.ReadFile(p); err == nil {
				b = data
			}
		}
	}
	if b == nil {
		data, err := fs.ReadFile(a.builtin, path.Clean(name))
		if err != nil {
			return ""
		}
		b = data
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:8]
}

// AssetURL exposes the hasher to Deps.
func (l *Loader) AssetURL(name string) string { return l.assets.URL(name) }

// ForgetAsset drops one cached hash (dev mode).
func (l *Loader) ForgetAsset(name string) { l.assets.Forget(name) }
