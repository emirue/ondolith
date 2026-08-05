package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/emirue/ondolith/internal/admin"
	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/config"
	"github.com/emirue/ondolith/internal/migrations"
)

// liveSite brings up the real tree — New(), not a hand-assembled test mux — so
// that what is exercised is the thing that ships. A test tree can pass while
// the wiring in New() is wrong, which is exactly the gap this file exists for.
func liveSite(t *testing.T) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()
	return liveSiteWith(t)
}

func liveSiteWith(t *testing.T, tweak ...func(*config.Config)) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s 미설정 — 통합 테스트를 건너뜁니다 (make test-integration)", dsnEnv)
	}
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, s := range []string{"DROP SCHEMA public CASCADE", "CREATE SCHEMA public"} {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	cfg := &config.Config{DatabaseURL: dsn, SiteName: "테스트 사이트"}
	for _, f := range tweak {
		f(cfg)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, cleanup, err := New(ctx, cfg, "1.0.0", log)
	if err != nil {
		t.Fatalf("기동 실패: %v", err)
	}
	t.Cleanup(cleanup)

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, pool
}

// liveSiteWithUploads is liveSite plus a temporary upload directory, for the
// screens that move files. The directory is configuration (NFR-304), so the
// test sets it the way an operator would rather than reaching past it.
func liveSiteWithUploads(t *testing.T) (*httptest.Server, *pgxpool.Pool, string) {
	t.Helper()
	root := t.TempDir()
	srv, pool := liveSiteWith(t, func(c *config.Config) { c.UploadDir = root })
	return srv, pool, root
}

// removeUnder deletes one stored file, for the "row without a file" case
// A-309 allows.
func removeUnder(root, rel string) error {
	return os.Remove(filepath.Join(root, filepath.FromSlash(rel)))
}

// client keeps cookies so a login survives to the next request, and stops at
// redirects so the test can see which one was issued.
func client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func migrateAndSeed(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	if err := migrations.Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
}

// The boot self-check has to run against the real table. A tree that registers
// a screen D11 does not declare, or a permission the database lacks, must stop
// the boot rather than serve (FR-110).
func TestRealTreePassesTheBootCheck(t *testing.T) {
	srv, _ := liveSite(t)
	// Reaching here means New() returned without error, which means Check()
	// found nothing. Confirm the tree is actually mounted rather than empty.
	resp, err := srv.Client().Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("홈이 HTTP %d", resp.StatusCode)
	}
}

// Every Phase 1 screen is reachable and answers. An unauthenticated caller sees
// the public ones and is redirected away from the admin tree — never a 404,
// which would mean the route is not registered at all.
func TestPublicScreensRespond(t *testing.T) {
	srv, _ := liveSite(t)
	c := client()

	for path, want := range map[string]int{
		"/":         http.StatusOK,
		"/login":    http.StatusOK,
		"/signup":   http.StatusOK,
		"/nothing":  http.StatusNotFound, // P-202 with no such page
		"/me":       http.StatusSeeOther, // P-108 needs a session
		"/admin/":   http.StatusSeeOther, // tree gate sends anonymous to P-101
		"/admin/us": http.StatusSeeOther,
	} {
		resp, err := c.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("GET %s → HTTP %d, want %d", path, resp.StatusCode, want)
		}
	}
}

