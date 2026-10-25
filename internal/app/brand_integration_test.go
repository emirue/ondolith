package app

import (
	"context"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/config"
	"github.com/emirue/ondolith/internal/theme"
)

// **활성 테마가 theme.json 에 지정한 로고·파비콘이 관리자 화면과 공개 화면에 나온다** (FR-310). 관리자
// 화면은 늘 바이너리에 든 기본 마크를 썼고, 테마가 로고를 정할 길이 없었다. 로고는 <img> 로만 그린다 —
// 남이 만든 테마의 SVG 를 문서에 끼워 넣으면 그 안의 스크립트가 사이트 출처로 돈다.
func TestThemeLogoAndFaviconReachAdminAndPublic(t *testing.T) {
	_, pool := liveSite(t)
	root := t.TempDir()
	dir := filepath.Join(root, "brandy")
	base, err := fs.ReadFile(theme.Builtin(), "base.html")
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"base.html":           string(base),
		"theme.json":          `{"name":"브랜디","logo":"img/logo.svg","favicon":"img/fav.png"}`,
		"static/img/logo.svg": `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><script>alert(1)</script><rect width="10" height="10"/></svg>`,
		"static/img/fav.png":  "png",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(context.Background(), `INSERT INTO settings (key, value) VALUES ('theme.active','brandy')
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`); err != nil {
		t.Fatal(err)
	}
	srv := restartOnSameSchema(t, func(c *config.Config) { c.ThemeDir = root })

	_, home := mustGet(t, client(), srv.URL+"/")
	for _, want := range []string{`<link rel="icon" href="/static/img/fav.png`, `<img class="site-logo" src="/static/img/logo.svg`} {
		if !strings.Contains(home, want) {
			t.Errorf("공개 화면에 %s 가 없다", want)
		}
	}
	// 구조화 데이터의 logo 는 절대 주소여야 한다 — site_url 이 없으면 싣지 않는다 (FR-719).
	if strings.Contains(home, `"logo":`) {
		t.Error("site_url 이 없는데 JSON-LD 에 logo 를 실었다")
	}
	c, _ := adminSession(t, srv, pool)
	code, admin := mustGet(t, c, srv.URL+"/admin/")
	if code != http.StatusOK {
		t.Fatalf("관리자 화면 HTTP %d", code)
	}
	for _, want := range []string{`<link rel="icon" href="/static/img/fav.png`, `<img class="adm-logo-img" src="/static/img/logo.svg`} {
		if !strings.Contains(admin, want) {
			t.Errorf("관리자 화면에 %s 가 없다", want)
		}
	}
	if strings.Contains(admin, "<script>alert(1)") || strings.Contains(home, "<script>alert(1)") {
		t.Error("테마의 SVG 를 문서에 끼워 넣었다 — <img> 로만 그려야 한다")
	}
}

// 테마가 로고를 지정하지 않으면 관리자 화면은 기본 마크를, 공개 화면은 기본 아이콘을 쓴다 (위 시험의 짝).
func TestDefaultBrandWithoutThemeLogo(t *testing.T) {
	srv, pool := liveSite(t)
	_, home := mustGet(t, client(), srv.URL+"/")
	if !strings.Contains(home, `<link rel="icon" href="data:image/svg+xml`) || strings.Contains(home, `<img class="site-logo"`) {
		t.Error("내장 테마의 기본 파비콘·이름만 머리글이 아니다")
	}
	c, _ := adminSession(t, srv, pool)
	code, admin := mustGet(t, c, srv.URL+"/admin/")
	if code != http.StatusOK || !strings.Contains(admin, `class="adm-mark"`) || strings.Contains(admin, `<img class="adm-logo-img"`) {
		i := strings.Index(admin, "adm-logo")
		t.Errorf("관리자 화면(HTTP %d)이 기본 마크를 쓰지 않았다: %.200s", code, admin[max(0, i):])
	}
}
