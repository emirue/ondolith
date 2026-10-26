package app

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/theme"
)

// FR-312: 공개 화면 템플릿이 그리다 실패하면 반쪽 페이지가 200 으로 나가지 않는다 — 버퍼에 다
// 그린 뒤 보내고, 실패하면 500 과 코어 내장 오류 화면이다. 테마가 실패 전에 쓴 조각은 한 바이트도
// 나가지 않는다. `.Theme` 을 쓰는 홈을 v0.4.0 으로 띄운 것(D41 11.4)이 바로 이 경우였다.
func TestThemeTemplateFailureIsA500WithTheBuiltinErrorPage(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		// 없는 필드 — 실행 중에야 실패한다. 실패 전에 조각을 이미 썼다.
		"home.html":         `{{define "body"}}<p id="theme-fragment">테마 조각</p>{{.Nope}}{{end}}`,
		"shop/variant.html": `{{define "body"}}<p id="theme-fragment">조각</p>{{.Nope}}{{end}}`,
		"page.html":         `{{define "body"}}<p id="t-ok">멀쩡한 화면</p>{{end}}`,
		// 테마의 오류 화면도 같은 이유로 깨져 있다 — 오류 화면은 코어 내장이어야 한다.
		"error.html": `{{define "body"}}<p id="theme-fragment">테마 오류 화면</p>{{.Nope}}{{end}}`,
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	funcs := theme.FuncMap(theme.Deps{URLFor: urlFor})
	active := theme.New(theme.Builtin(), dir, false, funcs)
	var logs bytes.Buffer
	d := &publicDeps{
		loader:  func() *theme.Loader { return active },
		builtin: theme.New(theme.Builtin(), "", false, funcs),
		log:     slog.New(slog.NewTextHandler(&logs, nil)),
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	v := theme.NewView(theme.Site{Name: "온돌"}, "/")

	for _, tc := range []struct {
		template string
		render   func(w http.ResponseWriter)
		page     bool
	}{
		{"home.html", func(w http.ResponseWriter) { d.renderPage(w, r, "home.html", http.StatusOK, v) }, true},
		{"shop/variant.html", func(w http.ResponseWriter) { d.renderPartial(w, r, "shop/variant.html", v) }, false},
	} {
		logs.Reset()
		rec := httptest.NewRecorder()
		tc.render(rec)
		body := rec.Body.String()
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s: HTTP %d, want 500", tc.template, rec.Code)
		}
		if strings.Contains(body, "theme-fragment") {
			t.Errorf("%s: 실패한 테마의 조각이 나갔다\n%s", tc.template, body)
		}
		if !strings.Contains(body, "<h1>일시적인 오류</h1>") {
			t.Errorf("%s: 내장 오류 화면이 아니다\n%s", tc.template, body)
		}
		// 화면은 문서 전체, 조각 응답은 조각 — htmx 가 끼워 넣을 자리에 문서가 들어가지 않는다.
		if got := strings.Contains(body, "</html>"); got != tc.page {
			t.Errorf("%s: 문서 전체 여부 = %v, want %v", tc.template, got, tc.page)
		}
		// 원인은 로그로만 (NFR-210) — 방문자에게 템플릿 구조를 보여 주지 않는다.
		if strings.Contains(body, "Nope") {
			t.Errorf("%s: 오류 원문이 화면에 나갔다", tc.template)
		}
		if l := logs.String(); !strings.Contains(l, "level=ERROR") || !strings.Contains(l, "template="+tc.template) ||
			!strings.Contains(l, "Nope") {
			t.Errorf("%s: ERROR 로그에 템플릿 이름과 오류가 없다: %s", tc.template, l)
		}
	}

	// 정상 화면의 출력은 바뀌지 않는다 — 버퍼를 거쳐도 상태와 바이트가 그대로다.
	rec := httptest.NewRecorder()
	d.renderPage(rec, r, "page.html", http.StatusNotFound, v)
	var direct bytes.Buffer
	if err := active.Render(&direct, "page.html", v); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusNotFound || rec.Body.String() != direct.String() || !strings.Contains(direct.String(), `id="t-ok"`) {
		t.Errorf("정상 화면: HTTP %d, 직접 그린 것과 같은가 %v", rec.Code, rec.Body.String() == direct.String())
	}
}
