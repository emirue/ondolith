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

// **좁은 화면 블록은 파일 맨 끝에 있어야 한다.**
//
// 미디어쿼리는 특정도를 올려 주지 않는다. 같은 특정도의 규칙이 뒤에 오면
// 그것이 이긴다 — 그래서 `@media (max-width: 767px)` 가 파일 중간에 있으면
// 그 아래 규칙들이 좁은 화면 설정을 조용히 덮는다. 실제로 그 상태였고, 게시판
// 검색칸의 `flex` 되돌림이 무시돼 입력칸이 12rem 높이 상자로 늘어났다.
//
// 화면을 보지 않으면 알 수 없는 종류라, 파일 순서로 고정한다.
func TestNarrowScreenRulesComeLast(t *testing.T) {
	b, err := fs.ReadFile(Builtin(), "static/css/style.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)

	at := strings.Index(css, "@media (max-width:")
	if at < 0 {
		t.Fatal("좁은 화면 블록이 없다 — 검사가 헛돌았다")
	}
	// 그 뒤에 미디어쿼리 밖 규칙이 있으면 안 된다. 중괄호 깊이가 0 인 자리에서
	// 선택자가 시작하면 그것이 최상위 규칙이다.
	rest := css[at:]
	depth := 0
	line := 0
	for _, ln := range strings.Split(rest, "\n") {
		line++
		trimmed := strings.TrimSpace(ln)
		if depth == 0 && line > 1 && trimmed != "" &&
			!strings.HasPrefix(trimmed, "/*") && !strings.HasPrefix(trimmed, "*") &&
			!strings.HasPrefix(trimmed, "@") && !strings.HasPrefix(trimmed, "}") {
			t.Errorf("좁은 화면 블록 뒤에 최상위 규칙이 있다 (%q) — 그 규칙이 좁은 화면 설정을 덮는다",
				trimmed)
			break
		}
		depth += strings.Count(ln, "{") - strings.Count(ln, "}")
	}
}

// **같은 선택자를 두 번 적지 않는다.**
//
// 나중 것이 앞 것을 부분적으로 덮으므로, 한 요소의 모양이 두 곳에 흩어진다 —
// 고칠 때 한쪽만 보고 고치면 나머지 절반이 남는다. 실제로 `.sort` 와
// `.adm-logo` 가 그랬다.
//
// **중괄호 깊이 0 의 단일 선택자만** 본다. 미디어쿼리 안에서 같은 선택자를
// 다시 적는 것은 재정의가 목적이므로 중복이 아니다.
func TestNoSelectorIsDefinedTwice(t *testing.T) {
	b, err := fs.ReadFile(Builtin(), "static/css/style.css")
	if err != nil {
		t.Fatal(err)
	}
	seen, dup := map[string]int{}, []string{}
	depth := 0
	for _, ln := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(ln)
		if depth == 0 {
			if m := topLevelRule.FindStringSubmatch(trimmed); m != nil {
				seen[m[1]]++
				if seen[m[1]] == 2 {
					dup = append(dup, m[1])
				}
			}
		}
		depth += strings.Count(ln, "{") - strings.Count(ln, "}")
	}
	if len(seen) < 20 {
		t.Fatalf("최상위 규칙을 %d개밖에 못 찾았다 — 검사가 헛돌았다", len(seen))
	}
	if len(dup) > 0 {
		t.Errorf("같은 선택자가 두 번 정의됐다 — 모양이 두 곳에 흩어진다: %v", dup)
	}
}

// 여러 선택자를 쉼표로 묶은 규칙은 대상이 아니다. 겹치는 것이 정상이고,
// 그것까지 잡으면 검사가 늘 울린다.
var topLevelRule = regexp.MustCompile(`^(\.[a-z][a-z0-9-]*)\s*\{`)

// **시각은 `date` 로 낸다.**
//
// `time.Time` 을 그대로 그리면 `2026-08-07 23:54:32.711708 +0900 KST` 가 나온다.
// 나노초와 타임존 이름은 사람이 읽으라고 있는 것이 아니고, 화면마다 형식이
// 다르면 같은 주문의 시각이 목록과 상세에서 달라 보인다. 배송 조회에서 실제로
// 그 문자열이 그려지고 있었다.
func TestTemplatesFormatTimes(t *testing.T) {
	// `…At` / `…Date` 로 끝나는 필드를 그대로 찍는 곳.
	raw := regexp.MustCompile(`\{\{\s*[.$][A-Za-z0-9.]*(At|Date)\s*\}\}`)
	var bad, scanned []string
	err := fs.WalkDir(Builtin(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".html" {
			return err
		}
		b, err := fs.ReadFile(Builtin(), p)
		if err != nil {
			return err
		}
		scanned = append(scanned, p)
		if m := raw.FindString(string(b)); m != "" {
			bad = append(bad, p+" "+m)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(scanned) < 10 {
		t.Fatalf("템플릿을 %d개밖에 못 읽었다 — 검사가 헛돌았다", len(scanned))
	}
	if len(bad) > 0 {
		t.Errorf("시각을 date 없이 그리는 템플릿: %v", bad)
	}
}
