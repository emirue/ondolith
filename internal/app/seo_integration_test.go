package app

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/content"
)

// FR-510: 발행된 페이지·글만 들어간다. 초안·비밀글·비공개 게시판 글이 없어야
// 한다 — 사이트맵은 로그인하지 않은 크롤러가 읽으므로, 익명이 열 수 있는
// 집합과 정확히 같아야 한다.
func TestSitemapContainsOnlyWhatAnonymousCanOpen(t *testing.T) {
	srv, pool := liveSite(t)
	ctx := context.Background()
	store := content.NewStore(pool)
	freeID := mkBoard(t, pool, "free", content.PresetPublic)
	staffID := mkBoard(t, pool, "staff", content.PresetPrivate)
	authorID := mkUser(t, pool, "a@example.com")

	// 발행된 페이지 하나, 초안 하나.
	published, err := store.CreatePage(ctx, content.Page{Slug: "about", Title: "회사", Body: "본문"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetPageStatus(ctx, published, "published"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePage(ctx, content.Page{Slug: "draft", Title: "초안", Body: "본문"}); err != nil {
		t.Fatal(err)
	}

	openPost, err := store.CreatePost(ctx, content.Post{
		BoardID: freeID, Title: "공개 글", Body: "본문"})
	if err != nil {
		t.Fatal(err)
	}
	secretPost, err := store.CreatePost(ctx, content.Post{
		BoardID: freeID, AuthorID: authorID, Title: "비밀 글", Body: "본문", IsSecret: true})
	if err != nil {
		t.Fatal(err)
	}
	hiddenPost, err := store.CreatePost(ctx, content.Post{
		BoardID: freeID, Title: "숨긴 글", Body: "본문"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetPostFlags(ctx, hiddenPost, false, "hidden"); err != nil {
		t.Fatal(err)
	}
	staffPost, err := store.CreatePost(ctx, content.Post{
		BoardID: staffID, Title: "내부 글", Body: "본문"})
	if err != nil {
		t.Fatal(err)
	}

	code, body := mustGet(t, client(), srv.URL+"/sitemap.xml")
	if code != http.StatusOK {
		t.Fatalf("사이트맵 HTTP %d", code)
	}
	if !strings.Contains(body, "<urlset") {
		t.Fatalf("XML 이 아니다: %.200s", body)
	}

	for _, want := range []string{"/about", "/board/free", "/board/free/" + openPost} {
		if !strings.Contains(body, want) {
			t.Errorf("사이트맵에 %s 가 없다", want)
		}
	}
	for path, what := range map[string]string{
		"/draft":                    "초안 페이지",
		"/board/staff":              "비공개 게시판",
		"/board/free/" + secretPost: "비밀글",
		"/board/free/" + hiddenPost: "숨긴 글",
		"/board/staff/" + staffPost: "비공개 게시판의 글",
	} {
		if strings.Contains(body, path) {
			t.Errorf("사이트맵에 %s 가 들어갔다: %s", what, path)
		}
	}
}

// 로그인한 관리자가 요청해도 사이트맵의 내용은 같다. 크롤러가 읽는 문서가
// 요청자에 따라 달라지면, 관리자가 한 번 열어 본 URL 이 색인될 수 있다.
func TestSitemapIsTheSameWhoeverAsks(t *testing.T) {
	srv, pool := liveSite(t)
	ctx := context.Background()
	staffID := mkBoard(t, pool, "staff", content.PresetPrivate)
	if _, err := content.NewStore(pool).CreatePost(ctx, content.Post{
		BoardID: staffID, Title: "내부 글", Body: "본문"}); err != nil {
		t.Fatal(err)
	}
	c, _ := adminSession(t, srv, pool)

	_, anonBody := mustGet(t, client(), srv.URL+"/sitemap.xml")
	_, adminBody := mustGet(t, c, srv.URL+"/sitemap.xml")
	if anonBody != adminBody {
		t.Error("요청자에 따라 사이트맵이 다르다")
	}
	if strings.Contains(adminBody, "/board/staff") {
		t.Error("관리자로 열었더니 비공개 게시판이 들어갔다")
	}
}

func TestRobotsDisallowsThePrivateTrees(t *testing.T) {
	srv, _ := liveSite(t)
	code, body := mustGet(t, client(), srv.URL+"/robots.txt")
	if code != http.StatusOK {
		t.Fatalf("robots HTTP %d", code)
	}
	for _, want := range []string{"Disallow: /admin/", "Disallow: /me", "Sitemap: "} {
		if !strings.Contains(body, want) {
			t.Errorf("robots.txt 에 %q 가 없다:\n%s", want, body)
		}
	}
	if !strings.Contains(body, "/sitemap.xml") {
		t.Error("Sitemap 줄이 사이트맵을 가리키지 않는다")
	}
}

// X-Forwarded-Host 를 믿지 않는다. 그것을 그대로 쓰면 헤더를 보낸 쪽의
// 출처로 채워진 사이트맵을 만들어 준다.
func TestSitemapIgnoresForwardedHost(t *testing.T) {
	srv, pool := liveSite(t)
	freeID := mkBoard(t, pool, "free", content.PresetPublic)
	if _, err := content.NewStore(pool).CreatePost(context.Background(), content.Post{
		BoardID: freeID, Title: "글", Body: "본문"}); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/sitemap.xml", nil)
	req.Header.Set("X-Forwarded-Host", "evil.example.com")
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err := client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])
	if strings.Contains(body, "evil.example.com") {
		t.Errorf("전달 헤더의 호스트가 사이트맵에 들어갔다:\n%s", body)
	}
}

// 사이트맵은 요청마다 만들어진다. 게시판 하나가 무제한으로 기여하면 크롤러
// 방문 한 번이 전 게시판 풀스캔이 된다 — NFR-101 의 티어에는 그럴 여유가 없다.
func TestSitemapCapsPostsPerBoard(t *testing.T) {
	srv, pool := liveSite(t)
	ctx := context.Background()
	boardID := mkBoard(t, pool, "free", content.PresetPublic)
	if _, err := pool.Exec(ctx, `
		INSERT INTO posts (board_id, title, body)
		SELECT $1, '글 ' || g, '본문' FROM generate_series(1, $2) g`,
		boardID, sitemapPostsPerBoard+50); err != nil {
		t.Fatal(err)
	}

	code, body := mustGet(t, client(), srv.URL+"/sitemap.xml")
	if code != http.StatusOK {
		t.Fatalf("사이트맵 HTTP %d", code)
	}
	// 홈 + 게시판 목록 + 글들. 상한을 넘으면 글 수가 그대로 나온다.
	n := strings.Count(body, "<loc>")
	if n > sitemapPostsPerBoard+10 {
		t.Errorf("사이트맵 URL %d개 — 게시판당 상한 %d 이 안 걸렸다", n, sitemapPostsPerBoard)
	}
	if n < sitemapPostsPerBoard {
		t.Errorf("사이트맵 URL %d개 — 상한보다 적다, 다른 이유로 잘렸다", n)
	}
}

// FR-511: `.Meta` 가 화면별로 채워진다. P-202·P-203·P-204 에서 제목·설명이
// 각각 다른 값이어야 한다 — 전부 사이트 기본값이면 검색 결과에서 구분되지
// 않는다.
func TestMetaDiffersPerScreen(t *testing.T) {
	srv, pool := liveSite(t)
	ctx := context.Background()
	store := content.NewStore(pool)
	c, post := adminSession(t, srv, pool)
	post("/admin/settings", urlValues("site.name", "테스트 사이트",
		"site.meta_description", "사이트 기본 설명", "site.og_image", "/기본.png",
		"site.type", "cms"))

	pageID, err := store.CreatePage(ctx, content.Page{
		Slug: "about", Title: "회사 소개", Body: "회사 소개 첫 줄입니다.\n둘째 줄"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetPageStatus(ctx, pageID, "published"); err != nil {
		t.Fatal(err)
	}
	boardID := mkBoard(t, pool, "free", content.PresetPublic)
	postID, err := store.CreatePost(ctx, content.Post{
		BoardID: boardID, Title: "글 제목", Body: "글 본문 첫 줄입니다.\n둘째 줄"})
	if err != nil {
		t.Fatal(err)
	}

	screens := map[string]struct{ title, desc string }{
		"/about":                {"회사 소개", "회사 소개 첫 줄입니다."},
		"/board/free":           {"free 게시판", "free 게시판 목록"},
		"/board/free/" + postID: {"글 제목", "글 본문 첫 줄입니다."},
	}
	seenTitle := map[string]bool{}
	for path, want := range screens {
		code, body := mustGet(t, c, srv.URL+path)
		if code != http.StatusOK {
			t.Fatalf("%s HTTP %d", path, code)
		}
		if !strings.Contains(body, "<title>"+want.title+" · 테스트 사이트</title>") {
			t.Errorf("%s: 제목이 %q 가 아니다", path, want.title)
		}
		if !strings.Contains(body, `name="description" content="`+want.desc+`"`) {
			t.Errorf("%s: 설명이 %q 가 아니다", path, want.desc)
		}
		if seenTitle[want.title] {
			t.Errorf("%s: 다른 화면과 같은 제목이다", path)
		}
		seenTitle[want.title] = true
		// 이미지는 화면이 정하지 않으면 사이트 기본값으로 채워진다 — 템플릿의
		// if 에 맡기지 않는다.
		if !strings.Contains(body, `property="og:image" content="/기본.png"`) {
			t.Errorf("%s: OG 이미지 기본값이 채워지지 않았다", path)
		}
	}

	// 홈은 자체 설명이 없다 — 사이트 기본값으로 채워져야 한다. 비어 있으면
	// 검색 결과에 설명 없는 항목이 뜨고, 그것을 채우는 곳이 어디인지 아무도
	// 모른다.
	_, home := mustGet(t, c, srv.URL+"/")
	if !strings.Contains(home, `name="description" content="사이트 기본 설명"`) {
		t.Errorf("홈에 사이트 기본 설명이 채워지지 않았다")
	}
	if !strings.Contains(home, `property="og:description" content="사이트 기본 설명"`) {
		t.Errorf("홈에 og:description 이 없다")
	}
}

func urlValues(kv ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v
}
