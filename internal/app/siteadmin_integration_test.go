package app

import (
	"context"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/config"
	"github.com/emirue/ondolith/internal/siteext"
)

// notesAdminExtension is notesExtension with an admin list page, a menu-less detail page and a permission.
func notesAdminExtension() siteext.Extension {
	ext := notesExtension()
	ext.Permissions = []siteext.Permission{{Key: "site.notes", Name: "메모 관리"}}
	ext.Admin = []siteext.AdminPage{
		{Menu: "메모", Path: "/notes", Permission: "site.notes", Handler: func(w http.ResponseWriter, r *http.Request) {
			a, _ := siteext.AdminFrom(r.Context())
			c, _ := siteext.CallerFrom(r.Context())
			if r.Method == http.MethodPost {
				if _, err := c.DB.ExecContext(r.Context(), `INSERT INTO site_notes (body, app) VALUES ($1, 'admin')`,
					r.PostFormValue("body")); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				if err := a.Record("site.note_add", "", "메모 추가"); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				a.Notify("/admin/site/notes", "메모를 남겼습니다.")
				http.Redirect(w, r, "/admin/site/notes", http.StatusSeeOther)
				return
			}
			a.Render(w, r, http.StatusOK, "메모", template.HTML(`<p class="site-notes">메모 화면 — `+
				template.HTMLEscapeString(c.Actor.Name)+`</p>`))
		}},
		{Path: "/notes/{id}", Permission: "site.notes", Handler: func(w http.ResponseWriter, r *http.Request) {
			a, _ := siteext.AdminFrom(r.Context())
			a.Render(w, r, http.StatusOK, "메모 상세", template.HTML("<p>메모 "+template.HTMLEscapeString(r.PathValue("id"))+"</p>"))
		}},
	}
	return ext
}

// **사이트 관리자 화면은 관리자 트리 안에서 코어의 틀·권한·알림·작업 로그를 그대로 쓴다** (D42 7절, A-214).
// 메뉴는 그 권한이 있는 관리자에게만 「사이트」 묶음에 보이고, 권한이 없으면 화면이 틀 안의 403 이다. 역할에 사이트
// 권한을 주면 열린다 — 역할 편집기는 그 권한을 사이트가 붙인 이름과 함께 보인다. 남의 출처에서 보낸 POST 는 막힌다.
func TestSiteAdminPagesRunInsideTheAdminTree(t *testing.T) {
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
			t.Fatal(err)
		}
	}
	cfg := &config.Config{DatabaseURL: dsn, SiteName: "사이트", UploadDir: t.TempDir()}
	h, cleanup, err := NewSite(ctx, cfg, "1.0.0", "example.com/notes v1.2.0", slog.New(slog.DiscardHandler), nil, notesAdminExtension())
	if err != nil {
		t.Fatalf("기동 실패 — 부팅 검사가 사이트 화면과 권한을 받아들여야 한다: %v", err)
	}
	t.Cleanup(cleanup)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	if _, err := pool.Exec(ctx, `INSERT INTO settings (key, value) VALUES ('auth.email_verification_required', '')
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`); err != nil {
		t.Fatal(err)
	}
	store := auth.NewStore(pool)
	bossID := mkUser(t, pool, "boss@example.com")
	grantSuperuser(t, store, bossID)
	edID := mkUser(t, pool, "ed@example.com")
	if err := store.AssignRole(ctx, edID, "editor"); err != nil {
		t.Fatal(err)
	}
	boss, ed := client(), client()
	login(t, srv.URL, "boss@example.com", boss)
	login(t, srv.URL, "ed@example.com", ed)
	get := func(c *http.Client, path string) (int, string) {
		t.Helper()
		res, err := c.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}

	code, page := get(boss, "/admin/site/notes")
	if code != 200 || !strings.Contains(page, `<p class="site-notes">메모 화면 — boss@example.com</p>`) ||
		!strings.Contains(page, "<title>메모 — 관리자</title>") || !strings.Contains(page, `href="/admin/site/notes"`) ||
		!strings.Contains(page, `<div class="adm-navgroup-label">사이트</div>`) {
		t.Fatalf("최고 관리자의 사이트 화면 = HTTP %d — 틀·제목·「사이트」 메뉴가 있어야 한다\n%s", code, page)
	}
	if code, page := get(boss, "/admin/site/notes/42"); code != 200 || !strings.Contains(page, "<p>메모 42</p>") ||
		strings.Contains(page, `href="/admin/site/notes/`) {
		t.Errorf("상세 화면 = HTTP %d — 경로 변수가 오고, 메뉴에는 없어야 한다", code)
	}

	res := httpPost(t, boss, srv.URL+"/admin/site/notes", url.Values{"body": {"첫 메모"}})
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/admin/site/notes" {
		t.Fatalf("저장 = HTTP %d → %q", res.StatusCode, res.Header.Get("Location"))
	}
	if _, page := get(boss, "/admin/site/notes"); !strings.Contains(page, "메모를 남겼습니다.") {
		t.Error("돌아간 화면에 저장 알림이 없다")
	}
	var logged int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operation_logs WHERE action = 'site.note_add' AND actor_user_id = $1
		AND target_type = 'site'`, bossID).Scan(&logged); err != nil || logged != 1 {
		t.Errorf("작업 로그 %d줄(want 1): %v", logged, err)
	}

	// 남의 출처에서 보낸 POST — 관리자 트리 안이라 교차 출처 보호가 막는다.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/admin/site/notes", strings.NewReader("body=evil"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if res, err := boss.Do(req); err != nil || res.StatusCode != http.StatusForbidden {
		t.Errorf("남의 출처 POST = %v %v, want 403", res.StatusCode, err)
	}
	var notes int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM site_notes`).Scan(&notes); err != nil || notes != 1 {
		t.Errorf("메모 %d개(want 1) — 막힌 요청이 썼다", notes)
	}

	// 관리자 문은 지나지만 사이트 권한이 없는 편집자 — 메뉴도 없고 화면도 403.
	if code, page := get(ed, "/admin/site/notes"); code != http.StatusForbidden || !strings.Contains(page, "<html") {
		t.Errorf("권한 없는 편집자 = HTTP %d — 틀 안의 403 이어야 한다", code)
	}
	if _, page := get(ed, "/admin/"); strings.Contains(page, "/admin/site/notes") {
		t.Error("권한 없는 편집자에게 사이트 메뉴가 보인다")
	}
	if err := store.GrantPermission(ctx, "editor", "site.notes", auth.Global, ""); err != nil {
		t.Fatal(err)
	}
	if code, page := get(ed, "/admin/site/notes"); code != 200 || !strings.Contains(page, `href="/admin/site/notes"`) {
		t.Errorf("권한을 받은 편집자 = HTTP %d, 메뉴 %v", code, strings.Contains(page, `href="/admin/site/notes"`))
	}
	if _, page := get(boss, "/admin/roles"); !strings.Contains(page, "site.notes — 메모 관리") {
		t.Error("역할 편집기가 사이트 권한을 이름과 함께 보이지 않는다")
	}
	// 시스템 정보(A-602)는 코어 버전 옆에 사이트 버전을 보인다 (D42 12절) — 어느 사이트 빌드가 도는지.
	if _, page := get(boss, "/admin/system"); !strings.Contains(page, "1.0.0") || !strings.Contains(page, "example.com/notes v1.2.0") {
		t.Error("시스템 정보에 코어·사이트 버전이 없다")
	}
}
