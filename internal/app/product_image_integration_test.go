package app

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emirue/ondolith/internal/config"
)

// imageSite 는 shop 모드 + **임시 업로드 루트**로 뜬 사이트와 로그인한 관리자다.
// 루트를 정하지 않으면 테스트가 작업 디렉터리 아래 `uploads/` 에 파일을 쓴다.
func imageSite(t *testing.T) (srv *httptest.Server, pool *pgxpool.Pool, admin *http.Client, root string) {
	t.Helper()
	_, pool = liveSite(t)
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO settings (key, value) VALUES ('site.type','shop')
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`); err != nil {
		t.Fatal(err)
	}
	root = t.TempDir()
	cfg := &config.Config{DatabaseURL: os.Getenv(dsnEnv), SiteName: "테스트 사이트", UploadDir: root}
	h, cleanup, err := New(context.Background(), cfg, "1.0.0",
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil {
		t.Fatalf("재기동 실패: %v", err)
	}
	t.Cleanup(cleanup)
	srv = httptest.NewServer(h)
	t.Cleanup(srv.Close)
	admin, _ = adminSession(t, srv, pool)
	return srv, pool, admin, root
}

// uploadImages 는 A-502 의 이미지 폼을 브라우저가 보내는 그대로(multipart) 보낸다.
// 필드 이름은 `image` 다 — 어긋나면 파일은 실려 오지만 아무도 읽지 않는다.
func uploadImages(t *testing.T, c *http.Client, srv *httptest.Server, productID string,
	files ...namedFile) (int, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, f := range files {
		part, err := w.CreateFormFile("image", f.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(f.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/admin/products/"+productID+"/images", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Origin", srv.URL)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(b)
}

type namedFile struct {
	name string
	data []byte
}

// storedFiles 는 업로드 루트 아래의 파일 경로(루트 기준)다.
func storedFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func imageRows(t *testing.T, pool *pgxpool.Pool) (ids []string) {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT id FROM product_images ORDER BY created_at, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

var storedNameRe = regexp.MustCompile(`^[0-9]{4}/[0-9]{2}/[0-9a-f-]{36}$`)

// **올린 이미지가 그대로 내려오고, 상품 화면들이 그것을 그린다** (FR-601).
//
// A-502 에 파일 칸이 없었고 이미지를 담을 표도 없었다. 관리자 POST 에서 공개
// GET 까지 간다 — 저장 함수만 부르면 폼 필드 이름·라우트·템플릿이 어긋난 것을
// 못 본다.
func TestProductImageRoundTripsAndShowsOnTheShop(t *testing.T) {
	srv, pool, admin, root := imageSite(t)
	productID := seedProducts(t, pool, 1) // 상품00, slug p00, 공개

	if code, body := uploadImages(t, admin, srv, productID, namedFile{"사진.GIF", gifBytes}); code != http.StatusSeeOther {
		t.Fatalf("업로드 = HTTP %d, want 303: %s", code, body)
	}
	ids := imageRows(t, pool)
	if len(ids) != 1 {
		t.Fatalf("이미지 행 %d개, want 1", len(ids))
	}
	// 디스크의 이름은 서버가 만든 것이다 (D60 3항): YYYY/MM/<uuid>, 확장자 없음.
	files := storedFiles(t, root)
	if len(files) != 1 || !storedNameRe.MatchString(files[0]) {
		t.Fatalf("업로드 루트의 파일 = %v, want YYYY/MM/<uuid> 하나", files)
	}

	// P-306: 로그인하지 않은 손님이 받는다.
	resp, err := http.Get(srv.URL + "/shop/images/" + ids[0])
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("P-306 = HTTP %d", resp.StatusCode)
	}
	if !bytes.Equal(got, gifBytes) {
		t.Error("내려온 바이트가 올린 것과 다르다")
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/gif" {
		t.Errorf("Content-Type = %q, want image/gif (서버가 잰 값)", ct)
	}
	if v := resp.Header.Get("X-Content-Type-Options"); v != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", v)
	}
	if v := resp.Header.Get("Content-Disposition"); v != "inline" {
		t.Errorf("Content-Disposition = %q, want inline", v)
	}

	// Content-Type 은 **기록된 값**이다. 내용을 다시 추측하면 GIF 는 어차피
	// image/gif 로 나와 이 단언이 헛돌므로, 기록을 바꿔 놓고 그 값이 오는지 본다.
	if _, err := pool.Exec(context.Background(),
		`UPDATE product_images SET mime_type = 'image/webp' WHERE id = $1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	resp, err = http.Get(srv.URL + "/shop/images/" + ids[0])
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "image/webp" {
		t.Errorf("Content-Type = %q, want 기록된 image/webp — 내용을 추측해 내보낸다", ct)
	}

	src := `src="/shop/images/` + ids[0] + `" alt="상품00"`
	// P-303 상세.
	if _, body := mustGet(t, http.DefaultClient, srv.URL+"/shop/p/p00"); !strings.Contains(body, src) {
		t.Errorf("P-303 에 이미지 %s 가 없다", src)
	}
	// P-301 목록·P-305 검색·홈의 대표 이미지. 화면 아래쪽일 수 있으므로 lazy 다.
	for _, path := range []string{"/shop", "/shop/search?q=상품00", "/"} {
		_, body := mustGet(t, http.DefaultClient, srv.URL+path)
		i := strings.Index(body, src)
		if i < 0 {
			t.Errorf("%s 에 대표 이미지 %s 가 없다", path, src)
			continue
		}
		tag := body[i : i+strings.Index(body[i:], ">")]
		if !strings.Contains(tag, `loading="lazy"`) || !strings.Contains(tag, `width="`) {
			t.Errorf("%s 의 대표 이미지에 lazy·크기 속성이 없다: %s", path, tag)
		}
	}
	// A-502 가 올린 것을 보여 주고 지우는 폼을 건다.
	if _, body := mustGet(t, admin, srv.URL+"/admin/products/"+productID); !strings.Contains(body, src) ||
		!strings.Contains(body, `action="/admin/product-images/`+ids[0]+`/delete"`) {
		t.Error("A-502 가 올린 이미지나 삭제 폼을 그리지 않는다")
	}

	// 두 번째 장을 올려도 대표는 첫 장이다.
	if code, _ := uploadImages(t, admin, srv, productID, namedFile{"b.png", pngHeader}); code != http.StatusSeeOther {
		t.Fatalf("둘째 업로드 = HTTP %d", code)
	}
	if _, body := mustGet(t, http.DefaultClient, srv.URL+"/shop"); !strings.Contains(body, src) {
		t.Error("둘째 장을 올리자 목록의 대표 이미지가 바뀌었다")
	}
	if ids2 := imageRows(t, pool); len(ids2) != 2 {
		t.Fatalf("이미지 행 %d개, want 2", len(ids2))
	} else if _, body := mustGet(t, http.DefaultClient, srv.URL+"/shop/p/p00"); !strings.Contains(body, "/shop/images/"+ids2[1]) {
		t.Error("P-303 이 둘째 이미지를 그리지 않는다")
	}
}

