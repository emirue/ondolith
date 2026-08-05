package app

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emirue/ondolith/internal/content"
)

// grantScoped gives one role a scoped permission on one board.
func grantScoped(t *testing.T, pool *pgxpool.Pool, role, perm, boardID string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO role_permissions (role_id, permission_id, board_id)
		SELECT r.id, p.id, $3 FROM roles r, permissions p
		WHERE r.key = $1 AND p.key = $2`, role, perm, boardID); err != nil {
		t.Fatal(err)
	}
}

// assignRole puts a user in a role.
func assignRole(t *testing.T, pool *pgxpool.Pool, userID, role string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE key = $2`,
		userID, role); err != nil {
		t.Fatal(err)
	}
}

// W2-21: 남의 글 삭제에 **그 게시판의** post.moderate 가 요구된다. 게시판 A 의
// 조정자가 게시판 B 의 글을 지울 수 없다 — 한 게시판의 관리자가 다음 게시판의
// 관리자는 아니다 (D15 2.4).
func TestPostModerationIsScopedToOneBoard(t *testing.T) {
	srv, pool := liveSite(t)
	ctx := context.Background()
	store := content.NewStore(pool)
	boardA := mkBoard(t, pool, "a", content.PresetPublic)
	boardB := mkBoard(t, pool, "b", content.PresetPublic)

	postA, err := store.CreatePost(ctx, content.Post{BoardID: boardA, Title: "A 의 글", Body: "본문"})
	if err != nil {
		t.Fatal(err)
	}
	postB, err := store.CreatePost(ctx, content.Post{BoardID: boardB, Title: "B 의 글", Body: "본문"})
	if err != nil {
		t.Fatal(err)
	}

	// 게시판 A 에만 조정 권한을 가진 운영자.
	uid := mkUser(t, pool, "mod@example.com")
	assignRole(t, pool, uid, "operator")
	// operator 는 시드에서 전역 post.moderate 를 갖고, 공개 프리셋도 게시판마다
	// 그것을 준다. 스코프를 시험하려면 둘 다 걷어내고 게시판 A 에만 남긴다 —
	// 전역 부여가 하나라도 남아 있으면 이 테스트는 아무것도 확인하지 않는다.
	if _, err := pool.Exec(ctx, `
		DELETE FROM role_permissions rp USING roles r, permissions p
		WHERE rp.role_id = r.id AND rp.permission_id = p.id
		  AND r.key = 'operator' AND p.key IN ('post.moderate','comment.moderate')`); err != nil {
		t.Fatal(err)
	}
	grantScoped(t, pool, "operator", "post.moderate", boardA)

	c := client()
	login(t, srv.URL, "mod@example.com", c)

	// 게시판 A 의 글은 지울 수 있다.
	resp := httpPost(t, c, srv.URL+"/admin/posts",
		url.Values{"post_id": {postA}, "action": {"delete"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("A 게시판 조정자가 A 의 글을 못 지운다: HTTP %d", resp.StatusCode)
	}

	// 게시판 B 의 글은 못 지운다.
	resp = httpPost(t, c, srv.URL+"/admin/posts",
		url.Values{"post_id": {postB}, "action": {"delete"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("A 게시판 조정자가 B 의 글을 HTTP %d 로 지웠다", resp.StatusCode)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM posts WHERE id = $1`, postB).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 1 {
		t.Error("다른 게시판의 글이 지워졌다")
	}
}

// 댓글도 같은 규칙이다.
func TestCommentModerationIsScopedToOneBoard(t *testing.T) {
	srv, pool := liveSite(t)
	ctx := context.Background()
	store := content.NewStore(pool)
	boardA := mkBoard(t, pool, "a", content.PresetPublic)
	boardB := mkBoard(t, pool, "b", content.PresetPublic)
	postA, _ := store.CreatePost(ctx, content.Post{BoardID: boardA, Title: "A", Body: "본문"})
	postB, _ := store.CreatePost(ctx, content.Post{BoardID: boardB, Title: "B", Body: "본문"})
	cA, err := store.CreateComment(ctx, content.Comment{PostID: postA, Body: "A 댓글"})
	if err != nil {
		t.Fatal(err)
	}
	cB, err := store.CreateComment(ctx, content.Comment{PostID: postB, Body: "B 댓글"})
	if err != nil {
		t.Fatal(err)
	}

	uid := mkUser(t, pool, "mod@example.com")
	assignRole(t, pool, uid, "operator")
	if _, err := pool.Exec(ctx, `
		DELETE FROM role_permissions rp USING roles r, permissions p
		WHERE rp.role_id = r.id AND rp.permission_id = p.id
		  AND r.key = 'operator' AND p.key = 'comment.moderate'`); err != nil {
		t.Fatal(err)
	}
	grantScoped(t, pool, "operator", "comment.moderate", boardA)

	c := client()
	login(t, srv.URL, "mod@example.com", c)

	resp := httpPost(t, c, srv.URL+"/admin/comments", url.Values{"comment_id": {cA}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("A 게시판 조정자가 A 의 댓글을 못 지운다: HTTP %d", resp.StatusCode)
	}
	resp = httpPost(t, c, srv.URL+"/admin/comments", url.Values{"comment_id": {cB}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("A 게시판 조정자가 B 의 댓글을 HTTP %d 로 지웠다", resp.StatusCode)
	}
}

// D15 7절: 남의 글·댓글 삭제는 작업 로그에 남는다. 분쟁의 근거다.
func TestModerationIsLogged(t *testing.T) {
	srv, pool := liveSite(t)
	ctx := context.Background()
	store := content.NewStore(pool)
	boardID := mkBoard(t, pool, "free", content.PresetPublic)
	postID, _ := store.CreatePost(ctx, content.Post{BoardID: boardID, Title: "글", Body: "본문"})
	commentID, _ := store.CreateComment(ctx, content.Comment{PostID: postID, Body: "댓글"})

	c, _ := adminSession(t, srv, pool)
	resp := httpPost(t, c, srv.URL+"/admin/comments", url.Values{"comment_id": {commentID}})
	resp.Body.Close()
	resp = httpPost(t, c, srv.URL+"/admin/posts", url.Values{"post_id": {postID}, "action": {"delete"}})
	resp.Body.Close()

	logs, err := store.OpLog().Recent(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sawPost, sawComment bool
	for _, e := range logs {
		switch e.Action {
		case "post.moderate":
			sawPost = true
			if e.TargetID != postID {
				t.Errorf("글 삭제 로그의 대상이 다르다: %s", e.TargetID)
			}
		case "comment.moderate":
			sawComment = true
		}
		if e.ActorEmail == "" {
			t.Error("주체 이메일 스냅샷이 비었다")
		}
	}
	if !sawPost || !sawComment {
		t.Errorf("삭제가 작업 로그에 없다 (글=%v 댓글=%v)", sawPost, sawComment)
	}
}

// D14 4.2: 부여 행이 없는 게시판은 아무에게도 안 보인다. A-304 가 그것을
// 표시하지 않으면 운영자는 평범해 보이는 행을 보고 다른 데서 버그를 찾는다.
func TestBoardListMarksUnreachableBoards(t *testing.T) {
	srv, pool := liveSite(t)
	ctx := context.Background()
	mkBoard(t, pool, "free", content.PresetPublic)
	// 부여 행 없이 직접 만든 게시판.
	if _, err := pool.Exec(ctx,
		`INSERT INTO boards (slug, name) VALUES ('orphan', '고아 게시판')`); err != nil {
		t.Fatal(err)
	}

	c, _ := adminSession(t, srv, pool)
	code, body := mustGet(t, c, srv.URL+"/admin/boards")
	if code != http.StatusOK {
		t.Fatalf("게시판 목록 HTTP %d", code)
	}
	if !strings.Contains(body, "고아 게시판") {
		t.Fatal("게시판 목록에 없다")
	}
	if !strings.Contains(body, "아무도 못 봄") {
		t.Error("부여 행이 없는 게시판이 표시되지 않았다")
	}
	// 정상 게시판에는 그 표시가 없어야 한다 — 전부 표시하면 표시가 무의미하다.
	before := strings.Index(body, "free 게시판")
	orphan := strings.Index(body, "고아 게시판")
	mark := strings.Index(body, "아무도 못 봄")
	if before < 0 || orphan < 0 || mark < 0 {
		t.Fatalf("행을 찾지 못했다")
	}
	if mark < orphan && mark > before {
		t.Error("정상 게시판에도 표시가 붙었다")
	}
}

// A-305: 게시판 생성 → 필드 추가 → 공개 폼 반영이 화면만으로 끝난다.
func TestBoardCreationToPublicFormIsScreensOnly(t *testing.T) {
	srv, pool := liveSite(t)
	_, post := adminSession(t, srv, pool)

	resp := post("/admin/boards/new", url.Values{
		"name": {"공지사항"}, "slug": {"notice"}, "preset": {string(content.PresetPublic)},
		"allow_comments": {"1"}, "per_page": {"20"},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("게시판 생성 HTTP %d", resp.StatusCode)
	}
	var boardID string
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM boards WHERE slug = 'notice'`).Scan(&boardID); err != nil {
		t.Fatal(err)
	}

	resp = post("/admin/boards/"+boardID+"/fields", url.Values{
		"key": {"color"}, "label": {"색상"}, "field_type": {"select"},
		"options": {"빨강\n파랑"}, "show_in_list": {"1"},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("필드 추가 HTTP %d", resp.StatusCode)
	}

	// 공개 쓰기 폼에 나타난다 — 코드 수정 없이.
	mkUser(t, pool, "m@example.com")
	visitor := client()
	login(t, srv.URL, "m@example.com", visitor)
	code, form := mustGet(t, visitor, srv.URL+"/board/notice/write")
	if code != http.StatusOK {
		t.Fatalf("공개 쓰기 폼 HTTP %d", code)
	}
	for _, want := range []string{`<select id="f-color"`, "색상", "빨강", "파랑"} {
		if !strings.Contains(form, want) {
			t.Errorf("공개 폼에 %q 가 없다", want)
		}
	}

	// 그리고 그 변경이 작업 로그에 남는다 (D15 7절: 공개 화면 동작이 바뀐다).
	logs, err := content.NewStore(pool).OpLog().Recent(context.Background(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sawBoard, sawField bool
	for _, e := range logs {
		if e.Action == "board.manage" && e.TargetType == "board" {
			sawBoard = true
		}
		if e.Action == "board.manage" && e.TargetType == "board_field" {
			sawField = true
		}
	}
	if !sawBoard || !sawField {
		t.Errorf("게시판·필드 변경이 로그에 없다 (게시판=%v 필드=%v)", sawBoard, sawField)
	}
}

// A-309: SC-7. 판정은 첨부 id 가 아니라 부모 글의 게시판에 건다 — id 는 그
// 파일이 어디 있는지 말하지 않는다 (D15 8절 1번과 같은 이유).
func TestAttachmentManagementIsScopedAndLogged(t *testing.T) {
	srv, pool, root := liveSiteWithUploads(t)
	ctx := context.Background()
	store := content.NewStore(pool)
	boardA := mkBoard(t, pool, "a", content.PresetPublic)
	boardB := mkBoard(t, pool, "b", content.PresetPublic)
	postA, _ := store.CreatePost(ctx, content.Post{BoardID: boardA, Title: "A", Body: "본문"})
	postB, _ := store.CreatePost(ctx, content.Post{BoardID: boardB, Title: "B", Body: "본문"})
	fileA := attach(t, pool, root, postA, "a.png")
	fileB := attach(t, pool, root, postB, "b.png")

	uid := mkUser(t, pool, "mod@example.com")
	assignRole(t, pool, uid, "operator")
	if _, err := pool.Exec(ctx, `
		DELETE FROM role_permissions rp USING roles r, permissions p
		WHERE rp.role_id = r.id AND rp.permission_id = p.id
		  AND r.key = 'operator' AND p.key = 'post.moderate'`); err != nil {
		t.Fatal(err)
	}
	grantScoped(t, pool, "operator", "post.moderate", boardA)

	c := client()
	login(t, srv.URL, "mod@example.com", c)

	// 목록도 게시판 단위다.
	if code, _ := mustGet(t, c, srv.URL+"/admin/attachments?board=a"); code != http.StatusOK {
		t.Errorf("A 게시판 첨부 목록 HTTP %d", code)
	}
	if code, _ := mustGet(t, c, srv.URL+"/admin/attachments?board=b"); code != http.StatusForbidden {
		t.Errorf("권한 없는 게시판의 첨부 목록이 HTTP %d", code)
	}

	// 삭제도 마찬가지.
	resp := httpPost(t, c, srv.URL+"/admin/attachments", url.Values{"attachment_id": {fileB}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("다른 게시판의 첨부가 HTTP %d 로 삭제됐다", resp.StatusCode)
	}
	resp = httpPost(t, c, srv.URL+"/admin/attachments", url.Values{"attachment_id": {fileA}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("A 게시판의 첨부 삭제 HTTP %d", resp.StatusCode)
	}

	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM attachments`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 1 {
		t.Errorf("첨부 %d행 — B 의 것만 남아야 한다", left)
	}

	// D15 7절: 남의 첨부 삭제도 로그에 남는다.
	logs, err := store.OpLog().Recent(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, e := range logs {
		if e.TargetType == "attachment" && e.TargetID == fileA {
			saw = true
		}
	}
	if !saw {
		t.Error("첨부 삭제가 작업 로그에 없다")
	}
}

// A-601: 작업 로그 화면. log.view 가 필요하고, 표는 읽기 전용이다.
func TestOpLogScreenNeedsLogViewAndShowsEntries(t *testing.T) {
	srv, pool := liveSite(t)
	ctx := context.Background()
	store := content.NewStore(pool)
	boardID := mkBoard(t, pool, "free", content.PresetPublic)
	postID, _ := store.CreatePost(ctx, content.Post{BoardID: boardID, Title: "글", Body: "본문"})

	// 조정으로 기록을 하나 만든다.
	admin, _ := adminSession(t, srv, pool)
	resp := httpPost(t, admin, srv.URL+"/admin/posts",
		url.Values{"post_id": {postID}, "action": {"delete"}})
	resp.Body.Close()

	code, body := mustGet(t, admin, srv.URL+"/admin/oplog")
	if code != http.StatusOK {
		t.Fatalf("작업 로그 HTTP %d", code)
	}
	if !strings.Contains(body, "post.moderate") {
		t.Errorf("기록이 화면에 없다: %.400s", body)
	}
	if !strings.Contains(body, "admin@example.com") {
		t.Error("주체 이메일이 화면에 없다")
	}

	// 한 쪽에 상한이 걸린다. 이 표는 영원히 늘어나므로 (D15 7절이 지우지
	// 않는다) 상한 없는 조회는 매일 느려지는 질의다.
	if _, err := pool.Exec(ctx, `
		INSERT INTO operation_logs (actor_email, action, target_type, summary)
		SELECT 'bulk@example.com', 'user.update', 'user', '대량 ' || g
		FROM generate_series(1, 120) g`); err != nil {
		t.Fatal(err)
	}
	_, page1 := mustGet(t, admin, srv.URL+"/admin/oplog")
	if n := strings.Count(page1, "user.update"); n > 100 {
		t.Errorf("한 쪽에 %d행 — 상한 100 이 안 걸렸다", n)
	}
	if n := strings.Count(page1, "user.update"); n < 90 {
		t.Errorf("한 쪽에 %d행 — 다른 이유로 잘렸다", n)
	}
	// 다음 쪽이 있고, 앞 쪽과 다른 행을 보여준다.
	if !strings.Contains(page1, "/admin/oplog?page=1") {
		t.Error("다음 쪽 링크가 없다")
	}
	_, page2 := mustGet(t, admin, srv.URL+"/admin/oplog?page=1")
	// 쪽 번호는 본문에 찍히므로 두 쪽은 언제나 다르게 보인다. 겹치는지는
	// **행 내용**으로 봐야 한다 — 같은 요약이 두 쪽에 다 있으면 오프셋이
	// 질의에 닿지 않은 것이다.
	onFirst := map[string]bool{}
	for i := 1; i <= 120; i++ {
		if strings.Contains(page1, "대량 "+strconv.Itoa(i)+"<") {
			onFirst[strconv.Itoa(i)] = true
		}
	}
	if len(onFirst) == 0 {
		t.Fatal("첫 쪽에서 행을 하나도 못 읽었다")
	}
	for id := range onFirst {
		if strings.Contains(page2, "대량 "+id+"<") {
			t.Errorf("행 '대량 %s' 가 두 쪽에 모두 있다 — 오프셋이 질의에 닿지 않았다", id)
			break
		}
	}

	// log.view 없는 사람은 못 본다. operator 는 갖고 있고 editor 는 없다.
	uid := mkUser(t, pool, "ed@example.com")
	assignRole(t, pool, uid, "editor")
	c := client()
	login(t, srv.URL, "ed@example.com", c)
	if code, _ := mustGet(t, c, srv.URL+"/admin/oplog"); code != http.StatusForbidden {
		t.Errorf("log.view 없이 작업 로그가 HTTP %d 로 열렸다", code)
	}
}
