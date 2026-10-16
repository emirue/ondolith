package app

import (
	"cmp"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/alexedwards/scs/v2"

	"github.com/emirue/ondolith/internal/admin"
	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/content"
)

type boardDeps struct {
	*publicDeps
	sm          *scs.SessionManager
	attachments *content.Attachments
	authStore   *auth.Store
	log         *slog.Logger
	// siteURL is config.SiteURL — 운영자가 정한 출처. 사이트맵·robots 가 쓴다.
	siteURL string
	// media 는 에디터로 올린 본문 사진이다 (P-213·P-214, FR-514).
	media *content.MediaStore
	spam  spamGuard
}

// countView bumps the counter once per browser session per post.
//
// SC-1 3항 allows a write on a GET here because it decides nothing — but a
// refresh must not inflate it, so the browser remembers what it has seen.
//
// **세션이 아니라 쿠키 하나에 적는다.** 앞 판은 세션에 적어서, 쿠키 없는
// 크롤러가 글 하나를 열 때마다 sessions 에 행이 하나씩 쌓였고 로그인한 사람은
// 글을 열 때마다 세션 행을 다시 썼다. 이 값은 조회수 중복을 거르는 데만 쓰이고
// 아무것도 허락하지 않으므로, 고쳐 보내도 자기 조회수가 한 번 더 셀 뿐이다.
// 브라우저를 닫으면 사라지는 쿠키라 「세션당 한 번」은 그대로다.
const viewedCookie = "ondolith_viewed"

// maxViewedRemembered 는 쿠키에 남기는 글 수다. 50개 × 37자 ≈ 1.9KB 로 쿠키 한
// 개의 상한(4KB) 안이다. 넘치면 오래된 것부터 잊는다.
const maxViewedRemembered = 50

