package content

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/content/contentq"
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
	posts, err := s.ListPosts(ctx, boardID, q)
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

// FR-512 / W2-24: 비밀글은 **목록에 제목이 나오고 본문은 404** 다.
//
// 목록에서까지 숨기면 비밀글이 존재하는 이유(자기 질문이 접수됐는지 보는 것)를
// 없앤다. 지키는 것은 본문이고, 그것은 PostByID 가 한다.
func TestSecretPostsAreListedButTheirBodyIsNot(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	boardID := seedBoard(t, s)

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

	secret, err := s.CreatePost(ctx, Post{BoardID: boardID, AuthorID: authorID,
		Title: "비밀 제목", Body: "비밀 본문", IsSecret: true})
	if err != nil {
		t.Fatal(err)
	}
	mkPost(t, s, boardID, "공개글")

	q := ParseListQuery(url.Values{}, 20)
	got, err := s.ListPosts(ctx, boardID, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("%d행, want 2행 — 비밀글도 목록에 나온다", len(got))
	}
	n, err := s.CountPosts(ctx, boardID, q)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("합계 %d, want 2 — 목록과 합계가 다르면 페이저가 거짓말한다", n)
	}
	var marked bool
	for _, p := range got {
		if p.ID == secret {
			marked = p.IsSecret
		}
	}
	if !marked {
		t.Error("비밀글이 목록에서 비밀글로 표시되지 않는다")
	}

	// 본문은 남에게 404 다.
	if _, err := s.PostByID(ctx, secret, otherID, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("남이 비밀글 본문을 읽었다: %v", err)
	}
	if _, err := s.PostByID(ctx, secret, authorID, false); err != nil {
		t.Errorf("작성자가 자기 비밀글을 못 읽는다: %v", err)
	}
	if _, err := s.PostByID(ctx, secret, otherID, true); err != nil {
		t.Errorf("post.read_secret 보유자가 못 읽는다: %v", err)
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
		if _, err := s.ListPosts(ctx, boardID, q); err != nil {
			t.Errorf("검색어 %q 가 오류를 냈다: %v", term, err)
		}
	}
	// ...그리고 실제로 찾는다.
	q := ParseListQuery(url.Values{"q": {"게시판"}}, 20)
	got, err := s.ListPosts(ctx, boardID, q)
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
	got, err := s.ListPosts(ctx, boardID, ParseListQuery(url.Values{}, 20))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("숨긴 글이 목록에 있다: %d행", len(got))
	}
}

