package theme

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeStatic puts files under dir/static.
func writeStatic(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		p := filepath.Join(dir, "static", filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// **테마가 theme.json 에 로고·파비콘을 지정하면 그 자산 주소를 준다** (FR-310). 관리자 화면과 공개 화면이
// 이 값을 쓴다. 경로는 static/ 아래의 이미지 파일만이다 — 밖을 가리키거나(../), 이미지가 아니거나, 파일이
// 없으면 비운다(기본 마크·아이콘을 쓴다). 내장 테마만 쓰는 사이트는 지정이 없다.
func TestBrandComesFromTheManifest(t *testing.T) {
	dir := themeDir(t, `{"name":"브랜드","logo":"img/logo.svg","favicon":"img/fav.png"}`)
	writeStatic(t, dir, "img/logo.svg", "img/fav.png")
	b := New(Builtin(), dir, false, FuncMap(Deps{})).Brand()
	if !strings.HasPrefix(b.Logo, "/static/img/logo.svg") || !strings.HasPrefix(b.Favicon, "/static/img/fav.png") {
		t.Errorf("Brand = %+v, want /static/img/logo.svg·/static/img/fav.png", b)
	}

	for name, manifest := range map[string]string{
		"밖을 가리킨다":  `{"logo":"../secret.png","favicon":"/etc/fav.png"}`,
		"이미지가 아니다": `{"logo":"img/logo.html","favicon":"img/fav.js"}`,
		"파일이 없다":   `{"logo":"img/none.svg","favicon":"img/none.png"}`,
	} {
		d := themeDir(t, manifest)
		writeStatic(t, d, "img/logo.html", "img/fav.js")
		if b := New(Builtin(), d, false, FuncMap(Deps{})).Brand(); b.Logo != "" || b.Favicon != "" {
			t.Errorf("%s: Brand = %+v, want 빈 값", name, b)
		}
	}
	if b := New(Builtin(), "", false, FuncMap(Deps{})).Brand(); b != (Brand{}) {
		t.Errorf("내장 테마만 = %+v, want 빈 값", b)
	}
}

// 지정이 틀리면 활성화(A-202)가 경고한다 — 테마 작성자가 「로고가 왜 안 나오지」를 여기서 안다. 거부하지는
// 않는다(D17 규칙 2: 필수는 base.html 하나).
func TestBrandMistakesAreWarnedAtActivation(t *testing.T) {
	dir := themeDir(t, `{"logo":"img/none.svg","favicon":"../fav.ico"}`)
	warn, err := ValidateThemeDir(dir, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warn, "logo") || !strings.Contains(warn, "favicon") {
		t.Errorf("경고 = %q, want logo·favicon 둘 다", warn)
	}
}
