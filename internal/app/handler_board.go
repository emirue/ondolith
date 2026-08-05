package app

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/alexedwards/scs/v2"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/content"
)

type boardDeps struct {
	*publicDeps
	sm          *scs.SessionManager
	attachments *content.Attachments
	authStore   *auth.Store
	log         *slog.Logger
}

// countView bumps the counter once per session per post.
//
// SC-1 3항 allows a write on a GET here because it decides nothing — but a
// refresh must not inflate it, so the session remembers. The list is capped:
// an unbounded set in the session grows with every post a crawler opens, and
// the session row is written back on every request.
const viewedKey = "viewed_posts"

const maxViewedRemembered = 200

func (d *boardDeps) countView(r *http.Request, postID string) {
	ctx := r.Context()
	seen, _ := d.sm.Get(ctx, viewedKey).(string)
	for _, id := range strings.Split(seen, ",") {
		if id == postID {
			return
		}
	}
	if err := d.content.BumpViewCount(ctx, postID); err != nil {
		d.log.Warn("조회수 증가", "post", postID, "err", err)
		return
	}
	ids := strings.Split(strings.TrimPrefix(seen+","+postID, ","), ",")
	if len(ids) > maxViewedRemembered {
		ids = ids[len(ids)-maxViewedRemembered:]
	}
	d.sm.Put(ctx, viewedKey, strings.Join(ids, ","))
}

// board loads the board named in the path and checks the caller may read it.
//
// A board the caller cannot read is 404, not 403 (D15 SC-1 4항): "forbidden"
// confirms the board exists, which is the difference between bouncing off and
// learning that /board/internal is where the staff talk.
func (d *boardDeps) board(w http.ResponseWriter, r *http.Request, perm string) (*content.Board, *Actor, bool) {
	b, err := d.content.BoardBySlug(r.Context(), r.PathValue("slug"))
	if errors.Is(err, content.ErrNotFound) {
		d.notFound(w, r)
		return nil, nil, false
	}
	if err != nil {
		d.serverError(w, r, err)
		return nil, nil, false
	}
	a := ActorFrom(r.Context())
	if !a.CanOn(perm, auth.BoardID(b.ID)) {
		d.notFound(w, r)
		return nil, nil, false
	}
	return b, a, true
}

// P-203 GET /board/{slug} — the list.
func (d *boardDeps) boardList(w http.ResponseWriter, r *http.Request) {
	b, a, ok := d.board(w, r, "post.read")
	if !ok {
		return
	}
	ctx := r.Context()
	q := content.ParseListQuery(r.URL.Query(), b.PerPage)

	// 비밀글도 목록에는 나온다 (FR-512). 본문은 P-204 가 지킨다.
	posts, err := d.content.ListPosts(ctx, b.ID, q)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	total, err := d.content.CountPosts(ctx, b.ID, q)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	fields, err := d.content.BoardFields(ctx, b.ID)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	// Only the columns the schema marks for the list. The template does not
	// decide: A-306 does (D14 3절 규칙 1).
	var columns []content.FieldSchema
	for _, f := range fields {
		if f.ShowInList {
			columns = append(columns, f)
		}
	}

	v := d.view(r, b.Name, b.Name+" 목록")
	v.Data = map[string]any{
		"Board": b, "Posts": posts, "Total": total, "Query": q, "Columns": columns,
		"CanWrite": a.CanOn("post.write", auth.BoardID(b.ID)),
		"Pager":    pagerFor("/board/"+b.Slug, q, total),
	}
	d.renderPage(w, r, d.boardTemplate(b, "board/list.html"), http.StatusOK, v)
}

