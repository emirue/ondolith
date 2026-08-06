package theme

import (
	"archive/zip"
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// zipOf builds an archive in memory. entries maps name → contents; a name
// ending in "/" is a directory.
func zipOf(t *testing.T, entries map[string]string) (*bytes.Reader, int64) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		if strings.HasSuffix(name, "/") {
			if _, err := zw.Create(name); err != nil {
				t.Fatal(err)
			}
			continue
		}
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	return bytes.NewReader(b), int64(len(b))
}

func countFiles(t *testing.T, root string) int {
	t.Helper()
	var n int
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func TestInstallUnpacksAValidTheme(t *testing.T) {
	root := t.TempDir()
	r, n := zipOf(t, map[string]string{
		"base.html":            `<html>{{block "body" .}}{{end}}</html>`,
		"page.html":            `{{define "body"}}쪽{{end}}`,
		"partials/header.html": `<header></header>`,
		"static/css/style.css": `body{}`,
	})
	if err := Install(root, "mytheme", r, n, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"base.html", "page.html", "partials/header.html", "static/css/style.css"} {
		if _, err := os.Stat(filepath.Join(root, "mytheme", filepath.FromSlash(want))); err != nil {
			t.Errorf("%s 가 풀리지 않았다: %v", want, err)
		}
	}
	// 실행 권한이 붙지 않는다.
	st, err := os.Stat(filepath.Join(root, "mytheme", "base.html"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o111 != 0 {
		t.Errorf("실행 권한이 붙었다: %v", st.Mode().Perm())
	}
}

// D60 3: Zip Slip. 이름의 어느 구성요소든 루트 밖을 가리키면 거부한다.
// 공격 형태마다 한 건씩.
func TestZipSlipIsRefused(t *testing.T) {
	attacks := map[string]string{
		"상위 경로":    "../evil.html",
		"깊은 상위 경로": "a/b/../../../evil.html",
		"절대 경로":    "/etc/passwd",
		"윈도 역슬래시":  `..\..\evil.html`,
		"윈도 드라이브":  `C:/evil.html`,
		"점만 있는 경로": "./../evil.html",
		"이름에 NUL":  "evil\x00.html",
	}
	for name, entry := range attacks {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			r, n := zipOf(t, map[string]string{
				"base.html": "<html></html>",
				entry:       "공격",
			})
			err := Install(root, "t", r, n, false)
			if err == nil {
				t.Fatalf("%q 가 통과했다", entry)
			}
			// 루트 밖에 아무것도 남지 않는다.
			parent := filepath.Dir(root)
			if _, err := os.Stat(filepath.Join(parent, "evil.html")); err == nil {
				t.Error("루트 밖에 파일이 생겼다")
			}
			// 실패했으면 임시 디렉터리도 남지 않는다.
			if countFiles(t, root) != 0 {
				t.Errorf("실패했는데 파일 %d개가 남았다", countFiles(t, root))
			}
		})
	}
}

// 심볼릭 링크는 `..` 없이 트리 밖으로 쓰는 방법이다.
func TestSymlinkEntryIsRefused(t *testing.T) {
	root := t.TempDir()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if w, err := zw.Create("base.html"); err == nil {
		_, _ = w.Write([]byte("<html></html>"))
	} else {
		t.Fatal(err)
	}
	h := &zip.FileHeader{Name: "link.html", Method: zip.Deflate}
	h.SetMode(fs.ModeSymlink | 0o777)
	w, err := zw.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("/etc/passwd")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	b := buf.Bytes()
	if err := Install(root, "t", bytes.NewReader(b), int64(len(b)), false); !errors.Is(err, ErrZipEntry) {
		t.Errorf("심볼릭 링크가 통과했다: %v", err)
	}
	if countFiles(t, root) != 0 {
		t.Error("실패했는데 파일이 남았다")
	}
}

// D60 2: UncompressedSize64 는 아카이브의 주장이다. 실제로 읽은 바이트를
// 세다가 상한에서 멈춘다 — zip 폭탄은 작은 크기를 신고하고 기가바이트를 준다.
func TestZipBombIsStoppedByBytesRead(t *testing.T) {
	root := t.TempDir()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if w, err := zw.Create("base.html"); err == nil {
		_, _ = w.Write([]byte("<html></html>"))
	}
	w, err := zw.Create("bomb.html")
	if err != nil {
		t.Fatal(err)
	}
	// 0 으로만 채우면 압축비가 극단적으로 높다.
	if _, err := w.Write(bytes.Repeat([]byte{0}, MaxEntryBytes+1<<20)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()

	err = Install(root, "t", bytes.NewReader(b), int64(len(b)), false)
	if err == nil {
		t.Fatal("zip 폭탄이 통과했다")
	}
	if !errors.Is(err, ErrZipTooLarge) && !errors.Is(err, ErrZipRatio) {
		t.Errorf("다른 이유로 실패했다: %v", err)
	}
	if countFiles(t, root) != 0 {
		t.Errorf("실패했는데 파일 %d개가 남았다", countFiles(t, root))
	}
}

func TestEntryCountAndDepthAreBounded(t *testing.T) {
	root := t.TempDir()
	many := map[string]string{"base.html": "<html></html>"}
	for i := range MaxEntries + 10 {
		many["f"+strings.Repeat("0", i%3)+string(rune('a'+i%26))+string(rune('a'+(i/26)%26))+string(rune('a'+(i/676)%26))+".html"] = "x"
	}
	r, n := zipOf(t, many)
	if err := Install(root, "t", r, n, false); err == nil {
		t.Error("엔트리 수 상한이 안 걸렸다")
	}

	deep := "a/a/a/a/a/a/a/a/a/a/a/a/deep.html"
	r2, n2 := zipOf(t, map[string]string{"base.html": "<html></html>", deep: "x"})
	if err := Install(t.TempDir(), "t", r2, n2, false); !errors.Is(err, ErrZipTooDeep) {
		t.Errorf("깊이 상한이 안 걸렸다: %v", err)
	}
}

// D17: base.html 이 없는 테마는 활성화 시 모든 페이지가 죽는다. 업로드에서
// 거부하는 편이 낫다.
func TestThemeWithoutBaseIsRefused(t *testing.T) {
	root := t.TempDir()
	r, n := zipOf(t, map[string]string{"page.html": `{{define "body"}}쪽{{end}}`})
	if err := Install(root, "t", r, n, false); !errors.Is(err, ErrZipNoBase) {
		t.Errorf("base.html 없는 테마가 통과했다: %v", err)
	}
	if countFiles(t, root) != 0 {
		t.Error("거부됐는데 파일이 남았다")
	}
}

// D60 5: 성공했을 때만 rename 한다. 실패하면 임시 디렉터리째 사라진다 —
// 반쯤 풀린 테마는 base.html 만 도착하고 조각은 안 온 상태다.
func TestFailureLeavesNothingBehind(t *testing.T) {
	root := t.TempDir()
	r, n := zipOf(t, map[string]string{
		"base.html":    "<html></html>",
		"ok.html":      "정상",
		"../evil.html": "공격",
	})
	if err := Install(root, "t", r, n, false); err == nil {
		t.Fatal("공격이 통과했다")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("실패했는데 %v 가 남았다", names)
	}
}

func TestThemeNameIsBounded(t *testing.T) {
	for _, name := range []string{"", "../evil", "a/b", "Theme", "테마", strings.Repeat("a", 65), "a_b"} {
		r, n := zipOf(t, map[string]string{"base.html": "<html></html>"})
		if err := Install(t.TempDir(), name, r, n, false); !errors.Is(err, ErrThemeBadName) {
			t.Errorf("이름 %q 가 통과했다: %v", name, err)
		}
	}
	r, n := zipOf(t, map[string]string{"base.html": "<html></html>"})
	if err := Install(t.TempDir(), "my-theme-2", r, n, false); err != nil {
		t.Errorf("정상 이름이 거부됐다: %v", err)
	}
}

func TestExistingThemeIsNotOverwritten(t *testing.T) {
	root := t.TempDir()
	r, n := zipOf(t, map[string]string{"base.html": "<html>첫판</html>"})
	if err := Install(root, "t", r, n, false); err != nil {
		t.Fatal(err)
	}
	r2, n2 := zipOf(t, map[string]string{"base.html": "<html>둘째판</html>"})
	if err := Install(root, "t", r2, n2, false); !errors.Is(err, ErrThemeExists) {
		t.Errorf("기존 테마가 덮였다: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "t", "base.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "첫판") {
		t.Errorf("내용이 바뀌었다: %s", got)
	}
}

func TestOversizeArchiveIsRefusedBeforeReading(t *testing.T) {
	if err := Install(t.TempDir(), "t", bytes.NewReader(nil), MaxZipBytes+1, false); !errors.Is(err, ErrZipTooLarge) {
		t.Errorf("큰 아카이브가 통과했다: %v", err)
	}
}

// entryName 을 직접 시험한다. Install 을 통한 변이는 os.Root 가 대신 막아
// 물지 않지만, 이 함수가 무엇을 거부하는지는 이 함수에 물어야 안다 — 그리고
// 역슬래시는 os.Root 가 통과시키므로 여기서만 막힌다.
func TestEntryNameRefusesEscapes(t *testing.T) {
	refused := map[string]string{
		"상위 경로":    "../evil.html",
		"깊은 상위 경로": "a/b/../../../evil.html",
		"상위만":      "..",
		"절대 경로":    "/etc/passwd",
		"역슬래시 상위":  `..\evil.html`,
		"역슬래시 깊이":  `a\..\..\evil.html`,
		"드라이브 문자":  `C:/evil.html`,
		"NUL":      "evil\x00.html",
		"깊이 초과":    "a/a/a/a/a/a/a/a/a/a/a/deep.html",
	}
	for name, entry := range refused {
		if got, err := entryName(entry); err == nil {
			t.Errorf("%s: %q 가 %q 로 통과했다", name, entry, got)
		}
	}

	accepted := map[string]string{
		"base.html":            "base.html",
		"partials/header.html": "partials/header.html",
		"./page.html":          "page.html",
		"a/./b.html":           "a/b.html",
		"static/css/style.css": "static/css/style.css",
	}
	for entry, want := range accepted {
		got, err := entryName(entry)
		if err != nil {
			t.Errorf("%q 가 거부됐다: %v", entry, err)
			continue
		}
		if got != want {
			t.Errorf("%q → %q, want %q", entry, got, want)
		}
	}

	// 아카이브 자신의 루트 엔트리는 빈 문자열로 무시된다.
	if got, err := entryName("./"); err != nil || got != "" {
		t.Errorf(`"./" → %q, %v`, got, err)
	}
}

// 압축하지 않은(Store) 큰 엔트리. 압축비는 1 이라 비율 검사가 잡지 못하고,
// **실제로 읽은 바이트**를 세는 상한만이 막는다 — D60 2 가 헤더의 선언 크기를
// 믿지 말라고 한 이유가 이 경로다.
func TestUncompressedOversizeEntryIsStoppedByTheByteCap(t *testing.T) {
	root := t.TempDir()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if w, err := zw.Create("base.html"); err == nil {
		_, _ = w.Write([]byte("<html></html>"))
	} else {
		t.Fatal(err)
	}
	// Store 는 압축하지 않는다 → CompressedSize == UncompressedSize → 비율 1.
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "big.html", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(bytes.Repeat([]byte("a"), MaxEntryBytes+1<<20)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()

	err = Install(root, "t", bytes.NewReader(b), int64(len(b)), false)
	if !errors.Is(err, ErrZipTooLarge) {
		t.Fatalf("용량 상한이 안 걸렸다: %v", err)
	}
	if countFiles(t, root) != 0 {
		t.Errorf("실패했는데 파일 %d개가 남았다", countFiles(t, root))
	}
}

// 압축비 검사는 **용량 상한 아래**에서 의미가 있다. 4 KiB 가 8 MiB 로 풀리는
// 엔트리는 20 MiB 상한에 걸리지 않고, 그런 엔트리 500개면 NFR-101 티어의
// 디스크가 찬다 — 하나하나는 멀쩡해 보인다.
func TestHighRatioEntryUnderTheByteCapIsRefused(t *testing.T) {
	root := t.TempDir()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if w, err := zw.Create("base.html"); err == nil {
		_, _ = w.Write([]byte("<html></html>"))
	} else {
		t.Fatal(err)
	}
	w, err := zw.Create("ratio.html")
	if err != nil {
		t.Fatal(err)
	}
	// 0 8 MiB → deflate 로 수 KiB. 용량 상한(20 MiB)에는 못 미치고 비율만 높다.
	if _, err := w.Write(bytes.Repeat([]byte{0}, 8<<20)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	if int64(len(b)) > MaxZipBytes {
		t.Fatalf("아카이브가 %d 바이트 — 상한 아래여야 이 테스트가 뜻을 갖는다", len(b))
	}

	err = Install(root, "t", bytes.NewReader(b), int64(len(b)), false)
	if !errors.Is(err, ErrZipRatio) {
		t.Fatalf("압축비 검사가 안 걸렸다: %v", err)
	}
	if countFiles(t, root) != 0 {
		t.Errorf("실패했는데 파일 %d개가 남았다", countFiles(t, root))
	}
}

// **덮어쓰기는 바꿔치기다** (OPEN-42 결정). 옛 판의 파일이 남으면 안 된다 —
// 새 zip 에 없는 파셜이 살아남으면 그리는 것과 올린 것이 달라진다.
func TestReplaceSwapsTheWholeDirectory(t *testing.T) {
	root := t.TempDir()
	r, n := zipOf(t, map[string]string{
		"base.html":             "<html>첫판</html>",
		"partials/removed.html": "지워질 것",
	})
	if err := Install(root, "t", r, n, false); err != nil {
		t.Fatal(err)
	}

	r2, n2 := zipOf(t, map[string]string{"base.html": "<html>둘째판</html>"})
	if err := Install(root, "t", r2, n2, true); err != nil {
		t.Fatalf("덮어쓰기가 막혔다: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(root, "t", "base.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "둘째판") {
		t.Errorf("base.html 이 %q — 덮어쓰지 않았다", got)
	}
	if _, err := os.Stat(filepath.Join(root, "t", "partials", "removed.html")); !os.IsNotExist(err) {
		t.Errorf("옛 판의 파일이 남았다 (err=%v) — 비우지 않고 채웠다", err)
	}
	// 밀어 둔 옛 디렉터리도 치운다. 남으면 업로드마다 쓰레기가 는다.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "t" {
			t.Errorf("찌꺼기 %q 가 남았다", e.Name())
		}
	}
}

// **거부된 zip 은 자리를 바꾸기 전에 멈춘다.** 압축을 다 풀고 나서 거부되는
// 경로(base.html 없음)에서도 옛 테마는 그대로다 — 검사가 rename 앞에 있다는
// 것이 이 테스트가 보는 것이다.
//
// 두 rename **사이**의 실패 복구는 여기서 보지 못한다. 그 창은 테스트에서
// 만들 수 없다 (upload.go 의 주석 참조).
func TestRejectedZipNeverTouchesTheOldTheme(t *testing.T) {
	root := t.TempDir()
	r, n := zipOf(t, map[string]string{"base.html": "<html>첫판</html>"})
	if err := Install(root, "t", r, n, false); err != nil {
		t.Fatal(err)
	}
	// base.html 이 없는 zip 은 압축을 다 푼 뒤에 거부된다 — 자리 바꾸기
	// 직전까지 갔다가 멈추는 경로다.
	r2, n2 := zipOf(t, map[string]string{"partials/x.html": "조각뿐"})
	if err := Install(root, "t", r2, n2, true); !errors.Is(err, ErrZipNoBase) {
		t.Fatalf("= %v, want ErrZipNoBase", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "t", "base.html"))
	if err != nil {
		t.Fatalf("옛 테마가 사라졌다: %v", err)
	}
	if !strings.Contains(string(got), "첫판") {
		t.Errorf("base.html 이 %q", got)
	}
}