func (d *boardDeps) countView(w http.ResponseWriter, r *http.Request, postID string) {
	var seen string
	if c, err := r.Cookie(viewedCookie); err == nil {
		seen = c.Value
	}
	for _, id := range strings.Split(seen, ".") {
		if id == postID {
			return
		}
	}
	if err := d.content.BumpViewCount(r.Context(), postID); err != nil {
		d.log.Warn("조회수 증가", "post", postID, "err", err)
		return
	}
	ids := strings.Split(strings.TrimPrefix(seen+"."+postID, "."), ".")
	if len(ids) > maxViewedRemembered {
		ids = ids[len(ids)-maxViewedRemembered:]
	}
	http.SetCookie(w, &http.Cookie{Name: viewedCookie, Value: strings.Join(ids, "."),
		Path: "/board/", HttpOnly: true, Secure: d.sm.Cookie.Secure, SameSite: http.SameSiteLaxMode})
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
	// 검색이 비밀글 본문의 신탁이 되지 않도록 (post.sql CountPostsSearch).
	q.ViewerID, q.CanSecret = actorID(a), a.CanOn("post.read_secret", auth.BoardID(b.ID))

	// 비밀글도 목록에는 나온다 (FR-512). 본문은 P-204 가 지킨다.
	// 총수는 화면 요소다 — 내장 테마의 페이저가 「전체 N건」을 그린다
	// (partials/pagination.html). 한 줄 더 읽어 HasNext 만 아는 것으로는 부족하다.
	posts, total, err := d.content.PostPage(ctx, b.ID, q)
	// 마지막 쪽 너머는 마지막 쪽이다 — 낡은 즐겨찾기나 글이 지워진 뒤의 쪽 번호. 상한이
	// 사라졌으니 빈 쪽을 그리면 번호만 있고 글이 없는 화면이 된다.
	if last := lastPage(total, q); err == nil && q.Page+1 > last {
		q.Page = last - 1
		posts, total, err = d.content.PostPage(ctx, b.ID, q)
	}
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
	files, err := d.attachments.List(ctx, p.ID)
	if err != nil {
		d.serverError(w, r, err)
		return
	}

	// SC-1 3항 allows this write on a GET because it carries no permission
	// decision. The session remembers what it has counted so a refresh does not
	// inflate the number.
	d.countView(w, r, p.ID)

	v := d.view(r, p.Title, firstLine(p.Body))
	commentForm := map[string]any{"Action": "/board/" + b.Slug + "/" + p.ID + "/comments",
		"Turnstile": v.Site.Turnstile}
	// 「답글」(?reply=댓글)은 같은 폼을 그 댓글에 대한 답글로 연다 (FR-505). 이 글의
	// 최상위 댓글일 때만이다 — 저장할 때도 같은 판정을 한다(commentCreate).
	if id := r.URL.Query().Get("reply"); id != "" && d.commentBelongsTo(ctx, id, p.ID) {
		commentForm["ParentID"] = id
	}
	v.Data = map[string]any{
		"Board": b, "Post": p, "Comments": comments, "Fields": fields,
		"Attachments": files,
		"CanComment":  b.AllowComments && a.CanOn("comment.write", auth.BoardID(b.ID)),
		"CommentForm": commentForm,
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
	if d.unverified(a) {
		d.refuseUnverified(w, r, "글을 쓸 수 있습니다.")
		return
	}
	ctx := r.Context()
	form, fields, ok2 := d.readPostForm(w, r, b)
	if !ok2 {
		return
	}

	custom, err := content.ValidateCustomFields(fields, customValues(r), nil)
	if err != nil {
		d.renderFormError(w, r, b, fields, form, http.StatusUnprocessableEntity, fieldsMessage(err))
		return
	}
	// board_id and author_id are NOT read from the form (SC-2 5항). The board
	// comes from the path, the author from the session — a form field for
	// either is a way to post as someone else, somewhere else.
	form.BoardID = b.ID
	form.AuthorID = a.User.ID
	form.CustomFields = custom
	// 다른 검증을 다 지난 뒤에 본다 — Turnstile 은 바깥 호출이고 토큰은 한 번만 쓴다.
	if code, msg := d.spam.check(r); code != 0 {
		d.renderFormError(w, r, b, fields, form, code, msg)
		return
	}

	id, err := d.content.CreatePost(ctx, form)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	// **글이 먼저 있어야 첨부를 붙일 수 있다.** 첨부가 거부되면 글은 이미
	// 저장돼 있으므로, 없던 일로 하지 않고 수정 화면으로 보내 이유를 말한다 —
	// 여기서 글을 지우면 사용자가 쓴 본문이 파일 하나 때문에 사라진다.
	if err := d.saveUploads(r, b, id); err != nil {
		d.renderUploadError(w, r, b, id, err)
		return
	}
	http.Redirect(w, r, "/board/"+b.Slug+"/"+id, http.StatusSeeOther)
}

// maxPostBodyBytes bounds one write request. 첨부 여러 개가 함께 오므로 파일
// 하나의 상한(A-309 설정)보다 넉넉하되, 무한은 아니다.
const maxPostBodyBytes = 64 << 20

// postFormMemory is how much of a multipart body stays in memory; the rest
// spools to a temp file that the runtime removes.
const postFormMemory = 8 << 20

// saveUploads stores the files that came with the form (FR-506).
//
// **게시판이 첨부를 허용할 때만 받는다** (D19 P-205). 허용하지 않는 게시판의
// 폼에는 파일 칸이 없고, 그래도 실려 온 것은 폼을 고쳐 보낸 것이다.
//
// 검증은 하지 않는다 — 확장자 허용목록·매직바이트·웹루트 밖 저장·파일명 재생성
// 네 겹은 전부 content.StoreUpload 안에 있다 (D60 3절). 여기서 한 겹이라도
// 다시 쓰면 두 벌이 되고, 갈라진 쪽은 조용히 약해진다.
func (d *boardDeps) saveUploads(r *http.Request, b *content.Board, postID string) error {
	if r.MultipartForm == nil {
		return nil
	}
	files := r.MultipartForm.File["attachments"]
	if len(files) == 0 {
		return nil
	}
	if !b.AllowAttachments {
		// 확장자 오류가 아니다 — 이 게시판이 첨부를 아예 받지 않는다.
		return content.ErrUploadNotAllowed
	}
	for _, fh := range files {
		if fh.Size == 0 {
			// 파일 칸을 비워 두면 브라우저가 빈 항목을 보낸다.
			continue
		}
		f, err := fh.Open()
		if err != nil {
			return err
		}
		_, err = d.attachments.Save(r.Context(), postID, fh.Filename, f)
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
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
		d.renderFormError(w, r, b, fields, form, http.StatusUnprocessableEntity, fieldsMessage(err))
		return
	}
	form.CustomFields = custom
	if err := d.content.UpdatePost(r.Context(), p.ID, form); err != nil {
		d.serverError(w, r, err)
		return
	}
	if err := d.saveUploads(r, b, p.ID); err != nil {
		d.renderUploadError(w, r, b, p.ID, err)
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
	// **첨부가 있는 폼은 multipart 다.** `ParseForm` 은 그 본문을 읽지 않아
	// 제목까지 빈 값이 된다 — 첨부를 붙이는 순간 글이 안 써지는 자리다.
	//
	// 본문 크기를 먼저 막는다. 파싱이 먼저면 상한을 넘긴 요청이 이미 메모리와
	// 임시 파일을 쓴 뒤가 된다 (D60 NFR-206, 테마 업로드와 같은 규칙).
	var err error
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, maxPostBodyBytes)
		err = r.ParseMultipartForm(postFormMemory)
	} else {
		err = r.ParseForm()
	}
	if err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return content.Post{}, nil, false
	}
	fields, err := d.content.BoardFields(r.Context(), b.ID)
	if err != nil {
		d.serverError(w, r, err)
		return content.Post{}, nil, false
	}
	body, bodyHTML := content.FormBody(r.PostFormValue("body"), r.PostFormValue("body_html"))
	p := content.Post{
		Title:    strings.TrimSpace(r.PostFormValue("title")),
		Body:     body,
		BodyHTML: bodyHTML,
		// A board with secret posts turned off cannot have one, whatever the
		// form says — the checkbox is absent from the page, and a posted value
		// is somebody trying it anyway.
		IsSecret: b.AllowSecret && r.PostFormValue("is_secret") != "",
	}
	if p.Title == "" {
		d.renderFormError(w, r, b, fields, p, http.StatusUnprocessableEntity, "제목을 입력하세요.")
		return content.Post{}, nil, false
	}
	// DB CHECK 에 맡기면 500 이고 쓴 글이 사라진다. 여기서 막으면 폼이 그대로 돌아온다.
	if content.TooLong(p.Title, content.MaxTitleLen) {
		d.renderFormError(w, r, b, fields, p, http.StatusUnprocessableEntity, "제목은 200자까지 쓸 수 있습니다.")
		return content.Post{}, nil, false
	}
	if content.TooLong(p.Body, content.MaxPostBodyLen) || content.TooLong(p.BodyHTML, content.MaxBodyHTMLLen) {
		d.renderFormError(w, r, b, fields, p, http.StatusUnprocessableEntity,
			"본문이 너무 깁니다. 글자 50,000자(서식 포함 200,000자)까지 쓸 수 있습니다.")
		return content.Post{}, nil, false
	}
	return p, fields, true
}

