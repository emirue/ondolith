package content

import (
	"context"
	"fmt"
	"net/url"
	"testing"
)

// 이 파일은 어떤 테스트도 실행한 적 없던 생성 질의를 실제 PostgreSQL 위에서
// 돌린다 (D22 7절). 기대값은 queries/post.sql 의 문장에서 읽은 것이다.

// 검색 합계는 **그 게시판의 공개 글 중 맞는 것**을 세고, 같은 질의의 목록과 같다.
func TestCountPostsWithSearchAgreesWithTheList(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	boardID := seedBoard(t, s)
	otherBoard := seedBoardSlug(t, s, "notice")

	mkPost(t, s, boardID, "게시판을 만든다") // 접두 일치: 저장된 토큰은 「게시판을」
	mkPost(t, s, boardID, "게시판 소개")
	mkPost(t, s, boardID, "다른 이야기")
	hidden := mkPost(t, s, boardID, "게시판 숨긴 글")
	if err := s.SetPostFlags(ctx, hidden, false, "hidden"); err != nil {
		t.Fatal(err)
	}
	mkPost(t, s, otherBoard, "게시판 공지")

	q := ParseListQuery(url.Values{"q": {"게시판"}}, 20)
	n, err := s.CountPosts(ctx, boardID, q)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("검색 합계 %d, want 2 — 숨긴 글·다른 게시판·맞지 않는 글은 세지 않는다", n)
	}
	list, err := s.ListPosts(ctx, boardID, q)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(list)) != n {
		t.Errorf("목록 %d행, 합계 %d — 페이저가 거짓말한다", len(list), n)
	}

	none, err := s.CountPosts(ctx, boardID, ParseListQuery(url.Values{"q": {"없는말"}}, 20))
	if err != nil {
		t.Fatal(err)
	}
	if none != 0 {
		t.Errorf("맞지 않는 검색어 합계 %d, want 0", none)
	}
}

// A-307 목록은 숨긴 글·비밀글까지, 한 게시판만, 고정글 먼저 그다음 최신순이다.
func TestModeratePostsSeesHiddenAndSecretInOneBoard(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	boardID := seedBoard(t, s)
	otherBoard := seedBoardSlug(t, s, "notice")

	pinned := mkPost(t, s, boardID, "고정") // 가장 오래됐지만 고정이라 맨 위
	hidden := mkPost(t, s, boardID, "숨김")
	secret, err := s.CreatePost(ctx, Post{BoardID: boardID, Title: "비밀", Body: "비밀 본문", IsSecret: true})
	if err != nil {
		t.Fatal(err)
	}
	newest := mkPost(t, s, boardID, "최신")
	mkPost(t, s, otherBoard, "남의 게시판")
	if err := s.SetPostFlags(ctx, pinned, true, "published"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPostFlags(ctx, hidden, false, "hidden"); err != nil {
		t.Fatal(err)
	}

	got, err := s.ModeratePosts(ctx, boardID, 10)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range got {
		ids = append(ids, p.ID)
		if p.BoardID != boardID {
			t.Errorf("다른 게시판의 글 %q 이 나왔다", p.Title)
		}
	}
	want := []string{pinned, newest, secret, hidden}
	if fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Fatalf("순서 %v, want %v (고정 → 최신 → 비밀 → 숨김)", ids, want)
	}
	if !got[0].IsPinned {
		t.Error("고정글의 IsPinned 가 꺼져 있다")
	}
	if !got[2].IsSecret {
		t.Error("비밀글의 IsSecret 이 꺼져 있다")
	}
	if got[3].Status != "hidden" {
		t.Errorf("숨긴 글 상태 %q, want hidden", got[3].Status)
	}

	two, err := s.ModeratePosts(ctx, boardID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(two) != 2 {
		t.Fatalf("limit 2 인데 %d행", len(two))
	}
	if two[0].ID != pinned || two[1].ID != newest {
		t.Errorf("limit 2 = [%s %s], want 앞의 둘", two[0].Title, two[1].Title)
	}
}

// A-308 목록은 묘비 댓글·숨긴 글의 댓글까지, 한 게시판만, 최신순이다.
func TestModerateCommentsSeesTombstonesInOneBoard(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	boardID := seedBoard(t, s)
	otherBoard := seedBoardSlug(t, s, "notice")
	post := mkPost(t, s, boardID, "글")
	hiddenPost := mkPost(t, s, boardID, "숨긴 글")
	if err := s.SetPostFlags(ctx, hiddenPost, false, "hidden"); err != nil {
		t.Fatal(err)
	}
	elsewhere := mkPost(t, s, otherBoard, "남의 글")

	comment := func(c Comment) string {
		t.Helper()
		id, err := s.CreateComment(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	parent := comment(Comment{PostID: post, Body: "부모"})
	reply := comment(Comment{PostID: post, ParentID: parent, Body: "답글"})
	onHidden := comment(Comment{PostID: hiddenPost, Body: "숨긴 글의 댓글"})
	comment(Comment{PostID: elsewhere, Body: "남의 게시판 댓글"})
	// 답글이 달린 댓글은 지워지지 않고 묘비가 된다.
	if err := s.DeleteComment(ctx, parent); err != nil {
		t.Fatal(err)
	}

	got, err := s.ModerateComments(ctx, boardID, 10)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range got {
		ids = append(ids, c.ID)
	}
	want := []string{onHidden, reply, parent}
	if fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Fatalf("순서 %v, want %v (최신순, 다른 게시판 제외)", ids, want)
	}
	if !got[2].IsTombstone() {
		t.Error("지운 부모 댓글이 묘비로 읽히지 않았다")
	}
	if got[2].Body != "" {
		t.Errorf("묘비 본문 %q, want 빈 문자열", got[2].Body)
	}
	if got[1].IsTombstone() {
		t.Error("살아 있는 답글이 묘비로 읽혔다")
	}
	if got[1].ParentID != parent {
		t.Errorf("답글의 부모 %q, want %q", got[1].ParentID, parent)
	}

	two, err := s.ModerateComments(ctx, boardID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(two) != 2 {
		t.Fatalf("limit 2 인데 %d행", len(two))
	}
	if two[0].ID != onHidden || two[1].ID != reply {
		t.Errorf("limit 2 = [%q %q], want 최신 둘", two[0].Body, two[1].Body)
	}
}
