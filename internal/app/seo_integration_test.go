package app

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/config"
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
	post("/admin/settings", urlValues("site.name", "테스트 사이트", "site.type", "cms"))
	post("/admin/settings/seo", urlValues("site.meta_description", "사이트 기본 설명", "site.og_image", "/기본.png"))

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

// **사이트맵·robots 는 설정의 site_url 을 출처로 쓴다.** 요청으로 짐작하면 nginx 가
// TLS 를 끝내는 배포(D72)에서는 r.TLS 가 비어 https 사이트의 사이트맵이 http 주소로
// 채워진다 — 검색엔진은 그 주소를 따라가 리다이렉트를 한 번씩 더 겪는다.
func TestSitemapUsesTheConfiguredSiteURL(t *testing.T) {
	srv, pool := liveSiteWith(t, func(c *config.Config) { c.SiteURL = "https://shop.example.com/" })
	freeID := mkBoard(t, pool, "free", content.PresetPublic)
	if _, err := content.NewStore(pool).CreatePost(context.Background(), content.Post{
		BoardID: freeID, Title: "글", Body: "본문"}); err != nil {
		t.Fatal(err)
	}
	_, sitemap := mustGet(t, client(), srv.URL+"/sitemap.xml")
	if !strings.Contains(sitemap, "<loc>https://shop.example.com/board/free") {
		t.Errorf("사이트맵이 site_url 을 쓰지 않는다:\n%s", sitemap)
	}
	_, robots := mustGet(t, client(), srv.URL+"/robots.txt")
	if !strings.Contains(robots, "Sitemap: https://shop.example.com/sitemap.xml\n") {
		t.Errorf("robots 의 Sitemap 줄이 site_url 을 쓰지 않는다:\n%s", robots)
	}
}

// **「검색 노출」(A-211)의 소유 확인·검색 노출 끄기가 공개 화면 <head> 에, 추가 규칙이
// robots.txt 에 나간다** (FR-718). 소유 확인은 구글·네이버·빙이 홈의 <head> 에서 찾는다.
// noindex 는 robots.txt 로 막지 않는다 — 막으면 크롤러가 그 태그를 읽지 못해 이미 색인된
// 쪽이 빠지지 않는다(구글 안내).
func TestSearchSettingsReachTheHeadAndRobots(t *testing.T) {
	srv, pool := liveSite(t)
	c, post := adminSession(t, srv, pool)
	post("/admin/settings/seo", urlValues(
		"seo.google_verification", "g-token_1", "seo.naver_verification", "n0token",
		"seo.bing_verification", "B1NG", "seo.noindex", "1", "seo.robots_extra", "Disallow: /tmp"))

	_, home := mustGet(t, c, srv.URL+"/")
	for _, want := range []string{
		`<meta name="google-site-verification" content="g-token_1">`,
		`<meta name="naver-site-verification" content="n0token">`,
		`<meta name="msvalidate.01" content="B1NG">`,
		`<meta name="robots" content="noindex">`,
	} {
		if !strings.Contains(home, want) {
			t.Errorf("홈 <head> 에 %s 가 없다", want)
		}
	}
	_, robots := mustGet(t, client(), srv.URL+"/robots.txt")
	if i, j := strings.Index(robots, "Disallow: /tmp\n"), strings.Index(robots, "Sitemap: "); i < 0 || i > j {
		t.Errorf("추가 규칙이 Sitemap 줄 앞에 없다:\n%s", robots)
	}
	if strings.Contains(robots, "Disallow: /\n") {
		t.Errorf("검색 노출 끄기가 robots.txt 로 사이트 전체를 막았다:\n%s", robots)
	}

	post("/admin/settings/seo", urlValues("seo.noindex", ""))
	if _, home := mustGet(t, c, srv.URL+"/"); strings.Contains(home, `name="robots"`) {
		t.Error("검색 노출을 다시 켰는데 noindex 가 남았다")
	}
}

