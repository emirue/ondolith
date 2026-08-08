package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/content"
)

// ---- A-304 / A-305 게시판 ------------------------------------------------------

// BoardList is A-304.
//
// D14 4.2: a board with no grant rows is invisible to everyone including the
// person who just made it, so the list marks it. Without the mark the operator
// sees a normal-looking row and goes looking for the bug in the theme.
func (d *Deps) BoardList(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "board.view"); !ok {
		return
	}
	ctx := r.Context()
	boards, err := d.Content.Boards(ctx)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	granted, err := d.Auth.BoardsWithGrants(ctx)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	type row struct {
		content.Board
		Unreachable bool
	}
	rows := make([]row, 0, len(boards))
	for _, b := range boards {
		rows = append(rows, row{Board: b, Unreachable: !granted[b.ID]})
	}
	d.Render(w, r, "admin/boards.html", http.StatusOK, map[string]any{
		"Boards": rows, "Presets": content.Presets(),
	})
}

// BoardForm is A-305's GET. "new" is the empty form, as with pages.
func (d *Deps) BoardForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "board.manage"); !ok {
		return
	}
	id := r.PathValue("id")
	data := map[string]any{"Presets": content.Presets()}
	if !isCreate(id) {
		b, err := d.Content.BoardByID(r.Context(), id)
		if errors.Is(err, content.ErrNotFound) {
			http.Error(w, "게시판을 찾을 수 없습니다.", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
			return
		}
		data["Board"] = b
	}
	d.Render(w, r, "admin/board-edit.html", http.StatusOK, data)
}

// BoardSave is A-305's POST: create or update.
func (d *Deps) BoardSave(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "board.manage")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	b := content.Board{
		Slug:             strings.TrimSpace(r.PostFormValue("slug")),
		Name:             strings.TrimSpace(r.PostFormValue("name")),
		Skin:             strings.TrimSpace(r.PostFormValue("skin")),
		AllowAttachments: r.PostFormValue("allow_attachments") != "",
		AllowComments:    r.PostFormValue("allow_comments") != "",
		AllowSecret:      r.PostFormValue("allow_secret") != "",
		PerPage:          20,
	}
	if n, err := strconv.Atoi(r.PostFormValue("per_page")); err == nil && n > 0 {
		b.PerPage = n
	}

	id := r.PathValue("id")
	if isCreate(id) {
		newID, err := d.Content.CreateBoard(ctx, b, content.BoardPreset(r.PostFormValue("preset")))
		switch {
		case errors.Is(err, content.ErrSlugTakenBoard):
			d.boardFormError(w, r, b, "이미 사용 중인 주소입니다.", http.StatusConflict)
			return
		case errors.Is(err, content.ErrUnknownPreset):
			d.boardFormError(w, r, b, "권한 프리셋을 고르세요.", http.StatusUnprocessableEntity)
			return
		case err != nil:
			d.boardFormError(w, r, b, "저장하지 못했습니다.", http.StatusUnprocessableEntity)
			return
		}
		// D15 7절: 게시판 정의 변경은 공개 화면 동작을 바꾼다.
		d.log(r, c, "board.manage", "board", newID, "게시판 '"+b.Slug+"' 생성 (프리셋 "+r.PostFormValue("preset")+")")
		http.Redirect(w, r, "/admin/boards", http.StatusSeeOther)
		return
	}

	// The slug is not editable: it is in every link anyone saved (D19 A-305).
	if err := d.Content.UpdateBoard(ctx, id, b); err != nil {
		d.boardFormError(w, r, b, "저장하지 못했습니다.", http.StatusUnprocessableEntity)
		return
	}
	d.log(r, c, "board.manage", "board", id, "게시판 설정 변경")
	http.Redirect(w, r, "/admin/boards", http.StatusSeeOther)
}

func (d *Deps) boardFormError(w http.ResponseWriter, r *http.Request, b content.Board, msg string, code int) {
	d.Render(w, r, "admin/board-edit.html", code, map[string]any{
		"Board": &b, "Presets": content.Presets(), "Error": msg,
	})
}

