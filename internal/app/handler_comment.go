package app

import (
	"errors"
	"net/http"
	"strings"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/content"
)

// P-207 POST /board/{slug}/{id}/delete — the author deletes their own post.
//
// POST only. A GET that deletes is reachable by a crawler and by a browser
// prefetching a link (D15 P5), and the route table's boot check refuses to
// register a state-changing class on a safe method.
func (d *boardDeps) postDelete(w http.ResponseWriter, r *http.Request) {
	b, a, ok := d.board(w, r, "post.read")
	if !ok {
		return
	}
	p, ok2 := d.ownPost(w, r, b, a)
	if !ok2 {
		return
	}
	if err := d.content.DeletePost(r.Context(), p.ID); err != nil {
		d.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/board/"+b.Slug, http.StatusSeeOther)
}

// P-208 POST — write a comment.
func (d *boardDeps) commentCreate(w http.ResponseWriter, r *http.Request) {
	b, a, ok := d.board(w, r, "comment.write")
	if !ok {
		return
	}
	if !a.IsAuthenticated() {
		http.Redirect(w, r, loginPath, http.StatusSeeOther)
		return
	}
	// A board with comments turned off has no comment form. The check is here
	// as well, because the form's absence is UX and the POST still arrives
	// (D15 4.3).
	if !b.AllowComments {
		d.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	// The post is loaded through the same secret-post filter as P-204: a
	// comment on a post you cannot read would be a way to confirm it exists.
	p, err := d.content.PostByID(ctx, r.PathValue("id"), a.User.ID,
		a.CanOn("post.read_secret", auth.BoardID(b.ID)))
	if errors.Is(err, content.ErrNotFound) {
		d.notFound(w, r)
		return
	}
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	if p.BoardID != b.ID || p.Status != "published" {
		d.notFound(w, r)
		return
	}

	body := strings.TrimSpace(r.PostFormValue("body"))
	if body == "" {
		http.Redirect(w, r, "/board/"+b.Slug+"/"+p.ID, http.StatusSeeOther)
		return
	}
	// parent_id is accepted but verified: a reply must belong to THIS post, or
	// a comment tree could be grafted onto another post's thread.
	parent := r.PostFormValue("parent_id")
	if parent != "" && !d.commentBelongsTo(ctx, parent, p.ID) {
		d.notFound(w, r)
		return
	}

	if _, err := d.content.CreateComment(ctx, content.Comment{
		PostID: p.ID, ParentID: parent, AuthorID: a.User.ID, Body: body,
	}); err != nil {
		d.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/board/"+b.Slug+"/"+p.ID, http.StatusSeeOther)
}

func (d *boardDeps) commentBelongsTo(ctx contextLike, commentID, postID string) bool {
	c, err := d.content.CommentByID(ctx, commentID)
	if err != nil {
		return false
	}
	// One level only (D30): a reply to a reply would build a tree no screen
	// draws, so the parent must be a top-level comment.
	return c.PostID == postID && c.ParentID == ""
}

// P-209 GET — the edit form for one's own comment.
func (d *boardDeps) commentEditForm(w http.ResponseWriter, r *http.Request) {
	c, b, ok := d.ownComment(w, r)
	if !ok {
		return
	}
	v := d.view(r, "댓글 수정", "")
	v.Data = map[string]any{"Comment": c, "Board": b}
	d.renderPage(w, r, "board/comment-edit.html", http.StatusOK, v)
}

// P-209 POST — update one's own comment.
func (d *boardDeps) commentUpdate(w http.ResponseWriter, r *http.Request) {
	c, b, ok := d.ownComment(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	body := strings.TrimSpace(r.PostFormValue("body"))
	if body == "" {
		v := d.view(r, "댓글 수정", "")
		v.Data = map[string]any{"Comment": c, "Board": b, "Error": "내용을 입력하세요."}
		d.renderPage(w, r, "board/comment-edit.html", http.StatusUnprocessableEntity, v)
		return
	}
	if err := d.content.UpdateComment(r.Context(), c.ID, body); err != nil {
		d.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/board/"+b.Slug+"/"+c.PostID, http.StatusSeeOther)
}

// P-210 POST — delete one's own comment.
func (d *boardDeps) commentDelete(w http.ResponseWriter, r *http.Request) {
	c, b, ok := d.ownComment(w, r)
	if !ok {
		return
	}
	// Physical or tombstone, decided by whether it has replies — the foreign
	// key makes that choice, not this handler (D30).
	if err := d.content.DeleteComment(r.Context(), c.ID); err != nil {
		d.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/board/"+b.Slug+"/"+c.PostID, http.StatusSeeOther)
}

// ownComment loads a comment the caller wrote, together with its board.
//
// P-209 and P-210 have no slug in the path (D11), so the board is reached
// through the comment's post — and the board's post.read still decides, or a
// comment id would be a way into a board the caller cannot open.
func (d *boardDeps) ownComment(w http.ResponseWriter, r *http.Request) (*content.Comment, *content.Board, bool) {
	a := ActorFrom(r.Context())
	if !a.IsAuthenticated() {
		http.Redirect(w, r, loginPath, http.StatusSeeOther)
		return nil, nil, false
	}
	ctx := r.Context()
	c, err := d.content.CommentByID(ctx, r.PathValue("id"))
	if errors.Is(err, content.ErrNotFound) {
		d.notFound(w, r)
		return nil, nil, false
	}
	if err != nil {
		d.serverError(w, r, err)
		return nil, nil, false
	}
	// Somebody else's comment is 404, and so is a tombstone: editing one would
	// bring back a body the author already removed.
	if c.AuthorID != a.User.ID || c.IsTombstone() {
		d.notFound(w, r)
		return nil, nil, false
	}

	b, err := d.content.BoardByPost(ctx, c.PostID)
	if err != nil {
		d.serverError(w, r, err)
		return nil, nil, false
	}
	if !a.CanOn("post.read", auth.BoardID(b.ID)) {
		d.notFound(w, r)
		return nil, nil, false
	}
	return c, b, true
}