// pngHeader 는 http.DetectContentType 이 image/png 로 읽는 최소 바이트다.
var pngHeader = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR" + strings.Repeat("\x00", 40))

// **위장 파일·이미지가 아닌 파일·너무 큰 파일은 422 이고 아무것도 남지 않는다**
// (NFR-206, D19 A-502).
//
// 검증기는 첨부와 같은 것이다 (content.StoreUpload). 여기서 고정하는 것은 그
// 검증기가 **이 경로에 실제로 걸려 있다**는 것과, 상품 이미지가 허용목록을
// 이미지로 좁힌다는 것이다 — pdf 는 첨부로는 통과하지만 여기서는 거부된다.
func TestProductImageUploadRefusesWhatTheValidatorRefuses(t *testing.T) {
	srv, pool, admin, root := imageSite(t)
	ctx := context.Background()
	productID := seedProducts(t, pool, 1)

	html := []byte("<!doctype html><html><script>alert(1)</script></html>")
	for _, tc := range []struct {
		why  string
		file namedFile
	}{
		{"HTML 을 .png 로 위장", namedFile{"x.png", html}},
		{"허용목록에 없는 확장자(.svg)", namedFile{"x.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)}},
		{"확장자 없음", namedFile{"x", gifBytes}},
		{"이미지가 아닌 허용 형식(.pdf)", namedFile{"x.pdf", []byte("%PDF-1.4\n%âãÏÓ\n")}},
		{"GIF 를 .png 로 올림 (확장자와 내용 불일치)", namedFile{"x.png", gifBytes}},
	} {
		code, _ := uploadImages(t, admin, srv, productID, tc.file)
		if code != http.StatusUnprocessableEntity {
			t.Errorf("%s = HTTP %d, want 422", tc.why, code)
		}
	}
	// 거부된 시도는 작업 로그에 남는다 (D19 A-502 오류표).
	var logged int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM operation_logs WHERE summary LIKE '상품 이미지 업로드 거부%'`).Scan(&logged); err != nil {
		t.Fatal(err)
	}
	if logged != 5 {
		t.Errorf("거부 기록 %d건, want 5", logged)
	}

	// 크기 상한은 A-309 의 설정이다. 26바이트 GIF 가 16바이트 상한에 걸린다.
	if _, err := pool.Exec(ctx,
		`INSERT INTO settings (key, value) VALUES ('upload.max_bytes','16')`); err != nil {
		t.Fatal(err)
	}
	if code, _ := uploadImages(t, admin, srv, productID, namedFile{"big.gif", gifBytes}); code != http.StatusUnprocessableEntity {
		t.Errorf("상한 초과 = HTTP %d, want 422", code)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM settings WHERE key = 'upload.max_bytes'`); err != nil {
		t.Fatal(err)
	}

	// 파일을 고르지 않은 제출.
	if code, _ := uploadImages(t, admin, srv, productID); code != http.StatusUnprocessableEntity {
		t.Errorf("빈 제출 = HTTP %d, want 422", code)
	}

	if ids := imageRows(t, pool); len(ids) != 0 {
		t.Errorf("거부됐는데 행 %d개가 남았다", len(ids))
	}
	if files := storedFiles(t, root); len(files) != 0 {
		t.Errorf("거부됐는데 파일이 남았다: %v", files)
	}

	// **DB 도 같은 집합을 막는다** (D30 product_images). 이 표의 파일은 inline
	// 으로 나가므로, 검증기를 지나친 경로가 생겨도 문서로 해석될 타입은 행이
	// 될 수 없어야 한다.
	for i, mime := range []string{"image/svg+xml", "text/html"} {
		// 경로를 달리한다 — 같으면 둘째는 UNIQUE 에 걸려 이 단언이 헛돈다.
		path := "2026/10/00000000-0000-4000-8000-00000000000" + string(rune('0'+i))
		if _, err := pool.Exec(ctx, `
			INSERT INTO product_images (product_id, stored_path, original_name, mime_type, byte_size)
			VALUES ($1, $2, 'x', $3, 1)`,
			productID, path, mime); err == nil {
			t.Errorf("product_images 가 mime_type %q 를 받았다", mime)
		}
	}

	// 헛돌기 방지: 같은 경로로 진짜 이미지는 들어간다.
	if code, body := uploadImages(t, admin, srv, productID, namedFile{"ok.gif", gifBytes}); code != http.StatusSeeOther {
		t.Fatalf("정상 이미지 = HTTP %d: %s", code, body)
	}
}

// **행이 안 들어가면 파일도 남지 않는다.** 없는 상품에 올리면 FK 가 INSERT 를
// 막는데, 그때 파일은 이미 디스크에 쓰인 뒤다. 남기면 아무것도 가리키지 않는
// 파일이 살아 있는 이미지와 구분되지 않는다.
func TestProductImageFailedRowLeavesNoFile(t *testing.T) {
	srv, pool, admin, root := imageSite(t)

	code, _ := uploadImages(t, admin, srv, "00000000-0000-0000-0000-000000000000",
		namedFile{"a.gif", gifBytes})
	if code != http.StatusNotFound {
		t.Errorf("없는 상품에 업로드 = HTTP %d, want 404", code)
	}
	if files := storedFiles(t, root); len(files) != 0 {
		t.Errorf("행이 없는데 파일이 남았다: %v", files)
	}
	if ids := imageRows(t, pool); len(ids) != 0 {
		t.Errorf("행 %d개", len(ids))
	}
}

// **상품당 상한이 있다.** 한 요청에 열한 장을 실으면 열 장만 들어간다.
func TestProductImageCountIsBounded(t *testing.T) {
	srv, pool, admin, root := imageSite(t)
	productID := seedProducts(t, pool, 1)

	var files []namedFile
	for range 11 {
		files = append(files, namedFile{"a.gif", gifBytes})
	}
	if code, _ := uploadImages(t, admin, srv, productID, files...); code != http.StatusUnprocessableEntity {
		t.Errorf("열한 장 = HTTP %d, want 422", code)
	}
	if n := len(imageRows(t, pool)); n != 10 {
		t.Errorf("이미지 행 %d개, want 10 (상한)", n)
	}
	if n := len(storedFiles(t, root)); n != 10 {
		t.Errorf("파일 %d개, want 10 — 상한을 넘긴 파일이 디스크에 남았다", n)
	}
}

// **지우면 행과 파일이 함께 간다.** 상품을 지울 때도 그렇다 — 행은 CASCADE 로
// 가지만 파일은 따라가지 않고, 정리 잡이 없다 (NFR-103).
func TestProductImageDeleteRemovesRowAndFile(t *testing.T) {
	srv, pool, admin, root := imageSite(t)
	productID := seedProducts(t, pool, 1)

	uploadImages(t, admin, srv, productID, namedFile{"a.gif", gifBytes}, namedFile{"b.png", pngHeader})
	ids := imageRows(t, pool)
	if len(ids) != 2 || len(storedFiles(t, root)) != 2 {
		t.Fatalf("준비: 행 %d개·파일 %d개, want 2·2", len(ids), len(storedFiles(t, root)))
	}

	code, _ := adminPostForm(t, admin, srv, "/admin/product-images/"+ids[0]+"/delete", nil)
	if code != http.StatusSeeOther {
		t.Fatalf("이미지 삭제 = HTTP %d, want 303", code)
	}
	if left := imageRows(t, pool); len(left) != 1 || left[0] != ids[1] {
		t.Errorf("남은 행 = %v, want [%s]", left, ids[1])
	}
	if n := len(storedFiles(t, root)); n != 1 {
		t.Errorf("파일 %d개, want 1 — 지운 이미지의 파일이 남았다", n)
	}
	if code, _ := mustGet(t, http.DefaultClient, srv.URL+"/shop/images/"+ids[0]); code != http.StatusNotFound {
		t.Errorf("지운 이미지 = HTTP %d, want 404", code)
	}
	// 없는 이미지를 지우면 404 다.
	if code, _ := adminPostForm(t, admin, srv, "/admin/product-images/"+ids[0]+"/delete", nil); code != http.StatusNotFound {
		t.Errorf("이미 지운 이미지 삭제 = HTTP %d, want 404", code)
	}

	// 상품 삭제 — 조합이 붙어 있어도 주문이 없으면 지워진다.
	if code, body := adminPostForm(t, admin, srv, "/admin/products/"+productID+"/delete", nil); code != http.StatusSeeOther {
		t.Fatalf("상품 삭제 = HTTP %d: %s", code, body)
	}
	if n := len(imageRows(t, pool)); n != 0 {
		t.Errorf("상품을 지웠는데 이미지 행 %d개", n)
	}
	if files := storedFiles(t, root); len(files) != 0 {
		t.Errorf("상품을 지웠는데 파일이 남았다: %v", files)
	}
}

// **숨긴 상품의 이미지는 공개되지 않는다** (D15 SC-7 2항).
//
// 이미지 id 는 누구나 쥘 수 있는 URL 이다. 상품을 숨겨도 이미지가 열리면
// 숨긴 상품이 있다는 것과 그 생김새가 샌다. P-303 과 같이 404 다. 운영자
// (`product.manage`)는 A-502 미리보기를 위해 볼 수 있다.
func TestHiddenProductImageIsNotPublic(t *testing.T) {
	srv, pool, admin, _ := imageSite(t)
	ctx := context.Background()
	productID := seedProducts(t, pool, 1)
	uploadImages(t, admin, srv, productID, namedFile{"a.gif", gifBytes})
	ids := imageRows(t, pool)
	if len(ids) != 1 {
		t.Fatalf("준비: 이미지 행 %d개", len(ids))
	}
	url := srv.URL + "/shop/images/" + ids[0]

	if code, _ := mustGet(t, http.DefaultClient, url); code != http.StatusOK {
		t.Fatalf("공개 상품의 이미지 = HTTP %d — 아래 단언이 헛돈다", code)
	}
	if _, err := pool.Exec(ctx, `UPDATE products SET is_visible = false WHERE id = $1`, productID); err != nil {
		t.Fatal(err)
	}
	if code, _ := mustGet(t, http.DefaultClient, url); code != http.StatusNotFound {
		t.Errorf("숨긴 상품의 이미지(손님) = HTTP %d, want 404", code)
	}
	resp, err := admin.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("숨긴 상품의 이미지(운영자) = HTTP %d, want 200", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("운영자 전용 응답의 Cache-Control = %q, want no-store", cc)
	}
}
