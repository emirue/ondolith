package theme

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const goodAdmin = `{
  "screens": [
    {"key": "hero", "title": "메인 배너", "group": "디자인", "order": 10, "kind": "settings",
     "fields": [
       {"key": "headline", "label": "큰 제목", "type": "text", "required": true, "default": "환영합니다"},
       {"key": "body", "label": "설명", "type": "textarea"},
       {"key": "count", "label": "숫자", "type": "number", "default": 3},
       {"key": "photo", "label": "배경 사진", "type": "image", "help": "가로 1600px 이상"},
       {"key": "link", "label": "링크", "type": "url", "default": "/shop"},
       {"key": "tone", "label": "글자색", "type": "select", "options": ["밝게", "어둡게"], "default": "밝게"}
     ]},
    {"key": "partners", "title": "협력사", "kind": "collection",
     "fields": [{"key": "name", "label": "이름", "type": "text", "required": true}]}
  ]
}`

func TestParseAdminReadsAGoodDeclaration(t *testing.T) {
	screens, warns := ParseAdmin([]byte(goodAdmin))
	if len(warns) != 0 {
		t.Fatalf("경고가 없어야 한다: %v", warns)
	}
	if len(screens) != 2 {
		t.Fatalf("화면 2개여야 한다: %d", len(screens))
	}
	hero := screens[0]
	if hero.Key != "hero" || hero.Title != "메인 배너" || hero.Group != "디자인" || hero.Order != 10 || hero.Collection() {
		t.Errorf("hero = %+v", hero)
	}
	if len(hero.Fields) != 6 || hero.Fields[3].Type != FieldImage || hero.ImageFields() != 1 {
		t.Errorf("hero 칸 = %+v", hero.Fields)
	}
	// 숫자 기본값은 정수로 정규화된다 — 템플릿에서 3 이 3 으로 찍혀야 한다.
	if d := hero.Fields[2].Default; d != int64(3) {
		t.Errorf("숫자 기본값 = %#v, want int64(3)", d)
	}
	p := screens[1]
	if !p.Collection() || p.Group != "테마" || p.Order != 0 {
		t.Errorf("기본 묶음·순서 = %+v", p)
	}
}

