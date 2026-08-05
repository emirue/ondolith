package app

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emirue/ondolith/internal/content"
)

// writePost logs in as email and writes one post, returning its id.
func writePost(t *testing.T, srv string, pool *pgxpool.Pool, c *http.Client, slug, title string) string {
	t.Helper()
	resp := httpPost(t, c, srv+"/board/"+slug+"/write", url.Values{
		"title": {title}, "body": {"본문"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("글쓰기 HTTP %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	return loc[strings.LastIndex(loc, "/")+1:]
}

// P5: 삭제는 POST 만. GET 으로 상태를 바꾸면 크롤러와 프리페치가 지운다.
// 라우트 등록 자체가 이것을 강제하므로 GET 은 아예 없다.
func TestDeleteIsPostOnly(t *testing.T) {
	srv, pool := liveSite(t)
	mkBoard(t, pool, "free", content.PresetPublic)
	mkUser(t, pool, "a@example.com")
	c := client()
	login(t, srv.URL, "a@example.com", c)
	id := writePost(t, srv.URL, pool, c, "free", "지울 글")

	code, _ := mustGet(t, c, srv.URL+"/board/free/"+id+"/delete")
	if code == http.StatusOK || code == http.StatusSeeOther {
		t.Errorf("GET 삭제가 HTTP %d 로 받아들여졌다", code)
	}
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM posts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("GET 으로 글이 지워졌다")
	}

	resp := httpPost(t, c, srv.URL+"/board/free/"+id+"/delete", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST 삭제 HTTP %d", resp.StatusCode)
	}
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM posts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("POST 삭제가 안 됐다")
	}
}

// 남의 글 삭제는 404 다. 403 이면 그 글이 있다는 사실과 내 것이 아니라는
// 사실 두 가지를 알려준다.
func TestDeletingSomebodyElsesPostIs404(t *testing.T) {
	srv, pool := liveSite(t)
	mkBoard(t, pool, "free", content.PresetPublic)
	mkUser(t, pool, "a@example.com")
	mkUser(t, pool, "b@example.com")

	author := client()
	login(t, srv.URL, "a@example.com", author)
	id := writePost(t, srv.URL, pool, author, "free", "내 글")

	other := client()
	login(t, srv.URL, "b@example.com", other)
	resp := httpPost(t, other, srv.URL+"/board/free/"+id+"/delete", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("남의 글 삭제가 HTTP %d", resp.StatusCode)
	}
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM posts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("남의 글이 지워졌다")
	}
}

// 댓글 작성 → 표시 → 수정 → 삭제가 HTTP 로 돈다.
func TestCommentLifecycle(t *testing.T) {
	srv, pool := liveSite(t)
	mkBoard(t, pool, "free", content.PresetPublic)
	mkUser(t, pool, "a@example.com")
	c := client()
	login(t, srv.URL, "a@example.com", c)
	postID := writePost(t, srv.URL, pool, c, "free", "글")

	resp := httpPost(t, c, srv.URL+"/board/free/"+postID+"/comments", url.Values{"body": {"첫 댓글"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("댓글 작성 HTTP %d", resp.StatusCode)
	}
	code, view := mustGet(t, c, srv.URL+"/board/free/"+postID)
	if code != http.StatusOK || !strings.Contains(view, "첫 댓글") {
		t.Fatalf("댓글이 상세에 없다: HTTP %d", code)
	}

	var commentID string
	if err := pool.QueryRow(context.Background(), `SELECT id FROM comments`).Scan(&commentID); err != nil {
		t.Fatal(err)
	}
	resp = httpPost(t, c, srv.URL+"/comments/"+commentID+"/edit", url.Values{"body": {"고친 댓글"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("댓글 수정 HTTP %d", resp.StatusCode)
	}
	if _, view = mustGet(t, c, srv.URL+"/board/free/"+postID); !strings.Contains(view, "고친 댓글") {
		t.Error("수정이 반영되지 않았다")
	}

	resp = httpPost(t, c, srv.URL+"/comments/"+commentID+"/delete", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("댓글 삭제 HTTP %d", resp.StatusCode)
	}
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM comments`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("자식 없는 댓글이 물리 삭제되지 않았다: %d개", n)
	}
}

// 남의 댓글은 404 다. 그리고 툼스톤은 수정할 수 없다 — 작성자가 이미 지운
// 본문을 되살리는 편집이다.
func TestCommentOwnershipAndTombstones(t *testing.T) {
	srv, pool := liveSite(t)
	mkBoard(t, pool, "free", content.PresetPublic)
	mkUser(t, pool, "a@example.com")
	mkUser(t, pool, "b@example.com")
	ctx := context.Background()

	author := client()
	login(t, srv.URL, "a@example.com", author)
	postID := writePost(t, srv.URL, pool, author, "free", "글")
	resp := httpPost(t, author, srv.URL+"/board/free/"+postID+"/comments", url.Values{"body": {"부모"}})
	resp.Body.Close()
	var parentID string
	if err := pool.QueryRow(ctx, `SELECT id FROM comments`).Scan(&parentID); err != nil {
		t.Fatal(err)
	}

	other := client()
	login(t, srv.URL, "b@example.com", other)
	for _, target := range []string{"/comments/" + parentID + "/edit", "/comments/" + parentID + "/delete"} {
		resp := httpPost(t, other, srv.URL+target, url.Values{"body": {"가로채기"}})
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s → HTTP %d, want 404", target, resp.StatusCode)
		}
	}
	if code, _ := mustGet(t, other, srv.URL+"/comments/"+parentID+"/edit"); code != http.StatusNotFound {
		t.Errorf("남의 댓글 수정 폼이 HTTP %d", code)
	}

	// 대댓글을 달면 부모 삭제는 툼스톤이 된다.
	resp = httpPost(t, other, srv.URL+"/board/free/"+postID+"/comments",
		url.Values{"body": {"대댓글"}, "parent_id": {parentID}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("대댓글 HTTP %d", resp.StatusCode)
	}
	resp = httpPost(t, author, srv.URL+"/comments/"+parentID+"/delete", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("부모 댓글 삭제 HTTP %d", resp.StatusCode)
	}
	var body string
	var deleted *string
	if err := pool.QueryRow(ctx,
		`SELECT body, deleted_at::text FROM comments WHERE id = $1`, parentID).Scan(&body, &deleted); err != nil {
		t.Fatal(err)
	}
	if deleted == nil || body != "" {
		t.Errorf("툼스톤이 아니다: body=%q deleted=%v", body, deleted)
	}
	// 툼스톤 수정은 404 — 작성자 본인이어도 되살릴 수 없다.
	resp = httpPost(t, author, srv.URL+"/comments/"+parentID+"/edit", url.Values{"body": {"되살리기"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("툼스톤이 HTTP %d 로 수정됐다", resp.StatusCode)
	}
	if err := pool.QueryRow(ctx, `SELECT body FROM comments WHERE id = $1`, parentID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		t.Errorf("삭제된 댓글의 본문이 되살아났다: %q", body)
	}
}

// allow_comments 가 꺼진 게시판에는 댓글이 등록되지 않는다. 폼을 안 그리는
// 것은 UX 고, POST 는 그래도 도착한다 (D15 4.3).
func TestCommentsRefusedWhenTheBoardTurnsThemOff(t *testing.T) {
	srv, pool := liveSite(t)
	boardID := mkBoard(t, pool, "free", content.PresetPublic)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE boards SET allow_comments = false WHERE id = $1`, boardID); err != nil {
		t.Fatal(err)
	}
	mkUser(t, pool, "a@example.com")
	c := client()
	login(t, srv.URL, "a@example.com", c)
	postID := writePost(t, srv.URL, pool, c, "free", "글")

	code, view := mustGet(t, c, srv.URL+"/board/free/"+postID)
	if code != http.StatusOK {
		t.Fatalf("상세 HTTP %d", code)
	}
	if strings.Contains(view, "/comments\"") {
		t.Error("댓글을 끈 게시판에 댓글 폼이 그려졌다")
	}
	resp := httpPost(t, c, srv.URL+"/board/free/"+postID+"/comments", url.Values{"body": {"댓글"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("댓글을 끈 게시판에 댓글이 HTTP %d 로 등록됐다", resp.StatusCode)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM comments`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("댓글 %d개가 등록됐다", n)
	}
}

// 대댓글의 부모는 그 글의 것이어야 한다. 아니면 다른 글의 스레드에 가지를
// 붙일 수 있다. 그리고 대댓글에 다시 답을 달 수는 없다 (D30: 1단계).
func TestReplyParentMustBelongToTheSamePostAndBeTopLevel(t *testing.T) {
	srv, pool := liveSite(t)
	mkBoard(t, pool, "free", content.PresetPublic)
	mkUser(t, pool, "a@example.com")
	ctx := context.Background()
	c := client()
	login(t, srv.URL, "a@example.com", c)

	postA := writePost(t, srv.URL, pool, c, "free", "글 A")
	postB := writePost(t, srv.URL, pool, c, "free", "글 B")
	resp := httpPost(t, c, srv.URL+"/board/free/"+postA+"/comments", url.Values{"body": {"A 의 댓글"}})
	resp.Body.Close()
	var parentID string
	if err := pool.QueryRow(ctx, `SELECT id FROM comments`).Scan(&parentID); err != nil {
		t.Fatal(err)
	}

	// 다른 글에 그 부모로 대댓글.
	resp = httpPost(t, c, srv.URL+"/board/free/"+postB+"/comments",
		url.Values{"body": {"가지치기"}, "parent_id": {parentID}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("다른 글의 댓글에 답이 달렸다: HTTP %d", resp.StatusCode)
	}

	// 정상 대댓글.
	resp = httpPost(t, c, srv.URL+"/board/free/"+postA+"/comments",
		url.Values{"body": {"대댓글"}, "parent_id": {parentID}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("정상 대댓글 HTTP %d", resp.StatusCode)
	}
	var replyID string
	if err := pool.QueryRow(ctx,
		`SELECT id FROM comments WHERE parent_id = $1`, parentID).Scan(&replyID); err != nil {
		t.Fatal(err)
	}
	// 대댓글에 다시 답: 그리는 화면이 없는 트리가 된다.
	resp = httpPost(t, c, srv.URL+"/board/free/"+postA+"/comments",
		url.Values{"body": {"3단계"}, "parent_id": {replyID}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("2단계를 넘는 대댓글이 HTTP %d 로 달렸다", resp.StatusCode)
	}
}

// 읽을 수 없는 게시판의 글에는 댓글도 못 단다 — 댓글이 존재 확인 수단이 된다.
func TestCannotCommentOnAPostYouCannotRead(t *testing.T) {
	srv, pool := liveSite(t)
	staffID := mkBoard(t, pool, "staff", content.PresetPrivate)
	mkUser(t, pool, "a@example.com")
	ctx := context.Background()
	postID, err := content.NewStore(pool).CreatePost(ctx, content.Post{
		BoardID: staffID, Title: "내부", Body: "기밀"})
	if err != nil {
		t.Fatal(err)
	}

	c := client()
	login(t, srv.URL, "a@example.com", c)
	resp := httpPost(t, c, srv.URL+"/board/staff/"+postID+"/comments", url.Values{"body": {"댓글"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("비공개 게시판 글에 댓글이 HTTP %d", resp.StatusCode)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM comments`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("댓글이 달렸다")
	}
}

// 게시판을 못 읽게 되면 그 글의 내 댓글도 못 만진다. 댓글 id 는 경로에
// 게시판이 없으므로, 여기서 검사하지 않으면 못 여는 게시판으로 들어가는
// 문이 된다.
func TestLosingBoardAccessClosesTheCommentScreens(t *testing.T) {
	srv, pool := liveSite(t)
	boardID := mkBoard(t, pool, "free", content.PresetPublic)
	mkUser(t, pool, "a@example.com")
	ctx := context.Background()
	c := client()
	login(t, srv.URL, "a@example.com", c)
	postID := writePost(t, srv.URL, pool, c, "free", "글")
	resp := httpPost(t, c, srv.URL+"/board/free/"+postID+"/comments", url.Values{"body": {"내 댓글"}})
	resp.Body.Close()
	var commentID string
	if err := pool.QueryRow(ctx, `SELECT id FROM comments`).Scan(&commentID); err != nil {
		t.Fatal(err)
	}
	// 아직은 열린다.
	if code, _ := mustGet(t, c, srv.URL+"/comments/"+commentID+"/edit"); code != http.StatusOK {
		t.Fatalf("자기 댓글 수정 폼이 HTTP %d", code)
	}

	// 게시판을 비공개로 돌린다 — 이 사용자의 읽기 부여를 없앤다.
	if _, err := pool.Exec(ctx,
		`DELETE FROM role_permissions rp USING roles r, permissions p
		 WHERE rp.role_id = r.id AND rp.permission_id = p.id AND rp.board_id = $1
		   AND r.key IN ('anonymous','member')`, boardID); err != nil {
		t.Fatal(err)
	}

	if code, _ := mustGet(t, c, srv.URL+"/comments/"+commentID+"/edit"); code != http.StatusNotFound {
		t.Errorf("못 읽는 게시판의 내 댓글 수정 폼이 HTTP %d", code)
	}
	resp = httpPost(t, c, srv.URL+"/comments/"+commentID+"/edit", url.Values{"body": {"고침"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("못 읽는 게시판의 내 댓글이 HTTP %d 로 수정됐다", resp.StatusCode)
	}
	resp = httpPost(t, c, srv.URL+"/comments/"+commentID+"/delete", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("못 읽는 게시판의 내 댓글이 HTTP %d 로 삭제됐다", resp.StatusCode)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM comments`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("댓글이 지워졌다")
	}
}
