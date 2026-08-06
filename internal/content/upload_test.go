package content

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Real magic bytes. A test that feeds arbitrary bytes and expects them to pass
// is asserting that the check does nothing.
var (
	pngBytes  = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR" + strings.Repeat("\x00", 40))
	gifBytes  = []byte("GIF89a" + strings.Repeat("\x00", 40))
	jpegBytes = []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00" + strings.Repeat("\x00", 40))
	pdfBytes  = []byte("%PDF-1.7\n" + strings.Repeat("x", 40))
	phpBytes  = []byte("<?php system($_GET['c']); ?>" + strings.Repeat(" ", 40))
)

var uploadAt = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

// D60 ①: 허용 목록이지 차단 목록이 아니다. `.php5`·`.phtml` 같은 것을 일일이
// 막을 수는 없다.
func TestExtensionAllowList(t *testing.T) {
	for _, ext := range []string{".php", ".php5", ".phtml", ".svg", ".html", ".htm",
		".js", ".sh", ".exe", "", ".PHP", ".png.php"} {
		if _, _, err := ValidateUpload("payload"+ext, bytes.NewReader(pngBytes), DefaultUploadLimits()); !errors.Is(err, ErrUploadExt) {
			t.Errorf("확장자 %q 가 통과했다: %v", ext, err)
		}
	}
	// 대문자 확장자는 허용목록에 있으면 통과한다 — 케이스로 우회하지 못한다.
	for _, name := range []string{"a.png", "a.PNG", "a.PnG"} {
		if _, _, err := ValidateUpload(name, bytes.NewReader(pngBytes), DefaultUploadLimits()); err != nil {
			t.Errorf("%s 가 거부됐다: %v", name, err)
		}
	}
}

// D60 ②: 클라이언트가 보낸 Content-Type 을 믿지 않는다. **확장자를 위조하고
// 매직바이트가 다른 파일**이 이 검사에서 걸린다 — 이것이 없으면 .php 를
// .png 로 이름만 바꿔 올리는 것이 통과한다.
func TestMagicBytesMustMatchTheExtension(t *testing.T) {
	tests := map[string]struct {
		name string
		body []byte
		ok   bool
	}{
		"PHP 를 png 로 위장":  {"shell.png", phpBytes, false},
		"PHP 를 jpg 로 위장":  {"shell.jpg", phpBytes, false},
		"gif 를 png 로 위장":  {"x.png", gifBytes, false},
		"png 를 pdf 로 위장":  {"x.pdf", pngBytes, false},
		"진짜 png":          {"x.png", pngBytes, true},
		"진짜 jpeg (.jpeg)": {"x.jpeg", jpegBytes, true},
		"진짜 jpeg (.jpg)":  {"x.jpg", jpegBytes, true},
		"진짜 gif":          {"x.gif", gifBytes, true},
		"진짜 pdf":          {"x.pdf", pdfBytes, true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			mime, _, err := ValidateUpload(tc.name, bytes.NewReader(tc.body), DefaultUploadLimits())
			if tc.ok {
				if err != nil {
					t.Errorf("정상 파일이 거부됐다: %v", err)
				}
				if mime == "" {
					t.Error("MIME 을 산출하지 않았다")
				}
				return
			}
			if !errors.Is(err, ErrUploadContent) {
				t.Errorf("위장 파일이 통과했다: err=%v mime=%q", err, mime)
			}
		})
	}
}

// 검사 후에도 본문 전체를 읽을 수 있어야 한다. 앞 512바이트를 소비하고 끝나면
// 저장된 파일이 잘린다 — 조용하고, 이미지가 깨져서야 드러난다.
func TestValidatedBodyStillYieldsEverything(t *testing.T) {
	full := append(append([]byte(nil), pngBytes...), []byte(strings.Repeat("Z", 5000))...)
	_, body, err := ValidateUpload("x.png", bytes.NewReader(full), DefaultUploadLimits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, full) {
		t.Errorf("본문이 %d 바이트로 잘렸다 (원본 %d)", len(got), len(full))
	}
}

func TestEmptyUploadIsRefused(t *testing.T) {
	if _, _, err := ValidateUpload("x.png", bytes.NewReader(nil), DefaultUploadLimits()); !errors.Is(err, ErrUploadEmpty) {
		t.Errorf("빈 파일이 통과했다: %v", err)
	}
}

var storedPathRe = regexp.MustCompile(`^[0-9]{4}/[0-9]{2}/[0-9a-f-]{36}$`)

