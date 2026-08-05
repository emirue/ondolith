package theme

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Theme zip extraction (FR-307, D60 「압축 해제」).
//
// This screen is the highest-risk one in the product: an upload here is
// arbitrary file write, and a file written here is executed as a template. All
// five defences are applied, and each exists because the others do not cover
// its case.

var (
	ErrZipTooLarge  = errors.New("theme: 압축 파일이 너무 큽니다")
	ErrZipTooMany   = errors.New("theme: 파일이 너무 많습니다")
	ErrZipTooDeep   = errors.New("theme: 디렉터리가 너무 깊습니다")
	ErrZipEntry     = errors.New("theme: 허용되지 않는 엔트리")
	ErrZipRatio     = errors.New("theme: 압축비가 너무 높습니다")
	ErrZipNoBase    = errors.New("theme: base.html 이 없습니다")
	ErrThemeExists  = errors.New("theme: 같은 이름의 테마가 이미 있습니다")
	ErrThemeBadName = errors.New("theme: 테마 이름이 올바르지 않습니다")
)

// Limits are D60's numbers. NFR-101 sizes the box at 1 vCPU and 512MB–1GB, and
// unpacking has to fit in that tier.
const (
	MaxZipBytes     = 20 << 20 // the archive itself
	MaxTotalBytes   = 20 << 20 // everything it unpacks to
	MaxEntryBytes   = 20 << 20 // any single entry
	MaxEntries      = 2000
	MaxDepth        = 10
	MaxCompressRate = 100 // unpacked : stored
)

// Install unpacks a theme zip into root/<name>.
//
// It writes to a temporary directory and renames on success (D60 5), so a
// failure part-way leaves nothing: a half-extracted theme is one whose base.html
// arrived and whose partials did not, which renders as a stack of errors on
// every page.
func Install(root, name string, r io.ReaderAt, size int64) (err error) {
	if err := validThemeName(name); err != nil {
		return err
	}
	if size > MaxZipBytes {
		return fmt.Errorf("%w: %d 바이트", ErrZipTooLarge, size)
	}

	rt, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer rt.Close()
	if _, err := rt.Stat(name); err == nil {
		return ErrThemeExists
	}

	zr, err := zip.NewReader(r, size)
	if err != nil {
		return fmt.Errorf("theme: zip 을 읽을 수 없습니다: %w", err)
	}
	if len(zr.File) > MaxEntries {
		return fmt.Errorf("%w: %d개", ErrZipTooMany, len(zr.File))
	}

	// A temp directory beside the target, so the rename is on one filesystem.
	tmp, err := os.MkdirTemp(root, ".tmp-"+name+"-")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(tmp)
		}
	}()

	// Every write goes through os.Root. The escape check is the standard
	// library's, not one written here (NFR-201): `path escapes from parent` is
	// an error we get for free, and a hand-written prefix check is the classic
	// place Zip Slip survives.
	out, err := os.OpenRoot(tmp)
	if err != nil {
		return err
	}
	defer out.Close()

	var total int64
	var sawBase bool
	for _, f := range zr.File {
		clean, err := entryName(f.Name)
		if err != nil {
			return err
		}
		if clean == "" {
			continue // the archive's own root entry
		}
		// Symlinks and devices are refused. A symlink is how an archive writes
		// outside the tree without any `..` in a name.
		mode := f.Mode()
		if mode&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: 심볼릭 링크 %q", ErrZipEntry, f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := out.MkdirAll(clean, 0o755); err != nil {
				return err
			}
			continue
		}
		if !mode.IsRegular() {
			return fmt.Errorf("%w: 정규 파일이 아님 %q", ErrZipEntry, f.Name)
		}

		if err := out.MkdirAll(path.Dir(clean), 0o755); err != nil {
			return err
		}
		written, err := extractOne(out, f, clean, total)
		if err != nil {
			return err
		}
		total += written
		if clean == "base.html" {
			sawBase = true
		}
	}

	// D17: base.html is the one required file. A theme without it activates
	// into a site where every page fails, including the screen that switches
	// back — better to refuse the upload.
	if !sawBase {
		return ErrZipNoBase
	}
	return os.Rename(tmp, filepath.Join(root, name))
}