func (d *boardDeps) renderFormError(w http.ResponseWriter, r *http.Request,
	b *content.Board, fields []content.FieldSchema, p content.Post, code int, msg string,
) {
	v := d.view(r, b.Name+" 글쓰기", "")
	v.Data = map[string]any{"Board": b, "Fields": fields, "Post": &p,
		"Inputs":    content.FieldInputs(fields, p.CustomFields),
		"CanSecret": b.AllowSecret, "Error": msg}
	d.renderPage(w, r, d.boardTemplate(b, "board/form.html"), code, v)
}

// renderUploadError draws the edit form of a post that saved but whose
// attachment did not.
//
// 목록으로 돌려보내지 않는다 — 첨부가 거부된 이유를 말할 자리가 거기에는
// 없고, 사용자는 「저장은 됐는데 파일이 없다」만 보게 된다.
func (d *boardDeps) renderUploadError(w http.ResponseWriter, r *http.Request,
	b *content.Board, postID string, err error,
) {
	msg := uploadMessage(err)
	switch {
	case msg == "":
		// 디스크·DB 고장이다. 원인은 경로와 SQL 을 담고 있어 화면에 내지 않는다.
		d.log.Error("첨부 저장 실패", "post", postID, "err", err)
		msg = "잠시 후 다시 시도해 주세요."
	case errors.Is(err, content.ErrUploadSetting):
		// 손님이 고칠 수 없는 운영자 설정이다 — 운영자가 알 길은 로그뿐이다.
		d.log.Error("첨부 설정 오류", "post", postID, "err", err)
	case errors.Is(err, content.ErrUploadExt), errors.Is(err, content.ErrUploadContent):
		// 위장 파일·허용 밖 확장자는 공격 시도 신호다 (D19 P-205). 로그인한 회원이 글을
		// 써야 닿는 경로라 밖에서 이 로그를 불릴 수 없다.
		d.log.Warn("첨부 거부", "post", postID, "err", err)
	}
	ctx := r.Context()
	p, perr := d.content.PostByID(ctx, postID, "", true)
	fields, ferr := d.content.BoardFields(ctx, b.ID)
	if perr != nil || ferr != nil {
		d.serverError(w, r, cmp.Or(perr, ferr))
		return
	}
	v := d.view(r, p.Title+" 수정", "")
	v.Data = map[string]any{"Board": b, "Fields": fields, "Post": p,
		"Inputs":    content.FieldInputs(fields, p.CustomFields),
		"CanSecret": b.AllowSecret,
		"Error":     "글은 저장되었지만 파일을 첨부하지 못했습니다. " + msg}
	d.renderPage(w, r, d.boardTemplate(b, "board/form.html"),
		http.StatusUnprocessableEntity, v)
}