// P-204 GET /board/{slug}/{id} — one post.
func (d *boardDeps) postView(w http.ResponseWriter, r *http.Request) {
	b, a, ok := d.board(w, r, "post.read")
	if !ok {
		return
	}
	ctx := r.Context()
	canSecret := a.CanOn("post.read_secret", auth.BoardID(b.ID))

	p, err := d.content.PostByID(ctx, r.PathValue("id"), actorID(a), canSecret)
	if errors.Is(err, content.ErrNotFound) {
		d.notFound(w, r)
		return
	}
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	// A post whose id belongs to another board must not render here: the slug
	// carries the permission decision, so honouring a mismatched pair would let
	// a public board's URL read a private board's post.
	if p.BoardID != b.ID || p.Status != "published" {
		d.notFound(w, r)
		return
	}

	comments, err := d.content.Comments(ctx, p.ID)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	fields, err := d.content.BoardFields(ctx, b.ID)
	if err != nil {
		d.serverError(w, r, err)
		return
	}

	// SC-1 3항 allows this write on a GET because it carries no permission
	// decision. The session remembers what it has counted so a refresh does not
	// inflate the number.
	d.countView(r, p.ID)

	v := d.view(r, p.Title, firstLine(p.Body))
	v.Data = map[string]any{
		"Board": b, "Post": p, "Comments": comments, "Fields": fields,
		"CanComment":  b.AllowComments && a.CanOn("comment.write", auth.BoardID(b.ID)),
		"CommentForm": map[string]any{"Action": "/board/" + b.Slug + "/" + p.ID + "/comments"},
		"CanEdit":     p.AuthorID != "" && p.AuthorID == actorID(a),
		"CanModerate": a.CanOn("post.moderate", auth.BoardID(b.ID)),
	}
	d.renderPage(w, r, d.boardTemplate(b, "board/view.html"), http.StatusOK, v)
}

// P-205 GET — the write form, built from the schema.
func (d *boardDeps) postForm(w http.ResponseWriter, r *http.Request) {
	b, a, ok := d.board(w, r, "post.write")
	if !ok {
		return
	}
	fields, err := d.content.BoardFields(r.Context(), b.ID)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	v := d.view(r, b.Name+" 글쓰기", "")
	// No field list in the code: the form is generated from the schema, which is
	// what makes A-306 mean anything (D14 3절 규칙 1).
	v.Data = map[string]any{"Board": b, "Fields": fields,
		"Inputs":    content.FieldInputs(fields, nil),
		"CanSecret": b.AllowSecret, "Post": &content.Post{}}
	_ = a
	d.renderPage(w, r, d.boardTemplate(b, "board/form.html"), http.StatusOK, v)
}

