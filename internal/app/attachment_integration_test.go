package app

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emirue/ondolith/internal/content"
)

var pngFixture = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR" + strings.Repeat("\x00", 40))

// attach uploads a file to a post through the store, returning the row id.
func attach(t *testing.T, pool *pgxpool.Pool, root, postID, name string) string {
	t.Helper()
	got, err := content.NewStore(pool).AttachmentsIn(root).
		Save(context.Background(), postID, name, bytes.NewReader(pngFixture))
	if err != nil {
		t.Fatal(err)
	}
	return got.ID
}

// D15 8절 1번: 첨부 다운로드가 부모 글의 읽기 권한을 **다시** 검사한다.
// 비공개 게시판의 첨부 ID 를 직접 요청하면 404 다 — "권한은 글 화면에서
// 봤다"가 이 구멍 앞에 오는 문장이다.
func TestAttachmentDownloadRechecksTheParentPostPermission(t *testing.T) {
	srv, pool, root := liveSiteWithUploads(t)
	staffID := mkBoard(t, pool, "staff", content.PresetPrivate)
	freeID := mkBoard(t, pool, "free", content.PresetPublic)
	store := content.NewStore(pool)
	ctx := context.Background()

	secretPost, err := store.CreatePost(ctx, content.Post{
		BoardID: staffID, Title: "내부 문서", Body: "기밀"})
	if err != nil {
		t.Fatal(err)
	}
	openPost, err := store.CreatePost(ctx, content.Post{
		BoardID: freeID, Title: "공개 글", Body: "본문"})
	if err != nil {
		t.Fatal(err)
	}
	secretFile := attach(t, pool, root, secretPost, "기밀.png")
	openFile := attach(t, pool, root, openPost, "사진.png")

	anon := client()
	// 비공개 게시판의 첨부 ID 를 직접 요청.
	code, body := mustGet(t, anon, srv.URL+"/attachments/"+secretFile)
	if code != http.StatusNotFound {
		t.Errorf("비공개 게시판 첨부가 HTTP %d 로 내려갔다", code)
	}
	if strings.Contains(body, "PNG") {
		t.Error("파일 내용이 새어 나왔다")
	}
	// 공개 게시판의 첨부는 내려간다.
	if code, _ := mustGet(t, anon, srv.URL+"/attachments/"+openFile); code != http.StatusOK {
		t.Errorf("공개 첨부가 HTTP %d", code)
	}
	// 없는 ID 도 같은 404 — 존재 여부가 응답으로 갈리면 그 자체가 정보다.
	if code, _ := mustGet(t, anon, srv.URL+"/attachments/00000000-0000-4000-8000-000000000000"); code != http.StatusNotFound {
		t.Errorf("없는 첨부가 HTTP %d", code)
	}
}

// 비밀글의 첨부는 그 글을 따라간다.
func TestSecretPostAttachmentFollowsThePost(t *testing.T) {
	srv, pool, root := liveSiteWithUploads(t)
	boardID := mkBoard(t, pool, "free", content.PresetPublic)
	authorID := mkUser(t, pool, "a@example.com")
	mkUser(t, pool, "b@example.com")
	store := content.NewStore(pool)
	ctx := context.Background()

	postID, err := store.CreatePost(ctx, content.Post{
		BoardID: boardID, AuthorID: authorID, Title: "비밀", Body: "본문", IsSecret: true})
	if err != nil {
		t.Fatal(err)
	}
	fileID := attach(t, pool, root, postID, "a.png")

	author := client()
	login(t, srv.URL, "a@example.com", author)
	if code, _ := mustGet(t, author, srv.URL+"/attachments/"+fileID); code != http.StatusOK {
		t.Errorf("작성자가 자기 비밀글 첨부를 못 받는다: HTTP %d", code)
	}

	other := client()
	login(t, srv.URL, "b@example.com", other)
	if code, _ := mustGet(t, other, srv.URL+"/attachments/"+fileID); code != http.StatusNotFound {
		t.Errorf("남이 비밀글 첨부를 HTTP %d 로 받았다", code)
	}
	if code, _ := mustGet(t, client(), srv.URL+"/attachments/"+fileID); code != http.StatusNotFound {
		t.Error("익명이 비밀글 첨부를 받았다")
	}
}

