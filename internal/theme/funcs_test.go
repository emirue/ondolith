package theme

import (
	"bytes"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// D17: "여기 없는 것은 템플릿에서 쓸 수 없다". Both directions are asserted —
// a documented function that is missing breaks themes, and an undocumented one
// that is present becomes API we cannot remove later.
func TestFuncMapMatchesContract(t *testing.T) {
	got := FuncMap(Deps{})
	var have []string
	for k := range got {
		have = append(have, k)
	}
	sort.Strings(have)
	want := append([]string(nil), FuncNames...)
	sort.Strings(want)

	if len(have) != len(want) {
		t.Fatalf("함수 %d개, want %d개\n등록: %v\n계약: %v", len(have), len(want), have, want)
	}
	for i := range want {
		if have[i] != want[i] {
			t.Fatalf("함수맵이 D17 과 다르다\n등록: %v\n계약: %v", have, want)
		}
	}
}

// NFR-203: a theme must not be able to turn escaping off. template.HTML is a
// core-only tool; if a theme could reach it, one stored value becomes XSS.
func TestForbiddenFunctionsAreAbsent(t *testing.T) {
	m := FuncMap(Deps{})
	for _, name := range ForbiddenFuncNames {
		if _, present := m[name]; present {
			t.Errorf("%q 가 함수맵에 있다 — 테마가 이스케이프를 끌 수 있다 (NFR-203)", name)
		}
	}
}

// `can` and `isCurrent` were removed from the map, not renamed: both are
// per-request and a func map is bound at parse time, so either they freeze at
// the first request or they race. The view model carries `.Can` and `.Path`
// instead (D17). This asserts they are gone — a template still calling them
// fails to parse, which is the loud failure we want.
func TestPerRequestFuncsAreNotInTheMap(t *testing.T) {
	m := FuncMap(Deps{})
	for _, name := range []string{"can", "isCurrent"} {
		if _, ok := m[name]; ok {
			t.Errorf("%s 가 함수맵에 남아 있다 — 요청마다 달라지는 값이다", name)
		}
	}
}

// nl2br escapes first and converts second. The other order lets input close a
// tag, which is the whole reason D17 spells the order out.
func TestNl2brEscapesBeforeConverting(t *testing.T) {
	m := FuncMap(Deps{})
	fn := m["nl2br"].(func(string) template.HTML)
	got := string(fn("<script>alert(1)</script>\n두 번째 줄"))
	if strings.Contains(got, "<script>") {
		t.Errorf("스크립트가 살아남았다: %s", got)
	}
	if !strings.Contains(got, "<br>") {
		t.Errorf("줄바꿈이 변환되지 않았다: %s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("이스케이프되지 않았다: %s", got)
	}
}

func TestMoneyAndNumber(t *testing.T) {
	m := FuncMap(Deps{})
	money := func(v any) string { out, err := m["money"].(func(any) (string, error))(v); _ = err; return out }
	number := m["number"].(func(int64) string)

	if got := money(1234567); got != "1,234,567원" {
		t.Errorf("money(1234567) = %q", got)
	}
	if got := money(0); got != "0원" {
		t.Errorf("money(0) = %q", got)
	}
	if got := number(-1234); got != "-1,234" {
		t.Errorf("number(-1234) = %q", got)
	}
	if got := number(999); got != "999" {
		t.Errorf("number(999) = %q", got)
	}
}

func TestTruncateCountsRunes(t *testing.T) {
	m := FuncMap(Deps{})
	fn := m["truncate"].(func(string, int) string)
	// Byte-based truncation would cut a Korean character in half and emit
	// invalid UTF-8 into the page.
	if got := fn("가나다라마", 3); got != "가나다…" {
		t.Errorf("truncate = %q, want 가나다…", got)
	}
	if got := fn("짧다", 10); got != "짧다" {
		t.Errorf("자르지 말아야 하는데 잘랐다: %q", got)
	}
}

func TestDateAgoUsesInjectedClock(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := FuncMap(Deps{Now: func() time.Time { return now }})
	fn := m["dateAgo"].(func(time.Time) string)
	if got := fn(now.Add(-30 * time.Second)); got != "방금" {
		t.Errorf("= %q, want 방금", got)
	}
	if got := fn(now.Add(-90 * time.Minute)); got != "1시간 전" {
		t.Errorf("= %q, want 1시간 전", got)
	}
}

func TestPageNumbers(t *testing.T) {
	m := FuncMap(Deps{})
	fn := m["pages"].(func(int, int) []int)
	got := fn(5, 10)
	want := []int{3, 4, 5, 6, 7}
	if len(got) != len(want) {
		t.Fatalf("= %v, want %v", got, want)
	}
	if n := fn(1, 0); len(n) != 0 {
		t.Errorf("총 0페이지인데 %v", n)
	}
}

// D17: the cache key is the file's CONTENT, not the theme version. Editing a
// file without bumping the version is the most common thing a theme author
// does, and a version-keyed URL does not change for it.
func TestAssetURLUsesContentHash(t *testing.T) {
	dir := t.TempDir()
	css := filepath.Join(dir, "static", "css")
	if err := os.MkdirAll(css, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(css, "style.css"), []byte("a{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := New(fakeBuiltin(), dir, true, nil)

	first := l.AssetURL("css/style.css")
	if !strings.HasPrefix(first, "/static/css/style.css?v=") {
		t.Fatalf("자산 URL = %q", first)
	}

	if err := os.WriteFile(filepath.Join(css, "style.css"), []byte("a{color:red}"), 0o600); err != nil {
		t.Fatal(err)
	}
	l.ForgetAsset("css/style.css")
	second := l.AssetURL("css/style.css")
	if first == second {
		t.Error("파일을 고쳤는데 자산 URL 이 그대로다 — 캐시가 안 깨진다")
	}
}

func TestAssetURLFallsBackToBuiltin(t *testing.T) {
	l := New(fakeBuiltin(), "", false, nil)
	if got := l.AssetURL("css/style.css"); !strings.Contains(got, "?v=") {
		t.Errorf("내장 자산에 해시가 없다: %q", got)
	}
	// A missing asset must not break the page: no hash, but still a URL.
	if got := l.AssetURL("css/none.css"); got != "/static/css/none.css" {
		t.Errorf("없는 자산 = %q", got)
	}
	if got := l.AssetURL("../outside.css"); got != "" {
		t.Errorf("탈출 경로가 URL 을 반환했다: %q", got)
	}
}

// The functions must actually work inside a template, not just as Go values.
func TestFuncsUsableFromTemplate(t *testing.T) {
	fsys := fstest.MapFS{
		"base.html": {Data: []byte(`{{block "body" .}}{{end}}`)},
		"page.html": {Data: []byte(`{{define "body"}}{{money 1500}}|{{truncate "가나다라" 2}}|{{if index .Can "x"}}Y{{else}}N{{end}}{{end}}`)},
	}
	l := New(fsys, "", false, FuncMap(Deps{}))
	var b bytes.Buffer
	if err := l.Render(&b, "page.html", View{Can: map[string]bool{"ok": true}}); err != nil {
		t.Fatal(err)
	}
	if got := b.String(); got != "1,500원|가나…|N" {
		t.Errorf("= %q", got)
	}
}

// **money 는 실수를 받지 않는다.**
//
// D30 이 금액을 정수 minor unit 으로 두는 이유가 반올림이다. 표시 단계에서
// float 를 받아 조용히 반올림하면 그 결정이 여기서 되살아난다 — 12000.4 와
// 12000.6 이 같은 화면에서 다른 총합으로 보이는 종류의 사고다.
func TestMoneyRefusesFloats(t *testing.T) {
	m := FuncMap(Deps{})
	money := m["money"].(func(any) (string, error))

	for _, bad := range []any{12000.0, float32(12000), "12000", nil} {
		if _, err := money(bad); err == nil {
			t.Errorf("money(%#v) 가 통과했다 — 정수만 받아야 한다", bad)
		}
	}
	for _, ok := range []any{int(12000), int32(12000), int64(12000)} {
		got, err := money(ok)
		if err != nil {
			t.Errorf("money(%#v): %v", ok, err)
		}
		if got != "12,000원" {
			t.Errorf("money(%#v) = %q", ok, got)
		}
	}
}
