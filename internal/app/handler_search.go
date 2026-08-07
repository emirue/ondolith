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

	boards, err := d.content.Boards(ctx)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	readable, secretIn := readableBoards(a, boards)

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

// readableBoards splits boards into "this caller may read" and "…including
// secret posts".
//
// 결정은 Go 에서(부여 모델이 거기 있다), 걸러내기는 SQL 에서(행이 거기 있다) —
// id 목록을 넘기는 것이 그 경계다. 홈(P-201)과 검색(P-212)이 같은 계산을
// 하므로 한 곳에 둔다. 두 곳이 갈라지면 한쪽에만 비공개 게시판 글이 샌다.
func readableBoards(a *Actor, boards []content.Board) (readable, secretIn []string) {
	for _, b := range boards {
		if !a.CanOn("post.read", auth.BoardID(b.ID)) {
			continue
		}
		readable = append(readable, b.ID)
		if a.CanOn("post.read_secret", auth.BoardID(b.ID)) {
			secretIn = append(secretIn, b.ID)
		}
	}
	return readable, secretIn
}