// **site_url 이 있으면 canonical·og:url 이 그 주소이고, 글은 article·첫 사진이다** (FR-511).
// 링크 미리보기는 상대 주소를 읽지 못해 og:image 도 절대 주소다. site_url 이 없으면 canonical 을
// 싣지 않는다 — 요청의 Host 로 짐작한 주소를 정본으로 알리면 잘못된 주소가 검색에 굳는다.
func TestShareTagsUseTheSiteURL(t *testing.T) {
	srv, pool := liveSiteWith(t, func(c *config.Config) { c.SiteURL = "https://site.example/" })
	boardID := mkBoard(t, pool, "free", content.PresetPublic)
	const photo = "0e7f0c3a-0000-4000-8000-000000000001"
	postID, err := content.NewStore(pool).CreatePost(context.Background(), content.Post{
		BoardID: boardID, Title: "사진 글", Body: "본문",
		BodyHTML: `<p>본문</p><p><img src="/media/` + photo + `/thumb" alt=""></p>`})
	if err != nil {
		t.Fatal(err)
	}
	_, page := mustGet(t, client(), srv.URL+"/board/free/"+postID+"?utm_source=kakao")
	for _, want := range []string{
		`<link rel="canonical" href="https://site.example/board/free/` + postID + `">`,
		`<meta property="og:url" content="https://site.example/board/free/` + postID + `">`,
		`<meta property="og:type" content="article">`,
		`<meta property="og:image" content="https://site.example/media/` + photo + `">`,
		`<meta name="twitter:card" content="summary_large_image">`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("글 화면에 %s 가 없다", want)
		}
	}
	_, home := mustGet(t, client(), srv.URL+"/")
	if !strings.Contains(home, `<meta property="og:type" content="website">`) ||
		!strings.Contains(home, `<link rel="canonical" href="https://site.example/">`) {
		t.Error("홈의 og:type·canonical 이 틀렸다")
	}
}

// site_url 이 없으면 canonical·og:url 을 싣지 않는다 (위 시험의 짝).
func TestCanonicalIsAbsentWithoutSiteURL(t *testing.T) {
	srv, _ := liveSite(t)
	if _, home := mustGet(t, client(), srv.URL+"/"); strings.Contains(home, `rel="canonical"`) || strings.Contains(home, "og:url") {
		t.Error("site_url 이 없는데 canonical 을 실었다")
	}
}

// **쇼핑몰의 사이트맵에는 노출 상품과 상품 카테고리가 있다** (FR-510). 상품 화면이 검색 결과에
// 나오려면 크롤러가 그 주소를 알아야 하는데, 사이트맵에는 게시판 글만 있었다. 미노출 상품은
// 404 이므로 넣지 않는다 — 넣으면 검색엔진이 깨진 주소를 쌓는다.
func TestSitemapListsShopProductsAndCategories(t *testing.T) {
	_, pool := liveSite(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO settings (key, value) VALUES ('site.type','shop')
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;
		INSERT INTO products (slug,name,base_price,is_visible) VALUES
			('on-sale','파는 상품',1000,true), ('hidden-one','숨긴 상품',1000,false);
		INSERT INTO categories (name, slug) VALUES ('옷','clothes')`); err != nil {
		t.Fatal(err)
	}
	srv := restartOnSameSchema(t)
	_, sitemap := mustGet(t, client(), srv.URL+"/sitemap.xml")
	for _, want := range []string{"/shop</loc>", "/shop/p/on-sale</loc>", "/shop/c/clothes</loc>"} {
		if !strings.Contains(sitemap, want) {
			t.Errorf("사이트맵에 %s 가 없다:\n%s", want, sitemap)
		}
	}
	if strings.Contains(sitemap, "hidden-one") {
		t.Error("미노출 상품이 사이트맵에 있다")
	}
}

// cms 사이트의 사이트맵에는 상품이 없다 — 커머스 라우트가 없으니 주소도 없다 (FR-710).
func TestSitemapHasNoShopInCmsMode(t *testing.T) {
	srv, pool := liveSite(t)
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO products (slug,name,base_price,is_visible) VALUES ('on-sale','파는 상품',1000,true)`); err != nil {
		t.Fatal(err)
	}
	if _, sitemap := mustGet(t, client(), srv.URL+"/sitemap.xml"); strings.Contains(sitemap, "/shop") {
		t.Errorf("cms 사이트맵에 상품 주소가 있다:\n%s", sitemap)
	}
}

