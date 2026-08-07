package content

import (
	"context"
	"testing"
)

// mkUser 는 작성자 예외를 시험할 사용자를 만든다.
func mkUser(t *testing.T, s *Store, email string) string {
	t.Helper()
	var id string
	if err := s.pool.QueryRow(context.Background(),
		`INSERT INTO users (email, password_hash, display_name)
		 VALUES ($1,'h','사람') RETURNING id`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// titles 는 결과를 제목 집합으로 만든다. 순서가 아니라 **무엇이 들어왔는가**가
// 이 검사들의 내용이다.
func titles(posts []Post) map[string]bool {
	out := map[string]bool{}
	for _, p := range posts {
		out[p.Title] = true
	}
	return out
}

// **홈(P-201)의 최근 글은 읽을 수 있는 게시판의 것만 온다.**
//
// 이 함수는 여러 게시판을 가로지르고, 그 결과가 **인증 없이 열리는 `GET /`**
// 에 그대로 실린다 — 게시판 하나를 훑는 ListPosts 와 달리 「어느 게시판인가」를
// 스스로 정하지 않으므로, 호출자가 준 목록이 유일한 울타리다. 그 울타리가
// 새면 비공개 게시판 글이 사이트 첫 화면에 뜬다 (D12 P-201).
func TestRecentPostsStayInsideReadableBoards(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()

	open := seedBoard(t, s)
	closed, err := s.CreateBoard(ctx, newBoard("staff", "직원"), PresetPrivate)
	if err != nil {
		t.Fatal(err)
	}
	mkPost(t, s, open, "공개 게시판 글")
	mkPost(t, s, closed, "비공개 게시판 글")

	got, err := s.RecentPosts(ctx, []string{open}, nil, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	have := titles(got)
	if !have["공개 게시판 글"] {
		t.Error("읽을 수 있는 게시판의 글이 빠졌다")
	}
	if have["비공개 게시판 글"] {
		t.Error("읽을 수 없는 게시판의 글이 홈에 실렸다")
	}

	// **읽을 수 있는 게시판이 없으면 빈 목록이다.** 「전부」로 읽으면 익명
	// 방문자의 첫 화면이 전체 게시판의 글이 된다 — 빈 목록과 전체는 정반대다.
	if n, err := s.RecentPosts(ctx, nil, nil, "", 10); err != nil || len(n) != 0 {
		t.Errorf("읽을 수 있는 게시판이 없는데 %d행 (err=%v)", len(n), err)
	}
}

// **비밀글은 권한이 있거나 자기 글일 때만 온다** (SC-1 4항).
//
// 세 갈래를 전부 본다. 하나라도 빠지면 "안 보인다" 가 「권한이 막았다」인지
// 「아무것도 안 나온다」인지 구별되지 않는다.
func TestRecentPostsHideSecretsUnlessAllowed(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	board := seedBoard(t, s)
	author := mkUser(t, s, "author@example.com")
	other := mkUser(t, s, "other@example.com")

	if _, err := s.CreatePost(ctx, Post{BoardID: board, AuthorID: author,
		Title: "비밀글", Body: "본문", IsSecret: true}); err != nil {
		t.Fatal(err)
	}
	mkPost(t, s, board, "공개글")

	for _, tc := range []struct {
		name     string
		secretIn []string
		viewer   string
		want     bool
	}{
		{"남이 본다", nil, other, false},
		{"익명이 본다", nil, "", false},
		{"작성자가 본다", nil, author, true},
		{"읽기 권한이 있다", []string{board}, other, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.RecentPosts(ctx, []string{board}, tc.secretIn, tc.viewer, 10)
			if err != nil {
				t.Fatal(err)
			}
			have := titles(got)
			// **공개글은 언제나 와야 한다.** 이것이 없으면 아래 단언은
			// 「조회 자체가 0행」일 때도 통과한다.
			if !have["공개글"] {
				t.Fatal("공개글이 없다 — 이 경우 비밀글 단언은 아무것도 검사하지 않는다")
			}
			if have["비밀글"] != tc.want {
				t.Errorf("비밀글이 보인다=%v, want %v", have["비밀글"], tc.want)
			}
		})
	}
}

// 발행되지 않은 글은 오지 않는다. 홈은 공개 화면이고 초안은 공개가 아니다.
func TestRecentPostsOnlyPublished(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	board := seedBoard(t, s)

	mkPost(t, s, board, "발행글")
	hidden := mkPost(t, s, board, "숨긴글")
	if err := s.SetPostFlags(ctx, hidden, false, "hidden"); err != nil {
		t.Fatal(err)
	}

	got, err := s.RecentPosts(ctx, []string{board}, nil, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	have := titles(got)
	if !have["발행글"] {
		t.Fatal("발행글이 없다 — 아래 단언이 헛돈다")
	}
	if have["숨긴글"] {
		t.Error("숨긴 글이 홈에 실렸다")
	}
}

// limit 이 지켜진다. 홈은 한 화면이고, 지켜지지 않으면 글이 늘수록 첫 화면이
// 길어진다.
func TestRecentPostsRespectsLimit(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	board := seedBoard(t, s)
	for _, title := range []string{"1", "2", "3", "4", "5"} {
		mkPost(t, s, board, title)
	}

	got, err := s.RecentPosts(ctx, []string{board}, nil, "", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("%d행, want 3행", len(got))
	}
	// 최신순이다 — 홈이 오래된 글부터 보여주면 「최근 글」이 아니다.
	if len(got) > 1 && got[0].CreatedAt.Before(got[1].CreatedAt) {
		t.Error("최신순이 아니다")
	}
	if n, err := s.RecentPosts(ctx, []string{board}, nil, "", 0); err != nil || len(n) != 0 {
		t.Errorf("limit 0 인데 %d행 (err=%v)", len(n), err)
	}
}
