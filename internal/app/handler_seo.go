package app

import (
	"encoding/xml"
	"net/http"
	"time"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/content"
)

// urlEntry is one <url> in the sitemap.
type urlEntry struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

type urlSet struct {
	XMLName xml.Name   `xml:"urlset"`
	NS      string     `xml:"xmlns,attr"`
	URLs    []urlEntry `xml:"url"`
}

// P-901 GET /sitemap.xml
//
// FR-510: published pages and posts only. The rule is enforced by asking with
// ANONYMOUS permissions, not by filtering afterwards — a sitemap is read by
// crawlers that are not logged in, so the set it lists must be exactly the set
// an anonymous visitor can open. Drafts, secret posts and private boards fall
// out because the queries never see them.
func (d *boardDeps) sitemap(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	anon, err := d.authStore.LoadAnonymousPermissions(ctx)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	base := d.baseURL(r)

	set := urlSet{NS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	set.URLs = append(set.URLs, urlEntry{Loc: base + "/"})

	pages, err := d.content.PublishedPages(ctx)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	for _, p := range pages {
		set.URLs = append(set.URLs, urlEntry{Loc: base + "/" + p.Slug})
	}

	boards, err := d.content.Boards(ctx)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	for _, b := range boards {
		if !anon.CanOn("post.read", auth.BoardID(b.ID)) {
			continue
		}
		set.URLs = append(set.URLs, urlEntry{Loc: base + "/board/" + b.Slug})

		// Anonymous never holds post.read_secret here: the sitemap asks with
		// the anonymous permission set, so a secret post cannot be listed even
		// if some role could read it.
		posts, err := d.content.ListPosts(ctx, b.ID,
			content.ListQuery{Sort: "created", Desc: true, PerPage: sitemapPostsPerBoard},
			"", anon.CanOn("post.read_secret", auth.BoardID(b.ID)))
		if err != nil {
			d.serverError(w, r, err)
			return
		}
		for _, p := range posts {
			set.URLs = append(set.URLs, urlEntry{
				Loc:     base + "/board/" + b.Slug + "/" + p.ID,
				LastMod: p.UpdatedAt.UTC().Format(time.RFC3339),
			})
		}
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	if _, err := w.Write([]byte(xml.Header)); err != nil {
		return
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(set); err != nil {
		d.log.Warn("사이트맵 인코딩", "err", err)
	}
}

// sitemapPostsPerBoard bounds one board's contribution. A sitemap is built on
// every request, and an unbounded one turns a crawler visit into a full table
// scan of every board (NFR-101's tier does not have room for that).
const sitemapPostsPerBoard = 500

// P-902 GET /robots.txt
//
// The admin tree is listed as Disallow. That is a request, not a control —
// D15's gate is what actually refuses — but a crawler that indexes /admin/
// login pages puts them in search results, and the operator finds out from a
// stranger.
func (d *boardDeps) robots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("User-agent: *\n" +
		"Disallow: /admin/\n" +
		"Disallow: /me\n" +
		"Disallow: /search\n" +
		"Disallow: /attachments/\n" +
		"Sitemap: " + d.baseURL(r) + "/sitemap.xml\n"))
}

// baseURL reconstructs the site's own origin.
//
// It uses the request's Host and NOT a forwarded header: X-Forwarded-Host is
// attacker-controlled on any deployment that does not strip it, and a sitemap
// full of somebody else's origin is a gift to whoever sent the header.
func (d *boardDeps) baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
