package app

import (
	"context"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/config"
	"github.com/emirue/ondolith/internal/theme"
)

// D41 을 실제 서버로: admin.json 이 있는 디스크 테마를 켜면 사이드바에 화면이 붙고, 저장한
// 값이 공개 화면의 .Theme 으로 나온다. 테마를 바꿨다 되돌려도 값은 남는다(지배 원칙 2).
func TestThemeScreensReachTheMenuAndThePage(t *testing.T) {
	_, pool := liveSite(t)
	ctx := context.Background()
	root := t.TempDir()
	base, err := fs.ReadFile(theme.Builtin(), "base.html")
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"decl/base.html": string(base),
		"decl/admin.json": `{"screens": [
			{"key": "hero", "title": "메인 배너", "group": "디자인", "kind": "settings", "fields": [
				{"key": "headline", "label": "큰 제목", "type": "text", "default": "환영합니다"},
				{"key": "count", "label": "숫자", "type": "number"},
				{"key": "tone", "label": "글자색", "type": "select", "options": ["밝게", "어둡게"], "default": "밝게"}]},
			{"key": "partners", "title": "협력사", "group": "디자인", "kind": "collection", "fields": [
				{"key": "name", "label": "이름", "type": "text", "required": true}]}]}`,
		"decl/home.html": `{{define "body"}}<p id="t-head">{{.Theme.hero.headline}}</p><p id="t-count">{{.Theme.hero.count}}</p>` +
			`<p id="t-tone">{{.Theme.hero.tone}}</p>{{range .Theme.partners}}<i>{{.name}}</i>{{end}}{{end}}`,
	} {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO settings (key, value) VALUES ('theme.active', 'decl')
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`); err != nil {
		t.Fatal(err)
	}
	srv := restartOnSameSchema(t, func(c *config.Config) { c.ThemeDir = root })

	// 저장 전: 선언의 기본값.
	if code, home := mustGet(t, client(), srv.URL+"/"); code != http.StatusOK ||
		!strings.Contains(home, `<p id="t-head">환영합니다</p>`) || !strings.Contains(home, `<p id="t-tone">밝게</p>`) ||
		!strings.Contains(home, `<p id="t-count"></p>`) {
		t.Fatalf("기본값이 나와야 한다: HTTP %d\n%s", code, home)
	}

	c, post := adminSession(t, srv, pool)
	_, dash := mustGet(t, c, srv.URL+"/admin/")
	if !strings.Contains(dash, `href="/admin/theme/hero"`) || !strings.Contains(dash, "디자인") {
		t.Errorf("사이드바에 테마 화면이 없다")
	}
	if code, page := mustGet(t, c, srv.URL+"/admin/theme/hero"); code != http.StatusOK || !strings.Contains(page, "메인 배너") ||
		!strings.Contains(page, `placeholder="환영합니다"`) {
		t.Errorf("설정 화면: HTTP %d (제목·기본값 placeholder)", code)
	}

	for _, step := range []struct {
		path string
		form url.Values
	}{
		{"/admin/theme/hero", url.Values{"headline": {"온돌 봄 세일"}, "count": {"1000000"}, "tone": {""}}},
		{"/admin/theme/partners/new", url.Values{"name": {"가나상사"}, "_sort": {"1"}}},
	} {
		resp := post(step.path, step.form)
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST %s = HTTP %d", step.path, resp.StatusCode)
		}
	}
	_, home := mustGet(t, client(), srv.URL+"/")
	for _, want := range []string{`<p id="t-head">온돌 봄 세일</p>`, `<p id="t-count">1000000</p>`,
		`<p id="t-tone">밝게</p>`, `<i>가나상사</i>`} {
		if !strings.Contains(home, want) {
			t.Errorf("공개 화면에 %s 가 없다", want)
		}
	}

	// 내장 테마로 바꾸면 메뉴에서 빠지고, 값은 DB 에 남는다.
	resp := post("/admin/themes", url.Values{"theme": {""}})
	resp.Body.Close()
	if _, dash = mustGet(t, c, srv.URL+"/admin/"); strings.Contains(dash, "/admin/theme/hero") {
		t.Error("내장 테마인데 테마 화면 메뉴가 남았다")
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM theme_settings) + (SELECT count(*) FROM theme_items)`).Scan(&n); err != nil || n != 2 {
		t.Errorf("테마를 바꿨더니 값이 %d행 (%v), want 2", n, err)
	}
	resp = post("/admin/themes", url.Values{"theme": {"decl"}})
	resp.Body.Close()
	if _, home = mustGet(t, client(), srv.URL+"/"); !strings.Contains(home, "온돌 봄 세일") || !strings.Contains(home, "가나상사") {
		t.Error("테마를 되돌렸는데 값이 돌아오지 않았다")
	}
}

// 리뷰 #6: 값을 읽지 못해도 .Theme 은 선언된 키와 기본값을 갖는다 (D17 규약 1). 빈 맵이면 기본값이
// 사라지고, 테마가 `len`·`index` 를 쓰면 페이지가 통째로 깨진다.
func TestThemeDataKeepsDefaultsWhenTheValuesCannotBeRead(t *testing.T) {
	_, pool := liveSite(t)
	root := t.TempDir()
	base, err := fs.ReadFile(theme.Builtin(), "base.html")
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"decl/base.html":  string(base),
		"decl/admin.json": `{"screens": [{"key": "hero", "title": "배너", "kind": "settings", "fields": [{"key": "headline", "label": "제목", "type": "text", "default": "환영합니다"}]}]}`,
		"decl/home.html":  `{{define "body"}}<p id="t-head">{{.Theme.hero.headline}}</p>{{end}}`,
	} {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO settings (key, value) VALUES ('theme.active', 'decl')
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`); err != nil {
		t.Fatal(err)
	}
	srv := restartOnSameSchema(t, func(c *config.Config) { c.ThemeDir = root })
	// 읽기를 실패시킨다 — 표가 없으면 질의가 오류다.
	if _, err := pool.Exec(ctx, `ALTER TABLE theme_settings RENAME TO theme_settings_gone`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `ALTER TABLE theme_settings_gone RENAME TO theme_settings`)
	})
	if code, home := mustGet(t, client(), srv.URL+"/"); code != http.StatusOK || !strings.Contains(home, `<p id="t-head">환영합니다</p>`) {
		t.Errorf("읽기 실패 때 기본값이 나와야 한다: HTTP %d", code)
	}
}
