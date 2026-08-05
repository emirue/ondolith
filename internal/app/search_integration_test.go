package app

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/content"
)

// FR-510 을 뒤집어 읽은 것: 읽을 수 없는 게시판의 글은 검색 결과에 **한 행도**
// 들어가지 않는다. 걸러내기가 아니라 WHERE 다 — 결과를 훑어 지우는 방식은
// 다음에 그 루프를 고치는 사람이 continue 하나를 빠뜨리는 순간 샌다.
func TestSearchNeverReturnsPostsFromUnreadableBoards(t *testing.T) {
	srv, pool := liveSite(t)
	ctx := context.Background()
	store := content.NewStore(pool)
	freeID := mkBoard(t, pool, "free", content.PresetPublic)
	staffID := mkBoard(t, pool, "staff", content.PresetPrivate)

	if _, err := store.CreatePost(ctx, content.Post{
		BoardID: freeID, Title: "공개 안내", Body: "게시판을 새로 열었습니다"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePost(ctx, content.Post{
		BoardID: staffID, Title: "내부 안내", Body: "게시판을 내부용으로 만들었습니다"}); err != nil {
		t.Fatal(err)
	}

	code, body := mustGet(t, client(), srv.URL+"/search?q="+url.QueryEscape("게시판"))
	if code != http.StatusOK {
		t.Fatalf("검색 HTTP %d", code)
	}
	if !strings.Contains(body, "공개 안내") {
		t.Error("공개 게시판의 글이 결과에 없다")
	}
	if strings.Contains(body, "내부 안내") || strings.Contains(body, "내부용으로") {
		t.Error("비공개 게시판의 글이 검색 결과에 섞였다")
	}
	// 합계도 같은 규칙을 따라야 한다. 목록은 1건인데 합계가 2건이면 결과가
	// 있다는 사실 자체가 정보다.
	if !strings.Contains(body, "1건") {
		t.Errorf("합계가 권한을 반영하지 않는다: %.300s", body)
	}
}

// 비밀글도 마찬가지다. 읽을 수 있는 게시판이어도 남의 비밀글은 안 나온다.
func TestSearchHidesOtherPeoplesSecretPosts(t *testing.T) {
	srv, pool := liveSite(t)
	ctx := context.Background()
	store := content.NewStore(pool)
	boardID := mkBoard(t, pool, "free", content.PresetPublic)
	authorID := mkUser(t, pool, "a@example.com")
	mkUser(t, pool, "b@example.com")

	if _, err := store.CreatePost(ctx, content.Post{BoardID: boardID, AuthorID: authorID,
		Title: "비밀 안내", Body: "게시판 비밀 이야기", IsSecret: true}); err != nil {
		t.Fatal(err)
	}

	author := client()
	login(t, srv.URL, "a@example.com", author)
	if _, body := mustGet(t, author, srv.URL+"/search?q="+url.QueryEscape("게시판")); !strings.Contains(body, "비밀 안내") {
		t.Error("작성자가 자기 비밀글을 검색하지 못한다")
	}

	other := client()
	login(t, srv.URL, "b@example.com", other)
	if _, body := mustGet(t, other, srv.URL+"/search?q="+url.QueryEscape("게시판")); strings.Contains(body, "비밀 안내") {
		t.Error("남의 비밀글이 검색 결과에 나온다")
	}
	if _, body := mustGet(t, client(), srv.URL+"/search?q="+url.QueryEscape("게시판")); strings.Contains(body, "비밀 안내") {
		t.Error("익명 검색에 비밀글이 나온다")
	}
}

// 검색어 없이 열면 폼만 나온다. 빈 질의로 전체를 뿌리면 그것이 곧 전체 목록이다.
func TestEmptySearchReturnsNothing(t *testing.T) {
	srv, pool := liveSite(t)
	boardID := mkBoard(t, pool, "free", content.PresetPublic)
	if _, err := content.NewStore(pool).CreatePost(context.Background(), content.Post{
		BoardID: boardID, Title: "어떤 글", Body: "본문"}); err != nil {
		t.Fatal(err)
	}
	code, body := mustGet(t, client(), srv.URL+"/search")
	if code != http.StatusOK {
		t.Fatalf("HTTP %d", code)
	}
	if strings.Contains(body, "어떤 글") {
		t.Error("검색어 없이 글이 나왔다")
	}
	// 합계도 0 이어야 한다 — "0건"이 아니라 아무 숫자나 나오면 빈 질의가
	// 전체를 세고 있다는 뜻이다.
	if !strings.Contains(body, "0건") && strings.Contains(body, "건") {
		t.Errorf("빈 검색의 합계가 0 이 아니다: %.300s", body)
	}

	// 공백만 넣은 것도 빈 질의다.
	if _, body := mustGet(t, client(), srv.URL+"/search?q=%20%20"); strings.Contains(body, "어떤 글") {
		t.Error("공백 검색어로 글이 나왔다")
	}
}

// 검색어가 tsquery 문법을 조립하지 못한다. 문법 오류는 500 이 된다.
func TestSearchTermsAreNotTsquerySyntax(t *testing.T) {
	srv, pool := liveSite(t)
	mkBoard(t, pool, "free", content.PresetPublic)
	for _, term := range []string{"게시판 & !x", "(((", ":*", "'; DROP TABLE posts --", "!!!"} {
		code, _ := mustGet(t, client(), srv.URL+"/search?q="+url.QueryEscape(term))
		if code != http.StatusOK {
			t.Errorf("검색어 %q → HTTP %d", term, code)
		}
	}
}

// FR-508: 큰 오프셋에서도 무너지지 않는다. 상한이 걸린 페이지 번호로도
// 응답이 나오고, 페이지를 넘겨도 같은 글이 반복되지 않는다.
func TestSearchPagingHoldsAtLargeOffsets(t *testing.T) {
	srv, pool := liveSite(t)
	ctx := context.Background()
	store := content.NewStore(pool)
	boardID := mkBoard(t, pool, "free", content.PresetPublic)
	for i := range 45 {
		if _, err := store.CreatePost(ctx, content.Post{BoardID: boardID,
			Title: "게시판 글 " + string(rune('A'+i%26)) + strings.Repeat("x", i%3),
			Body:  "게시판을 다룬 본문"}); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[string]bool{}
	for page := 1; page <= 3; page++ {
		code, body := mustGet(t, client(),
			srv.URL+"/search?q="+url.QueryEscape("게시판")+"&per_page=20&page="+string(rune('0'+page)))
		if code != http.StatusOK {
			t.Fatalf("page=%d → HTTP %d", page, code)
		}
		for _, line := range strings.Split(body, "\n") {
			if !strings.Contains(line, "/board/free/") {
				continue
			}
			if seen[line] {
				t.Errorf("page=%d 에 앞 페이지의 글이 다시 나온다", page)
			}
			seen[line] = true
		}
	}
	// 터무니없는 페이지 번호도 오류가 아니라 상한이다.
	if code, _ := mustGet(t, client(), srv.URL+"/search?q=게시판&page=99999"); code != http.StatusOK {
		t.Errorf("큰 페이지 번호 → HTTP %d", code)
	}
}