// **홈에는 WebSite·Organization, 상품 화면에는 Product 가 JSON-LD 로 있다** (FR-719). 조직은
// 사이트 이름이 name 이고 상호가 다르면 legalName 이다(구글 안내). 상품의 가격은 파는 조합 중
// 가장 싼 값이고 재고가 하나라도 있으면 InStock 이다. 값은 html/template 이 JSON 으로 이스케이프
// 한다 — 상품 이름의 `</script>` 가 스크립트를 끊지 못한다.
func TestStructuredDataOnHomeAndProduct(t *testing.T) {
	_, pool := liveSite(t)
	ctx := context.Background()
	var productID, imageID string
	if err := pool.QueryRow(ctx, `INSERT INTO products (slug,name,description,base_price,is_visible)
		VALUES ('tee', $1, E'부드러운 면\n둘째 줄', 12000, true) RETURNING id`,
		`면 티 </script><script>alert(1)</script>`).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO product_variants (product_id,option_values,price_delta,stock)
		VALUES ($1,'{"크기":"L"}',1000,3), ($1,'{"크기":"S"}',0,0)`, productID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO product_images (product_id,stored_path,original_name,mime_type,byte_size)
		VALUES ($1,'2026/10/0e7f0c3a-0000-4000-8000-000000000002','a.png','image/png',10) RETURNING id`,
		productID).Scan(&imageID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO settings (key, value) VALUES
		('site.type','shop'), ('business.name','온돌리스 주식회사'), ('business.reg_no','123-45-67890'),
		('business.phone','02-000-0000')
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`); err != nil {
		t.Fatal(err)
	}
	srv := restartOnSameSchema(t, func(c *config.Config) { c.SiteURL = "https://shop.example" })

	_, home := mustGet(t, client(), srv.URL+"/")
	for _, want := range []string{`<script type="application/ld+json">`, `"@type":"WebSite"`, `"@type":"Organization"`,
		`"name":"테스트 사이트"`, `"legalName":"온돌리스 주식회사"`, `"taxID":"123-45-67890"`,
		`"telephone":"02-000-0000"`, `"url":"https://shop.example/"`} {
		if !strings.Contains(home, want) {
			t.Errorf("홈 JSON-LD 에 %s 가 없다", want)
		}
	}
	_, page := mustGet(t, client(), srv.URL+"/shop/p/tee")
	image := "https://shop.example/shop/images/" + imageID
	for _, want := range []string{`"@type":"Product"`, `"priceCurrency":"KRW"`, `"price":12000`,
		`"availability":"https://schema.org/InStock"`, `"image":"` + image + `"`,
		`"url":"https://shop.example/shop/p/tee"`, `"description":"부드러운 면"`,
		`<meta property="og:image" content="` + image + `">`,
		`<meta name="description" content="부드러운 면">`} {
		if !strings.Contains(page, want) {
			t.Errorf("상품 화면에 %s 가 없다", want)
		}
	}
	if strings.Contains(page, "</script><script>alert(1)") {
		t.Error("상품 이름이 JSON-LD 스크립트를 끊었다")
	}
	if !strings.Contains(page, `\u003c/script\u003e`) {
		t.Error("상품 이름의 < 가 JSON 으로 이스케이프되지 않았다")
	}
}