// customValues is the form minus the post's own fields, so a custom field can
// never be named `title` and quietly win. ValidateCustomFields refuses unknown
// keys, and these are known keys that are not custom fields.
func customValues(r *http.Request) map[string][]string {
	out := map[string][]string{}
	for k, v := range r.PostForm {
		switch k {
		case "title", "body", "body_html", "is_secret", "attachments",
			honeypotField, auth.TurnstileField:
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
//
// 번호는 열 개씩(pageWindow) 묶어 보이고 처음·이전·다음·마지막을 단다 — 그누보드와
// 같은 모양이다. 주소는 통째로 만들어 넘긴다: 정렬·검색어를 템플릿에서 이으면 화면마다
// 빠뜨릴 자리가 생기고, 빠뜨린 쪽에서 다음 쪽의 정렬이 풀린다. 앞의 키들(PageNo·
// PrevPage·NextPage·HasPrev·HasNext)은 이 조각을 덮어쓴 디스크 테마를 위해 남긴다.
func pagerFor(base string, q content.ListQuery, total int64) map[string]any {
	last := lastPage(total, q)
	cur := min(q.Page+1, last) // 핸들러가 맞추지 않은 쪽(빈 검색 등)도 없는 쪽을 가리키지 않는다
	keep := url.Values{"q": {q.Search}, "sort": {q.SortParam()}, "per_page": {q.PerPageParam()}}
	link := func(n int) string { return admin.ListURL(base, keep, n) }

	start := (cur-1)/pageWindow*pageWindow + 1
	pages := []pageLink{}
	for n := start; n <= min(start+pageWindow-1, last); n++ {
		pages = append(pages, pageLink{No: n, URL: link(n), Current: n == cur})
	}
	p := map[string]any{
		"Base": base, "Query": q, "Total": total,
		"PageNo":   cur,
		"PrevPage": cur - 1,
		"NextPage": cur + 1,
		"HasPrev":  cur > 1,
		"HasNext":  cur < last,
		"LastPage": last, "Pages": pages,
		// 검색은 SearchCountCap 까지만 센다 — 넘으면 「10,000건 이상」이다.
		// CapTotal 은 int64 다 — 테마 함수 number 가 int64 만 받는다. 상수를 그대로 넣으면 int 로
		// 들어가 「전체 」 뒤에서 렌더링이 끊긴다(200 이 나간 뒤라 잘린 화면이 그대로 간다).
		"Capped": q.Search != "" && total > content.SearchCountCap, "CapTotal": int64(content.SearchCountCap),
		"FirstURL": "", "PrevURL": "", "NextURL": "", "LastURL": "",
	}
	if cur > 1 {
		p["FirstURL"], p["PrevURL"] = link(1), link(cur-1)
	}
	if cur < last {
		p["NextURL"], p["LastURL"] = link(cur+1), link(last)
	}
	return p
}

// pageWindow is how many page numbers the pager shows at once.
const pageWindow = 10

// pageLink is one number in the pager.
type pageLink struct {
	No      int
	URL     string
	Current bool
}

// lastPage is the number of the last page that has rows; 1 for an empty list.
// It never passes q.MaxPages(), where ParseListQuery stops.
func lastPage(total int64, q content.ListQuery) int {
	if total <= 0 || q.PerPage <= 0 {
		return 1
	}
	return int(min((total+int64(q.PerPage)-1)/int64(q.PerPage), int64(q.MaxPages())))
}
