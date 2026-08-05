package app

import (
	"net/http"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/content"
)

// P-212 GET /search — full-text across boards.
//
// FR-510 in the negative: a board the caller cannot read must not contribute a
// single row. The filter is a WHERE clause built from the caller's grants, not
// a pass over the results — the rows must not leave the database, and a
// post-filter is one `continue` away from leaking the next time somebody edits
// the loop.
func (d *boardDeps) search(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a := ActorFrom(ctx)
	q := content.ParseListQuery(r.URL.Query(), 20)

	// The boards this caller may read, resolved once. Passing the ids is what
	// keeps the permission decision in Go (where the grant model lives) and the
	// filtering in SQL (where the rows are).
	boards, err := d.content.Boards(ctx)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	var readable, secretIn []string
	for _, b := range boards {
		if a.CanOn("post.read", auth.BoardID(b.ID)) {
			readable = append(readable, b.ID)
			if a.CanOn("post.read_secret", auth.BoardID(b.ID)) {
				secretIn = append(secretIn, b.ID)
			}
		}
	}

	var results []content.Post
	var total int64
	if q.Search != "" && len(readable) > 0 {
		results, err = d.content.SearchPosts(ctx, readable, secretIn, q, actorID(a))
		if err != nil {
			d.serverError(w, r, err)
			return
		}
		total, err = d.content.CountSearchPosts(ctx, readable, secretIn, q, actorID(a))
		if err != nil {
			d.serverError(w, r, err)
			return
		}
	}

	byID := map[string]content.Board{}
	for _, b := range boards {
		byID[b.ID] = b
	}
	v := d.view(r, "검색", "")
	v.Data = map[string]any{
		"Query": q, "Results": results, "Total": total, "Boards": byID,
	}
	d.renderPage(w, r, "search.html", http.StatusOK, v)
}
