package app

import (
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/theme"
)

// **관리자 템플릿은 값을 최상위에서 읽는다.**
//
// adminRenderer.Render 는 핸들러가 준 map 을 뷰 모델에 **병합**한다 — `.Data`
// 라는 것은 만들지 않는다. 그런데 커머스 화면 7개가 프론트 테마의 관례를 따라
// `{{with .Data}}` 로 감싸고 있었다. `with` 는 nil 이면 블록을 통째로 건너뛰므로
// **상품·주문·환불·반품·배송·카테고리 화면이 제목만 남고 비어 있었다.**
//
// 응답은 200 이고 템플릿 파싱도 성공한다. `TestEveryAdminScreenRenders` 가
// 초록이었던 이유가 그것이다 — 그 검사는 상태 코드를 볼 뿐 내용을 보지 않는다.
// 계약 위반은 렌더가 아니라 **소스에서** 잡아야 한다.
// ownH1 은 화면이 스스로 그리는 최상위 제목이다. 레이아웃 안의 것과 구별하려고
// 줄 처음에 오는 것만 본다.
var ownH1 = regexp.MustCompile(`(?m)^\s*<h1[ >]`)

func TestAdminTemplatesUseTheRendererContract(t *testing.T) {
	var withData, ownH1Names, bareTable, scanned []string

	err := fs.WalkDir(adminFS, "templates/admin", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".html" {
			return err
		}
		b, err := adminFS.ReadFile(p)
		if err != nil {
			return err
		}
		name := path.Base(p)
		scanned = append(scanned, name)
		body := string(b)

		if strings.Contains(body, ".Data") {
			withData = append(withData, name)
		}
		// 레이아웃이 `<h1>{{.Title}}</h1>` 를 그린다. 화면이 또 그리면 제목이
		// 두 번 나온다 — 실제로 커머스 화면 전부가 그랬다.
		if name != "layout.html" && ownH1.MatchString(body) {
			ownH1Names = append(ownH1Names, name)
		}
		// 클래스 없는 `<table>` 은 스타일이 붙지 않는다. 관리자 CSS 는
		// `.adm-table` 에만 규칙을 두므로, 표가 브라우저 기본 모양으로
		// 그려진다 — 커머스 화면 5개가 그랬다.
		if strings.Contains(body, "<table>") {
			bareTable = append(bareTable, name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(scanned) < 10 {
		t.Fatalf("관리자 템플릿을 %d개밖에 못 읽었다 — 검사가 헛돌았다", len(scanned))
	}
	if len(withData) > 0 {
		t.Errorf(".Data 를 읽는 템플릿 — 렌더러는 그것을 만들지 않는다. 화면이 비어 나온다: %v",
			withData)
	}
	if len(ownH1Names) > 0 {
		t.Errorf("자기 <h1> 을 그리는 템플릿 — layout.html 이 이미 그린다: %v", ownH1Names)
	}
	if len(bareTable) > 0 {
		t.Errorf("클래스 없는 <table> — .adm-table 이 아니면 스타일이 붙지 않는다: %v",
			bareTable)
	}
}

// **관리자 스타일시트도 폼 요소를 요소 선택자로 잡아야 한다.**
//
// 규칙이 `.adm-field`·`.adm-filterbar` 안에만 있었고, 그 밖의 입력칸은 브라우저
// 기본 모양이었다 — 13개 화면의 입력 요소 40개가 그랬다. 컨테이너 안에 있는지는
// 화면마다 다르고, 새 화면이 그 컨테이너를 안 쓰면 다시 벌어진다.
//
// 좁은 화면 블록이 맨 끝인지도 함께 본다: 미디어쿼리는 특정도를 올려 주지
// 않으므로, 뒤에 오는 규칙이 같은 특정도로 덮으면 조용히 무시된다
// (프론트 테마에서 실제로 그랬다 — internal/theme 의 같은 이름 검사).
func TestAdminStylesheetCoversFormElementsAndOrdersMediaLast(t *testing.T) {
	b, err := adminFS.ReadFile("templates/admin/admin.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)

	for _, sel := range []string{"\ninput,", "\nselect,", "\ntextarea {"} {
		if !strings.Contains(css, sel) {
			t.Errorf("요소 선택자 %q 가 없다 — 컨테이너 밖 입력칸에 스타일이 붙지 않는다",
				strings.TrimSpace(sel))
		}
	}

	at := strings.Index(css, "@media (max-width:")
	if at < 0 {
		t.Fatal("좁은 화면 블록이 없다 — 검사가 헛돌았다")
	}
	depth, line := 0, 0
	for _, ln := range strings.Split(css[at:], "\n") {
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

// **관리자 화면도 금액을 `money` 로 낸다.**
//
// 프론트 테마에는 같은 검사가 있었지만(internal/theme) 관리자는 없었고, 실제로
// 주문 목록·상세·환불·반품·상품 화면이 `378000` 을 그대로 찍고 있었다. 앞뒤
// 화면에서 같은 주문의 금액이 다르게 보이면 그것이 환불액을 잘못 넣는 경로다.
//
// 금액으로 보이는 필드 이름을 목록으로 둔다 — 관리자 템플릿은 뷰 모델이
// 화면마다 달라서 「모든 정수」로는 잡을 수 없다.
func TestAdminTemplatesFormatMoney(t *testing.T) {
	// 이름이 금액인 것들. 새 화면이 다른 이름을 쓰면 여기 더한다.
	money := regexp.MustCompile(
		`\{\{\s*[.$][A-Za-z0-9.]*(Total|Amount|Price|Refunded|Paid|Fee|Diff|Delta|Approved|Remaining|Discount|Balance)\s*\}\}`)
	// 시각도 같다 — `time.Time` 을 그대로 찍으면 나노초와 타임존 이름이 나온다.
	when := regexp.MustCompile(`\{\{\s*[.$][A-Za-z0-9.]*(At|Date)\s*\}\}`)
	// 입력칸의 `value=` 는 예외다. 사람이 고칠 숫자는 쉼표가 붙으면 안 된다 —
	// 폼이 그 값을 다시 받아 파싱하기 때문이다.
	valueAttr := regexp.MustCompile(`value="\{\{[^}]*\}\}"`)

	var raw, scanned []string
	err := fs.WalkDir(adminFS, "templates/admin", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".html" {
			return err
		}
		b, err := adminFS.ReadFile(p)
		if err != nil {
			return err
		}
		scanned = append(scanned, path.Base(p))
		body := valueAttr.ReplaceAllString(string(b), "")
		if m := money.FindString(body); m != "" {
			raw = append(raw, path.Base(p)+" "+m)
		}
		if m := when.FindString(body); m != "" {
			raw = append(raw, path.Base(p)+" "+m+" (date 필요)")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(scanned) < 10 {
		t.Fatalf("관리자 템플릿을 %d개밖에 못 읽었다 — 검사가 헛돌았다", len(scanned))
	}
	if len(raw) > 0 {
		t.Errorf("금액·시각을 money/date 없이 그리는 관리자 템플릿: %v", raw)
	}
}

// **관리자 템플릿이 쓰는 클래스는 전부 스타일이 있어야 한다.**
//
// 규칙이 없는 클래스는 조용히 무시된다 — 마크업은 그려지고 브라우저는 오류를
// 내지 않으며 핸들러는 200 을 돌려준다. 환불 화면의 「환불할 품목」 상자와
// 체크박스 줄이 그 상태였다. 프론트 테마에는 같은 검사가 있었는데
// (internal/theme) 관리자에는 없었다.
func TestEveryAdminClassHasAStyle(t *testing.T) {
	classAttr := regexp.MustCompile(`class="([^"]*)"`)
	cssClass := regexp.MustCompile(`\.([a-z][a-z0-9-]*)`)
	plainWord := regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	tmplExpr := regexp.MustCompile(`\{\{[^}]*\}\}`)

	used := map[string]bool{}
	err := fs.WalkDir(adminFS, "templates/admin", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".html" {
			return err
		}
		b, err := adminFS.ReadFile(p)
		if err != nil {
			return err
		}
		// **템플릿 표현식을 먼저 걷어낸다.** `class="a{{if eq $x "y"}} b{{end}}"`
		// 에서 `eq` 는 클래스가 아니라 함수 이름인데, 공백으로 자르면 클래스로
		// 보인다 — 그것 때문에 있지도 않은 클래스의 규칙을 쓸 뻔했다.
		body := tmplExpr.ReplaceAllString(string(b), " ")
		for _, m := range classAttr.FindAllStringSubmatch(body, -1) {
			for _, c := range strings.Fields(m[1]) {
				// 템플릿 표현식 조각은 클래스 이름이 아니다.
				if plainWord.MatchString(c) {
					used[c] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	css, err := adminFS.ReadFile("templates/admin/admin.css")
	if err != nil {
		t.Fatal(err)
	}
	defined := map[string]bool{}
	for _, m := range cssClass.FindAllStringSubmatch(string(css), -1) {
		defined[m[1]] = true
	}

	if len(used) < 20 || len(defined) < 20 {
		t.Fatalf("클래스를 사용 %d · 정의 %d 개밖에 못 찾았다 — 검사가 헛돌았다",
			len(used), len(defined))
	}
	var missing []string
	for c := range used {
		if !defined[c] {
			missing = append(missing, c)
		}
	}
	slices.Sort(missing)
	if len(missing) > 0 {
		t.Errorf("스타일이 없는 관리자 클래스 %d개 — 그 화면은 스타일 없이 그려진다:\n  %s",
			len(missing), strings.Join(missing, " "))
	}
}

// **폼을 구성하는 요소에도 규칙이 있어야 한다.**
//
// 클래스 검사만으로는 `<fieldset>`·`<legend>`·`<label>` 처럼 클래스를 안 붙이는
// 것들이 빠진다. 관리자 템플릿에 label 이 88개 있는데 규칙은 `.adm-field label`
// 뿐이었고, 그 밖의 것은 브라우저 기본이었다.
func TestAdminStylesheetStylesBareFormElements(t *testing.T) {
	css, err := adminFS.ReadFile("templates/admin/admin.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, el := range []string{"fieldset", "legend", "label", "dl"} {
		if !regexp.MustCompile(`(?m)^` + el + `[ ,{]`).Match(css) {
			t.Errorf("`%s` 에 요소 규칙이 없다 — 클래스를 안 붙이는 자리는 기본 모양으로 남는다", el)
		}
	}
}

// **`var()` 로 쓰는 토큰은 전부 정의돼 있다.**
//
// 정의되지 않은 토큰은 조용히 「값 없음」이 되고, 그 속성은 상속값이나 초깃값으로
// 그려진다 — 배경이 사라지거나 테두리가 검게 나오는데 오류는 없다. 실제로 첨부
// 목록의 배경이 `var(--surface-2, transparent)` 였고, 그런 토큰은 없어서 늘
// 투명이었다. 대체값이 있으면 더 조용하다.
//
// **두 스타일시트를 한 검사가 본다.** 관리자 화면과 테마는 팔레트를 공유하되
// 파일이 다르고, 판정을 두 벌로 두면 한쪽만 고쳐진다. 이 패키지가 양쪽을 다
// 읽을 수 있어 여기 있다.
func TestEveryCSSTokenIsDefined(t *testing.T) {
	adminCSS, err := adminFS.ReadFile("templates/admin/admin.css")
	if err != nil {
		t.Fatal(err)
	}
	themeCSS, err := fs.ReadFile(theme.Builtin(), "static/css/style.css")
	if err != nil {
		t.Fatal(err)
	}
	def := regexp.MustCompile(`(--[a-z0-9-]+)\s*:`)
	use := regexp.MustCompile(`var\((--[a-z0-9-]+)`)

	for name, src := range map[string]string{
		"admin/admin.css":   string(adminCSS),
		"builtin/style.css": string(themeCSS),
	} {
		defined := map[string]bool{}
		for _, m := range def.FindAllStringSubmatch(src, -1) {
			defined[m[1]] = true
		}
		uses := use.FindAllStringSubmatch(src, -1)
		if len(uses) < 20 {
			t.Fatalf("%s: var() 를 %d 개밖에 못 찾았다 — 검사가 헛돌았다", name, len(uses))
		}
		for _, m := range uses {
			if !defined[m[1]] {
				t.Errorf("%s: %s 를 쓰는데 정의가 없다 — 그 속성은 조용히 빈 값이 된다",
					name, m[1])
			}
		}
	}
}
