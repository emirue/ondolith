package app

import "net/url"

// urlFor is D17's `url` function: a theme names a kind and gets a path.
//
// Themes never concatenate paths themselves. A theme that hard-codes
// `/board/{{.Slug}}` breaks the moment a route moves, and it is third-party
// code that nobody here can edit (FR-302).
//
// An unknown kind returns "" rather than guessing. A wrong link is harder to
// notice than a missing one.
func urlFor(kind string, args ...string) string {
	esc := make([]string, len(args))
	for i, a := range args {
		esc[i] = url.PathEscape(a)
	}
	switch {
	case kind == "page" && len(esc) == 1:
		return "/" + esc[0]
	case kind == "board" && len(esc) == 1:
		return "/board/" + esc[0]
	case kind == "post" && len(esc) == 2:
		return "/board/" + esc[0] + "/" + esc[1]
	case kind == "product" && len(esc) == 1:
		return "/product/" + esc[0]
	case kind == "order" && len(esc) == 1:
		return "/order/" + esc[0]
	}
	return ""
}
