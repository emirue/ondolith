package theme

import (
	"bytes"
	"io"
	"net/http"
	"path"
	"strings"
)

// StaticHandler serves theme assets at /static/ (P-906, FR-309).
//
// Same two sources and same order as templates: the active theme's directory
// first, the embedded theme second. A theme that ships only a stylesheet gets
// the built-in images for free.
//
// This is an SC-7 surface — a path from the request reaching the filesystem —
// so the refusals matter more than the successes. `../`, an absolute path and a
// symlink out of the theme are all 404, never 403: telling an attacker which
// paths exist is itself information (D15 SC-1 4항).
func (l *Loader) StaticHandler(prefix string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "허용되지 않는 메서드입니다.", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, prefix)
		name = strings.TrimPrefix(name, "/")

		if err := validName(name); err != nil {
			http.NotFound(w, r)
			return
		}
		// D17 puts assets under the theme's `static/` directory, and only that
		// directory is served. Without this the whole theme is reachable —
		// `/static/page.html` would return the raw template, `{{...}}` and all.
		f, _, err := openThemeFile(l.builtin, l.dir, path.Join("static", name))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil || !st.Mode().IsRegular() {
			// A directory listing would enumerate the theme; 404 instead.
			http.NotFound(w, r)
			return
		}
		// A stale theme hash must not make a different theme's bytes immutable.
		// Errors also stay out of the asset cache: a missing file may be added.
		v := r.URL.Query().Get("v")
		switch {
		case l.dev:
			w.Header().Set("Cache-Control", "no-store")
		case v == "":
			w.Header().Set("Cache-Control", "public, max-age=3600")
		case l.AssetURL(name) == "/static/"+name+"?v="+v:
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		default:
			w.Header().Set("Cache-Control", "no-store")
		}
		rs, ok := f.(io.ReadSeeker)
		if !ok {
			data, err := io.ReadAll(f)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			http.ServeContent(w, r, st.Name(), st.ModTime(), bytes.NewReader(data))
			return
		}
		http.ServeContent(w, r, st.Name(), st.ModTime(), rs)
	})
}
