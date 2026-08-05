package app

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/content"
)

// P-211 GET /attachments/{id} — download.
//
// D15 8절 1번: the parent post's read permission is checked HERE, again. "The
// permission was checked on the post screen" is the sentence that precedes the
// hole — the attachment id is a URL anyone can hold, and nothing about it says
// which board it belongs to.
func (d *boardDeps) attachmentDownload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a := ActorFrom(ctx)

	at, err := d.attachments.ByID(ctx, r.PathValue("id"))
	if errors.Is(err, content.ErrNotFound) {
		d.notFound(w, r)
		return
	}
	if err != nil {
		d.serverError(w, r, err)
		return
	}

	b, err := d.content.BoardByPost(ctx, at.PostID)
	if errors.Is(err, content.ErrNotFound) {
		d.notFound(w, r)
		return
	}
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	if !a.CanOn("post.read", auth.BoardID(b.ID)) {
		d.notFound(w, r)
		return
	}
	// A secret post's attachment follows the post: the same filter, so an
	// attachment id cannot be a way around the post's own visibility.
	p, err := d.content.PostByID(ctx, at.PostID, actorID(a),
		a.CanOn("post.read_secret", auth.BoardID(b.ID)))
	if errors.Is(err, content.ErrNotFound) {
		d.notFound(w, r)
		return
	}
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	if p.Status != "published" {
		d.notFound(w, r)
		return
	}

	f, err := d.attachments.Open(at)
	if err != nil {
		// The row exists and the file does not. A-309 allows that state, so it
		// is a 404 rather than a 500: nothing is broken, the file is gone.
		d.notFound(w, r)
		return
	}
	defer f.Close()

	// Content-Type comes from what the server measured at upload, never from
	// the request or the filename (D60 §2).
	w.Header().Set("Content-Type", at.MIMEType)
	w.Header().Set("Content-Length", strconv.FormatInt(at.ByteSize, 10))
	// Always an attachment, never inline. An HTML or SVG file rendered inline
	// runs on this origin with this session's cookies — the allow-list keeps
	// those out today, and this keeps the decision from depending on it.
	w.Header().Set("Content-Disposition", contentDisposition(at.OriginalName))
	// The stored name is a UUID and the browser must not sniff past our type.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := io.Copy(w, f); err != nil {
		d.log.Warn("첨부 전송", "attachment", at.ID, "err", err)
	}
}

// contentDisposition builds the header with the display name.
//
// The name is user input that lands in a header: a newline would inject another
// header, and a quote would end the filename early. ASCII gets the quoted form,
// and everything else goes through RFC 5987's filename* — a Korean filename
// would otherwise arrive as mojibake or be dropped.
func contentDisposition(name string) string {
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' {
			return -1
		}
		return r
	}, name)
	if clean == "" {
		clean = "download"
	}
	ascii := clean
	if !isASCII(clean) {
		ascii = "download"
	}
	return `attachment; filename="` + ascii + `"; filename*=UTF-8''` + url.PathEscape(clean)
}

func isASCII(s string) bool {
	for _, r := range s {
		if r > 0x7f {
			return false
		}
	}
	return true
}
