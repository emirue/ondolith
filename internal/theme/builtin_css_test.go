package theme

import (
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
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

// classAttr·plainWord 는 두 검사가 함께 쓴다.
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
	// **주석을 먼저 걷어낸다.** 주석 속 `style.css` 와 `app.defaultAdminTheme`
	// 이 `.css`·`.default` 로 읽혀 없는 클래스가 잡혔다.
	seen := map[string]bool{}
	for _, m := range cssClass.FindAllStringSubmatch(stripComments(string(b)), -1) {
		seen[m[1]] = true
	}
	return sortedKeys(seen)
}

// condClass 는 `{{…}}` 사이에 낀 평범한 낱말을 집는다 — 조건부 클래스다.
var condClass = regexp.MustCompile(`\}\}([a-z][a-z0-9-]*)\{\{`)

var cssComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

func stripComments(css string) string { return cssComment.ReplaceAllString(css, " ") }

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

// **규칙만 있고 아무 템플릿도 안 쓰는 클래스가 없어야 한다.**
//
// 앞의 검사(TestEveryBuiltinClassHasAStyle)는 한 방향만 본다 — 템플릿이 쓰는
// 것에 규칙이 있는가. 반대 방향은 **계획된 화면이 만들어지지 않은 것**을
// 가리킨다: `.hero-eyebrow`·`.cards`·`.card-list` 에 규칙이 있는데 홈이 그것을
// 안 쓰고 있었고, 그래서 첫 화면이 사이트 이름 하나뿐이었다. 한 방향만 보는
// 검사가 그 공백을 못 봤다.
//
// **템플릿이 이름을 조립하는 경우를 빼야 한다.** `class="social-btn--{{.Provider}}"`
// 는 실행 시 `social-btn--google` 이 되지만 정적 스캔에는 안 보인다 — 그것을
// 미사용으로 보고하면 검사가 오탐을 내고, 오탐이 나는 검사는 곧 무시된다.
// 접두사가 쓰이면 그 접두사로 시작하는 규칙은 쓰인 것으로 본다.
func TestNoStyleIsWrittenForAScreenThatDoesNotExist(t *testing.T) {
	used := map[string]bool{}
	prefixes := []string{}
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
				if plainWord.MatchString(c) {
					used[c] = true
					continue
				}
				// `social-btn--{{.Provider}}` → 접두사 `social-btn--`
				if i := strings.Index(c, "{{"); i > 0 {
					prefixes = append(prefixes, c[:i])
				}
				// `{{if .ParentID}}reply{{end}}` — 조건부로 붙는 이름도 쓰인다.
				for _, w := range condClass.FindAllStringSubmatch(c, -1) {
					used[w[1]] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	defined := classesInStylesheet(t)
	if len(used) < 30 || len(defined) < 20 {
		t.Fatalf("사용 %d · 정의 %d — 검사가 헛돌았다", len(used), len(defined))
	}

	var orphan []string
	for _, c := range defined {
		if used[c] {
			continue
		}
		covered := false
		for _, pre := range prefixes {
			if strings.HasPrefix(c, pre) {
				covered = true
				break
			}
		}
		if !covered {
			orphan = append(orphan, c)
		}
	}
	if len(orphan) > 0 {
		t.Errorf("규칙만 있고 아무 템플릿도 안 쓰는 클래스 %d개 — 계획된 화면이 없는 것이다:\n  %s",
			len(orphan), strings.Join(orphan, " "))
	}
}

// **같은 블록이 한 템플릿에 두 번 나오지 않는다.**
//
// 로그인 화면에 소셜 로그인 블록이 통째로 두 번 있었다 — 붙여넣기 사고다.
// 버튼이 6개로 그려지는데 둘 다 동작하므로 오류가 나지 않고, 응답 코드도
// 200 이다. 눈으로 세기 전에는 안 보인다.
//
// 한 화면에 한 번만 나와야 하는 블록의 클래스를 센다. 목록으로 두는 이유는
// 「모든 클래스가 한 번씩」이 참이 아니기 때문이다 — `.card-list` 처럼 여러
// 번 나오는 것이 정상인 것도 있다.
func TestSingletonBlocksAppearOnce(t *testing.T) {
	once := []string{"social-login", "auth", "hero", "totals"}
	var dup, scanned []string
	err := fs.WalkDir(Builtin(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".html" {
			return err
		}
		b, err := fs.ReadFile(Builtin(), p)
		if err != nil {
			return err
		}
		scanned = append(scanned, p)
		for _, c := range once {
			if n := strings.Count(string(b), `class="`+c+`"`); n > 1 {
				dup = append(dup, p+" "+c+"×"+strconv.Itoa(n))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(scanned) < 10 {
		t.Fatalf("템플릿을 %d개밖에 못 읽었다 — 검사가 헛돌았다", len(scanned))
	}
	// 검사가 헛돌지 않는지: 목록의 클래스가 실제로 어딘가 쓰이는가.
	used := classesInTemplates(t)
	for _, c := range once {
		if !slices.Contains(used, c) {
			t.Fatalf("%q 를 쓰는 템플릿이 없다 — 목록이 낡았고 검사가 헛돈다", c)
		}
	}
	if len(dup) > 0 {
		t.Errorf("한 번만 나와야 하는 블록이 여러 번 있다: %v", dup)
	}
}
