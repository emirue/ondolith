package theme

import (
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
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
		name := strings.TrimPrefix(r.URL.Path, prefix)
		name = strings.TrimPrefix(name, "/")

		if err := validName(name); err != nil {
			http.NotFound(w, r)
			return
		}

		if l.dir != "" {
			p := filepath.Join(l.dir, filepath.FromSlash(name))
			if !withinDir(l.dir, p) {
				http.NotFound(w, r)
				return
			}
			// A symlink inside the theme can still point outside it; the
			// resolved path is what decides.
			real, err := filepath.EvalSymlinks(p)
			if err == nil && !withinDir(l.root(), real) {
				http.NotFound(w, r)
				return
			}
			if err == nil {
				if st, serr := os.Stat(real); serr == nil && st.Mode().IsRegular() {
					http.ServeFile(w, r, real)
					return
				}
			}
		}

		f, err := l.builtin.Open(path.Clean(name))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil || st.IsDir() {
			// A directory listing would enumerate the theme; 404 instead.
			http.NotFound(w, r)
			return
		}
		rs, ok := f.(io.ReadSeeker)
		if !ok {
			data, err := fs.ReadFile(l.builtin, path.Clean(name))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			http.ServeContent(w, r, st.Name(), st.ModTime(), strings.NewReader(string(data)))
			return
		}
		http.ServeContent(w, r, st.Name(), st.ModTime(), rs)
	})
}