// extractOne writes a single entry under the limits.
func extractOne(out *os.Root, f *zip.File, clean string, soFar int64) (int64, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()

	dst, err := out.OpenFile(clean, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return 0, err
	}
	defer dst.Close()

	// D60 2: UncompressedSize64 is what the ARCHIVE CLAIMS, so the bytes
	// actually read are what count.
	//
	// The LimitReader and the size check below do different jobs. The check
	// decides whether to ACCEPT; the LimitReader decides how much work to do
	// before deciding — without it a bomb declaring 10 GiB is fully written to
	// disk and then rejected. Only the first is visible in the return value, so
	// removing the LimitReader does not fail a test; it fails the machine.
	remaining := int64(MaxEntryBytes)
	if left := int64(MaxTotalBytes) - soFar; left < remaining {
		remaining = left
	}
	written, err := io.Copy(dst, io.LimitReader(rc, remaining+1))
	if err != nil {
		return 0, err
	}
	if written > remaining {
		return 0, fmt.Errorf("%w: %q 에서 상한 초과", ErrZipTooLarge, f.Name)
	}
	// A ratio check on top of the byte cap. It matters BELOW the cap: 4 KiB that
	// unpacks to 8 MiB never trips the 20 MiB limit, and 500 such entries fill
	// the disk of an NFR-101 box while every single one looks reasonable.
	if f.CompressedSize64 > 0 && written/int64(f.CompressedSize64) > MaxCompressRate {
		return 0, fmt.Errorf("%w: %q (%d배)", ErrZipRatio, f.Name, written/int64(f.CompressedSize64))
	}
	return written, nil
}

// entryName canonicalises an archive entry name and refuses anything that is
// not a plain relative path inside the theme.
//
// 측정한 것 (2026-08-05, go1.26, macOS): os.Root 는 `/etc/passwd` 와
// `../escaped` 를 모두 `path escapes from parent` 로 거부한다. 그래서 이 함수의
// 상위·절대 경로 검사를 지워도 파일은 루트 밖으로 나가지 않는다 — Install 을
// 통한 변이는 물지 않는다.
//
// 그래도 두는 이유는 둘이다.
//
//  1. **역슬래시.** `..\escaped` 는 os.Root 가 **통과시킨다** — POSIX 에서
//     역슬래시는 평범한 문자라 루트 안에 이상한 이름의 파일이 생기고, 같은
//     아카이브가 Windows 에서는 실제로 탈출한다. 위의 정규화가 그것을 `../` 로
//     바꿔 아래 검사에 걸리게 한다. 이 검사는 변이하면 문다.
//  2. **오류 메시지.** 어느 엔트리가 왜 거부됐는지 말한다. os.Root 의 오류는
//     openat 경로만 말하고, 업로드 화면은 그것을 그대로 보여줄 수 없다.
//
// 아래 검사들은 entryName 자체를 대상으로 시험한다 (upload_test.go) — Install
// 을 통과시키는 것은 os.Root 이지만, 이 함수가 무엇을 거부하는지는 이 함수에
// 물어야 한다.
func entryName(name string) (string, error) {
	if strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("%w: 이름에 NUL", ErrZipEntry)
	}
	// Windows-created archives use backslashes; treating them as ordinary
	// characters would let `..\..\x` through the dot-dot check below.
	n := strings.ReplaceAll(name, `\`, "/")
	if path.IsAbs(n) || strings.HasPrefix(n, "/") {
		return "", fmt.Errorf("%w: 절대 경로 %q", ErrZipEntry, name)
	}
	if len(n) > 1 && n[1] == ':' {
		return "", fmt.Errorf("%w: 드라이브 문자 %q", ErrZipEntry, name)
	}
	clean := path.Clean(n)
	if clean == "." {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: 상위 경로 %q", ErrZipEntry, name)
	}
	if depth := strings.Count(clean, "/") + 1; depth > MaxDepth {
		return "", fmt.Errorf("%w: %q (%d단계)", ErrZipTooDeep, name, depth)
	}
	return clean, nil
}

// validThemeName bounds the directory a theme lands in. It is the same shape
// D30 gives boards.skin, and it is what keeps the name from being a path.
func validThemeName(name string) error {
	if name == "" || len(name) > 64 || name != path.Base(name) {
		return ErrThemeBadName
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return ErrThemeBadName
		}
	}
	return nil
}