// 지배 원칙 5: 잘못된 화면만 빠지고 나머지는 쓴다. 경고는 화면 키와 이유를 말한다.
func TestParseAdminDropsOnlyTheBrokenScreen(t *testing.T) {
	ok := `{"key": "ok", "title": "정상", "kind": "settings", "fields": [{"key": "a", "label": "A", "type": "text"}]}`
	cases := []struct {
		name, screen, want string
	}{
		{"모르는 속성", `{"key": "bad", "title": "x", "kind": "settings", "lable": "x", "fields": [{"key": "a", "label": "A", "type": "text"}]}`, "lable"},
		{"칸의 모르는 속성", `{"key": "bad", "title": "x", "kind": "settings", "fields": [{"key": "a", "lable": "A", "type": "text"}]}`, "lable"},
		{"키 형식", `{"key": "Bad", "title": "x", "kind": "settings", "fields": [{"key": "a", "label": "A", "type": "text"}]}`, "key"},
		{"제목 없음", `{"key": "bad", "kind": "settings", "fields": [{"key": "a", "label": "A", "type": "text"}]}`, "title"},
		{"종류", `{"key": "bad", "title": "x", "kind": "list", "fields": [{"key": "a", "label": "A", "type": "text"}]}`, "kind"},
		{"칸 없음", `{"key": "bad", "title": "x", "kind": "settings", "fields": []}`, "fields"},
		{"칸 종류", `{"key": "bad", "title": "x", "kind": "settings", "fields": [{"key": "a", "label": "A", "type": "date"}]}`, "type"},
		{"칸 키 중복", `{"key": "bad", "title": "x", "kind": "settings", "fields": [{"key": "a", "label": "A", "type": "text"}, {"key": "a", "label": "B", "type": "text"}]}`, "겹"},
		{"고르기에 선택지 없음", `{"key": "bad", "title": "x", "kind": "settings", "fields": [{"key": "a", "label": "A", "type": "select"}]}`, "options"},
		{"다른 종류에 선택지", `{"key": "bad", "title": "x", "kind": "settings", "fields": [{"key": "a", "label": "A", "type": "text", "options": ["x"]}]}`, "options"},
		{"선택지 중복", `{"key": "bad", "title": "x", "kind": "settings", "fields": [{"key": "a", "label": "A", "type": "select", "options": ["x", "x"]}]}`, "options"},
		{"기본값이 선택지 밖", `{"key": "bad", "title": "x", "kind": "settings", "fields": [{"key": "a", "label": "A", "type": "select", "options": ["x"], "default": "y"}]}`, "default"},
		{"숫자 기본값이 문자열", `{"key": "bad", "title": "x", "kind": "settings", "fields": [{"key": "a", "label": "A", "type": "number", "default": "3"}]}`, "default"},
		{"링크 기본값 스킴", `{"key": "bad", "title": "x", "kind": "settings", "fields": [{"key": "a", "label": "A", "type": "url", "default": "javascript:alert(1)"}]}`, "default"},
		{"사진 기본값", `{"key": "bad", "title": "x", "kind": "settings", "fields": [{"key": "a", "label": "A", "type": "image", "default": "/x.png"}]}`, "default"},
		{"도움말 201자", `{"key": "bad", "title": "x", "kind": "settings", "fields": [{"key": "a", "label": "A", "type": "text", "help": "` + strings.Repeat("가", 201) + `"}]}`, "help"},
		{"사진 칸 11개", screenWithImages(11), "사진"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			screens, warns := ParseAdmin([]byte(`{"screens": [` + ok + `, ` + tc.screen + `]}`))
			if len(screens) != 1 || screens[0].Key != "ok" {
				t.Fatalf("정상 화면 하나만 남아야 한다: %+v", screens)
			}
			if len(warns) != 1 || !strings.Contains(warns[0], tc.want) || !strings.Contains(warns[0], "admin.json") {
				t.Errorf("경고 = %v, %q 를 담아야 한다", warns, tc.want)
			}
		})
	}
}

func screenWithImages(n int) string {
	fields := make([]string, n)
	for i := range fields {
		fields[i] = fmt.Sprintf(`{"key": "p%d", "label": "P", "type": "image"}`, i)
	}
	return `{"key": "bad", "title": "x", "kind": "settings", "fields": [` + strings.Join(fields, ",") + `]}`
}

func TestParseAdminDropsEverythingOnAFileError(t *testing.T) {
	for name, body := range map[string]string{
		"JSON 아님":       `{"screens": [`,
		"screens 없음":    `{"screen": []}`,
		"screens 배열 아님": `{"screens": {}}`,
		"64KiB 초과":      `{"screens": []}` + strings.Repeat(" ", MaxAdminBytes),
	} {
		t.Run(name, func(t *testing.T) {
			screens, warns := ParseAdmin([]byte(body))
			if screens != nil || len(warns) != 1 {
				t.Errorf("화면 없음 + 경고 하나: %v %v", screens, warns)
			}
		})
	}
}

func TestParseAdminCapsAndDedupesScreens(t *testing.T) {
	var parts []string
	for i := range MaxScreens + 1 {
		parts = append(parts, fmt.Sprintf(`{"key": "s%d", "title": "x", "kind": "settings", "fields": [{"key": "a", "label": "A", "type": "text"}]}`, i))
	}
	parts = append(parts, `{"key": "s0", "title": "dup", "kind": "settings", "fields": [{"key": "a", "label": "A", "type": "text"}]}`)
	screens, warns := ParseAdmin([]byte(`{"screens": [` + strings.Join(parts, ",") + `]}`))
	if len(screens) != MaxScreens {
		t.Errorf("화면 %d 개, want %d", len(screens), MaxScreens)
	}
	if screens[0].Title != "x" {
		t.Errorf("키가 겹치면 뒤의 것이 빠진다: %+v", screens[0])
	}
	if len(warns) != 2 {
		t.Errorf("상한·중복 경고 둘: %v", warns)
	}
}

