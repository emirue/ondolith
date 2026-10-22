package app

import (
	"bytes"
	"context"
	"image"
	"io"
	"net/http"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emirue/ondolith/internal/s3store/fakes3"
)

// setStorage writes A-210's keys the way the screen leaves them (FR-717).
func setStorage(t *testing.T, pool *pgxpool.Pool, endpoint, backend string) {
	t.Helper()
	for k, v := range map[string]string{
		"storage.backend": backend, "storage.s3.endpoint": endpoint, "storage.s3.region": "us-east-1",
		"storage.s3.bucket": "site", "storage.s3.access_key": "AK", "storage.s3.secret_key": "SK",
		"storage.s3.path_style": "1",
	} {
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO settings (key, value) VALUES ($1, $2)
			 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, k, v); err != nil {
			t.Fatal(err)
		}
	}
}

func fetch(t *testing.T, c *http.Client, url string, header ...string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func mediaURL(t *testing.T, out map[string]any) string {
	t.Helper()
	list, _ := out["result"].([]any)
	if len(list) != 1 {
		t.Fatalf("result = %v", out)
	}
	u, _ := list[0].(map[string]any)["url"].(string)
	return u
}

// **S3 로 바꾼 사이트의 업로드는 버킷에만 있고, 비공개 버킷인데도 같은 주소로
// 내려온다** (FR-717, W6-12). 첨부(P-211)·본문 사진(P-213)·상품 이미지(P-306)가
// 모두 같은 저장소(content.Files)를 지나는지, 화면에서 찌른다.
func TestS3StorageKeepsAndServesEveryUpload(t *testing.T) {
	srv, pool, admin, root := imageSite(t)
	bucket := fakes3.New("site")
	t.Cleanup(bucket.Close)
	writer, slug := boardWithAttachments(t, srv.URL, pool, true)
	ctx := context.Background()

	// 바꾸기 전에 올린 사진은 로컬에 있다.
	code, out := editorUpload(t, writer, srv.URL+"/board/"+slug+"/media", map[string][]byte{"전.jpg": photo(t, 40, 30)})
	if code != http.StatusOK {
		t.Fatalf("로컬 사진 업로드 HTTP %d", code)
	}
	localPic := mediaURL(t, out)
	before := storedFiles(t, root)
	if len(before) != 2 {
		t.Fatalf("로컬 사진 = %v, want 사진·썸네일", before)
	}

	setStorage(t, pool, bucket.URL, "s3")

	code, out = editorUpload(t, writer, srv.URL+"/board/"+slug+"/media", map[string][]byte{"후.jpg": photo(t, 2400, 1600)})
	if code != http.StatusOK {
		t.Fatalf("S3 사진 업로드 HTTP %d (%v)", code, out)
	}
	pic := mediaURL(t, out)
	if resp := writePostMultipart(t, writer, srv.URL, slug, "버킷 글", map[string][]byte{"안내.gif": gifBytes}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("첨부 글쓰기 HTTP %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	var attID string
	if err := pool.QueryRow(ctx, `SELECT id FROM attachments`).Scan(&attID); err != nil {
		t.Fatal(err)
	}
	productID := seedProducts(t, pool, 1)
	if code, body := uploadImages(t, admin, srv, productID, namedFile{"상품.gif", gifBytes}); code != http.StatusSeeOther {
		t.Fatalf("상품 이미지 HTTP %d: %s", code, body)
	}
	imgID := imageRows(t, pool)[0]

	if got := storedFiles(t, root); len(got) != len(before) {
		t.Fatalf("버킷으로 간 업로드가 로컬에 남았다: %v", got)
	}
	if keys := bucket.Keys(); len(keys) != 4 {
		t.Fatalf("버킷 = %v, want 사진·썸네일·첨부·상품 이미지", keys)
	}

	if resp, b := fetch(t, writer, srv.URL+"/attachments/"+attID); resp.StatusCode != http.StatusOK || !bytes.Equal(b, gifBytes) {
		t.Errorf("첨부 = HTTP %d, %d 바이트", resp.StatusCode, len(b))
	}
	if resp, b := fetch(t, http.DefaultClient, srv.URL+"/shop/images/"+imgID); resp.StatusCode != http.StatusOK || !bytes.Equal(b, gifBytes) {
		t.Errorf("상품 이미지 = HTTP %d, %d 바이트", resp.StatusCode, len(b))
	}
	resp, b := fetch(t, http.DefaultClient, srv.URL+pic)
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(b)); resp.StatusCode != http.StatusOK || err != nil || cfg.Width != 1920 {
		t.Fatalf("버킷의 사진 = HTTP %d, 폭 %d (%v)", resp.StatusCode, cfg.Width, err)
	}
	full := len(b)
	// Range 는 표준 라이브러리가 그대로 처리한다 — 버킷에서 그 자리부터 받는다.
	resp, part := fetch(t, http.DefaultClient, srv.URL+pic, "Range", "bytes=0-9")
	if resp.StatusCode != http.StatusPartialContent || len(part) != 10 || !bytes.Equal(part, b[:10]) {
		t.Fatalf("Range = HTTP %d, %d 바이트", resp.StatusCode, len(part))
	}
	if cr := resp.Header.Get("Content-Range"); cr != "bytes 0-9/"+strconv.Itoa(full) {
		t.Errorf("Content-Range = %q", cr)
	}

	// 바꾸기 전 로컬 사진도 보인다 — 옮기기 전이다.
	if resp, _ := fetch(t, http.DefaultClient, srv.URL+localPic); resp.StatusCode != http.StatusOK {
		t.Errorf("바꾸기 전 사진 = HTTP %d", resp.StatusCode)
	}
	// 로컬로 되돌려도 버킷의 파일이 보인다.
	setStorage(t, pool, bucket.URL, "")
	if resp, _ := fetch(t, http.DefaultClient, srv.URL+pic); resp.StatusCode != http.StatusOK {
		t.Errorf("로컬로 되돌린 뒤 버킷 사진 = HTTP %d", resp.StatusCode)
	}
	// 버킷이 답하지 않는 것은 「없다」가 아니다 — 404 면 지워진 줄 안다.
	setStorage(t, pool, bucket.URL, "s3")
	bucket.Close()
	for _, u := range []struct {
		c    *http.Client
		path string
	}{{http.DefaultClient, pic}, {writer, "/attachments/" + attID}, {http.DefaultClient, "/shop/images/" + imgID}} {
		if resp, _ := fetch(t, u.c, srv.URL+u.path); resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("버킷 불통 %s = HTTP %d, want 500", u.path, resp.StatusCode)
		}
	}
}

// 작성자가 지운 글(P-207)도 버킷이 파일 지우기를 거부할 때 500 이 아니다 — 글은 이미 갔다.
func TestAuthorPostDeleteGoesOnWhenTheBucketRefuses(t *testing.T) {
	srv, pool, _ := liveSiteWithUploads(t)
	bucket := fakes3.New("site")
	t.Cleanup(bucket.Close)
	setStorage(t, pool, bucket.URL, "s3")
	writer, slug := boardWithAttachments(t, srv.URL, pool, true)
	resp := writePostMultipart(t, writer, srv.URL, slug, "지울 글", map[string][]byte{"a.gif": gifBytes})
	resp.Body.Close()
	var postID string
	if err := pool.QueryRow(context.Background(), `SELECT id FROM posts`).Scan(&postID); err != nil {
		t.Fatal(err)
	}
	if len(bucket.Keys()) != 1 {
		t.Fatalf("버킷 %v", bucket.Keys())
	}
	bucket.RejectCode = "AccessDenied"
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/board/"+slug+"/"+postID+"/delete", nil)
	del, err := writer.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	del.Body.Close()
	if del.StatusCode != http.StatusSeeOther {
		t.Fatalf("P-207 = HTTP %d, want 303", del.StatusCode)
	}
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM posts`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("글 행 %d (%v)", n, err)
	}
}