// D60 ③: 파일명 재생성. 원본 이름은 표시용으로만 남고 디스크에는 닿지 않는다 —
// 경로 순회와 실행 가능한 배치가 함께 죽는다.
func TestFilenameIsRegeneratedAndOriginalNeverTouchesDisk(t *testing.T) {
	root := t.TempDir()

	for _, name := range []string{
		"../../etc/cron.d/evil.png",
		"/etc/passwd.png",
		"..\\..\\windows.png",
		"normal.png",
		"공백 있는 이름.png",
	} {
		got, err := StoreUpload(root, name, bytes.NewReader(pngBytes), uploadAt, DefaultUploadLimits())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !storedPathRe.MatchString(got.StoredPath) {
			t.Errorf("%s → 저장 경로 %q 가 D30 형식이 아니다", name, got.StoredPath)
		}
		if strings.Contains(got.StoredPath, "..") || strings.Contains(got.StoredPath, "etc") {
			t.Errorf("원본 이름 조각이 저장 경로에 남았다: %q", got.StoredPath)
		}
		// 확장자가 없어야 한다. 웹루트가 어쩌다 서빙돼도 실행 가능한 이름이
		// 디스크에 없다.
		if filepath.Ext(got.StoredPath) != "" {
			t.Errorf("저장 경로에 확장자가 붙었다: %q", got.StoredPath)
		}
		if got.OriginalName == "" {
			t.Error("표시용 원본 이름이 비었다")
		}
	}

	// 루트 밖으로 나간 파일이 하나도 없어야 한다.
	parent := filepath.Dir(root)
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".png") {
			t.Errorf("업로드 루트 밖에 파일이 생겼다: %s", e.Name())
		}
	}
}

// D60 ④: 실행 권한 제거. 0644.
func TestStoredFileHasNoExecuteBit(t *testing.T) {
	root := t.TempDir()
	got, err := StoreUpload(root, "x.png", bytes.NewReader(pngBytes), uploadAt, DefaultUploadLimits())
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(root, filepath.FromSlash(got.StoredPath)))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o644 {
		t.Errorf("권한 %v, want 0644", st.Mode().Perm())
	}
	if st.Mode().Perm()&0o111 != 0 {
		t.Errorf("실행 권한이 붙었다: %v", st.Mode().Perm())
	}
}

func TestStoredContentMatchesWhatWasSent(t *testing.T) {
	root := t.TempDir()
	full := append(append([]byte(nil), pngBytes...), []byte(strings.Repeat("Z", 3000))...)
	got, err := StoreUpload(root, "x.png", bytes.NewReader(full), uploadAt, DefaultUploadLimits())
	if err != nil {
		t.Fatal(err)
	}
	if got.ByteSize != int64(len(full)) {
		t.Errorf("크기 %d, want %d", got.ByteSize, len(full))
	}
	on, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(got.StoredPath)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(on, full) {
		t.Errorf("저장된 내용이 다르다: %d 바이트", len(on))
	}
}

// 상한을 넘긴 업로드는 거부되고, **부분 파일을 남기지 않는다** — 어떤 행도
// 가리키지 않는 반쪽 파일은 나중에 누구도 정체를 알 수 없다.
func TestOversizeUploadIsRefusedAndLeavesNothing(t *testing.T) {
	root := t.TempDir()
	big := io.MultiReader(bytes.NewReader(pngBytes),
		io.LimitReader(zeros{}, MaxAttachmentBytes+1))

	if _, err := StoreUpload(root, "x.png", big, uploadAt, DefaultUploadLimits()); !errors.Is(err, ErrUploadTooLarge) {
		t.Fatalf("상한 초과가 통과했다: %v", err)
	}
	var left int
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			left++
		}
		return nil
	})
	if left != 0 {
		t.Errorf("거부됐는데 파일 %d개가 남았다", left)
	}
}

// 정확히 상한인 파일은 통과한다. 경계에서 거부하면 "20MiB 까지"가 거짓이 된다.
func TestExactlyAtTheLimitIsAccepted(t *testing.T) {
	root := t.TempDir()
	body := io.MultiReader(bytes.NewReader(pngBytes),
		io.LimitReader(zeros{}, MaxAttachmentBytes-int64(len(pngBytes))))
	got, err := StoreUpload(root, "x.png", body, uploadAt, DefaultUploadLimits())
	if err != nil {
		t.Fatalf("정확히 상한인 파일이 거부됐다: %v", err)
	}
	if got.ByteSize != MaxAttachmentBytes {
		t.Errorf("크기 %d, want %d", got.ByteSize, MaxAttachmentBytes)
	}
}

// 저장 경로가 겹치지 않는다. 겹치면 한쪽을 지울 때 다른 쪽 실물이 사라진다
// (D30 이 stored_path 에 UNIQUE 를 둔 이유).
func TestStoredPathsDoNotCollide(t *testing.T) {
	root := t.TempDir()
	seen := map[string]bool{}
	for range 200 {
		got, err := StoreUpload(root, "x.png", bytes.NewReader(pngBytes), uploadAt, DefaultUploadLimits())
		if err != nil {
			t.Fatal(err)
		}
		if seen[got.StoredPath] {
			t.Fatalf("저장 경로가 겹쳤다: %s", got.StoredPath)
		}
		seen[got.StoredPath] = true
	}
}

// 위장 파일은 디스크에 닿지도 않는다. 검사 후 저장이라는 순서가 뒤집히면
// 거부된 셸이 파일로 남는다.
func TestRefusedUploadNeverReachesDisk(t *testing.T) {
	root := t.TempDir()
	if _, err := StoreUpload(root, "shell.png", bytes.NewReader(phpBytes), uploadAt, DefaultUploadLimits()); err == nil {
		t.Fatal("위장 파일이 저장됐다")
	}
	var left int
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			left++
		}
		return nil
	})
	if left != 0 {
		t.Errorf("거부됐는데 파일 %d개가 남았다", left)
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}
