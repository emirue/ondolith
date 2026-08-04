package theme

import (
	"embed"
	"io/fs"
)

// Builtin is the theme compiled into the binary. It is the floor every lookup
// falls back to, so a name missing here is a core bug rather than a theme
// problem (D17 폴백 동작).
//
// The static/ subtree is served by StaticHandler; the .html files are
// templates. Both live in one FS because a theme on disk is also one directory.
//
//go:embed all:builtin
var builtinFS embed.FS

// Builtin returns the embedded theme rooted at its own directory, so template
// names are "page.html" rather than "builtin/page.html".
func Builtin() subFS { return mustSub(builtinFS, "builtin") }

type subFS = fs.FS

func mustSub(f embed.FS, dir string) subFS {
	s, err := fs.Sub(f, dir)
	if err != nil {
		panic("theme: 내장 테마를 열 수 없다: " + err.Error())
	}
	return s
}
