package app

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// **긴 제목·본문·댓글은 500 이 아니라 422 다** (D19 P-205·P-208). 앱이 길이를 보지
// 않아 DB CHECK 위반이 「일시적인 오류」로 나갔고, 쓴 내용이 사라졌다. 글은 쓴 것을
// 그대로 되돌려 보인다.
func TestOverlongPostsAndCommentsAreRefusedNotServerErrors(t *testing.T) {
	srv, pool := liveSite(t)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO boards (slug, name, allow_comments) VALUES ('free', '자유', true)`,
		`INSERT INTO role_permissions (role_id, permission_id)
		 SELECT r.id, p.id FROM roles r, permissions p
		 WHERE r.key = 'member' AND p.key IN ('post.write', 'post.read', 'comment.write') ON CONFLICT DO NOTHING`,
		`INSERT INTO settings (key, value) VALUES ('auth.email_verification_required', '')
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	post := memberSession(t, srv, pool, "long@example.com")

	longTitle := strings.Repeat("가", 201)
	for name, form := range map[string]url.Values{
		"제목 201자":   {"title": {longTitle}, "body": {"본문"}},
		"본문 50001자": {"title": {"제목"}, "body": {strings.Repeat("가", 50001)}},
	} {
		resp := post("/board/free/write", form)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s = HTTP %d, want 422", name, resp.StatusCode)
		}
		if name == "제목 201자" && !strings.Contains(string(b), `value="`+longTitle+`"`) {
			t.Errorf("%s: 거절 화면이 쓴 제목을 되돌려 보이지 않는다", name)
		}
	}

	resp := post("/board/free/write", url.Values{"title": {"짧은 제목"}, "body": {"본문"}})
	resp.Body.Close()
	var postID string
	if err := pool.QueryRow(ctx, `SELECT id FROM posts`).Scan(&postID); err != nil {
		t.Fatalf("짧은 글이 저장되지 않았다: %v", err)
	}
	resp = post("/board/free/"+postID+"/comments", url.Values{"body": {strings.Repeat("가", 2001)}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("댓글 2001자 = HTTP %d, want 422", resp.StatusCode)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM comments`).Scan(&n); err != nil || n != 0 {
		t.Errorf("거절된 댓글 %d건 (%v)", n, err)
	}
}

// **답글을 달 수 있다** (FR-505 「댓글 (대댓글 포함)」). 서버는 parent_id 를 받고
// 들여쓰기 CSS 까지 있었지만, 화면에 「답글」이 없어 사용자가 답글을 달 길이 없었다.
// 「답글」은 같은 글의 댓글 폼을 그 댓글에 대한 답글로 연다 — 다른 글의 댓글을
// 가리키면 보통 댓글 폼이다.
func TestCommentsOfferReplies(t *testing.T) {
	srv, pool := liveSite(t)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO boards (slug, name, allow_comments) VALUES ('free', '자유', true)`,
		`INSERT INTO role_permissions (role_id, permission_id)
		 SELECT r.id, p.id FROM roles r, permissions p
		 WHERE r.key = 'member' AND p.key IN ('post.write', 'post.read', 'comment.write') ON CONFLICT DO NOTHING`,
		`INSERT INTO settings (key, value) VALUES ('auth.email_verification_required', '')
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	post := memberSession(t, srv, pool, "reply@example.com")
	for _, title := range []string{"첫 글", "둘째 글"} {
		resp := post("/board/free/write", url.Values{"title": {title}, "body": {"본문"}})
		resp.Body.Close()
	}
	var first, second string
	if err := pool.QueryRow(ctx, `SELECT id FROM posts WHERE title = '첫 글'`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM posts WHERE title = '둘째 글'`).Scan(&second); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{first, second} {
		resp := post("/board/free/"+p+"/comments", url.Values{"body": {"댓글"}})
		resp.Body.Close()
	}
	var comment, otherComment string
	if err := pool.QueryRow(ctx, `SELECT id FROM comments WHERE post_id = $1`, first).Scan(&comment); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM comments WHERE post_id = $1`, second).Scan(&otherComment); err != nil {
		t.Fatal(err)
	}

	reader := client() // 댓글을 쓸 수 있는 회원의 눈으로 본다
	login(t, srv.URL, "reply@example.com", reader)
	view := func(query string) string {
		resp, err := reader.Get(srv.URL + "/board/free/" + first + query)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	if page := view(""); !strings.Contains(page, `?reply=`+comment) {
		t.Error("댓글에 「답글」이 없다")
	}
	if page := view("?reply=" + comment); !strings.Contains(page, `name="parent_id" value="`+comment+`"`) {
		t.Error("「답글」로 연 폼이 그 댓글을 가리키지 않는다")
	}
	if page := view("?reply=" + otherComment); strings.Contains(page, `name="parent_id"`) {
		t.Error("다른 글의 댓글에 대한 답글 폼이 열렸다")
	}

	resp := post("/board/free/"+first+"/comments", url.Values{"body": {"답글"}, "parent_id": {comment}})
	resp.Body.Close()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM comments WHERE parent_id = $1`, comment).Scan(&n); err != nil || n != 1 {
		t.Errorf("답글 %d건 (%v), want 1", n, err)
	}
}
