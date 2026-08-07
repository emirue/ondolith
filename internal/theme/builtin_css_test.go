package theme

import (
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// **내장 테마가 쓰는 클래스는 전부 스타일이 있어야 한다.**
//
// 규칙이 없는 클래스는 화면에서 조용히 사라진다 — 마크업은 그려지고 브라우저는
// 오류를 내지 않으며 테스트는 200 을 받는다. 그래서 게시판 목록의 표가 스타일
// 없이 한 줄로 뭉쳐 있는 동안 900건이 전부 초록이었다. 「화면이 응답한다」와
// 「화면이 쓸 만하다」는 다른 것이고, 후자를 자동으로 볼 수 있는 최소한이 이것이다.
//
// 반대 방향(안 쓰는 규칙)은 보지 않는다. 디스크 테마가 자기 마크업에서 코어의
// 클래스를 재사용할 수 있고(D17 폴백), 그것은 죽은 코드가 아니다.
func TestEveryBuiltinClassHasAStyle(t *testing.T) {
	used := classesInTemplates(t)
	defined := classesInStylesheet(t)

	if len(used) < 30 {
		t.Fatalf("템플릿에서 클래스를 %d개밖에 못 찾았다 — 검사가 헛돌았다", len(used))
	}
	if len(defined) < 20 {
		t.Fatalf("스타일시트에서 클래스를 %d개밖에 못 찾았다 — 검사가 헛돌았다", len(defined))
	}

	var missing []string
	for _, c := range used {
		if !slices.Contains(defined, c) {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		t.Errorf("스타일이 없는 클래스 %d개 — 그 화면은 스타일 없이 그려진다:\n  %s",
			len(missing), strings.Join(missing, " "))
	}
}

// classAttr 는 `class="..."` 를 집는다. 템플릿 표현식이 섞인 값도 있으므로
// (`class="{{if .ParentID}}reply{{end}}"`) 값을 통째로 꺼낸 뒤 걸러 낸다.
var (
	classAttr = regexp.MustCompile(`class="([^"]*)"`)
	cssClass  = regexp.MustCompile(`\.([a-z][a-z0-9-]*)`)
	plainWord = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

func classesInTemplates(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	err := fs.WalkDir(Builtin(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".html" {
			return err
		}
		b, err := fs.ReadFile(Builtin(), p)
		if err != nil {
			return err
		}
		for _, m := range classAttr.FindAllStringSubmatch(string(b), -1) {
			for _, c := range strings.Fields(m[1]) {
				// 템플릿 표현식은 클래스 이름이 아니다. `{{if …}}x{{end}}` 의
				// 조각이 그대로 들어오면 이름이 아닌 것을 요구하게 된다.
				if plainWord.MatchString(c) {
					seen[c] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sortedKeys(seen)
}

func classesInStylesheet(t *testing.T) []string {
	t.Helper()
	b, err := fs.ReadFile(Builtin(), "static/css/style.css")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, m := range cssClass.FindAllStringSubmatch(string(b), -1) {
		seen[m[1]] = true
	}
	return sortedKeys(seen)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
