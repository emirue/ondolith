package app

import (
	"context"
	"errors"
	"testing"

	"github.com/emirue/ondolith/internal/admin"
	"github.com/emirue/ondolith/internal/s3store/fakes3"
)

func storageKV(bucket, backend string) map[string]string {
	return map[string]string{"storage.backend": backend, "storage.s3.endpoint": "http://127.0.0.1:1",
		"storage.s3.region": "us-east-1", "storage.s3.bucket": bucket,
		"storage.s3.access_key": "AK", "storage.s3.secret_key": "SK", "storage.s3.path_style": "1"}
}

// 키가 새어 A-210 에서 새 키·새 버킷으로 바꾸면 **다음 요청부터** 새 클라이언트다 —
// 옛 클라이언트를 붙들면 재시작 전까지 폐기된 키로 업로드가 전부 실패한다.
func TestStorageRemoteFollowsTheSettings(t *testing.T) {
	kv := storageKV("one", "s3")
	s := &storageRemote{settings: func(context.Context, ...string) (map[string]string, []string, error) {
		return kv, nil, nil
	}}
	ctx := context.Background()
	first, write, err := s.resolve(ctx)
	if err != nil || first == nil || !write {
		t.Fatalf("resolve = %v, %v, %v", first, write, err)
	}
	again, _, _ := s.resolve(ctx)
	if again != first {
		t.Fatal("같은 설정인데 클라이언트를 새로 만들었다 — 연결을 다시 맺는다")
	}
	kv = storageKV("two", "s3")
	if next, _, _ := s.resolve(ctx); next == first {
		t.Fatal("버킷을 바꿨는데 옛 클라이언트를 쓴다")
	}
	// 로컬로 되돌리면 버킷은 읽기·지우기에만 쓴다 — 새 업로드를 보내지 않는다.
	kv = storageKV("two", "")
	if r, write, err := s.resolve(ctx); err != nil || r == nil || write {
		t.Fatalf("로컬로 되돌린 뒤 resolve = %v, write=%v, %v", r, write, err)
	}
}

// 설정 파일의 secret_key 가 바뀌어 S3 시크릿이 열리지 않을 때: 로컬이면 로컬로 계속 돈다
// (A-210 에서 다시 넣을 때까지 버킷 파일만 못 본다). S3 면 조용히 로컬로 쓰지 않고 실패한다.
func TestStorageRemoteWithAnUnreadableSecret(t *testing.T) {
	for _, backend := range []string{"", "s3"} {
		kv := storageKV("one", backend)
		delete(kv, "storage.s3.secret_key")
		s := &storageRemote{settings: func(context.Context, ...string) (map[string]string, []string, error) {
			return kv, []string{"storage.s3.secret_key"}, nil
		}}
		r, write, err := s.resolve(context.Background())
		switch backend {
		case "":
			if err != nil || r != nil || write {
				t.Errorf("로컬: resolve = %v, %v, %v — 로컬 업로드까지 막혔다", r, write, err)
			}
		case "s3":
			if err == nil {
				t.Error("S3: 열리지 않는 시크릿인데 오류가 없다 — 업로드가 조용히 로컬로 간다")
			}
		}
	}
}

// 연결 확인도 메타데이터 대역 판정(D60)을 지난다 — app 이 실제로 그 판정을 붙였는지.
func TestCheckStorageRefusesTheMetadataRange(t *testing.T) {
	kv := storageKV("one", "s3")
	kv["storage.s3.endpoint"] = "http://169.254.169.254:80"
	code, err := checkStorage(context.Background(), kv)
	if !errors.Is(err, ErrMailHostBlocked) || code != "" {
		t.Fatalf("checkStorage = %q, %v; want ErrMailHostBlocked, no code", code, err)
	}
}

// 없는 키를 403 으로 답하는 버킷은 「s3:ListBucket」 이 빠진 것이라고 말한다 (A-210).
func TestCheckStorageNamesTheMissingListPermission(t *testing.T) {
	srv := fakes3.New("one")
	defer srv.Close()
	srv.MissingDenied = true
	kv := storageKV("one", "s3")
	kv["storage.s3.endpoint"] = srv.URL
	code, err := checkStorage(context.Background(), kv)
	if err == nil || code != admin.StorageCodeListBucket {
		t.Fatalf("checkStorage = %q, %v; want %q", code, err, admin.StorageCodeListBucket)
	}
}
