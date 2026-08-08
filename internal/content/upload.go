package content

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Upload validation is D60's four checks. All four, because each one alone is
// bypassable:
//
//	1. extension allow-list  — a deny-list cannot enumerate .php5, .phtml, .svg…
//	2. magic bytes           — the client's Content-Type is a claim, not a fact
//	3. regenerated filename  — kills path traversal and executable placement
//	4. no execute bit (0644) — belt to the braces of storing outside the web root
//
// Dropping any one of them re-opens the hole the other three were closing from
// a different side: an allow-list without magic bytes accepts a .php renamed to
// .png; magic bytes without a regenerated name still writes `../../x.png`.

var (
	ErrUploadExt      = errors.New("content: 허용하지 않는 확장자")
	ErrUploadContent  = errors.New("content: 파일 내용이 확장자와 다릅니다")
	ErrUploadTooLarge = errors.New("content: 파일이 너무 큽니다")
	ErrUploadEmpty    = errors.New("content: 빈 파일")
	// ErrUploadNotAllowed 는 **게시판이 첨부 자체를 받지 않는다**는 뜻이다
	// (D19 P-205: `allow_attachments` 가 false). ErrUploadExt 를 재사용하면
	// 화면이 「허용하지 않는 확장자」라고 말해서, 파일을 바꿔 다시 올려 보는
	// 사람을 만든다 — 무엇을 올려도 결과는 같다.
	ErrUploadNotAllowed = errors.New("content: 이 게시판은 첨부를 받지 않습니다")
	// ErrUploadTooMany 는 글당 첨부 개수 상한을 넘었다는 뜻이다.
	ErrUploadTooMany = errors.New("content: 첨부가 너무 많습니다")
	// ErrUploadSetting 은 A-309 설정값이 올바르지 않다는 뜻이다.
	ErrUploadSetting = errors.New("content: 첨부 설정값이 올바르지 않습니다")
)

// MaxAttachmentBytes bounds one attachment by default. NFR-101 sizes the box at
// 1 vCPU and 512MB–1GB; a request that buffers more than this is a denial of
// service on its own, whatever the file turns out to be.
//
// 운영자가 A-309 에서 낮추거나 올릴 수 있다 (`upload.max_bytes`, OPEN-41 결정).
const MaxAttachmentBytes = 20 << 20 // 20 MiB

// MaxAttachmentsPerPost bounds how many files one post carries by default
// (`upload.max_per_post`). 상한이 없으면 글 하나가 디스크를 다 쓴다.
const MaxAttachmentsPerPost = 10

// 설정 키 (A-309). 값은 UploadLimits 가 읽고 검증한다.
const (
	SettingUploadMaxBytes   = "upload.max_bytes"
	SettingUploadMaxPerPost = "upload.max_per_post"
	SettingUploadDenyExt    = "upload.deny_ext"
)

// UploadLimits are the bounds one upload is checked against.
type UploadLimits struct {
	MaxBytes   int64
	MaxPerPost int
	// Denied 는 **내장 허용목록에서 뺄 확장자**다. 더할 수는 없다.
	//
	// 권고는 "허용 확장자 목록을 설정값으로" 였지만, 자유 목록이면 운영자가
	// `.svg` 를 한 줄로 되살릴 수 있다 — D60 이 SVG 를 뺀 이유(스크립트를
	// 실어 나른다)와 그때 함께 열기로 한 첨부 전용 서빙 경로가 폼 한 칸으로
	// 무효가 된다. 빼는 것은 안전하고, 더하는 것은 코드 변경이다.
	Denied map[string]bool
}

// DefaultUploadLimits is what a site with no settings gets.
func DefaultUploadLimits() UploadLimits {
	return UploadLimits{MaxBytes: MaxAttachmentBytes, MaxPerPost: MaxAttachmentsPerPost}
}

// Allows reports whether ext is uploadable under these limits.
func (l UploadLimits) Allows(ext string) bool {
	if l.Denied[ext] {
		return false
	}
	_, ok := allowedUploads[ext]
	return ok
}

// allowedUploads maps an allowed extension to the content types
// http.DetectContentType reports for that format.
//
// `.svg` is deliberately absent (D60): SVG carries script, and maintaining a
// sanitiser costs more than SVG attachments are worth. If it is ever needed it
// opens together with an attachment-only serving path.
var allowedUploads = map[string][]string{
	".jpg":  {"image/jpeg"},
	".jpeg": {"image/jpeg"},
	".png":  {"image/png"},
	".gif":  {"image/gif"},
	".webp": {"image/webp"},
	".pdf":  {"application/pdf"},
	".zip":  {"application/zip"},
	".txt":  {"text/plain; charset=utf-8"},
	".csv":  {"text/plain; charset=utf-8"},
}