// P-205 POST — create.
func (d *boardDeps) postCreate(w http.ResponseWriter, r *http.Request) {
	b, a, ok := d.board(w, r, "post.write")
	if !ok {
		return
	}
	if !a.IsAuthenticated() {
		http.Redirect(w, r, loginPath, http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	form, fields, ok2 := d.readPostForm(w, r, b)
	if !ok2 {
		return
	}

	custom, err := content.ValidateCustomFields(fields, customValues(r), nil)
	if err != nil {
		d.renderFormError(w, r, b, fields, form, err)
		return
	}
	// board_id and author_id are NOT read from the form (SC-2 5항). The board
	// comes from the path, the author from the session — a form field for
	// either is a way to post as someone else, somewhere else.
	form.BoardID = b.ID
	form.AuthorID = a.User.ID
	form.CustomFields = custom

	id, err := d.content.CreatePost(ctx, form)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/board/"+b.Slug+"/"+id, http.StatusSeeOther)
}

// P-206 GET — the edit form. SC-3: the owner comes from the session.
func (d *boardDeps) postEditForm(w http.ResponseWriter, r *http.Request) {
	b, a, ok := d.board(w, r, "post.read")
	if !ok {
		return
	}
	p, ok2 := d.ownPost(w, r, b, a)
	if !ok2 {
		return
	}
	fields, err := d.content.BoardFields(r.Context(), b.ID)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	v := d.view(r, p.Title+" 수정", "")
	v.Data = map[string]any{"Board": b, "Fields": fields, "Post": p,
		"Inputs": content.FieldInputs(fields, p.CustomFields), "CanSecret": b.AllowSecret}
	d.renderPage(w, r, d.boardTemplate(b, "board/form.html"), http.StatusOK, v)
}

// P-206 POST — update.
func (d *boardDeps) postUpdate(w http.ResponseWriter, r *http.Request) {
	b, a, ok := d.board(w, r, "post.read")
	if !ok {
		return
	}
	p, ok2 := d.ownPost(w, r, b, a)
	if !ok2 {
		return
	}
	form, fields, ok3 := d.readPostForm(w, r, b)
	if !ok3 {
		return
	}
	custom, err := content.ValidateCustomFields(fields, customValues(r), p.CustomFields)
	if err != nil {
		d.renderFormError(w, r, b, fields, form, err)
		return
	}
	form.CustomFields = custom
	if err := d.content.UpdatePost(r.Context(), p.ID, form); err != nil {
		d.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/board/"+b.Slug+"/"+p.ID, http.StatusSeeOther)
}

// ownPost loads a post the caller wrote.
//
// Somebody else's post is 404, not 403 (D19 SC-3): a 403 confirms the post
// exists and that it is not yours, which is two facts more than the caller had.
func (d *boardDeps) ownPost(w http.ResponseWriter, r *http.Request, b *content.Board, a *Actor) (*content.Post, bool) {
	if !a.IsAuthenticated() {
		http.Redirect(w, r, loginPath, http.StatusSeeOther)
		return nil, false
	}
	p, err := d.content.PostByID(r.Context(), r.PathValue("id"), a.User.ID,
		a.CanOn("post.read_secret", auth.BoardID(b.ID)))
	if errors.Is(err, content.ErrNotFound) {
		d.notFound(w, r)
		return nil, false
	}
	if err != nil {
		d.serverError(w, r, err)
		return nil, false
	}
	if p.BoardID != b.ID || p.AuthorID != a.User.ID {
		d.notFound(w, r)
		return nil, false
	}
	return p, true
}

// readPostForm reads the fields P-205 accepts and nothing else.
func (d *boardDeps) readPostForm(w http.ResponseWriter, r *http.Request, b *content.Board) (content.Post, []content.FieldSchema, bool) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return content.Post{}, nil, false
	}
	fields, err := d.content.BoardFields(r.Context(), b.ID)
	if err != nil {
		d.serverError(w, r, err)
		return content.Post{}, nil, false
	}
	p := content.Post{
		Title: strings.TrimSpace(r.PostFormValue("title")),
		Body:  r.PostFormValue("body"),
		// A board with secret posts turned off cannot have one, whatever the
		// form says — the checkbox is absent from the page, and a posted value
		// is somebody trying it anyway.
		IsSecret: b.AllowSecret && r.PostFormValue("is_secret") != "",
	}
	if p.Title == "" {
		d.renderFormError(w, r, b, fields, p, errors.New("제목을 입력하세요."))
		return content.Post{}, nil, false
	}
	return p, fields, true
}

func (d *boardDeps) renderFormError(w http.ResponseWriter, r *http.Request,
	b *content.Board, fields []content.FieldSchema, p content.Post, err error,
) {
	v := d.view(r, b.Name+" 글쓰기", "")
	v.Data = map[string]any{"Board": b, "Fields": fields, "Post": &p,
		"Inputs":    content.FieldInputs(fields, p.CustomFields),
		"CanSecret": b.AllowSecret, "Error": err.Error()}
	d.renderPage(w, r, d.boardTemplate(b, "board/form.html"), http.StatusUnprocessableEntity, v)
}

// customValues is the form minus the post's own fields, so a custom field can
// never be named `title` and quietly win. ValidateCustomFields refuses unknown
// keys, and these are known keys that are not custom fields.
func customValues(r *http.Request) map[string][]string {
	out := map[string][]string{}
	for k, v := range r.PostForm {
		switch k {
		case "title", "body", "is_secret":
			continue
		}
		out[k] = v
	}
	return out
}

// boardTemplate lets a board pick a skin (FR-501). An unknown skin falls back
// to the default template rather than erroring: a skin is display, and a typo
// in it must not take the board offline.
func (d *boardDeps) boardTemplate(b *content.Board, def string) string {
	if b.Skin == "" {
		return def
	}
	name := "board/" + b.Skin + "-" + strings.TrimPrefix(def, "board/")
	if d.loader().HasBuiltin(name) {
		return name
	}
	return def
}

func actorID(a *Actor) string {
	if a == nil || a.User == nil {
		return ""
	}
	return a.User.ID
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len([]rune(s)) > 160 {
		s = string([]rune(s)[:160])
	}
	return s
}

// pagerFor pre-computes what partials/pagination.html draws.
//
// The arithmetic is here because html/template has none, and D17 closes the
// function map — adding `add` for this would be the first of several, and the
// screen after next would want `mul`.
func pagerFor(base string, q content.ListQuery, total int64) map[string]any {
	prev := q.Page // URLs are 1-based, so the previous page's number IS q.Page
	return map[string]any{
		"Base": base, "Query": q, "Total": total,
		"PageNo":   q.Page + 1,
		"PrevPage": prev,
		"NextPage": q.Page + 2,
		"HasPrev":  q.Page > 0,
		"HasNext":  int64((q.Page+1)*q.PerPage) < total,
	}
}
