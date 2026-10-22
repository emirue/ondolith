// Package s3store keeps uploaded files in an S3-compatible bucket (FR-717,
// A-210). It is the remote side of content.Files: validation, naming and the
// permission checks before serving all stay where they are, and this package
// only moves bytes under the keys content already chose (`YYYY/MM/<uuid>`).
package s3store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// Config is what A-210 stores (D30 `storage.s3.*`).
type Config struct {
	// Endpoint is empty for AWS S3, or the scheme and host of another
	// S3-compatible service (Lightsail 버킷·R2·MinIO).
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	// PathStyle puts the bucket in the path instead of the host name — MinIO
	// and most self-hosted services need it.
	PathStyle bool
}

// Store is one bucket.
type Store struct {
	c      *s3.Client
	bucket string
}

// New builds a client for cfg. control, if not nil, runs after DNS resolution
// and right before each connect (net.Dialer.Control) — the app passes the
// metadata-range guard the SMTP dialer uses (D60 「파일 저장소」).
func New(cfg Config, control func(network, address string, c syscall.RawConn) error) *Store {
	// **기한을 적는다** (NFR-101, 메일과 같다). SDK 기본 전송은 접속 30초·TLS 10초만 있고
	// 응답 머리글 시한이 없다 — 받아 놓고 답하지 않는 버킷 하나가 내려받기 요청을 붙든다.
	httpClient := awshttp.NewBuildableClient().WithDialerOptions(func(d *net.Dialer) {
		d.Timeout = 10 * time.Second
		d.Control = control
	}).WithTransportOptions(func(t *http.Transport) {
		t.ResponseHeaderTimeout = 30 * time.Second
	})
	o := s3.Options{
		Region:       cfg.Region,
		Credentials:  aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")),
		UsePathStyle: cfg.PathStyle,
		HTTPClient:   httpClient,
		// **체크섬은 요구될 때만** 붙이고 검사한다. SDK 문서(「Data integrity protection
		// with checksums」)는 v1.74.1 부터 업로드마다 CRC32 를 기본으로 보낸다고 하는데,
		// 그 기본값은 config.LoadDefaultConfig 가 `WhenSupported` 로 채우는 것이고
		// s3.New 로 만들면 비어 있어 붙지 않는다(service/internal/checksum, 2026-10-21 확인).
		// 그 머리글을 모르는 S3 호환 서비스가 업로드를 거부하므로(FR-717 「호환이면
		// 어디든」) 기본값이 바뀌어도 그렇게 남도록 적어 둔다. fakes3 가 그 머리글을
		// 거부해 시험이 지킨다. 무결성은 TLS 와 SigV4 가 지킨다.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	}
	if cfg.Endpoint != "" {
		o.BaseEndpoint = aws.String(cfg.Endpoint)
	}
	return &Store{c: s3.New(o), bucket: cfg.Bucket}
}

// Put stores body under key. size is the exact length: the SDK signs the
// payload, so the body must be seekable and its length known.
func (s *Store) Put(ctx context.Context, key string, body io.ReadSeeker, size int64) error {
	_, err := s.c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key),
		Body: body, ContentLength: aws.Int64(size)})
	return err
}

// Open starts reading key. A missing object is fs.ErrNotExist, so the caller
// can look in the upload directory instead (content.Files).
//
// The returned reader seeks — http.ServeContent needs that for Range and for
// the size. The first GET's body serves a plain full read; a seek elsewhere
// re-requests from that offset only when it is read.
func (s *Store) Open(ctx context.Context, key string) (io.ReadSeekCloser, error) {
	out, err := s.c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, notExist(err)
	}
	if out.ContentLength == nil {
		out.Body.Close()
		return nil, fmt.Errorf("s3store: %s: 응답에 길이가 없다", key)
	}
	return &object{ctx: ctx, s: s, key: key, size: *out.ContentLength, body: out.Body}, nil
}

// Remove deletes key. S3 answers a missing key with success, which is what the
// local side does too (content.RemoveUpload).
func (s *Store) Remove(ctx context.Context, key string) error {
	_, err := s.c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	return err
}

// checkPrefix keeps the probe away from upload keys, which start with a year
// or `media/`.
const checkPrefix = "ondolith-check/"

