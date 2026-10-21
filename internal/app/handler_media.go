package app

import (
	"errors"
	"io/fs"
	"net/http"

	"github.com/emirue/ondolith/internal/admin"
	"github.com/emirue/ondolith/internal/content"
)

// P-213 GET /media/{id} · /media/{id}/thumb — 에디터로 올린 본문 사진 (FR-514).
//
// **누구에게나 나간다.** 사진은 글보다 먼저 생겨(글을 저장하기 전에 넣는다) 부모를
// 갖지 않으므로, 첨부(P-211)처럼 글의 읽기 권한을 다시 볼 수 없다. 주소의 uuid 를
// 아는 사람 — 그 글을 본 사람 — 만 닿는다. 비밀글에 넣은 사진도 그렇다 (D15 SC-7).
func (d *boardDeps) mediaServe(thumb bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		md, err := d.media.ByID(r.Context(), r.PathValue("id"))
		if errors.Is(err, content.ErrNotFound) {
			d.notFound(w, r)
			return
		}
		if err != nil {
			d.serverError(w, r, err)
			return
		}
		f, err := d.media.Open(r.Context(), md, thumb)
		if errors.Is(err, fs.ErrNotExist) {
			d.notFound(w, r) // 행은 있고 파일이 없다 — 고장이 아니라 없는 것이다 (P-211)
			return
		}
		if err != nil {
			d.serverError(w, r, err) // 버킷 불통은 없는 것이 아니다 (P-211)
			return
		}
		defer f.Close()
		ct := md.MIMEType
		if thumb {
			ct = md.ThumbMIME
		}
		// 타입은 다시 인코딩한 결과다 — 요청도 파일명도 아니다 (D60 2항).
		w.Header().Set("Content-Type", ct)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", "inline")
		// id 마다 내용이 바뀌지 않는다. private: 공유 캐시가 들지 않는다 (P-306 과 같은 이유).
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		http.ServeContent(w, r, "", md.CreatedAt, f)
	}
}

// P-214 POST /board/{slug}/media — 글쓰기·글수정 에디터의 사진 올리기.
//
// 그 게시판에 글을 쓸 수 있는 사람만이다 (post.write, 게시판 단위). 쓸 수 없으면
// 없는 게시판과 같은 404 다 (board 와 같다, D15 SC-1 4항). 인증 전 계정은 글을 쓸
// 수 없으므로 사진도 올리지 못한다 (FR-214).
func (d *boardDeps) mediaUpload(w http.ResponseWriter, r *http.Request) {
	_, a, ok := d.board(w, r, "post.write")
	if !ok {
		return
	}
	if !a.IsAuthenticated() || d.unverified(a) {
		admin.Forbidden(w)
		return
	}
	limits, err := d.attachments.Limits(r.Context())
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	admin.EditorUpload(w, r, d.media, limits, a.User.ID, d.log)
}