// BoardDelete removes a board and everything under it.
//
// The count is shown first (A-305's confirmation step): "이 게시판의 글 128건도
// 함께 삭제됩니다" is the only thing that makes a confirmation mean anything.
func (d *Deps) BoardDelete(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "board.manage")
	if !ok {
		return
	}
	if !reauthOK(c, r) {
		http.Error(w, "비밀번호를 다시 입력하세요.", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	force := r.PostFormValue("confirm") != ""
	err := d.Content.DeleteBoard(r.Context(), id, force)
	switch {
	case errors.Is(err, content.ErrBoardInUse):
		http.Error(w, err.Error()+" — 확인 후 다시 시도하세요.", http.StatusConflict)
	case errors.Is(err, content.ErrNotFound):
		http.Error(w, "게시판을 찾을 수 없습니다.", http.StatusNotFound)
	case err != nil:
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
	default:
		d.log(r, c, "board.manage", "board", id, "게시판 삭제")
		http.Redirect(w, r, "/admin/boards", http.StatusSeeOther)
	}
}

// ---- A-306 커스텀 필드 스키마 ---------------------------------------------------

func (d *Deps) BoardFields(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "board.manage"); !ok {
		return
	}
	ctx := r.Context()
	b, err := d.Content.BoardByID(ctx, r.PathValue("id"))
	if errors.Is(err, content.ErrNotFound) {
		http.Error(w, "게시판을 찾을 수 없습니다.", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	fields, err := d.Content.BoardFields(ctx, b.ID)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.Render(w, r, "admin/board-fields.html", http.StatusOK, map[string]any{
		"Board": b, "Fields": fields, "Types": content.FieldTypes(),
	})
}

func (d *Deps) BoardFieldSave(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "board.manage")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	boardID := r.PathValue("id")

	if key := r.PostFormValue("delete"); key != "" {
		if err := d.Content.DeleteBoardField(ctx, boardID, key); err != nil {
			http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
			return
		}
		// The stored values survive (D14 3절 규칙 4) — say so, because the
		// operator is about to wonder.
		d.log(r, c, "board.manage", "board_field", boardID,
			"필드 '"+key+"' 정의 삭제 (기존 글의 값은 보존)")
		http.Redirect(w, r, "/admin/boards/"+boardID+"/fields", http.StatusSeeOther)
		return
	}

	f := content.FieldSchema{
		Key:        strings.TrimSpace(r.PostFormValue("key")),
		Label:      strings.TrimSpace(r.PostFormValue("label")),
		Type:       content.FieldType(r.PostFormValue("field_type")),
		Required:   r.PostFormValue("is_required") != "",
		ShowInList: r.PostFormValue("show_in_list") != "",
		Options:    splitOptions(r.PostFormValue("options")),
	}
	if n, err := strconv.Atoi(r.PostFormValue("sort_order")); err == nil {
		f.Sort = n
	}
	if err := d.Content.SaveBoardField(ctx, boardID, f); err != nil {
		d.Render(w, r, "admin/board-fields.html", http.StatusUnprocessableEntity,
			map[string]any{"Error": "필드를 저장하지 못했습니다: " + err.Error()})
		return
	}
	d.log(r, c, "board.manage", "board_field", boardID, "필드 '"+f.Key+"' 저장")
	http.Redirect(w, r, "/admin/boards/"+boardID+"/fields", http.StatusSeeOther)
}

// splitOptions turns a newline-separated textarea into the option list. Blank
// lines are dropped: an empty option is a choice nobody can mean.
func splitOptions(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if v := strings.TrimSpace(line); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// ---- A-307 / A-308 / A-309 글·댓글·첨부 관리 -------------------------------------

// PostModerate is A-307. It requires post.moderate ON THAT BOARD — a global
// permission is not what D15 2.4 grants, and an administrator of one board is
// not an administrator of the next.
func (d *Deps) PostModerate(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "admin.access")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	postID := r.PostFormValue("post_id")
	// 형식이 깨진 값은 없는 것과 같다 — 그대로 내려가면 22P02 로 500 이 된다.
	if !content.IsUUID(postID) {
		http.NotFound(w, r)
		return
	}

	b, err := d.Content.BoardByPost(ctx, postID)
	if errors.Is(err, content.ErrNotFound) {
		http.Error(w, "글을 찾을 수 없습니다.", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	if !c.CanOn("post.moderate", auth.BoardID(b.ID)) {
		Forbidden(w)
		return
	}

	switch r.PostFormValue("action") {
	case "delete":
		// 첨부 실물까지 지운다 (OPEN-40). 행만 지우면 파일이 남는데,
		// 글이 사라졌으니 A-309 목록에도 안 나와 아무도 찾지 못한다.
		if err := d.Attachments.DeletePost(ctx, postID); err != nil {
			http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
			return
		}
		// D15 7절: 남의 글 삭제는 분쟁의 근거가 된다.
		d.log(r, c, "post.moderate", "post", postID, "게시판 '"+b.Slug+"' 의 글 삭제")
	case "pin", "unpin", "hide", "show":
		act := r.PostFormValue("action")
		status := "published"
		if act == "hide" {
			status = "hidden"
		}
		if err := d.Content.SetPostFlags(ctx, postID, act == "pin", status); err != nil {
			http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
			return
		}
		d.log(r, c, "post.moderate", "post", postID, "게시판 '"+b.Slug+"' 의 글 "+act)
	default:
		http.Error(w, "알 수 없는 동작입니다.", http.StatusUnprocessableEntity)
		return
	}
	http.Redirect(w, r, "/admin/posts?board="+b.Slug, http.StatusSeeOther)
}

// CommentModerate is A-308, with comment.moderate on the comment's board.
func (d *Deps) CommentModerate(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "admin.access")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	commentID := r.PostFormValue("comment_id")
	if !content.IsUUID(commentID) {
		http.NotFound(w, r)
		return
	}

	cm, err := d.Content.CommentByID(ctx, commentID)
	if errors.Is(err, content.ErrNotFound) {
		http.Error(w, "댓글을 찾을 수 없습니다.", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	b, err := d.Content.BoardByPost(ctx, cm.PostID)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	if !c.CanOn("comment.moderate", auth.BoardID(b.ID)) {
		Forbidden(w)
		return
	}
	if err := d.Content.DeleteComment(ctx, commentID); err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.log(r, c, "comment.moderate", "comment", commentID, "게시판 '"+b.Slug+"' 의 댓글 삭제")
	http.Redirect(w, r, "/admin/comments", http.StatusSeeOther)
}

// PostList is A-307's read. It needs a board: post.moderate is scoped, so
// "every post on the site" is a list nobody has permission for as a whole.
func (d *Deps) PostList(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "admin.access")
	if !ok {
		return
	}
	ctx := r.Context()
	slug := r.URL.Query().Get("board")
	data := map[string]any{
		"BoardSlug": slug,
		"Actions": map[string]string{
			"pin": "고정", "unpin": "고정 해제", "hide": "숨김", "show": "공개", "delete": "삭제",
		},
	}
	if slug == "" {
		d.Render(w, r, "admin/posts.html", http.StatusOK, data)
		return
	}
	b, err := d.Content.BoardBySlug(ctx, slug)
	if errors.Is(err, content.ErrNotFound) {
		d.Render(w, r, "admin/posts.html", http.StatusNotFound,
			map[string]any{"BoardSlug": slug, "Error": "게시판을 찾을 수 없습니다."})
		return
	}
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	if !c.CanOn("post.moderate", auth.BoardID(b.ID)) {
		Forbidden(w)
		return
	}
	// The moderator sees hidden and secret posts — that is what moderating is.
	posts, err := d.Content.ModeratePosts(ctx, b.ID, 100)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	data["Board"] = b
	data["Posts"] = posts
	d.Render(w, r, "admin/posts.html", http.StatusOK, data)
}

// CommentList is A-308's read, scoped the same way.
func (d *Deps) CommentList(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "admin.access")
	if !ok {
		return
	}
	ctx := r.Context()
	slug := r.URL.Query().Get("board")
	if slug == "" {
		d.Render(w, r, "admin/comments.html", http.StatusOK, map[string]any{"BoardSlug": slug})
		return
	}
	b, err := d.Content.BoardBySlug(ctx, slug)
	if errors.Is(err, content.ErrNotFound) {
		d.Render(w, r, "admin/comments.html", http.StatusNotFound,
			map[string]any{"BoardSlug": slug, "Error": "게시판을 찾을 수 없습니다."})
		return
	}
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	if !c.CanOn("comment.moderate", auth.BoardID(b.ID)) {
		Forbidden(w)
		return
	}
	comments, err := d.Content.ModerateComments(ctx, b.ID, 100)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.Render(w, r, "admin/comments.html", http.StatusOK,
		map[string]any{"BoardSlug": slug, "Board": b, "Comments": comments})
}

// AttachmentList is A-309's read, scoped like the other two.
func (d *Deps) AttachmentList(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "admin.access")
	if !ok {
		return
	}
	ctx := r.Context()
	slug := r.URL.Query().Get("board")
	if slug == "" {
		d.Render(w, r, "admin/attachments.html", http.StatusOK, map[string]any{"BoardSlug": slug})
		return
	}
	b, err := d.Content.BoardBySlug(ctx, slug)
	if errors.Is(err, content.ErrNotFound) {
		d.Render(w, r, "admin/attachments.html", http.StatusNotFound,
			map[string]any{"BoardSlug": slug, "Error": "게시판을 찾을 수 없습니다."})
		return
	}
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	if !c.CanOn("post.moderate", auth.BoardID(b.ID)) {
		Forbidden(w)
		return
	}
	files, err := d.Content.BoardAttachments(ctx, b.ID, 200)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	d.Render(w, r, "admin/attachments.html", http.StatusOK,
		map[string]any{"BoardSlug": slug, "Board": b, "Attachments": files})
}

// AttachmentDelete is A-309's destructive half.
//
// SC-7 checklist: the permission is judged on the parent post's board, not on
// the attachment id — the id says nothing about where the file lives, which is
// the same reason the download handler re-checks (D15 8절 1번).
func (d *Deps) AttachmentDelete(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "admin.access")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	if d.Attachments == nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	id := r.PostFormValue("attachment_id")
	if !content.IsUUID(id) {
		http.NotFound(w, r)
		return
	}

	at, err := d.Attachments.ByID(ctx, id)
	if errors.Is(err, content.ErrNotFound) {
		http.Error(w, "첨부를 찾을 수 없습니다.", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	b, err := d.Content.BoardByPost(ctx, at.PostID)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	if !c.CanOn("post.moderate", auth.BoardID(b.ID)) {
		Forbidden(w)
		return
	}

	if err := d.Attachments.Delete(ctx, id); err != nil {
		// A-309: 디스크 삭제 실패는 행 삭제를 되돌리지 않는다. 행은 이미
		// 갔으므로 실패를 보고하되 화면은 성공으로 넘긴다 — 다시 누르면
		// "찾을 수 없습니다"가 되어 운영자가 더 헷갈린다.
		if d.Logger != nil {
			d.Logger.Warn("첨부 파일 삭제", "attachment", id, "err", err)
		}
	}
	// D15 7절: 남의 첨부 삭제도 분쟁의 근거다.
	d.log(r, c, "post.moderate", "attachment", id,
		"게시판 '"+b.Slug+"' 의 첨부 '"+at.OriginalName+"' 삭제")
	http.Redirect(w, r, "/admin/attachments?board="+b.Slug, http.StatusSeeOther)
}