// D80 Phase 1 완료 기준 ③: 관리자 로그인 → 페이지 생성 → 발행 → 공개 URL 노출.
// This is the walk-through the phase is judged on, done over HTTP against the
// tree New() built.
func TestAdminCanPublishAPageThatThenAppearsPublicly(t *testing.T) {
	srv, pool := liveSite(t)
	ctx := context.Background()
	store := auth.NewStore(pool)

	hash, err := auth.HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.CreateUser(ctx, "admin@example.com", hash, "관리자")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE key='admin'`,
		id); err != nil {
		t.Fatal(err)
	}

	c := client()
	post := func(path string, form url.Values) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		// Same-origin, which is what the cross-origin protection checks.
		req.Header.Set("Origin", srv.URL)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		return resp
	}

	// 1. 로그인
	resp := post("/login", url.Values{
		"email": {"admin@example.com"}, "password": {"correct horse battery"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("로그인 HTTP %d: %s", resp.StatusCode, body)
	}

	// 2. 관리자 화면이 열린다
	adminResp, err := c.Get(srv.URL + "/admin/pages")
	if err != nil {
		t.Fatal(err)
	}
	adminBody, _ := io.ReadAll(adminResp.Body)
	adminResp.Body.Close()
	if adminResp.StatusCode != http.StatusOK {
		t.Fatalf("관리자 페이지 목록 HTTP %d: %s", adminResp.StatusCode, adminBody)
	}
	if !strings.Contains(string(adminBody), "페이지") {
		t.Errorf("관리자 화면이 그려지지 않았다: %s", adminBody)
	}

	// 3. 페이지 생성
	resp = post("/admin/pages/new", url.Values{
		"slug": {"about"}, "title": {"회사 소개"}, "body": {"본문입니다."}})
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("페이지 생성 HTTP %d: %s", resp.StatusCode, body)
	}

	// 초안은 공개되지 않는다.
	pub, err := c.Get(srv.URL + "/about")
	if err != nil {
		t.Fatal(err)
	}
	pub.Body.Close()
	if pub.StatusCode != http.StatusNotFound {
		t.Errorf("발행 전 초안이 HTTP %d 로 공개됐다", pub.StatusCode)
	}

	// 4. 발행
	var pageID string
	if err := pool.QueryRow(ctx, `SELECT id FROM pages WHERE slug='about'`).Scan(&pageID); err != nil {
		t.Fatal(err)
	}
	resp = post("/admin/pages/"+pageID+"/publish", url.Values{"status": {"published"}})
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("발행 HTTP %d: %s", resp.StatusCode, body)
	}

	// 5. 공개 URL 에 나타난다
	pub, err = c.Get(srv.URL + "/about")
	if err != nil {
		t.Fatal(err)
	}
	pubBody, _ := io.ReadAll(pub.Body)
	pub.Body.Close()
	if pub.StatusCode != http.StatusOK {
		t.Fatalf("발행 후 공개 URL HTTP %d: %s", pub.StatusCode, pubBody)
	}
	if !strings.Contains(string(pubBody), "회사 소개") || !strings.Contains(string(pubBody), "본문입니다.") {
		t.Errorf("발행된 페이지 내용이 없다: %s", pubBody)
	}
}

// D80 Phase 1 완료 기준 ④: 권한 없는 사용자의 `/admin/*` 차단. A logged-in
// visitor without admin.access must be refused, not redirected to a login form
// they are already past.
func TestLoggedInVisitorWithoutAdminAccessIsRefused(t *testing.T) {
	srv, pool := liveSite(t)
	ctx := context.Background()
	store := auth.NewStore(pool)

	hash, err := auth.HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateUser(ctx, "visitor@example.com", hash, "방문자"); err != nil {
		t.Fatal(err)
	}

	c := client()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/login",
		strings.NewReader(url.Values{
			"email": {"visitor@example.com"}, "password": {"correct horse battery"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", srv.URL)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("로그인 HTTP %d", resp.StatusCode)
	}

	for _, path := range []string{"/admin/", "/admin/users", "/admin/settings", "/admin/system"} {
		resp, err := c.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("GET %s → HTTP %d, want 403", path, resp.StatusCode)
		}
	}
}

// D80 Phase 1 완료 기준 ①: 기본 테마만으로 사이트가 뜬다. No theme directory is
// configured here, so every template comes from the embedded built-in.
func TestSiteRunsOnTheBuiltInThemeAlone(t *testing.T) {
	srv, _ := liveSite(t)

	resp, err := srv.Client().Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("홈 HTTP %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "<html") {
		t.Errorf("레이아웃이 그려지지 않았다: %s", body)
	}
	// The site name comes from config when no setting overrides it.
	if !strings.Contains(string(body), "테스트 사이트") {
		t.Errorf("사이트 이름이 없다: %s", body)
	}
}

// FR-110: a server that comes up in the wrong state has its wrong state
// discovered by a visitor. The boot check has to STOP the boot, not log and
// carry on — so this removes a permission the route table names and asserts
// New() refuses.
func TestBootRefusesWhenARouteNamesAMissingPermission(t *testing.T) {
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s 미설정 — 통합 테스트를 건너뜁니다 (make test-integration)", dsnEnv)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, s := range []string{"DROP SCHEMA public CASCADE", "CREATE SCHEMA public"} {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	migrateAndSeed(t, pool)

	// page.publish is named by A-303 and by nothing else. The grant rows go
	// first: role_permissions is RESTRICT, which is D30 refusing to let a
	// permission vanish out from under a role.
	for _, q := range []string{
		`DELETE FROM role_permissions rp USING permissions p
		 WHERE rp.permission_id = p.id AND p.key = 'page.publish'`,
		`DELETE FROM permissions WHERE key = 'page.publish'`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &config.Config{DatabaseURL: dsn, SiteName: "테스트 사이트"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, cleanup, err := New(ctx, cfg, "1.0.0", log)
	if cleanup != nil {
		cleanup()
	}
	if err == nil {
		t.Fatal("존재하지 않는 권한 키를 쓰는 트리로 기동했다")
	}
	if h != nil {
		t.Error("기동 실패인데 핸들러를 돌려줬다")
	}
	if !strings.Contains(err.Error(), "page.publish") {
		t.Errorf("어느 키가 문제인지 말하지 않는다: %v", err)
	}
}

// adminSession logs in an account holding the admin role and returns a client
// with the session, plus a POST helper that satisfies the cross-origin check.
func adminSession(t *testing.T, srv *httptest.Server, pool *pgxpool.Pool) (*http.Client,
	func(path string, form url.Values) *http.Response,
) {
	t.Helper()
	ctx := context.Background()
	store := auth.NewStore(pool)
	hash, err := auth.HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.CreateUser(ctx, "admin@example.com", hash, "관리자")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE key='admin'`,
		id); err != nil {
		t.Fatal(err)
	}

	c := client()
	post := func(path string, form url.Values) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", srv.URL)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		return resp
	}
	resp := post("/login", url.Values{
		"email": {"admin@example.com"}, "password": {"correct horse battery"}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("로그인 HTTP %d: %s", resp.StatusCode, body)
	}
	return c, post
}

func mustGet(t *testing.T, c *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(b)
}

// D80 Phase 1 완료 기준 ②: 디스크에 템플릿 하나를 놓으면 그 부분만 바뀐다,
// 재시작 없이 (FR-302, FR-303).
//
// The theme is activated over HTTP through A-202, exactly as an operator would,
// and the very next request has to use it. Capturing the theme directory at boot
// is the failure this pins: it passes every unit test and means "restart to
// change theme".
func TestDiskThemeOverridesOnlyThatTemplateWithoutRestart(t *testing.T) {
	srv, pool := liveSite(t)
	c, post := adminSession(t, srv, pool)

	// A page to look at, published, so the public URL renders the theme.
	if rec := post("/admin/pages/new", url.Values{
		"slug": {"about"}, "title": {"회사 소개"}, "body": {"본문입니다."}}); rec.StatusCode != http.StatusSeeOther {
		t.Fatalf("페이지 생성 HTTP %d", rec.StatusCode)
	}
	var pageID string
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM pages WHERE slug='about'`).Scan(&pageID); err != nil {
		t.Fatal(err)
	}
	if rec := post("/admin/pages/"+pageID+"/publish",
		url.Values{"status": {"published"}}); rec.StatusCode != http.StatusSeeOther {
		t.Fatalf("발행 HTTP %d", rec.StatusCode)
	}

	// Before: the built-in page template.
	code, before := mustGet(t, c, srv.URL+"/about")
	if code != http.StatusOK {
		t.Fatalf("공개 페이지 HTTP %d: %s", code, before)
	}
	if strings.Contains(before, "디스크에서 왔다") {
		t.Fatal("아직 놓지도 않은 디스크 템플릿이 이미 쓰이고 있다")
	}

	// Place ONE template on disk. base.html is required for the directory to be
	// a theme at all (D17); page.html is the override under test.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "base.html"),
		[]byte(`<html><body>{{block "content" .}}{{end}}</body></html>`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "page.html"),
		[]byte(`{{define "content"}}디스크에서 왔다: {{.Data.Title}}{{end}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Activate it through A-202, with no restart in between.
	resp := post("/admin/themes", url.Values{"theme": {dir}})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusOK {
		t.Fatalf("테마 활성화 HTTP %d: %s", resp.StatusCode, body)
	}

	// The very next request uses it.
	code, after := mustGet(t, c, srv.URL+"/about")
	if code != http.StatusOK {
		t.Fatalf("교체 후 공개 페이지 HTTP %d: %s", code, after)
	}
	if !strings.Contains(after, "디스크에서 왔다") {
		t.Errorf("재시작 없이 디스크 템플릿이 적용되지 않았다 (FR-303): %s", after)
	}

	// ...and only that template. The home page has no disk override, so it must
	// still come from the built-in theme rather than 404 or fall over.
	code, home := mustGet(t, c, srv.URL+"/")
	if code != http.StatusOK {
		t.Errorf("오버라이드하지 않은 화면이 HTTP %d: %s", code, home)
	}
	if strings.Contains(home, "디스크에서 왔다") {
		t.Errorf("오버라이드하지 않은 화면까지 디스크 템플릿이 먹었다: %s", home)
	}

	// Switching back to the built-in theme is also live.
	resp = post("/admin/themes", url.Values{"theme": {""}})
	resp.Body.Close()
	if _, back := mustGet(t, c, srv.URL+"/about"); strings.Contains(back, "디스크에서 왔다") {
		t.Errorf("내장 테마로 되돌아가는 것도 재시작이 필요하다: %s", back)
	}
}

// Every administrator screen renders, with the design system's shell around it.
//
// The handlers had tests before this and all of them passed while the templates
// referenced fields that do not exist — a template error is a runtime error, so
// nothing but rendering the real thing finds it (M13's shape again).
func TestEveryAdminScreenRenders(t *testing.T) {
	srv, pool := liveSite(t)
	c, post := adminSession(t, srv, pool)
	post("/admin/pages/new", url.Values{"slug": {"about"}, "title": {"회사 소개"}, "body": {"본문"}})
	post("/admin/menus", url.Values{"title": {"홈"}, "url": {"/"}})

	for _, p := range []string{
		"/admin/", "/admin/pages", "/admin/menus", "/admin/users",
		"/admin/roles", "/admin/settings", "/admin/settings/mail",
		"/admin/themes", "/admin/system",
	} {
		code, body := mustGet(t, c, srv.URL+p)
		if code != http.StatusOK {
			t.Errorf("%s → HTTP %d: %.200s", p, code, body)
			continue
		}
		// The shell, and the tokens the whole design hangs off.
		for _, want := range []string{"adm-header", "adm-sidebar", "adm-main", "--accent:"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: %q 가 없다 — 디자인이 적용되지 않았다", p, want)
			}
		}
		// html/template refuses to inline a value it cannot prove is CSS and
		// leaves ZgotmplZ behind. A stylesheet that silently vanished would
		// still return 200.
		if strings.Contains(body, "ZgotmplZ") {
			t.Errorf("%s: CSS 가 이스케이프됐다", p)
		}
	}
}

// A menu entry with no route 404s, and to the person clicking it that is
// indistinguishable from a screen they lack permission for — so nobody reports
// it. The boot check compares the two lists.
func TestAdminMenuOnlyPointsAtRegisteredRoutes(t *testing.T) {
	srv, pool := liveSite(t)
	c, _ := adminSession(t, srv, pool)

	for _, p := range admin.NavPaths() {
		code, _ := mustGet(t, c, srv.URL+p)
		if code == http.StatusNotFound {
			t.Errorf("메뉴 항목 %s 가 404 다", p)
		}
	}
}

// 공개 화면도 내장 테마의 디자인을 실제로 받는다. 스타일시트는 별도 요청이라
// 페이지가 200 이어도 자산이 404 면 아무 스타일 없이 뜬다.
func TestPublicScreensCarryTheBuiltInDesign(t *testing.T) {
	srv, pool := liveSite(t)
	c, post := adminSession(t, srv, pool)
	post("/admin/pages/new", url.Values{"slug": {"about"}, "title": {"회사 소개"}, "body": {"본문입니다."}})
	post("/admin/menus", url.Values{"title": {"회사소개"}, "url": {"/about"}})

	code, home := mustGet(t, c, srv.URL+"/")
	if code != http.StatusOK {
		t.Fatalf("홈 HTTP %d", code)
	}
	for _, want := range []string{`class="hero"`, `class="site-header"`, "css/style.css"} {
		if !strings.Contains(home, want) {
			t.Errorf("홈에 %q 가 없다: %.400s", want, home)
		}
	}

	// The stylesheet URL carries a content hash; follow whatever the page asked
	// for rather than guessing the path.
	href := home[strings.Index(home, "css/style.css")-1:]
	href = href[:strings.IndexAny(href, `"`)+0]
	start := strings.LastIndex(home[:strings.Index(home, "css/style.css")], `href="`)
	href = home[start+len(`href="`):]
	href = href[:strings.Index(href, `"`)]

	code, css := mustGet(t, c, srv.URL+href)
	if code != http.StatusOK {
		t.Fatalf("스타일시트 %s → HTTP %d", href, code)
	}
	if !strings.Contains(css, "--accent:") {
		t.Errorf("스타일시트에 토큰이 없다: %.200s", css)
	}
}
