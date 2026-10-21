package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/emirue/ondolith/internal/admin"
	"github.com/emirue/ondolith/internal/content"
	"github.com/emirue/ondolith/internal/s3store"
)

// s3ConfigOf reads A-210's keys (FR-717). ok is false until every field a
// request needs is there — 엔드포인트는 비어도 된다(AWS S3).
func s3ConfigOf(kv map[string]string) (s3store.Config, bool) {
	c := s3store.Config{
		Endpoint:  kv[content.SettingStorageEndpoint],
		Region:    kv[content.SettingStorageRegion],
		Bucket:    kv[content.SettingStorageBucket],
		AccessKey: kv[content.SettingStorageAccessKey],
		SecretKey: kv[content.SettingStorageSecretKey],
		PathStyle: kv[content.SettingStoragePathStyle] != "",
	}
	return c, c.Region != "" && c.Bucket != "" && c.AccessKey != "" && c.SecretKey != ""
}

// errStorageIncomplete means the form lacks a field S3 needs. A-210 checks the
// fields first, so this is the guard behind that check.
var errStorageIncomplete = errors.New("app: 파일 저장소 설정에 빈 칸이 있다")

// newS3Store dials through the same metadata-range guard as SMTP (D60 「파일 저장소」).
func newS3Store(c s3store.Config) *s3store.Store { return s3store.New(c, blockMetadataAddr) }

// storageRemote is content.RemoteFunc over the settings table.
//
// 설정은 요청마다 읽는다 — A-210 이 바꾸면 다음 요청부터다 (A-209 와 같다). 클라이언트는
// 설정이 같은 동안 하나를 쓴다: 요청마다 만들면 연결을 다시 맺고 다 쓴 연결이 쌓인다.
//
// **열리지 않는 시크릿**(설정 파일의 secret_key 가 바뀌었다, D60)은 저장 위치로 가른다.
// 로컬이면 버킷이 없는 것처럼 로컬로 계속 돈다 — 업로드까지 막으면 A-210 에서 다시 넣기
// 전까지 사이트가 멈춘다. S3 면 오류다: 조용히 로컬로 쓰면 운영자는 S3 로 간다고 믿는다.
type storageRemote struct {
	settings func(ctx context.Context, keys ...string) (map[string]string, []string, error)

	mu  sync.Mutex
	cfg s3store.Config
	st  *s3store.Store
}

func (s *storageRemote) resolve(ctx context.Context) (content.Remote, bool, error) {
	kv, unreadable, err := s.settings(ctx, content.StorageSettingKeys...)
	if err != nil {
		return nil, false, err
	}
	write := kv[content.SettingStorageBackend] == "s3"
	if write && len(unreadable) > 0 {
		return nil, false, fmt.Errorf("app: 파일 저장소 시크릿 키를 열 수 없다 — A-210 에서 다시 입력하라 (%v)", unreadable)
	}
	cfg, ok := s3ConfigOf(kv)
	if !ok {
		return nil, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st == nil || s.cfg != cfg {
		s.cfg, s.st = cfg, newS3Store(cfg)
	}
	return s.st, write, nil
}

// checkStorage is A-210's 연결 확인 against the values on the form, not the
// saved ones — 저장 전에 맞는지 알아야 한다. code is the service's error code
// when it answered with a refusal (s3store.Code).
func checkStorage(ctx context.Context, kv map[string]string) (code string, err error) {
	cfg, ok := s3ConfigOf(kv)
	if !ok {
		return "", errStorageIncomplete
	}
	err = newS3Store(cfg).Check(ctx)
	if errors.Is(err, s3store.ErrMissingNotReported) {
		return admin.StorageCodeListBucket, err
	}
	return s3store.Code(err), err
}