// 툼스톤은 저장소에서도 못 고친다. 핸들러가 유일한 방어면 다음 호출자가
// 뚫는다 — 작성자가 이미 지운 본문을 되살리는 것은 어떤 경로로도 안 된다.
func TestUpdateCommentRefusesATombstone(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	boardID := seedBoard(t, s)
	postID := mkPost(t, s, boardID, "글")

	parent, err := s.CreateComment(ctx, Comment{PostID: postID, Body: "부모"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateComment(ctx, Comment{PostID: postID, ParentID: parent, Body: "자식"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteComment(ctx, parent); err != nil {
		t.Fatal(err)
	}

	if err := s.UpdateComment(ctx, parent, "되살리기"); !errors.Is(err, ErrNotFound) {
		t.Errorf("툼스톤이 수정됐다: %v", err)
	}
	got, err := s.CommentByID(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != "" {
		t.Errorf("본문이 되살아났다: %q", got.Body)
	}
}

// 빈 검색어와 빈 게시판 목록은 아무것도 돌려주지 않는다.
//
// 저장소의 이른 반환을 지워도 이 테스트는 통과한다 — 빈 tsquery 와 `ANY('{}')`
// 가 어차피 0행이기 때문이다. 그래서 그 두 줄은 검사가 아니라 왕복 절약이라고
// 코드에 적어 뒀고, 이 테스트가 지키는 것은 **SQL 이 그렇게 동작한다**는
// 사실이다. PostgreSQL 이 빈 tsquery 를 다르게 다루기 시작하면 여기서 터진다.
func TestSearchWithNoTermReturnsNothing(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	boardID := seedBoard(t, s)
	mkPost(t, s, boardID, "어떤 글")

	q := ParseListQuery(url.Values{}, 20)
	got, err := s.SearchPosts(ctx, []string{boardID}, nil, q, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("빈 검색어가 %d행을 돌려줬다", len(got))
	}
	n, err := s.CountSearchPosts(ctx, []string{boardID}, nil, q, "")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("빈 검색어 합계 = %d", n)
	}

	// 읽을 수 있는 게시판이 없어도 마찬가지다.
	q2 := ParseListQuery(url.Values{"q": {"어떤"}}, 20)
	if got, err := s.SearchPosts(ctx, nil, nil, q2, "", 0); err != nil || len(got) != 0 {
		t.Errorf("읽을 게시판이 없는데 %d행 (err=%v)", len(got), err)
	}
}

// **댓글 스레드는 먼저 단 것이 먼저다** (D12 P-204). 앞 판은 스레드를
// `coalesce(parent_id, id)` — 무작위 uuid — 로 묶어 정렬해서, 스레드 순서가 시간과
// 무관했다: 어제 단 댓글이 오늘 단 댓글 뒤로 갈 수 있었다. uuid 순서가 시간과
// 거꾸로인 행을 일부러 넣어 본다.
func TestCommentThreadsAreChronological(t *testing.T) {
	s, pool := testStore(t)
	boardID := seedBoard(t, s)
	postID := mkPost(t, s, boardID, "글")
	ctx := context.Background()
	for _, c := range []struct{ id, parent, at string }{
		{"ffffffff-0000-4000-8000-000000000001", "", "2026-10-01 09:00"},                                     // 첫 댓글
		{"00000000-0000-4000-8000-000000000002", "", "2026-10-01 10:00"},                                     // 둘째 댓글
		{"88888888-0000-4000-8000-000000000003", "ffffffff-0000-4000-8000-000000000001", "2026-10-01 11:00"}, // 첫 댓글의 답
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO comments (id, post_id, parent_id, body, created_at)
			VALUES ($1, $2, NULLIF($3, '')::uuid, '본문', $4::timestamptz)`, c.id, postID, c.parent, c.at); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Comments(ctx, postID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range got {
		ids = append(ids, c.ID[:8])
	}
	if want := []string{"ffffffff", "88888888", "00000000"}; strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Errorf("댓글 순서 %v, want %v — 첫 댓글, 그 답, 둘째 댓글", ids, want)
	}
}

// 「전체 N건」은 기억해 두고, 이 저장소를 지나는 쓰기마다 다시 센다. 기억이 실제로 쓰이는지는
// 저장소를 거치지 않은 쓰기(psql 같은)가 바로 보이지 않는 것으로 안다 — 그래야 매 요청 세지
// 않는다는 뜻이다. 거치는 쓰기(쓰기·숨김·삭제) 뒤에는 바로 맞는 수다.
func TestBoardCountIsRememberedAndRefreshedByWrites(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	boardID := seedBoard(t, s)
	q := ParseListQuery(url.Values{}, 20)
	count := func() int64 {
		t.Helper()
		n, err := s.CountPosts(ctx, boardID, q)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	first := mkPost(t, s, boardID, "첫 글")
	if n := count(); n != 1 {
		t.Fatalf("첫 수 = %d, want 1", n)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO posts (board_id, title, body) VALUES ($1, '밖에서', '본문')`, boardID); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Errorf("저장소 밖 쓰기 뒤 = %d, want 1 — 기억을 쓰지 않고 매번 센다", n)
	}
	second := mkPost(t, s, boardID, "둘째 글")
	if n := count(); n != 3 {
		t.Errorf("글을 쓴 뒤 = %d, want 3", n)
	}
	if err := s.SetPostFlags(ctx, first, false, "hidden"); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 2 {
		t.Errorf("숨긴 뒤 = %d, want 2", n)
	}
	if err := s.DeletePost(ctx, second); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Errorf("발행 글을 지운 뒤 = %d, want 1", n)
	}
}

// **검색의 두 모양은 같은 결과를 낸다** — 맞는 글이 적거나 쪽이 깊을 때 모으는 모양(…Few), 흔한
// 낱말에서 걷는 모양. 어느 쪽을 고를지는 개수와 쪽 깊이만 정하므로, 정렬·고정 글·비밀글·쪽이
// 모양에 따라 달라지면 같은 검색이 글 수에 따라 다른 쪽을 보인다. 두 질의를 직접 부른다 —
// 고르는 문턱이 바뀌어도 비교가 「걷기 대 걷기」로 헛돌지 않게.
func TestSearchShapesAgree(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	boardID := seedBoard(t, s)
	var authorID string
	if err := s.pool.QueryRow(ctx, `INSERT INTO users (email, password_hash, display_name)
		VALUES ('w@example.com','h','작성자') RETURNING id`).Scan(&authorID); err != nil {
		t.Fatal(err)
	}
	for i := range 30 {
		id := mkPost(t, s, boardID, fmt.Sprintf("공지 %02d", i))
		var author any
		if i%3 == 0 {
			author = authorID // 이 작성자의 비밀글은 그에게만 검색된다
		}
		if _, err := s.pool.Exec(ctx, `UPDATE posts SET view_count = $2, is_pinned = $3, is_secret = $4,
			created_at = now() - $5 * interval '1 minute', author_id = $6 WHERE id = $1`,
			id, (i*7)%11, i%9 == 0, i%5 == 0, i, author); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(ps []Post, err error) []string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, p := range ps {
			out = append(out, p.ID)
		}
		return out
	}
	for _, who := range []struct {
		name      string
		viewer    string
		canSecret bool
	}{{"익명", "", false}, {"작성자", authorID, false}, {"비밀글 권한", "", true}} {
		for _, v := range []url.Values{
			{"q": {"공지"}},
			{"q": {"공지"}, "sort": {"views"}, "page": {"2"}, "per_page": {"7"}},
			{"q": {"공지"}, "sort": {"title"}, "per_page": {"10"}},
			{"q": {"공지"}, "sort": {"-title"}, "page": {"3"}, "per_page": {"10"}},
		} {
			q := ParseListQuery(v, 20)
			q.ViewerID, q.CanSecret = who.viewer, who.canSecret
			p := contentq.ListPostsSearchParams{BoardID: boardID, CanSecret: q.CanSecret, ViewerID: strPtr(q.ViewerID),
				Search: toPrefixQuery(q.Search), Sort: q.sortKey(), Desc: q.Desc,
				Limit: int32(q.PerPage), Offset: int32(q.Offset())}
			walk := ids(postsOf(s.q.ListPostsSearch(ctx, p)))
			few := ids(postsOf(s.q.ListPostsSearchFew(ctx, contentq.ListPostsSearchFewParams{
				BoardID: p.BoardID, CanSecret: p.CanSecret, ViewerID: p.ViewerID, Search: p.Search,
				Sort: p.Sort, Desc: p.Desc, Limit: p.Limit, Offset: p.Offset})))
			if !reflect.DeepEqual(walk, few) || len(walk) == 0 {
				t.Errorf("게시판 %s %v: 걷기 %v ≠ 모으기 %v", who.name, v, walk, few)
			}
			var secretIn []string
			if who.canSecret {
				secretIn = []string{boardID}
			}
			sp := contentq.SearchPostsParams{Readable: []string{boardID}, SecretIn: secretIn, ViewerID: strPtr(who.viewer),
				Search: toPrefixQuery(q.Search), Sort: q.sortKey(), Desc: q.Desc,
				Limit: int32(q.PerPage), Offset: int32(q.Offset())}
			sw := ids(postsOf(s.q.SearchPosts(ctx, sp)))
			sf := ids(postsOf(s.q.SearchPostsFew(ctx, contentq.SearchPostsFewParams{
				Readable: sp.Readable, SecretIn: sp.SecretIn, ViewerID: sp.ViewerID, Search: sp.Search,
				Sort: sp.Sort, Desc: sp.Desc, Limit: sp.Limit, Offset: sp.Offset})))
			if !reflect.DeepEqual(sw, sf) || len(sw) == 0 {
				t.Errorf("통합 %s %v: 걷기 %v ≠ 모으기 %v", who.name, v, sw, sf)
			}
		}
	}
}

// 검색 개수는 상한까지만 센다 — 넘으면 상한+1 을 돌려줘 화면이 「이상」을 안다.
func TestSearchCountStopsAtTheCap(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	boardID := seedBoard(t, s)
	if _, err := s.pool.Exec(ctx, `INSERT INTO posts (board_id, title, body)
		SELECT $1, '흔한말 ' || g, '본문' FROM generate_series(1, $2::int) g`, boardID, SearchCountCap+5); err != nil {
		t.Fatal(err)
	}
	q := ParseListQuery(url.Values{"q": {"흔한말"}}, 20)
	if n, err := s.CountPosts(ctx, boardID, q); err != nil || n != SearchCountCap+1 {
		t.Errorf("게시판 검색 개수 = %d (%v), want %d", n, err, SearchCountCap+1)
	}
	if n, err := s.CountSearchPosts(ctx, []string{boardID}, nil, q, ""); err != nil || n != SearchCountCap+1 {
		t.Errorf("통합 검색 개수 = %d (%v), want %d", n, err, SearchCountCap+1)
	}
}
