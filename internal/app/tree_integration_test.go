package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
	srv, pool, _ := liveSiteStoppable(t, tweak...)
	return srv, pool
}

// liveSiteStoppable is liveSiteWith whose stop the test can call midway — 멈추는 일(cleanup)이
// 남은 조회수를 쓰므로, 그 뒤의 DB 를 읽어야 하는 시험이 쓴다. 두 번 불려도 한 번만 멈춘다.
func liveSiteStoppable(t *testing.T, tweak ...func(*config.Config)) (*httptest.Server, *pgxpool.Pool, func()) {
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

	// 업로드는 시험마다 임시 폴더다. 비우면 기본값 `uploads` 가 시험의 현재 폴더
	// (internal/app) 아래로 잡혀 올린 파일이 저장소에 남는다 — 실제로 그렇게 커밋됐다.
	cfg := &config.Config{DatabaseURL: dsn, SiteName: "테스트 사이트", UploadDir: t.TempDir()}
	for _, f := range tweak {
		f(cfg)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, cleanup, err := New(ctx, cfg, "1.0.0", log, nil)
	if err != nil {
		t.Fatalf("기동 실패: %v", err)
	}

	srv := httptest.NewServer(h)
	var once sync.Once
	stop := func() { once.Do(func() { srv.Close(); cleanup() }) }
	t.Cleanup(stop)
	return srv, pool, stop
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

	cfg := &config.Config{DatabaseURL: dsn, SiteName: "테스트 사이트", UploadDir: t.TempDir()}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, cleanup, err := New(ctx, cfg, "1.0.0", log, nil)
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
	themeRoot := t.TempDir()
	srv, pool := liveSiteWith(t, func(c *config.Config) { c.ThemeDir = themeRoot })
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

	// Place ONE template on disk, inside the theme root and under a NAME —
	// that is what A-202 stores, and a path there would be an operator aiming
	// the loader at the filesystem. base.html is required for the directory to
	// be a theme at all (D17); page.html is the override under test.
	dir := filepath.Join(themeRoot, "disk")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "base.html"),
		[]byte(`<html><body>{{block "content" .}}{{end}}</body></html>`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "page.html"),
		[]byte(`{{define "content"}}디스크에서 왔다: {{.Data.Title}}{{end}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Activate it through A-202, with no restart in between.
	resp := post("/admin/themes", url.Values{"theme": {"disk"}})
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

	for _, p := range admin.NavPaths(false) {
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
	start := strings.LastIndex(home[:strings.Index(home, "css/style.css")], `href="`)
	href := home[start+len(`href="`):]
	href = href[:strings.Index(href, `"`)]

	code, css := mustGet(t, c, srv.URL+href)
	if code != http.StatusOK {
		t.Fatalf("스타일시트 %s → HTTP %d", href, code)
	}
	if !strings.Contains(css, "--accent:") {
		t.Errorf("스타일시트에 토큰이 없다: %.200s", css)
	}
}

// **`/admin` 은 `/admin/` 으로 보낸다.**
//
// A-101 의 패턴이 `{$}` 라 정확히 `/admin/` 만 받는다. ServeMux 의 슬래시
// 보정은 서브트리 패턴을 등록했을 때의 동작이고 여기에는 없어서, 슬래시 없이
// 친 `/admin` 이 404 였다 — 관리자가 주소창에 치는 것은 거의 언제나 그쪽이다.
// 브라우저로 설치를 마치고 처음 관리자 화면에 들어가려다 걸렸다 (D83 8단계).
func TestAdminWithoutTrailingSlashRedirects(t *testing.T) {
	srv, pool := liveSite(t)
	c, _ := adminSession(t, srv, pool)

	// 리다이렉트를 따라가지 않게 해야 응답 자체를 볼 수 있다. 쿠키 jar 는
	// 그대로 두고 CheckRedirect 만 갈아 끼운다 — 새 클라이언트를 만들면 세션이
	// 실리지 않아 「로그인으로 밀림」을 리다이렉트로 오인한다.
	follow := c.CheckRedirect
	c.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	res, err := c.Get(srv.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	c.CheckRedirect = follow

	if res.StatusCode/100 != 3 {
		t.Fatalf("/admin → HTTP %d, 리다이렉트가 아니다", res.StatusCode)
	}
	if got := res.Header.Get("Location"); got != "/admin/" {
		t.Fatalf("Location = %q, want /admin/ — 로그인으로 밀린 것일 수 있다", got)
	}
	// 따라갔을 때 대시보드가 나오는지까지 본다. 리다이렉트만 확인하면 목적지가
	// 깨져 있어도 통과한다.
	res2, err := c.Get(srv.URL + "/admin/")
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Errorf("/admin/ → HTTP %d", res2.StatusCode)
	}
}

// **프록시 뒤에서도 로그인 제한은 방문자마다다** (D72, D86 1.12).
//
// 앞 판은 RemoteAddr 만 보아서, D72 처럼 nginx 뒤에 두면 모든 요청이 127.0.0.1 이었다.
// 로그인은 IP 버킷이 비면 계정 확인 전에 429 를 내므로, 익명 한 명이 분당 10번
// 틀리면 관리자를 포함한 **모든 사람의 로그인**이 막혔다. httptest 서버의 연결은
// 루프백이라 그 구성을 그대로 재현한다.
func TestLoginRateLimitIsPerVisitorBehindTheProxy(t *testing.T) {
	srv, _ := liveSiteWith(t, func(c *config.Config) { c.ProxyIPHeader = "X-Real-IP" })
	try := func(visitor string, n int) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/login", strings.NewReader(
			url.Values{"email": {fmt.Sprintf("nobody%d@example.com", n)}, "password": {"guess"}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", srv.URL)
		req.Header.Set("X-Real-IP", visitor)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	// 한 방문자가 한도(분당 10)를 넘긴다 — 계정은 매번 달리해 IP 버킷만 찬다.
	// **「11번째」로 단언하지 않는다.** 버킷은 계속 채워진다(6초에 1개) — CI 에서 11번에 7.5초가
	// 걸리자 그사이 1개가 다시 차 11번째가 통과했다. 429 가 나올 때까지 보내고, 한도의 세 배를
	// 보내도 막히지 않으면 실패다.
	limited := false
	for i := 0; i < 30 && !limited; i++ {
		limited = try("203.0.113.5", i) == http.StatusTooManyRequests
	}
	if !limited {
		t.Fatal("한 방문자가 30번 연속 틀려도 429 가 나오지 않았다")
	}
	// 다른 방문자는 막히지 않는다.
	if code := try("203.0.113.9", 99); code == http.StatusTooManyRequests {
		t.Error("다른 방문자의 로그인이 429 다 — 프록시 뒤에서 IP 제한이 사이트 전체 제한이 됐다")
	}
}

// **메일이 끝내 나가지 못하면 작업 로그에 남는다** (D13 A-205, D15 7절).
//
// 화면은 실패를 말할 수 없다 — 「보냈습니다」가 계정 유무와 무관하게 같아야
// 해서다 (FR-207). 그래서 서버 로그에만 남기면 운영자는 관리 화면 어디에서도
// 메일이 나가지 않는다는 것을 알 수 없었다. 본문은 남기지 않는다: 재설정 링크가
// 곧 토큰이다 (D15 7절 「절대 기록하지 않음」).
func TestFinalMailFailureIsInTheOperationLog(t *testing.T) {
	srv, pool := liveSite(t)
	ctx := context.Background()
	hash, err := auth.HashPassword("member-integration-pass")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.NewStore(pool).CreateUser(ctx, "member@example.com", hash, "회원"); err != nil {
		t.Fatal(err)
	}

	// SMTP 는 있는데 닿지 않는 사이트 — 발송은 세 번 다 접속 거부로 실패한다. 비어
	// 있는 경우는 남기지 않는다(mailFailureEntry, 대시보드가 알린다).
	if _, err := pool.Exec(ctx, `INSERT INTO settings (key, value) VALUES
		('mail.smtp_host', '127.0.0.1'), ('mail.smtp_port', '1'), ('mail.tls_mode', 'none')`); err != nil {
		t.Fatal(err)
	}
	resp, err := client().PostForm(srv.URL+"/password/reset", url.Values{"email": {"member@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("재설정 요청 = HTTP %d, want 200 (실패를 화면이 말하면 안 된다)", resp.StatusCode)
	}

	var actor, target, summary string
	deadline := time.Now().Add(20 * time.Second) // 재시도 백오프 1s+2s 를 기다린다
	for {
		err := pool.QueryRow(ctx, `SELECT actor_email, coalesce(target_id, ''), summary FROM operation_logs
			WHERE action = 'mail.failed' ORDER BY created_at DESC LIMIT 1`).Scan(&actor, &target, &summary)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("최종 발송 실패가 작업 로그에 없다: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if actor != "system" {
		t.Errorf("주체 = %q, want system — 사람이 아니라 서버가 한 일이다", actor)
	}
	// 어떤 메일이 누구에게 왜 — 셋이 다 있어야 운영자가 손을 쓸 수 있다.
	if target != "비밀번호 재설정" {
		t.Errorf("대상 = %q, want 비밀번호 재설정 (메일 제목)", target)
	}
	for _, want := range []string{"member@example.com", "refused"} {
		if !strings.Contains(summary, want) {
			t.Errorf("요약에 %q 가 없다: %q", want, summary)
		}
	}
	if strings.Contains(summary, "/password/reset/") {
		t.Errorf("요약에 재설정 링크(토큰)가 실렸다: %q", summary)
	}
}