func TestReadAdminAndValidateThemeDir(t *testing.T) {
	dir := t.TempDir()
	if s, w := ReadAdmin(dir); s != nil || w != nil {
		t.Errorf("admin.json 이 없으면 화면도 경고도 없다: %v %v", s, w)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("base.html", `<div class="site-header"></div><div class="site-footer"></div>`)
	write("admin.json", `{"screens": [{"key": "Bad"}]}`)
	warn, err := ValidateThemeDir(dir, "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warn, "admin.json") {
		t.Errorf("활성화 경고에 선언 오류가 있어야 한다: %q", warn)
	}
	write("admin.json", goodAdmin)
	if s, w := ReadAdmin(dir); len(s) != 2 || len(w) != 0 {
		t.Errorf("ReadAdmin = %d 화면, 경고 %v", len(s), w)
	}
}

// 개발 모드가 아니면 로더마다 한 번 읽는다 — 로고·파비콘과 같다 (D41 7절).
func TestLoaderReadsAdminScreensOnce(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "admin.json"), []byte(goodAdmin), 0o644); err != nil {
		t.Fatal(err)
	}
	l := New(Builtin(), dir, false, nil)
	if n := len(l.AdminScreens()); n != 2 {
		t.Fatalf("화면 %d", n)
	}
	if err := os.WriteFile(filepath.Join(dir, "admin.json"), []byte(`{"screens": []}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := len(l.AdminScreens()); n != 2 {
		t.Errorf("운영 모드는 다시 읽지 않는다: %d", n)
	}
	if n := len(New(Builtin(), dir, true, nil).AdminScreens()); n != 0 {
		t.Errorf("개발 모드는 매번 읽는다: %d", n)
	}
	if New(Builtin(), "", false, nil).AdminScreens() != nil {
		t.Error("내장 테마에는 화면이 없다")
	}
}

func TestFieldCoerce(t *testing.T) {
	opts := []string{"밝게", "어둡게"}
	cases := []struct {
		name  string
		f     Field
		raw   string
		want  any
		isErr bool
	}{
		{"빈 값은 없음", Field{Type: FieldText}, "  ", nil, false},
		{"글 다듬기", Field{Type: FieldText}, "  안녕 ", "안녕", false},
		{"글 500자", Field{Type: FieldText}, strings.Repeat("가", 500), strings.Repeat("가", 500), false},
		{"글 501자", Field{Type: FieldText}, strings.Repeat("가", 501), nil, true},
		{"글에 줄바꿈", Field{Type: FieldText}, "a\nb", nil, true},
		{"긴 글 줄바꿈", Field{Type: FieldTextarea}, "a\nb", "a\nb", false},
		{"긴 글 5001자", Field{Type: FieldTextarea}, strings.Repeat("가", 5001), nil, true},
		{"숫자 정수", Field{Type: FieldNumber}, "1000000", float64(1000000), false},
		{"숫자 실수", Field{Type: FieldNumber}, "-1.5", -1.5, false},
		{"숫자 상한", Field{Type: FieldNumber}, "1e15", nil, true},
		{"숫자 아님", Field{Type: FieldNumber}, "십", nil, true},
		{"숫자 NaN", Field{Type: FieldNumber}, "NaN", nil, true},
		{"링크 절대", Field{Type: FieldURL}, "https://example.com/a", "https://example.com/a", false},
		{"링크 사이트 안", Field{Type: FieldURL}, "/shop", "/shop", false},
		{"링크 스킴", Field{Type: FieldURL}, "javascript:alert(1)", nil, true},
		{"링크 다른 호스트로 읽히는 경로", Field{Type: FieldURL}, "//evil.example", nil, true},
		{"링크 2001자", Field{Type: FieldURL}, "/" + strings.Repeat("a", 2000), nil, true},
		{"고르기", Field{Type: FieldSelect, Options: opts}, "어둡게", "어둡게", false},
		{"고르기 밖", Field{Type: FieldSelect, Options: opts}, "보통", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.f.Coerce(tc.raw)
			if (err != nil) != tc.isErr {
				t.Fatalf("err = %v, isErr %v", err, tc.isErr)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Coerce = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func heroScreen(t *testing.T) Screen {
	t.Helper()
	screens, warns := ParseAdmin([]byte(goodAdmin))
	if len(warns) != 0 {
		t.Fatal(warns)
	}
	return screens[0]
}

const photoID = "0b6e6f43-8d6e-4a3c-9a39-0d7c3d6b1c11"

// 지배 원칙 2: 저장은 선언된 칸만 바꾸고, 선언에 없는 칸의 저장값은 그대로 둔다.
func TestApplyMergesAndKeepsUndeclaredValues(t *testing.T) {
	s := heroScreen(t)
	prev := map[string]any{"gone": "옛 테마의 값", "photo": map[string]any{"id": photoID, "w": 10.0, "h": 20.0}, "body": "옛 설명"}
	form := url.Values{"headline": {"새 제목"}, "body": {""}, "count": {"7"}, "tone": {"어둡게"}}
	out, errs := s.Apply(prev, form, nil)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	want := map[string]any{"gone": "옛 테마의 값", "photo": prev["photo"], "headline": "새 제목", "count": float64(7), "tone": "어둡게"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("Apply = %#v\nwant %#v", out, want)
	}
	if prev["body"] != "옛 설명" {
		t.Error("prev 를 고치면 안 된다")
	}
}

func TestApplyImages(t *testing.T) {
	s := heroScreen(t)
	prev := map[string]any{"photo": map[string]any{"id": photoID}}
	base := url.Values{"headline": {"제목"}}

	img := map[string]any{"id": "11111111-1111-4111-8111-111111111111", "w": 1.0, "h": 2.0}
	out, _ := s.Apply(prev, base, map[string]map[string]any{"photo": img})
	if !reflect.DeepEqual(out["photo"], img) {
		t.Errorf("새 사진으로 바뀌어야 한다: %#v", out["photo"])
	}

	clear := url.Values{"headline": {"제목"}, "photo" + ClearSuffix: {"1"}}
	out, _ = s.Apply(prev, clear, nil)
	if _, ok := out["photo"]; ok {
		t.Error("사진 빼기가 값을 지우지 않았다")
	}

	// 사진이 함께 오면 빼기보다 새 사진이 이긴다 — 바꾸려고 고른 파일을 버리지 않는다.
	out, _ = s.Apply(prev, clear, map[string]map[string]any{"photo": img})
	if !reflect.DeepEqual(out["photo"], img) {
		t.Errorf("새 사진이 이겨야 한다: %#v", out["photo"])
	}
}

func TestApplyRefusesBadOrMissingValues(t *testing.T) {
	screens, _ := ParseAdmin([]byte(`{"screens": [{"key": "s", "title": "x", "kind": "settings", "fields": [
		{"key": "name", "label": "이름", "type": "text", "required": true},
		{"key": "logo", "label": "로고", "type": "image", "required": true},
		{"key": "n", "label": "수", "type": "number"},
		{"key": "d", "label": "기본값 있는 필수", "type": "text", "required": true, "default": "기본"}]}]}`))
	s := screens[0]
	_, errs := s.Apply(nil, url.Values{"name": {""}, "n": {"abc"}}, nil)
	got := map[string]bool{}
	for _, e := range errs {
		got[e.Key] = true
	}
	if !got["name"] || !got["logo"] || !got["n"] || got["d"] {
		t.Errorf("오류 칸 = %v (name·logo·n 이어야 하고, 기본값이 있는 필수 d 는 아니다)", got)
	}
	// 검사만 하는 첫 호출: 파일이 오는 사진 칸(값 nil)은 필수를 채운 것으로 본다.
	_, errs = s.Apply(nil, url.Values{"name": {"이름"}}, map[string]map[string]any{"logo": nil})
	if len(errs) != 0 {
		t.Errorf("올리는 중인 필수 사진을 빠졌다고 했다: %v", errs)
	}
}

func TestScreenValues(t *testing.T) {
	s := heroScreen(t)
	got := ScreenValues(s, map[string]any{
		"headline": 42.0, // 다른 테마가 숫자로 저장 — 타입이 맞지 않으면 기본값
		"body":     "줄1\n줄2",
		"count":    1000000.0,
		"photo":    map[string]any{"id": photoID, "w": 1600.0, "h": 900.0},
		"link":     "javascript:alert(1)", // 다른 테마의 글 칸이 남긴 값 — 링크로는 쓰지 않는다
		"tone":     "보통",                  // 선택지에서 빠진 값
		"gone":     "선언 밖",
	})
	want := map[string]any{
		"headline": "환영합니다",
		"body":     "줄1\n줄2",
		"count":    int64(1000000),
		"photo":    Image{URL: "/media/" + photoID, Thumb: "/media/" + photoID + "/thumb", Width: 1600, Height: 900},
		"link":     "/shop",
		"tone":     "밝게",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ScreenValues =\n%#v\nwant\n%#v", got, want)
	}

	empty := ScreenValues(s, nil)
	if empty["body"] != "" || empty["count"] != int64(3) || empty["photo"] != (Image{}) {
		t.Errorf("빈 값: %#v", empty)
	}
	if _, ok := ScreenValues(Screen{Fields: []Field{{Key: "n", Type: FieldNumber}}}, nil)["n"]; !ok {
		t.Error("선언된 칸은 값이 없어도 키가 있어야 한다")
	}
	if v := ScreenValues(s, map[string]any{"count": 2.5})["count"]; v != 2.5 {
		t.Errorf("실수는 실수로: %#v", v)
	}
	if v := ScreenValues(s, map[string]any{"photo": map[string]any{"id": "../etc"}})["photo"]; v != (Image{}) {
		t.Errorf("uuid 가 아닌 사진 id 는 없는 것: %#v", v)
	}
}

func TestBuildThemeData(t *testing.T) {
	screens, _ := ParseAdmin([]byte(goodAdmin))
	data := BuildThemeData(screens,
		map[string]map[string]any{"hero": {"headline": "제목"}},
		map[string][]map[string]any{"partners": {{"name": "가"}, {"name": "나"}}})
	if data["hero"].(map[string]any)["headline"] != "제목" {
		t.Errorf("설정형: %#v", data["hero"])
	}
	rows := data["partners"].([]map[string]any)
	if len(rows) != 2 || rows[1]["name"] != "나" {
		t.Errorf("모음형: %#v", rows)
	}
	none := BuildThemeData(screens, nil, nil)
	if r := none["partners"].([]map[string]any); r == nil || len(r) != 0 {
		t.Errorf("행이 없으면 빈 목록(nil 아님): %#v", none["partners"])
	}
	if BuildThemeData(nil, nil, nil) != nil {
		t.Error("선언이 없으면 nil")
	}
}

// 시드(lib-seed·screens)가 켜는 예시 선언이 경고 없이 읽혀야 `make ui`·`screens`·`crawl` 이
// A-212 를 잰다 — 깨지면 그 화면이 조용히 404 가 되고 게이트는 「없는 화면」으로 운다.
func TestQAThemeDeclarationIsClean(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "qa-theme", "admin.json"))
	if err != nil {
		t.Fatal(err)
	}
	screens, warns := ParseAdmin(b)
	if len(warns) != 0 || len(screens) != 2 {
		t.Fatalf("예시 선언: 화면 %d, 경고 %v", len(screens), warns)
	}
	types := map[string]bool{}
	for _, s := range screens {
		for _, f := range s.Fields {
			types[f.Type] = true
		}
	}
	if len(types) != 6 {
		t.Errorf("예시가 칸 종류 여섯을 다 쓰지 않는다: %v", types)
	}
}
