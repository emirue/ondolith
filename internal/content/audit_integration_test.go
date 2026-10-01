package content

import (
	"cmp"
	"context"
	"net/url"
	"slices"
	"testing"
	"time"
)

// 메뉴 항목을 고쳐도 updated_at 이 만든 날에 머물렀다 — 다른 표의 UPDATE 는 전부
// 찍는다.
func TestUpdateMenuItemTouchesUpdatedAt(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	id, err := s.CreateMenuItem(ctx, MenuItem{Title: "회사", URL: "/about", Sort: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE menus SET updated_at = now() - interval '1 day' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateMenuItem(ctx, id, MenuItem{Title: "회사 소개", URL: "/about", Sort: 2}); err != nil {
		t.Fatal(err)
	}
	var age time.Duration
	if err := pool.QueryRow(ctx,
		`SELECT now() - updated_at FROM menus WHERE id = $1`, id).Scan(&age); err != nil {
		t.Fatal(err)
	}
	if age > time.Hour {
		t.Errorf("수정 뒤 updated_at 이 %v 전이다 — 갱신되지 않았다", age)
	}
}

// P-203: 그 쪽에 행이 없으면 목록 질의를 하지 않는다. 맞는 글이 없는 검색은 LIMIT 을
// 채우지 못해 게시판 전체를 정렬 순서대로 끝까지 걸었다 (실측 86ms → 0.02ms).
func TestPostPageSkipsTheListWhenThePageIsEmpty(t *testing.T) {
	base, _ := testStore(t)
	ctx := context.Background()
	boardID := seedBoard(t, base)
	for _, title := range []string{"게시판 소개", "게시판 규칙", "다른 이야기"} {
		mkPost(t, base, boardID, title)
	}
	s, tr := tracedStore(t)

	for _, c := range []struct {
		name               string
		query              url.Values
		wantRows           int
		wantTotal, queries int64
	}{
		{"맞는 글이 없는 검색", url.Values{"q": {"없는말"}}, 0, 0, 1},
		{"마지막 쪽을 넘긴 page", url.Values{"page": {"9"}}, 0, 3, 1},
		{"맞는 글이 있는 검색", url.Values{"q": {"게시판"}}, 2, 2, 2},
		{"첫 쪽", url.Values{}, 3, 3, 2},
	} {
		tr.n.Store(0)
		posts, total, err := s.PostPage(ctx, boardID, ParseListQuery(c.query, 20))
		if err != nil {
			t.Fatal(err)
		}
		if len(posts) != c.wantRows || total != c.wantTotal {
			t.Errorf("%s: %d행·합계 %d, want %d행·합계 %d", c.name, len(posts), total, c.wantRows, c.wantTotal)
		}
		if n := tr.n.Load(); n != c.queries {
			t.Errorf("%s: 질의 %d회, want %d회", c.name, n, c.queries)
		}
	}
}

// 정렬되는 세 질의(ListPosts·ListPostsSearch·SearchPosts)는 쪽을 안쪽 질의에서 고른
// 뒤 바깥에서 다시 정렬한다. 키·방향마다 쪽을 이어 붙인 순서가 「고정 → 키 → id」
// 전순서와 같아야 한다 — 쪽 경계에서 행이 빠지거나 겹치면 여기서 드러난다.
func TestSortedListsPageInTheSameTotalOrder(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	boardID := seedBoard(t, s)

	// 제목은 ASCII 소문자다 — DB 콜레이션과 Go 의 비교가 같은 순서를 낸다.
	// 조회수는 일부러 겹친다: 동률은 id 가 가른다.
	views := map[string]int{"delta note": 5, "alpha note": 9, "echo note": 5,
		"bravo note": 1, "charlie note": 5, "foxtrot note": 0}
	for title, v := range views {
		id := mkPost(t, s, boardID, title)
		if _, err := pool.Exec(ctx, `UPDATE posts SET view_count = $2 WHERE id = $1`, id, v); err != nil {
			t.Fatal(err)
		}
		switch title {
		case "echo note": // 고정
			if err := s.SetPostFlags(ctx, id, true, "published"); err != nil {
				t.Fatal(err)
			}
		case "foxtrot note": // 목록에 없다
			if err := s.SetPostFlags(ctx, id, false, "hidden"); err != nil {
				t.Fatal(err)
			}
		}
	}

	type row struct {
		id, title string
		views     int
		pinned    bool
		created   time.Time
	}
	var all []row
	rows, err := pool.Query(ctx, `
		SELECT id::text, title, view_count, is_pinned, created_at FROM posts WHERE status = 'published'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.title, &r.views, &r.pinned, &r.created); err != nil {
			t.Fatal(err)
		}
		all = append(all, r)
	}
	rows.Close()
	if len(all) != 5 {
		t.Fatalf("준비: 발행 글 %d건, want 5", len(all))
	}

	want := func(key string, desc bool) []string {
		sorted := slices.Clone(all)
		slices.SortFunc(sorted, func(a, b row) int {
			if a.pinned != b.pinned { // 고정이 먼저 — 방향과 무관하다
				if a.pinned {
					return -1
				}
				return 1
			}
			var c int
			switch key {
			case "created":
				c = a.created.Compare(b.created)
			case "views":
				c = cmp.Compare(a.views, b.views)
			case "title":
				c = cmp.Compare(a.title, b.title)
			}
			if c == 0 {
				c = cmp.Compare(a.id, b.id)
			}
			if desc {
				c = -c
			}
			return c
		})
		var ids []string
		for _, r := range sorted {
			ids = append(ids, r.id)
		}
		return ids
	}

	lists := map[string]func(ListQuery) ([]Post, error){
		"ListPosts": func(q ListQuery) ([]Post, error) { return s.ListPosts(ctx, boardID, q) },
		"ListPostsSearch": func(q ListQuery) ([]Post, error) {
			q.Search = "note"
			return s.ListPosts(ctx, boardID, q)
		},
		"SearchPosts": func(q ListQuery) ([]Post, error) {
			q.Search = "note"
			return s.SearchPosts(ctx, []string{boardID}, nil, q, "")
		},
	}
	for name, list := range lists {
		for _, key := range SortKeys() {
			for _, desc := range []bool{true, false} {
				var got []string
				for page := range 3 { // 2건씩 세 쪽 = 5건
					posts, err := list(ListQuery{Sort: key, Desc: desc, Page: page, PerPage: 2})
					if err != nil {
						t.Fatal(err)
					}
					for _, p := range posts {
						got = append(got, p.ID)
					}
				}
				if w := want(key, desc); !slices.Equal(got, w) {
					t.Errorf("%s sort=%s desc=%v:\n got %v\nwant %v", name, key, desc, got, w)
				}
			}
		}
	}
}

// 사이트맵은 게시판마다 몫만큼만 읽는다. 고정 글이 몫의 앞에 오고, 비밀글·숨긴 글과
// 목록에 없는 게시판은 나오지 않는다.
func TestSitemapPostsTakeEachBoardsQuota(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	a := seedBoard(t, s)
	b := seedBoardSlug(t, s, "notice")
	unlisted := seedBoardSlug(t, s, "staff")

	pinned := mkPost(t, s, a, "오래된 고정 글")
	if err := s.SetPostFlags(ctx, pinned, true, "published"); err != nil {
		t.Fatal(err)
	}
	mkPost(t, s, a, "밀려난 글")
	newest := mkPost(t, s, a, "가장 새 공개 글")
	if _, err := s.CreatePost(ctx, Post{BoardID: a, Title: "비밀", Body: "본문", IsSecret: true}); err != nil {
		t.Fatal(err)
	}
	hidden := mkPost(t, s, a, "숨긴 글")
	if err := s.SetPostFlags(ctx, hidden, false, "hidden"); err != nil {
		t.Fatal(err)
	}
	only := mkPost(t, s, b, "공지 하나")
	mkPost(t, s, unlisted, "내부 글")

	got, err := s.SitemapPosts(ctx, []string{a, b}, 2)
	if err != nil {
		t.Fatal(err)
	}
	byBoard := map[string][]string{}
	for _, e := range got {
		byBoard[e.BoardID] = append(byBoard[e.BoardID], e.ID)
	}
	if !slices.Equal(byBoard[a], []string{pinned, newest}) {
		t.Errorf("게시판 A 의 몫 %v, want [고정 %s, 최신 %s]", byBoard[a], pinned, newest)
	}
	if !slices.Equal(byBoard[b], []string{only}) {
		t.Errorf("게시판 B 의 몫 %v, want [%s]", byBoard[b], only)
	}
	if len(got) != 3 {
		t.Errorf("전체 %d건, want 3 — 목록에 없는 게시판이나 가려야 할 글이 섞였다", len(got))
	}
}