// 응답 헤더가 브라우저에서 실행되지 않게 한다. inline 이면 이 출처에서 이
// 세션의 쿠키로 실행된다.
func TestDownloadIsAlwaysAnAttachmentAndNeverSniffed(t *testing.T) {
	srv, pool, root := liveSiteWithUploads(t)
	boardID := mkBoard(t, pool, "free", content.PresetPublic)
	postID, err := content.NewStore(pool).CreatePost(context.Background(), content.Post{
		BoardID: boardID, Title: "글", Body: "본문"})
	if err != nil {
		t.Fatal(err)
	}
	fileID := attach(t, pool, root, postID, "한글 이름.png")

	resp, err := client().Get(srv.URL + "/attachments/" + fileID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP %d", resp.StatusCode)
	}
	cd := resp.Header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment;") {
		t.Errorf("Content-Disposition = %q — inline 이면 브라우저가 실행한다", cd)
	}
	// 한글 이름은 filename* 으로 나간다. 안 그러면 깨지거나 버려진다.
	if !strings.Contains(cd, "filename*=UTF-8''") {
		t.Errorf("한글 파일명이 RFC 5987 형식이 아니다: %q", cd)
	}
	// 그리고 구식 filename= 쪽은 순수 ASCII 여야 한다. 여기에 한글을 그대로
	// 넣는 것이 애초에 filename* 이 생긴 이유다 — 헤더는 바이트 단위로
	// 해석되고, 클라이언트마다 다르게 깨진다.
	quoted := cd[strings.Index(cd, `filename="`)+len(`filename="`):]
	quoted = quoted[:strings.IndexByte(quoted, '"')]
	for _, r := range quoted {
		if r > 0x7f {
			t.Errorf("filename= 에 비ASCII 문자가 있다 (%q): %q", string(r), cd)
			break
		}
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff 가 없다 — 저장 이름이 UUID 라 브라우저가 추측한다")
	}
	// Content-Type 은 업로드 때 서버가 측정한 값이다.
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q", ct)
	}
}

// 헤더에 들어가는 이름은 사용자 입력이다. 줄바꿈은 헤더 주입이고 따옴표는
// filename 을 일찍 끝낸다.
func TestContentDispositionCannotInjectHeaders(t *testing.T) {
	for _, name := range []string{
		"a\r\nX-Injected: 1.png",
		`a".png`,
		"a\\b.png",
		"\x00.png",
		"",
	} {
		got := contentDisposition(name)
		for _, bad := range []string{"\r", "\n", "\x00"} {
			if strings.Contains(got, bad) {
				t.Errorf("%q → 헤더에 제어문자가 남았다: %q", name, got)
			}
		}
		if strings.Count(got, `"`) != 2 {
			t.Errorf("%q → 따옴표가 %d개다: %q", name, strings.Count(got, `"`), got)
		}
		if !strings.HasPrefix(got, "attachment;") {
			t.Errorf("%q → %q", name, got)
		}
	}
}

// 행은 있는데 파일이 없는 상태를 A-309 가 허용한다. 500 이 아니라 404 다 —
// 고장난 것이 아니라 파일이 없는 것이다.
func TestMissingFileIs404NotServerError(t *testing.T) {
	srv, pool, root := liveSiteWithUploads(t)
	boardID := mkBoard(t, pool, "free", content.PresetPublic)
	ctx := context.Background()
	postID, err := content.NewStore(pool).CreatePost(ctx, content.Post{
		BoardID: boardID, Title: "글", Body: "본문"})
	if err != nil {
		t.Fatal(err)
	}
	fileID := attach(t, pool, root, postID, "a.png")
	var stored string
	if err := pool.QueryRow(ctx,
		`SELECT stored_path FROM attachments WHERE id = $1`, fileID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if err := removeUnder(root, stored); err != nil {
		t.Fatal(err)
	}

	if code, _ := mustGet(t, client(), srv.URL+"/attachments/"+fileID); code != http.StatusNotFound {
		t.Errorf("파일 없는 첨부가 HTTP %d", code)
	}
}

// **본인 글 삭제(P-207)가 첨부 실물까지 지운다** (OPEN-40 결정).
//
// 행은 CASCADE 로 사라진다. 파일은 따라가지 않고 정리 잡도 없으므로
// (NFR-103), 화면이 파일까지 지우는 경로를 부르지 않으면 그 파일은 영원히
// 남는다 — 글이 없으니 A-309 목록에도 안 나와 아무도 찾지 못한다.
func TestDeletingOwnPostRemovesTheAttachmentFile(t *testing.T) {
	srv, pool, root := liveSiteWithUploads(t)
	boardID := mkBoard(t, pool, "free", content.PresetPublic)
	store := content.NewStore(pool)
	ctx := context.Background()

	author := mkUser(t, pool, "writer@example.com")
	post, err := store.CreatePost(ctx, content.Post{
		BoardID: boardID, Title: "지울 글", Body: "본문", AuthorID: author})
	if err != nil {
		t.Fatal(err)
	}
	attach(t, pool, root, post, "사진.png")

	var stored string
	if err := pool.QueryRow(ctx,
		`SELECT stored_path FROM attachments WHERE post_id = $1`, post).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(root, filepath.FromSlash(stored))
	if _, err := os.Stat(full); err != nil {
		t.Fatalf("업로드 직후 파일이 없다: %v", err)
	}

	c := client()
	login(t, srv.URL, "writer@example.com", c)
	resp := httpPost(t, c, srv.URL+"/board/free/"+post+"/delete", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("삭제 HTTP %d", resp.StatusCode)
	}

	if _, err := os.Stat(full); !os.IsNotExist(err) {
		t.Errorf("첨부 파일이 남았다 (err=%v) — 아무도 찾지 못하는 쓰레기다", err)
	}
}
