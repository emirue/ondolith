package app

import (
	"io/fs"
	"net/http"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/theme"
)

// 정적 자산은 본 트리 밖에서 나간다: 세션을 만들지 않고, `Vary: Cookie` 를
// 붙이지 않고, 해시 붙은 주소는 영구 캐시다. 보안 헤더는 그대로 진다.
func TestStaticAssetsSkipTheSessionAndAreCacheable(t *testing.T) {
	srv, _ := liveSite(t)
	names, err := fs.Glob(theme.Builtin(), "static/css/*.css")
	if err != nil || len(names) == 0 {
		t.Fatalf("내장 테마에 css 가 없다: %v", err)
	}
	path := "/" + names[0] + "?v=abcdef12"

	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s → HTTP %d", path, resp.StatusCode)
	}
	if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("Cache-Control = %q, want immutable (주소가 해시를 품는다)", got)
	}
	if strings.Contains(resp.Header.Get("Vary"), "Cookie") {
		t.Errorf("Vary = %q — 세션 미들웨어를 지났다", resp.Header.Get("Vary"))
	}
	if len(resp.Cookies()) != 0 {
		t.Errorf("정적 자산이 쿠키를 만들었다: %v", resp.Cookies())
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("보안 헤더가 빠졌다 — 트리 밖으로 옮기며 httpsec 을 잃었다")
	}

	// /sitemap.xml 도 캐시 힌트를 준다.
	resp, err = srv.Client().Get(srv.URL + "/sitemap.xml")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !strings.Contains(resp.Header.Get("Cache-Control"), "max-age") {
		t.Errorf("sitemap Cache-Control = %q", resp.Header.Get("Cache-Control"))
	}
}
