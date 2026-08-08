package app

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emirue/ondolith/internal/config"
	"github.com/emirue/ondolith/internal/content"
)

// gifBytes 는 매직바이트 검증(D60 3절 2겹)을 통과하는 가장 작은 GIF 다.
// 확장자만 맞고 내용이 다른 파일은 거부돼야 하므로, 통과 경로에는 진짜 헤더가
// 필요하다.
var gifBytes = []byte("GIF89a\x01\x00\x01\x00\x00\xff\x00,\x00\x00\x00\x00" +
	"\x01\x00\x01\x00\x00\x02\x00;")

// writePostMultipart 는 글쓰기 폼(P-205)을 **브라우저가 보내는 그대로** 보낸다 —
// multipart 다. `files` 가 비면 파일 칸 없이 보낸다.
func writePostMultipart(t *testing.T, c *http.Client, base, slug, title string,
	files map[string][]byte,
) *http.Response {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for k, v := range map[string]string{"title": title, "body": "본문입니다."} {
		if err := w.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range files {
		// **필드 이름은 `attachments` 다** (D19 P-205). 이름이 어긋나면 파일은
		// 실려 오지만 아무도 읽지 않는다.
		part, err := w.CreateFormFile("attachments", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, base+"/board/"+slug+"/write", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// boardWithAttachments 는 첨부를 허용하는 게시판을 만들고 로그인한 클라이언트를
// 준다. **`CreateBoard` 를 쓴다** — 프리셋이 역할 부여까지 하므로, INSERT 로
// 행만 넣으면 글쓰기가 404 가 된다 (권한 없는 게시판은 없는 게시판이다).
func boardWithAttachments(t *testing.T, srv string, pool *pgxpool.Pool,
	allow bool,
) (*http.Client, string) {
	t.Helper()
	const slug = "notice"
	b := content.Board{Slug: slug, Name: "공지", AllowAttachments: allow,
		AllowComments: true, PerPage: 20}
	if _, err := content.NewStore(pool).CreateBoard(
		context.Background(), b, content.PresetPublic); err != nil {
		t.Fatal(err)
	}
	mkUser(t, pool, "writer@example.com")
	c := client()
	login(t, srv, "writer@example.com", c)
	return c, slug
}

// **글쓰기 폼으로 올린 파일이 저장되고, 글 화면에 나오고, 내려받힌다** (FR-506).
//
// 이 경로가 **통째로 없었다**: `Attachments.Save` 의 호출자가 0개였고, 업로드
// 디렉터리를 설치도 부팅도 만들지 않았다. 검증 4겹은 이미 있었는데 그것을
// 부르는 코드가 없었다 — 그래서 여기서는 저장소가 아니라 **화면부터** 찌른다.
func TestWritingAPostWithAFileStoresAndServesIt(t *testing.T) {
	srv, pool, root := liveSiteWithUploads(t)
	c, slug := boardWithAttachments(t, srv.URL, pool, true)
	ctx := context.Background()

	resp := writePostMultipart(t, c, srv.URL, slug, "첨부가 있는 글",
		map[string][]byte{"안내.gif": gifBytes})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("첨부와 함께 글쓰기 = HTTP %d, want 303", resp.StatusCode)
	}

	var id, name, stored string
	var size int64
	if err := pool.QueryRow(ctx,
		`SELECT id, original_name, stored_path, byte_size FROM attachments`,
	).Scan(&id, &name, &stored, &size); err != nil {
		t.Fatalf("첨부 행이 없다: %v", err)
	}
	if name != "안내.gif" {
		t.Errorf("원래 이름 = %q, want 안내.gif", name)
	}
	if size != int64(len(gifBytes)) {
		t.Errorf("크기 = %d, want %d", size, len(gifBytes))
	}

	// **파일이 디스크에 있고 실행 비트가 없다** (D60 3절 4겹). 행만 확인하면
	// 「행은 있는데 파일이 없는」 상태를 통과시킨다.
	full := filepath.Join(root, filepath.FromSlash(stored))
	info, err := os.Stat(full)
	if err != nil {
		t.Fatalf("저장된 파일이 없다 (%s): %v", stored, err)
	}
	if info.Mode().Perm()&0o111 != 0 {
		t.Errorf("파일 권한 %v — 실행 비트가 있다", info.Mode().Perm())
	}
	// **이름이 재생성된다.** 원래 이름이 경로에 남으면 확장자도 함께 남는다.
	if strings.Contains(stored, "안내") || strings.HasSuffix(stored, ".gif") {
		t.Errorf("저장 경로 %q 에 사용자가 준 이름이 남아 있다", stored)
	}

	// 글 화면이 첨부를 그린다 — 이것이 없으면 올려도 아무도 못 찾는다.
	page := bodyOf(t, c, srv.URL+"/board/"+slug)
	if !strings.Contains(page, "첨부가 있는 글") {
		t.Fatalf("목록에 글이 없다: %.300s", page)
	}
	var postID string
	if err := pool.QueryRow(ctx, `SELECT id FROM posts LIMIT 1`).Scan(&postID); err != nil {
		t.Fatal(err)
	}
	detail := bodyOf(t, c, srv.URL+"/board/"+slug+"/"+postID)
	if !strings.Contains(detail, "/attachments/"+id) {
		t.Errorf("글 화면에 첨부 링크가 없다: %.500s", detail)
	}
	if !strings.Contains(detail, "안내.gif") {
		t.Error("글 화면에 첨부 이름이 없다")
	}

	// 다운로드(P-211)가 실제 바이트를 낸다.
	dl, err := c.Get(srv.URL + "/attachments/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer dl.Body.Close()
	if dl.StatusCode != http.StatusOK {
		t.Fatalf("다운로드 = HTTP %d, want 200", dl.StatusCode)
	}
	got := readAll(t, dl)
	if got != string(gifBytes) {
		t.Errorf("내려받은 바이트가 올린 것과 다르다 (%d 바이트)", len(got))
	}
}

// **첨부를 허용하지 않는 게시판은 파일을 받지 않는다** (D19 P-205).
//
// 그 화면에는 파일 칸이 없다. 그래도 실려 온 것은 폼을 고쳐 보낸 것이고,
// 거부 사유는 **확장자가 아니라 게시판 설정**이어야 한다 — 확장자라고 말하면
// 파일을 바꿔 다시 올려 보는 사람을 만든다.
func TestBoardThatDisallowsAttachmentsRefusesFiles(t *testing.T) {
	srv, pool, _ := liveSiteWithUploads(t)
	c, slug := boardWithAttachments(t, srv.URL, pool, false)
	ctx := context.Background()

	resp := writePostMultipart(t, c, srv.URL, slug, "몰래 첨부",
		map[string][]byte{"안내.gif": gifBytes})
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("첨부 금지 게시판에 첨부 = HTTP %d, want 422", resp.StatusCode)
	}
	if !strings.Contains(body, "첨부를 받지 않습니다") {
		t.Errorf("거부 사유가 게시판 설정이 아니다: %.300s", body)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM attachments`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("거부됐는데 첨부가 %d 개 저장됐다", n)
	}

	// 헛돌기 방지: 파일 없이 쓰면 통과한다. 아니면 위 단언은 「이 게시판에는
	// 글도 못 쓴다」일 수 있다.
	resp = writePostMultipart(t, c, srv.URL, slug, "파일 없는 글", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("파일 없는 글쓰기 = HTTP %d, want 303 — 위 단언이 헛돌았다", resp.StatusCode)
	}
}

// **내용이 확장자와 다른 파일은 거부된다** (D60 3절, NFR-206).
//
// 확장자 허용목록만으로는 `.gif` 로 이름 붙인 스크립트를 막지 못한다. 배선이
// 이 검증을 실제로 지나가는지 여기서 본다 — `StoreUpload` 를 직접 부르는
// 테스트는 배선이 끊겨도 통과한다.
func TestUploadWiringGoesThroughContentValidation(t *testing.T) {
	srv, pool, _ := liveSiteWithUploads(t)
	c, slug := boardWithAttachments(t, srv.URL, pool, true)
	ctx := context.Background()

	// 확장자는 허용목록에 있으나 내용이 GIF 가 아니다.
	resp := writePostMultipart(t, c, srv.URL, slug, "위장 파일",
		map[string][]byte{"payload.gif": []byte("<?php system($_GET['c']); ?>")})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("내용이 다른 파일 = HTTP %d, want 422", resp.StatusCode)
	}

	// 허용목록에 없는 확장자.
	resp = writePostMultipart(t, c, srv.URL, slug, "실행 파일",
		map[string][]byte{"shell.php": []byte("<?php ?>")})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("허용하지 않는 확장자 = HTTP %d, want 422", resp.StatusCode)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM attachments`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("거부됐는데 첨부가 %d 개 저장됐다", n)
	}

	// 헛돌기 방지: 진짜 GIF 는 통과한다.
	resp = writePostMultipart(t, c, srv.URL, slug, "진짜 이미지",
		map[string][]byte{"real.gif": gifBytes})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("진짜 GIF = HTTP %d, want 303 — 위 단언들이 헛돌았다", resp.StatusCode)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM attachments`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("통과한 파일이 %d 개 저장됐다, want 1", n)
	}
}

// **부팅이 업로드 디렉터리를 만든다** (FR-506).
//
// 아무도 만들지 않아서 `os.OpenRoot` 가 없는 경로에서 실패했고, 그래서 첨부는
// 화면을 붙여도 저장될 수 없었다. 경로는 설정값이므로(NFR-304) 운영자가 옮길 수
// 있고, 그러면 **뜰 때마다** 확인해야 한다 — 설치 한 번으로는 부족하다.
//
// `t.TempDir()` 은 이미 존재하므로 그것으로는 이 성질을 시험할 수 없다.
// **아직 없는 경로**를 주고, 뜬 뒤에 생겼는지 본다.
func TestBootCreatesTheUploadDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "uploads", "nested")
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("이 경로는 없어야 한다 (%v) — 검사가 헛돌았다", err)
	}

	srv, pool := liveSiteWith(t, func(c *config.Config) { c.UploadDir = missing })
	if _, err := os.Stat(missing); err != nil {
		t.Fatalf("부팅 뒤에도 업로드 디렉터리가 없다: %v", err)
	}

	// 그리고 실제로 저장된다 — 디렉터리만 있고 쓸 수 없으면 소용없다.
	c, slug := boardWithAttachments(t, srv.URL, pool, true)
	resp := writePostMultipart(t, c, srv.URL, slug, "첨부",
		map[string][]byte{"a.gif": gifBytes})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("첨부와 함께 글쓰기 = HTTP %d, want 303", resp.StatusCode)
	}
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM attachments`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("첨부 %d 개, want 1", n)
	}
}
