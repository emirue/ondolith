package admin

import (
	"bytes"
	"encoding/json"
	"image"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

// **페이지 편집기(A-302)의 사진 올리기는 만들기·고치기 어느 권한으로든 되고, 둘 다
// 없으면 403 이다** (A-310, FR-514). 응답은 에디터 규약의 JSON 이다.
func TestPageMediaUploadNeedsAPagePermission(t *testing.T) {
	var pic bytes.Buffer
	if err := jpeg.Encode(&pic, image.NewRGBA(image.Rect(0, 0, 40, 30)), nil); err != nil {
		t.Fatal(err)
	}
	upload := func(d *Deps) *httptest.ResponseRecorder {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		part, _ := mw.CreateFormFile("file-0", "약도.jpg")
		part.Write(pic.Bytes())
		mw.Close()
		req := httptest.NewRequest(http.MethodPost, "/admin/pages/media", &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := httptest.NewRecorder()
		d.PageMediaUpload(rec, req)
		return rec
	}
	for _, perm := range []string{"page.update", "page.create"} {
		d, _ := fixture(t, &fakeCaller{perms: map[string]bool{perm: true}})
		rec := upload(d)
		var out struct {
			Result []struct{ URL string } `json:"result"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || len(out.Result) != 1 {
			t.Errorf("%s 로 올리기 = HTTP %d %s", perm, rec.Code, rec.Body.String())
		}
	}
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"page.view": true}})
	if rec := upload(d); rec.Code != http.StatusForbidden {
		t.Errorf("page.view 만으로 = HTTP %d, want 403", rec.Code)
	}
}
