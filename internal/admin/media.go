package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/emirue/ondolith/internal/content"
)

// editorMaxFiles 는 한 번에 받는 사진 수다. 에디터는 여러 장을 file-0, file-1… 로 싣는다.
const editorMaxFiles = 10

type editorFile struct {
	URL  string `json:"url"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// EditorUpload answers the editor's picture upload (P-214·A-310, FR-514).
//
// **에디터(SunEditor)의 규약이다.** 성공은 200 + `{"result":[{url,name,size}]}`,
// 사용자가 고칠 수 있는 실패(형식·크기·픽셀)도 200 + `{"errorMessage"}` — 200 이
// 아니면 에디터가 `[SUNEDITOR…] status: 413` 같은 개발자용 문구를 띄운다. 권한과
// 대상은 호출자가 이미 판정했다. 공개 게시판(P-214)과 관리자(A-310)가 같은 함수를
// 지나야 검증이 한 벌이다.
func EditorUpload(w http.ResponseWriter, r *http.Request, m *content.MediaStore,
	limits content.UploadLimits, uploaderID string, log *slog.Logger) {

	// 파싱 전에 크기를 막는다 (첨부와 같은 규칙, D60 NFR-206).
	r.Body = http.MaxBytesReader(w, r.Body, limits.MaxBytes*editorMaxFiles+1<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		editorFail(w, "사진이 너무 큽니다.")
		return
	}
	var out []editorFile
	for i := range editorMaxFiles {
		f, h, err := r.FormFile(fmt.Sprintf("file-%d", i))
		if errors.Is(err, http.ErrMissingFile) {
			break
		}
		if err != nil {
			editorFail(w, "사진을 읽을 수 없습니다.")
			return
		}
		md, err := m.Save(r.Context(), uploaderID, h.Filename, f, limits, time.Now())
		f.Close()
		if msg := editorMessage(err, limits); msg != "" {
			editorFail(w, msg)
			return
		}
		if err != nil {
			log.Error("본문 사진 저장", "err", err)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"errorMessage": "일시적인 오류입니다."})
			return
		}
		out = append(out, editorFile{URL: "/media/" + md.ID, Name: h.Filename, Size: md.ByteSize})
	}
	if len(out) == 0 {
		editorFail(w, "사진을 고르세요.")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"result": out})
}

// editorMessage is what the person can act on, or "" for a server fault.
func editorMessage(err error, limits content.UploadLimits) string {
	var tooBig *http.MaxBytesError
	switch {
	case err == nil:
		return ""
	case errors.Is(err, content.ErrUploadExt), errors.Is(err, content.ErrUploadContent):
		return "jpg·png·gif·webp 사진만 올릴 수 있습니다."
	case errors.Is(err, content.ErrUploadTooLarge), errors.As(err, &tooBig):
		return fmt.Sprintf("사진이 너무 큽니다 (한 장에 %d MB 까지).", limits.MaxBytes>>20)
	case errors.Is(err, content.ErrImageTooLarge):
		return "사진의 화소가 너무 많습니다 (4,000만 화소까지)."
	case errors.Is(err, content.ErrImageDecode), errors.Is(err, content.ErrUploadEmpty):
		return "사진을 읽을 수 없습니다."
	}
	return ""
}

func editorFail(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"errorMessage": msg})
}

// PageMediaUpload is A-310 POST — 페이지 편집기(A-302)의 사진 올리기.
//
// 만들기(page.create)와 고치기(page.update) 어느 쪽이든 그 화면에서 사진을 넣는다.
func (d *Deps) PageMediaUpload(w http.ResponseWriter, r *http.Request) {
	c := d.Caller(r)
	if !c.Can("page.update") && !c.Can("page.create") {
		Forbidden(w)
		return
	}
	limits, err := d.Attachments.Limits(r.Context())
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	EditorUpload(w, r, d.Media, limits, c.UserID(), d.Logger)
}
