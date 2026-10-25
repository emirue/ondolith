package theme

import (
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Brand is the active theme's logo and favicon (FR-310): 자산 주소이고, 테마가 지정하지 않았거나
// 지정이 틀리면 빈 값이다 — 그때 관리자 화면과 내장 테마는 기본 마크·아이콘을 쓴다.
type Brand struct {
	Logo    string
	Favicon string
}

// 이미지 확장자만 받는다. 로고는 <img> 로만 그린다 — 남이 만든 테마의 SVG 를 문서에 끼워 넣으면 그 안의
// 스크립트가 사이트 출처로 돈다.
var (
	logoExts    = map[string]bool{".svg": true, ".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true}
	faviconExts = map[string]bool{".ico": true, ".png": true, ".svg": true}
)

// Brand reads theme.json's logo and favicon (static/ 아래 경로). 개발 모드가 아니면 테마를 바꿀 때마다
// 로더가 새로 만들어지므로 한 번 읽으면 된다 — 개발 모드는 theme.json 을 고치면 바로 보이게 매번 읽는다.
func (l *Loader) Brand() Brand {
	switch {
	case l.dir == "":
		return Brand{}
	case l.dev:
		return l.readBrand()
	}
	l.brandOnce.Do(func() { l.brand = l.readBrand() })
	return l.brand
}

func (l *Loader) readBrand() Brand {
	m, err := ReadManifest(l.dir)
	if err != nil {
		return Brand{}
	}
	return Brand{Logo: l.brandURL(m.Logo, logoExts), Favicon: l.brandURL(m.Favicon, faviconExts)}
}

// brandURL is name's asset URL when it is an existing image under static/, else "".
func (l *Loader) brandURL(name string, exts map[string]bool) string {
	if !brandOK(l.dir, name, exts) {
		return ""
	}
	return l.AssetURL(name)
}

// brandOK reports whether name is an image file under dir/static — 경로 검사는 정적 자산(P-906)과 같고,
// OpenInRoot 가 심볼릭 링크로 테마 밖에 나가는 것을 막는다.
func brandOK(dir, name string, exts map[string]bool) bool {
	if name == "" || validName(name) != nil || !exts[strings.ToLower(path.Ext(name))] {
		return false
	}
	f, err := os.OpenInRoot(dir, filepath.Join("static", filepath.FromSlash(name)))
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	return err == nil && st.Mode().IsRegular()
}

// brandWarnings is what ValidateThemeDir says about a wrong logo/favicon — 거부하지 않고 알린다.
func brandWarnings(dir string, m Manifest) []string {
	var out []string
	if m.Logo != "" && !brandOK(dir, m.Logo, logoExts) {
		out = append(out, "theme.json 의 logo 가 static/ 안의 이미지 파일(svg·png·jpg·webp·gif)이 아니다 — 기본 마크가 나온다")
	}
	if m.Favicon != "" && !brandOK(dir, m.Favicon, faviconExts) {
		out = append(out, "theme.json 의 favicon 이 static/ 안의 아이콘 파일(ico·png·svg)이 아니다 — 기본 아이콘이 나온다")
	}
	return out
}