// Check writes, reads back and deletes a probe object — A-210's 연결 확인. The
// three calls are the three the site makes; a key that may list but not write
// passes a HEAD-bucket check and then fails every upload.
func (s *Store) Check(ctx context.Context) error {
	key := checkPrefix + rand.Text()
	const body = "ondolith"
	if err := s.Put(ctx, key, strings.NewReader(body), int64(len(body))); err != nil {
		return err
	}
	f, err := s.Open(ctx, key)
	if err != nil {
		return err
	}
	got, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		return err
	}
	if string(got) != body {
		return fmt.Errorf("s3store: 시험 객체를 다르게 돌려받았다 (%d 바이트)", len(got))
	}
	if err := s.Remove(ctx, key); err != nil {
		return err
	}
	// **지운 키는 「없다」로 와야 한다.** AWS 는 키에 s3:ListBucket 이 없으면 없는 객체를
	// 403 AccessDenied 로 답한다(GetObject 문서). 그러면 행은 있고 파일이 없는 첨부가
	// 404 가 아니라 500 이 된다 — 저장 전에 거부한다.
	if f, err := s.Open(ctx, key); err == nil {
		f.Close()
		return fmt.Errorf("s3store: 지운 시험 객체가 그대로 열린다")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %w", ErrMissingNotReported, err)
	}
	return nil
}

// ErrMissingNotReported means the bucket answers a missing key with something
// other than NoSuchKey — on AWS, the key lacks s3:ListBucket (A-210 연결 확인).
var ErrMissingNotReported = errors.New("s3store: 없는 객체를 「없다」로 답하지 않는다 — 키에 s3:ListBucket 권한이 필요하다")

// Code is the service's error code (`NoSuchBucket`, `InvalidAccessKeyId` …), or
// "" when the request never got an answer — a refused dial, a timeout. A-210
// shows the code and nothing else from the response (D60 「파일 저장소」).
func Code(err error) string {
	var api smithy.APIError
	if errors.As(err, &api) {
		return api.ErrorCode()
	}
	return ""
}

func notExist(err error) error {
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return fmt.Errorf("%w: %w", fs.ErrNotExist, err)
	}
	return err
}

// object is an S3 object as an io.ReadSeekCloser.
//
// **잠금이 있다.** http.ServeContent 는 여러 구간 요청을 고루틴으로 읽고, 클라이언트가
// 끊기면 핸들러의 Close 가 그 Read 와 겹친다. *os.File 은 그것을 견디지만 이 구조체의
// 필드는 그렇지 않았다 — body 를 nil 로 바꾸는 Close 와 그것을 읽는 Read 가 겹치면 핸들러
// 밖 고루틴의 nil 역참조로 프로세스가 죽는다. 네트워크에서 읽는 동안은 잠그지 않는다:
// Close 가 그 읽기를 기다리지 않게. 응답 본문은 Read 와 Close 가 겹쳐도 된다(net/http).
type object struct {
	ctx  context.Context
	s    *Store
	key  string
	size int64

	mu     sync.Mutex
	off    int64
	closed bool
	// body streams from bodyOff; nil until a read needs it.
	body    io.ReadCloser
	bodyOff int64
}

func (o *object) Read(p []byte) (int, error) {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return 0, os.ErrClosed
	}
	if o.body != nil && o.bodyOff != o.off {
		o.body.Close()
		o.body = nil
	}
	if o.off >= o.size {
		o.mu.Unlock()
		return 0, io.EOF
	}
	if o.body == nil {
		out, err := o.s.c.GetObject(o.ctx, &s3.GetObjectInput{Bucket: aws.String(o.s.bucket),
			Key: aws.String(o.key), Range: aws.String(fmt.Sprintf("bytes=%d-", o.off))})
		if err != nil {
			o.mu.Unlock()
			return 0, notExist(err)
		}
		o.body, o.bodyOff = out.Body, o.off
	}
	body := o.body
	o.mu.Unlock()

	n, err := body.Read(p)

	o.mu.Lock()
	if o.body == body {
		o.off += int64(n)
		o.bodyOff += int64(n)
	}
	o.mu.Unlock()
	return n, err
}

func (o *object) Seek(offset int64, whence int) (int64, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = o.off + offset
	case io.SeekEnd:
		abs = o.size + offset
	default:
		return 0, fmt.Errorf("s3store: whence %d", whence)
	}
	if abs < 0 {
		return 0, fmt.Errorf("s3store: 음수 위치 %d", abs)
	}
	o.off = abs
	return abs, nil
}

func (o *object) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closed = true
	if o.body == nil {
		return nil
	}
	err := o.body.Close()
	o.body = nil
	return err
}
