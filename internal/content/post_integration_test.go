package content

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"
)

func seedBoard(t *testing.T, s *Store) string {
	t.Helper()
	id, err := s.CreateBoard(context.Background(), newBoard("free", "자유"), PresetPublic)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mkPost(t *testing.T, s *Store, boardID, title string) string {
	t.Helper()
	id, err := s.CreatePost(context.Background(), Post{
		BoardID: boardID, Title: title, Body: "본문 " + title})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// NFR-105: 목록 1페이지는 **상수 개수 쿼리**다. 댓글 수·첨부 유무를 행마다
// 따로 물으면 20행짜리 페이지가 41번 쿼리한다 — 게시판이 커지고 나서야
// 드러나고, 그때는 아무도 그 화면을 테스트하고 있지 않다.
//
// 쿼리 수를 **세지 않으면** 이 요구사항은 검증되지 않는다. "결과가 맞다"는
// N+1 에서도 참이다.
func TestListIsAConstantNumberOfQueries(t *testing.T) {
	base, _ := testStore(t)
	boardID := seedBoard(t, base)
	ctx := context.Background()

	s, tr := tracedStore(t)
	for i := range 25 {
		pid := mkPost(t, base, boardID, fmt.Sprintf("글 %d", i))
		// 절반에는 댓글과 첨부를 달아 서브쿼리가 실제로 값을 만들게 한다.
		if i%2 == 0 {
			if _, err := base.CreateComment(ctx, Comment{PostID: pid, Body: "댓글"}); err != nil {
				t.Fatal(err)
			}
			if _, err := base.pool.Exec(ctx, `
				INSERT INTO attachments (post_id, stored_path, original_name, mime_type, byte_size)
				VALUES ($1, $2, 'a.png', 'image/png', 10)`,
				pid, fmt.Sprintf("2026/08/0189a1b2-c3d4-5e6f-7081-%012d", i)); err != nil {
				t.Fatal(err)
			}
		}
	}

	q := ParseListQuery(url.Values{}, 20)
	tr.n.Store(0)
	posts, err := s.ListPosts(ctx, boardID, q, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if n := tr.n.Load(); n != 1 {
		t.Errorf("목록 1페이지에 쿼리 %d회 — 상수여야 한다 (NFR-105)", n)
	}
	if len(posts) != 20 {
		t.Fatalf("%d행, want 20행", len(posts))
	}
	// 서브쿼리가 실제로 값을 채웠는지 본다. 0 만 나오면 쿼리 1회는 맞지만
	// 아무것도 세지 않은 것이다.
	var withComments, withAttachments int
	for _, p := range posts {
		if p.CommentCount > 0 {
			withComments++
		}
		if p.HasAttachment {
			withAttachments++
		}
	}
	if withComments == 0 || withAttachments == 0 {
		t.Errorf("댓글 수·첨부 유무가 채워지지 않았다 (댓글 %d, 첨부 %d)", withComments, withAttachments)
	}
}

// 비밀글은 SQL 에서 걸러진다. 가져온 뒤 Go 에서 거르면 그 행은 이미 DB 를
// 떠났고, 다음 호출자가 그 검사를 빠뜨린다 (SC-1 4항).
func TestSecretPostsAreFilteredInSQL(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	boardID := seedBoard(t, s)

	author, err := s.pool.Exec(ctx, `SELECT 1`)
	_ = author
	if err != nil {
		t.Fatal(err)
	}
	var authorID, otherID string
	if err := s.pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, display_name)
		 VALUES ('a@example.com','h','작성자') RETURNING id`).Scan(&authorID); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, display_name)
		 VALUES ('b@example.com','h','남') RETURNING id`).Scan(&otherID); err != nil {
		t.Fatal(err)
	}

	if _, err := s.CreatePost(ctx, Post{BoardID: boardID, AuthorID: authorID,
		Title: "비밀글", Body: "본문", IsSecret: true}); err != nil {
		t.Fatal(err)
	}
	mkPost(t, s, boardID, "공개글")

	q := ParseListQuery(url.Values{}, 20)
	cases := map[string]struct {
		viewer    string
		canSecret bool
		want      int
	}{
		"익명":               {"", false, 1},
		"남":                {otherID, false, 1},
		"작성자 본인":           {authorID, false, 2},
		"post.read_secret": {otherID, true, 2},
	}
	for name, tc := range cases {
		got, err := s.ListPosts(ctx, boardID, q, tc.viewer, tc.canSecret)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(got) != tc.want {
			t.Errorf("%s: %d행, want %d행", name, len(got), tc.want)
		}
		n, err := s.CountPosts(ctx, boardID, q, tc.viewer, tc.canSecret)
		if err != nil {
			t.Fatal(err)
		}
		if n != int64(tc.want) {
			t.Errorf("%s: count %d, want %d — 목록과 합계가 다르면 페이저가 거짓말한다", name, n, tc.want)
		}
	}
}

// 상세 조회도 같은 규칙이다. 목록에서 숨기고 상세에서 보여주면 id 만 알면 된다.
func TestSecretPostDetailIsRefusedToOthers(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	boardID := seedBoard(t, s)
	var authorID string
	if err := s.pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, display_name)
		 VALUES ('a@example.com','h','작성자') RETURNING id`).Scan(&authorID); err != nil {
		t.Fatal(err)
	}
	id, err := s.CreatePost(ctx, Post{BoardID: boardID, AuthorID: authorID,
		Title: "비밀글", Body: "본문", IsSecret: true})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.PostByID(ctx, id, "", false); !errors.Is(err, ErrNotFound) {
		t.Errorf("익명이 비밀글 상세를 봤다: %v", err)
	}
	if _, err := s.PostByID(ctx, id, authorID, false); err != nil {
		t.Errorf("작성자가 자기 비밀글을 못 본다: %v", err)
	}
	if _, err := s.PostByID(ctx, id, "", true); err != nil {
		t.Errorf("post.read_secret 보유자가 못 본다: %v", err)
	}
}

// 검색어는 tsquery 문법이 아니라 낱말로만 들어간다. `&`·`!`·`:` 를 그대로
// 넘기면 방문자가 질의를 조립하고, 문법 오류는 500 이 된다.
func TestSearchTermsCannotComposeTsquery(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	boardID := seedBoard(t, s)
	// 본문에 조사를 붙인다. 제목이 `게시판` 이면 토큰이 정확히 일치해서
	// 접두 질의가 없어도 찾힌다 — 그러면 `:*` 를 지우는 변이가 안 잡힌다.
	if _, err := s.CreatePost(ctx, Post{BoardID: boardID,
		Title: "공지", Body: "게시판을 새로 열었습니다"}); err != nil {
		t.Fatal(err)
	}

	for _, term := range []string{
		"게시판", "게시판 안내", "게시판 & 안내", "게시판 | !안내", "'; DROP TABLE posts --",
		"(((", ":*", "게시판:*:*", "!!!",
	} {
		q := ParseListQuery(url.Values{"q": {term}}, 20)
		if _, err := s.ListPosts(ctx, boardID, q, "", false); err != nil {
			t.Errorf("검색어 %q 가 오류를 냈다: %v", term, err)
		}
	}
	// ...그리고 실제로 찾는다.
	q := ParseListQuery(url.Values{"q": {"게시판"}}, 20)
	got, err := s.ListPosts(ctx, boardID, q, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("접두 검색이 %d행 — 조사 때문에 못 찾았다", len(got))
	}
}

// D30: 자식이 있는 댓글은 물리 삭제되지 않는다. FK 가 거부하고, 그래서
// 툼스톤이 설계가 아니라 결과다. 본문은 DB 에서 비운다 — 테마는 제3자가
// 작성하고 `if` 를 빠뜨린다.
func TestCommentDeletionIsTombstoneOrPhysical(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	boardID := seedBoard(t, s)
	postID := mkPost(t, s, boardID, "글")

	parent, err := s.CreateComment(ctx, Comment{PostID: postID, Body: "부모 댓글"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateComment(ctx, Comment{PostID: postID, ParentID: parent, Body: "자식 댓글"})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteComment(ctx, parent); err != nil {
		t.Fatal(err)
	}
	got, err := s.Comments(ctx, postID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("댓글 %d개, want 2개 (부모는 툼스톤으로 남아야 한다)", len(got))
	}
	for _, c := range got {
		if c.ID != parent {
			continue
		}
		if !c.IsTombstone() {
			t.Error("자식이 있는 댓글이 툼스톤이 아니다")
		}
		if c.Body != "" {
			t.Errorf("툼스톤에 본문이 남았다: %q — 테마의 if 에 기대게 된다", c.Body)
		}
	}

	// 자식은 자식이 없으므로 물리 삭제.
	if err := s.DeleteComment(ctx, child); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Comments(ctx, postID)
	if len(got) != 1 {
		t.Errorf("자식 없는 댓글이 물리 삭제되지 않았다: %d개", len(got))
	}
}

// 댓글도 한 번의 쿼리로 읽는다. 대댓글마다 조회하면 P-204 가 댓글 수만큼
// 쿼리한다.
func TestCommentsAreOneQuery(t *testing.T) {
	base, _ := testStore(t)
	boardID := seedBoard(t, base)
	postID := mkPost(t, base, boardID, "글")
	ctx := context.Background()
	// 부모를 전부 먼저 만들고 대댓글을 나중에 단다. 번갈아 만들면 생성 순서와
	// 트리 순서가 우연히 같아져서, 정렬을 생성순으로 바꾸는 변이가 안 잡힌다.
	var parents []string
	for i := range 10 {
		id, err := base.CreateComment(ctx, Comment{PostID: postID, Body: fmt.Sprintf("댓글 %d", i)})
		if err != nil {
			t.Fatal(err)
		}
		parents = append(parents, id)
	}
	for _, parent := range parents {
		if _, err := base.CreateComment(ctx, Comment{
			PostID: postID, ParentID: parent, Body: "대댓글"}); err != nil {
			t.Fatal(err)
		}
	}

	s, tr := tracedStore(t)
	tr.n.Store(0)
	got, err := s.Comments(ctx, postID)
	if err != nil {
		t.Fatal(err)
	}
	if n := tr.n.Load(); n != 1 {
		t.Errorf("댓글 조회에 쿼리 %d회", n)
	}
	if len(got) != 20 {
		t.Errorf("%d개, want 20개", len(got))
	}
	// 대댓글이 부모 바로 뒤에 온다 — 트리 조립이 메모리에서 한 번에 끝난다.
	for i := 0; i < len(got); i += 2 {
		if got[i].ParentID != "" {
			t.Errorf("%d번째가 대댓글이다 — 정렬이 트리 순서가 아니다", i)
			break
		}
		if got[i+1].ParentID != got[i].ID {
			t.Errorf("%d번째 대댓글의 부모가 앞 댓글이 아니다", i+1)
			break
		}
	}
}

// 수정은 고정·상태를 건드리지 않는다. 건드리면 작성자가 조정자의 권한을 갖는다.
func TestUpdatePostDoesNotCarryModeratorFlags(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	boardID := seedBoard(t, s)
	id := mkPost(t, s, boardID, "글")

	if err := s.SetPostFlags(ctx, id, true, "hidden"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePost(ctx, id, Post{Title: "고친 제목", Body: "고친 본문"}); err != nil {
		t.Fatal(err)
	}
	var pinned bool
	var status string
	if err := s.pool.QueryRow(ctx,
		`SELECT is_pinned, status FROM posts WHERE id = $1`, id).Scan(&pinned, &status); err != nil {
		t.Fatal(err)
	}
	if !pinned || status != "hidden" {
		t.Errorf("수정이 조정자 플래그를 덮었다: pinned=%v status=%s", pinned, status)
	}
}

// 숨긴 글은 목록에 없다.
func TestHiddenPostsAreNotListed(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	boardID := seedBoard(t, s)
	id := mkPost(t, s, boardID, "글")
	mkPost(t, s, boardID, "보이는 글")

	if err := s.SetPostFlags(ctx, id, false, "hidden"); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListPosts(ctx, boardID, ParseListQuery(url.Values{}, 20), "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("숨긴 글이 목록에 있다: %d행", len(got))
	}
}