// StoredUpload is what the caller writes to attachments (D30).
type StoredUpload struct {
	// StoredPath is YYYY/MM/<uuid>, relative and without an extension. Absolute
	// paths die the moment an operator moves the upload directory.
	StoredPath string
	// OriginalName is for display only. It never reaches the filesystem.
	OriginalName string
	MIMEType     string
	ByteSize     int64
}

// ValidateUpload runs checks 1 and 2 and reports the type it actually found.
//
// It reads the head of r and returns a reader that still yields the whole
// content, so the caller does not have to buffer the file twice.
func ValidateUpload(name string, r io.Reader, limits UploadLimits) (mime string, body io.Reader, err error) {
	ext := strings.ToLower(filepath.Ext(name))
	if !limits.Allows(ext) {
		return "", nil, fmt.Errorf("%w: %q", ErrUploadExt, ext)
	}
	want := allowedUploads[ext]

	// 512 bytes is what http.DetectContentType looks at.
	head := make([]byte, 512)
	n, err := io.ReadFull(r, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", nil, err
	}
	head = head[:n]
	if n == 0 {
		return "", nil, ErrUploadEmpty
	}

	got := http.DetectContentType(head)
	if !matchesType(got, want) {
		// The message says what was found, not what was expected: an attacker
		// probing the allow-list learns nothing they did not already send.
		return "", nil, fmt.Errorf("%w: %s", ErrUploadContent, got)
	}
	return got, io.MultiReader(strings.NewReader(string(head)), r), nil
}

func matchesType(got string, want []string) bool {
	// DetectContentType appends parameters for text types.
	base, _, _ := strings.Cut(got, ";")
	base = strings.TrimSpace(base)
	for _, w := range want {
		wb, _, _ := strings.Cut(w, ";")
		if strings.TrimSpace(wb) == base {
			return true
		}
	}
	return false
}

// StoreUpload runs checks 3 and 4: it writes the body under a regenerated name
// with no execute bit, and returns the row to record.
//
// root is the upload directory, which lives outside the web root. Writes go
// through os.Root so that no component of the path can leave it — the escape
// check is the standard library's, not one written here (NFR-201).
func StoreUpload(root, name string, r io.Reader, now time.Time, limits UploadLimits) (StoredUpload, error) {
	mime, body, err := ValidateUpload(name, r, limits)
	if err != nil {
		return StoredUpload{}, err
	}

	dir := now.UTC().Format("2006/01")
	// The name on disk is a UUID with no extension. D60 §3: path traversal and
	// executable placement die together, because there is no attacker-supplied
	// component and no name a web server would ever execute.
	rel := path.Join(dir, newUUID())

	rt, err := os.OpenRoot(root)
	if err != nil {
		return StoredUpload{}, err
	}
	defer rt.Close()

	// 0755 on the directory: readable and traversable, and the execute bit on a
	// directory is what "traversable" means — it is not the executable bit D60
	// §4 is about, which is the file's.
	if err := rt.MkdirAll(dir, 0o755); err != nil {
		return StoredUpload{}, err
	}

	f, err := rt.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return StoredUpload{}, err
	}
	// One more byte than the limit, so hitting exactly the limit is not
	// mistaken for overflow and overflow is always detected.
	written, copyErr := io.Copy(f, io.LimitReader(body, limits.MaxBytes+1))
	closeErr := f.Close()
	switch {
	case copyErr != nil:
		_ = rt.Remove(rel)
		return StoredUpload{}, copyErr
	case closeErr != nil:
		_ = rt.Remove(rel)
		return StoredUpload{}, closeErr
	case written > limits.MaxBytes:
		// The partial file goes: a half-written attachment that no row points
		// at is litter nobody will ever identify.
		_ = rt.Remove(rel)
		return StoredUpload{}, fmt.Errorf("%w: %d 바이트 초과", ErrUploadTooLarge, limits.MaxBytes)
	case written == 0:
		_ = rt.Remove(rel)
		return StoredUpload{}, ErrUploadEmpty
	}

	return StoredUpload{
		StoredPath:   rel,
		OriginalName: filepath.Base(name),
		MIMEType:     mime,
		ByteSize:     written,
	}, nil
}

// newUUID is a v4 UUID in the 8-4-4-4-12 form D30's stored_path CHECK expects.
//
// No dependency for this: it is ten lines of crypto/rand, and a new module
// would need a D21 entry and an NFR-209 vulnerability check to carry them.
// crypto/rand.Read cannot fail on any supported platform — it panics instead —
// so there is no error to return.
func newUUID() string {
	var b [16]byte
	rand.Read(b[:]) // #nosec G104 -- crypto/rand.Read panics rather than failing
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
