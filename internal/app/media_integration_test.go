package app

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/emirue/ondolith/internal/content"
)

func photo(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	img.Set(0, 0, color.Black)
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// editorUpload posts files the way SunEditor does: multipart, fields file-0, file-1…
func editorUpload(t *testing.T, c *http.Client, url string, files map[string][]byte) (int, map[string]any) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	i := 0
	for name, data := range files {
		part, err := mw.CreateFormFile("file-"+string(rune('0'+i)), name)
		if err != nil {
			t.Fatal(err)
		}
		part.Write(data)
		i++
	}
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, url, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// **글쓴이는 에디터에서 사진을 올리고, 그 주소로 줄인 사진과 썸네일을 받는다** (FR-514, P-213·P-214).
//
// 응답은 에디터(SunEditor)의 규약이다: {"result":[{"url","name","size"}]}.
func TestEditorUploadThenServe(t *testing.T) {
	srv, pool := liveSite(t)
	mkBoard(t, pool, "free", content.PresetPublic)
	mkUser(t, pool, "m@example.com")
	c := client()
	login(t, srv.URL, "m@example.com", c)

	code, out := editorUpload(t, c, srv.URL+"/board/free/media", map[string][]byte{"거실.jpg": photo(t, 2400, 1600)})
	if code != http.StatusOK {
		t.Fatalf("업로드 HTTP %d (%v)", code, out)
	}
	list, _ := out["result"].([]any)
	if len(list) != 1 {
		t.Fatalf("result = %v", out)
	}
	url, _ := list[0].(map[string]any)["url"].(string)
	if len(url) != len("/media/")+36 {
		t.Fatalf("url = %q", url)
	}
	for path, wantW := range map[string]int{url: 1920, url + "/thumb": 480} {
		resp, err := c.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		cfg, _, err := image.DecodeConfig(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || err != nil || cfg.Width != wantW {
			t.Errorf("%s = HTTP %d, 폭 %d (%v), want 200·%d", path, resp.StatusCode, cfg.Width, err, wantW)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s 헤더 Content-Type=%q nosniff=%q", path, ct, resp.Header.Get("X-Content-Type-Options"))
		}
	}
}

// 쓸 권한이 없으면 없는 게시판과 같은 404 이고(D15 SC-1 4항 — 403 은 게시판이 있다고
// 알려 준다), 그림이 아니면 에디터가 띄울
// 한국어 문구와 함께 200 이다 — 200 이 아니면 에디터는 개발자용 문구를 띄운다.
func TestEditorUploadRefusals(t *testing.T) {
	srv, pool := liveSite(t)
	mkBoard(t, pool, "free", content.PresetPublic)
	mkUser(t, pool, "m@example.com")
	pic := map[string][]byte{"a.jpg": photo(t, 10, 10)}

	if code, _ := editorUpload(t, client(), srv.URL+"/board/free/media", pic); code != http.StatusNotFound {
		t.Errorf("익명 업로드 = HTTP %d, want 404", code)
	}
	c := client()
	login(t, srv.URL, "m@example.com", c)
	if code, _ := editorUpload(t, c, srv.URL+"/board/nope/media", pic); code != http.StatusNotFound {
		t.Errorf("없는 게시판 = HTTP %d, want 404", code)
	}
	code, out := editorUpload(t, c, srv.URL+"/board/free/media", map[string][]byte{"사진.jpg": []byte("<html>not a picture")})
	if msg, _ := out["errorMessage"].(string); code != http.StatusOK || msg == "" {
		t.Errorf("그림 아님 = HTTP %d %v, want 200 + errorMessage", code, out)
	}
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM media`).Scan(&n); err != nil || n != 0 {
		t.Errorf("거부된 업로드가 media 행 %d 을 남겼다 (%v)", n, err)
	}
}
