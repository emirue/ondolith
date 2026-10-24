package admin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emirue/ondolith/internal/content"
	"github.com/emirue/ondolith/internal/s3store"
	"github.com/emirue/ondolith/internal/s3store/fakes3"
)

// A-210 의 S3 칸 — 형식이 맞는 한 벌.
func storageForm(extra url.Values) url.Values {
	v := url.Values{
		"storage.backend":       {"s3"},
		"storage.s3.endpoint":   {"https://s3.example.com"},
		"storage.s3.region":     {"us-east-1"},
		"storage.s3.bucket":     {"ondolith-site"},
		"storage.s3.access_key": {"AKIDEXAMPLE"},
		"storage.s3.secret_key": {"SUPERSECRET-S3"},
	}
	for k, vs := range extra {
		v[k] = vs
	}
	return v
}

func superStorage(t *testing.T) (*Deps, *[]map[string]string) {
	t.Helper()
	d, _ := fixture(t, &fakeCaller{superuser: true, email: "root@example.com"})
	var checked []map[string]string
	d.CheckStorage = func(_ context.Context, kv map[string]string) (string, error) {
		cp := map[string]string{}
		for k, v := range kv {
			cp[k] = v
		}
		checked = append(checked, cp)
		return "", nil
	}
	return d, &checked
}

// R8: 저장소를 바꾸는 사람은 그 뒤의 모든 파일을 받는다 — superuser 만 (D15 5.1).
// 보는 것은 settings.update 로 된다.
func TestStorageSettingsAreSuperuserOnly(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"settings.update": true}, email: "op@example.com"})
	d.CheckStorage = func(context.Context, map[string]string) (string, error) {
		t.Fatal("운영자의 요청이 저장소에 닿았다")
		return "", nil
	}
	if rec := get(d.StorageSettingsForm, "/admin/settings/storage"); rec.Code != http.StatusOK {
		t.Fatalf("GET = HTTP %d", rec.Code)
	}
	for _, action := range []string{"", "verify", "migrate"} {
		rec := postAdmin(t, d.StorageSettingsSave, "/admin/settings/storage", nil,
			storageForm(url.Values{"action": {action}}))
		if rec.Code != http.StatusForbidden {
			t.Errorf("action=%q: HTTP %d, want 403", action, rec.Code)
		}
	}
	kv, _ := d.Content.Settings(context.Background(), content.StorageSettingKeys...)
	if kv["storage.backend"] != "" {
		t.Fatalf("운영자의 저장이 들어갔다: %v", kv)
	}
}

func TestStorageSettingsRequireReauth(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{superuser: true, reauth: true, password: "pw-correct"})
	d.CheckStorage = func(context.Context, map[string]string) (string, error) { return "", nil }
	// 연결 확인·옮기기도 같은 문이다 — 연결 확인은 폼의 엔드포인트로 서버가 접속하고,
	// 옮기기는 이미 있는 파일을 보낸다 (D15 R8).
	for _, action := range []string{"", "verify", "migrate"} {
		rec := postAdmin(t, d.StorageSettingsSave, "/admin/settings/storage", nil,
			storageForm(url.Values{"action": {action}}))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("action=%q 재인증 없이 HTTP %d, want 403", action, rec.Code)
		}
	}
	rec := postAdmin(t, d.StorageSettingsSave, "/admin/settings/storage", nil,
		storageForm(url.Values{"password": {"pw-correct"}}))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("재인증하고 HTTP %d (%s)", rec.Code, rec.Body.String())
	}
}

// 「S3」로 저장하려면 연결 확인이 먼저 통과해야 한다 (FR-717). 틀린 키로 바꾸면 그
// 순간부터 모든 업로드가 실패하는데, 운영자는 누가 글을 올릴 때에야 안다.
func TestStorageSaveNeedsAPassingCheck(t *testing.T) {
	d, checked := superStorage(t)
	ctx := context.Background()
	d.CheckStorage = func(_ context.Context, kv map[string]string) (string, error) {
		*checked = append(*checked, kv)
		return "InvalidAccessKeyId", errors.New("api error InvalidAccessKeyId")
	}
	rec := postAdmin(t, d.StorageSettingsSave, "/admin/settings/storage", nil, storageForm(nil))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "InvalidAccessKeyId") {
		t.Fatalf("거부된 키로 저장 = HTTP %d (%s), want 422 + 오류 코드", rec.Code, rec.Body.String())
	}
	if kv, _ := d.Content.Settings(ctx, content.StorageSettingKeys...); kv["storage.backend"] != "" || kv["storage.s3.bucket"] != "" {
		t.Fatalf("확인에 실패했는데 저장됐다: %v", kv)
	}

	// 닿지 못함은 502 다 — 키가 틀린 것과 다른 일이다.
	d.CheckStorage = func(context.Context, map[string]string) (string, error) {
		return "", errors.New("dial tcp: connection refused")
	}
	if rec := postAdmin(t, d.StorageSettingsSave, "/admin/settings/storage", nil, storageForm(nil)); rec.Code != http.StatusBadGateway {
		t.Fatalf("닿지 못함 = HTTP %d, want 502", rec.Code)
	}

	d.CheckStorage = func(context.Context, map[string]string) (string, error) { return "", nil }
	if rec := postAdmin(t, d.StorageSettingsSave, "/admin/settings/storage", nil, storageForm(nil)); rec.Code != http.StatusSeeOther {
		t.Fatalf("통과한 저장 = HTTP %d (%s)", rec.Code, rec.Body.String())
	}
	kv, err := d.Content.Settings(ctx, content.StorageSettingKeys...)
	if err != nil {
		t.Fatal(err)
	}
	if kv["storage.backend"] != "s3" || kv["storage.s3.bucket"] != "ondolith-site" || kv["storage.s3.secret_key"] != "SUPERSECRET-S3" {
		t.Fatalf("저장된 값 %v", kv)
	}
}

// 로컬로 저장하는 데는 확인이 필요 없다 — 버킷에 닿지 않는다. 자격증명만 먼저 넣어
// 둘 수도 있다.
func TestStorageLocalSaveSkipsTheCheck(t *testing.T) {
	d, checked := superStorage(t)
	rec := postAdmin(t, d.StorageSettingsSave, "/admin/settings/storage", nil,
		storageForm(url.Values{"storage.backend": {""}}))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("HTTP %d (%s)", rec.Code, rec.Body.String())
	}
	if len(*checked) != 0 {
		t.Fatalf("로컬 저장이 버킷을 확인했다: %v", *checked)
	}
}

func TestStorageSettingsValidate(t *testing.T) {
	d, checked := superStorage(t)
	for name, extra := range map[string]url.Values{
		"모르는 저장 위치":     {"storage.backend": {"gcs"}},
		"엔드포인트에 경로":     {"storage.s3.endpoint": {"https://s3.example.com/other"}},
		"엔드포인트에 질의":     {"storage.s3.endpoint": {"https://s3.example.com?x=1"}},
		"엔드포인트에 사용자 정보": {"storage.s3.endpoint": {"https://user:pw@s3.example.com"}},
		"엔드포인트 스킴":      {"storage.s3.endpoint": {"ftp://s3.example.com"}},
		"버킷 대문자":        {"storage.s3.bucket": {"Ondolith"}},
		"버킷 짧음":         {"storage.s3.bucket": {"ab"}},
		"버킷 끝이 점":       {"storage.s3.bucket": {"ondolith."}},
		"리전 형식":         {"storage.s3.region": {"us east 1"}},
		"리전 없음":         {"storage.s3.region": {""}},
		"액세스 키 공백":      {"storage.s3.access_key": {"AK ID"}},
		"액세스 키 없음":      {"storage.s3.access_key": {""}},
		"시크릿 없음":        {"storage.s3.secret_key": {""}},
		"시크릿 너무 김":      {"storage.s3.secret_key": {strings.Repeat("s", 201)}},
	} {
		rec := postAdmin(t, d.StorageSettingsSave, "/admin/settings/storage", nil, storageForm(extra))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: HTTP %d, want 422", name, rec.Code)
		}
	}
	if len(*checked) != 0 {
		t.Fatalf("형식이 틀린 값으로 버킷을 불렀다: %d 번", len(*checked))
	}
	kv, _ := d.Content.Settings(context.Background(), content.StorageSettingKeys...)
	for k, v := range kv {
		if v != "" {
			t.Errorf("거부된 저장이 %s=%q 를 남겼다", k, v)
		}
	}
}

// 시크릿은 봉인 목록에 있고(IsSecretSetting) 화면으로 돌아오지 않는다. 빈 칸은
// 「그대로 두라」 — 연결 확인도 저장된 시크릿으로 한다.
func TestStorageSecretStaysAndIsNeverRedisplayed(t *testing.T) {
	d, checked := superStorage(t)
	if rec := postAdmin(t, d.StorageSettingsSave, "/admin/settings/storage", nil, storageForm(nil)); rec.Code != http.StatusSeeOther {
		t.Fatalf("HTTP %d (%s)", rec.Code, rec.Body.String())
	}
	*checked = nil
	rec := postAdmin(t, d.StorageSettingsSave, "/admin/settings/storage", nil,
		storageForm(url.Values{"storage.s3.secret_key": {""}, "action": {"verify"}}))
	if rec.Code != http.StatusOK {
		t.Fatalf("연결 확인 = HTTP %d (%s)", rec.Code, rec.Body.String())
	}
	if len(*checked) != 1 || (*checked)[0]["storage.s3.secret_key"] != "SUPERSECRET-S3" {
		t.Fatalf("빈 시크릿 칸으로 확인할 때 저장된 시크릿을 쓰지 않았다: %v", *checked)
	}
	if strings.Contains(rec.Body.String(), "SUPERSECRET") {
		t.Fatal("시크릿이 화면에 실렸다")
	}

	var shown map[string]string
	var saved map[string]bool
	d.Render = func(_ http.ResponseWriter, _ *http.Request, _ string, _ int, data any) {
		m, _ := data.(map[string]any)
		shown, _ = m["Settings"].(map[string]string)
		saved, _ = m["SecretSaved"].(map[string]bool)
	}
	get(d.StorageSettingsForm, "/admin/settings/storage")
	if _, ok := shown["storage.s3.secret_key"]; ok || !saved["storage.s3.secret_key"] {
		t.Fatalf("shown=%v saved=%v — 시크릿은 저장 여부만 나가야 한다", shown, saved)
	}
	if shown["storage.s3.bucket"] != "ondolith-site" {
		t.Fatalf("버킷 이름이 보이지 않는다: %v", shown)
	}
}

// 연결 확인은 저장하지 않는다.
func TestStorageVerifyDoesNotSave(t *testing.T) {
	d, checked := superStorage(t)
	rec := postAdmin(t, d.StorageSettingsSave, "/admin/settings/storage", nil,
		storageForm(url.Values{"action": {"verify"}}))
	if rec.Code != http.StatusOK || len(*checked) != 1 {
		t.Fatalf("HTTP %d, 확인 %d 번", rec.Code, len(*checked))
	}
	if kv, _ := d.Content.Settings(context.Background(), content.StorageSettingKeys...); kv["storage.s3.bucket"] != "" {
		t.Fatalf("연결 확인이 저장했다: %v", kv)
	}
}

// 작업 로그에는 저장 위치의 이전·새 값과 버킷이 남고, 키는 남지 않는다 (D15 7절).
func TestStorageSaveIsLoggedWithoutTheKeys(t *testing.T) {
	d, pool := fixture(t, &fakeCaller{superuser: true, email: "root@example.com"})
	d.CheckStorage = func(context.Context, map[string]string) (string, error) { return "", nil }
	if rec := postAdmin(t, d.StorageSettingsSave, "/admin/settings/storage", nil, storageForm(nil)); rec.Code != http.StatusSeeOther {
		t.Fatalf("HTTP %d", rec.Code)
	}
	got := lastLog(t, pool)
	for _, want := range []string{"로컬", "S3", "ondolith-site"} {
		if !strings.Contains(got, want) {
			t.Errorf("작업 로그 %q 에 %q 가 없다", got, want)
		}
	}
	for _, leak := range []string{"SUPERSECRET", "AKIDEXAMPLE"} {
		if strings.Contains(got, leak) {
			t.Errorf("작업 로그 %q 에 키가 남았다", got)
		}
	}
}

// 옮기기는 로컬 업로드를 버킷으로 보내고 로컬 사본을 지운다. 저장소가 S3 가
// 아니면 422 — 보낼 곳이 없다.
func TestStorageMigrateMovesLocalUploads(t *testing.T) {
	d, _ := superStorage(t)
	ctx := context.Background()
	root := t.TempDir()
	st, err := content.LocalFiles(root).Store(ctx, "a.pdf", strings.NewReader("%PDF-1.7\n"+strings.Repeat("x", 40)),
		time.Now(), content.DefaultUploadLimits())
	if err != nil {
		t.Fatal(err)
	}
	// 막 쓰인 파일은 옮기지 않는다(쓰는 중일 수 있다) — 오래된 업로드로 만든다.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(st.StoredPath)), old, old); err != nil {
		t.Fatal(err)
	}

	d.Files = content.LocalFiles(root)
	if rec := postAdmin(t, d.StorageSettingsSave, "/admin/settings/storage", nil,
		url.Values{"action": {"migrate"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("로컬 저장소에서 옮기기 = HTTP %d, want 422", rec.Code)
	}

	bucket := fakes3.New("site")
	defer bucket.Close()
	remote := s3store.New(s3store.Config{Endpoint: bucket.URL, Region: "us-east-1", Bucket: "site",
		AccessKey: "AK", SecretKey: "SK", PathStyle: true}, nil)
	d.Files = content.NewFiles(root, func(context.Context) (content.Remote, bool, error) { return remote, true, nil })
	rec := postAdmin(t, d.StorageSettingsSave, "/admin/settings/storage", nil, url.Values{"action": {"migrate"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "1") {
		t.Fatalf("옮기기 = HTTP %d (%s)", rec.Code, rec.Body.String())
	}
	if _, ok := bucket.Object(st.StoredPath); !ok {
		t.Fatalf("버킷에 %s 가 없다 (%v)", st.StoredPath, bucket.Keys())
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(st.StoredPath))); !os.IsNotExist(err) {
		t.Fatalf("옮긴 뒤 로컬 사본이 남았다: %v", err)
	}
}

// 모르는 동작은 422 다 — 저장으로 흘러가지 않는다.
func TestStorageUnknownActionIsRefused(t *testing.T) {
	d, checked := superStorage(t)
	rec := postAdmin(t, d.StorageSettingsSave, "/admin/settings/storage", nil,
		storageForm(url.Values{"action": {"delete-all"}}))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("HTTP %d, want 422", rec.Code)
	}
	if len(*checked) != 0 {
		t.Fatal("모르는 동작이 버킷을 불렀다")
	}
	if kv, _ := d.Content.Settings(context.Background(), content.StorageSettingKeys...); kv["storage.backend"] != "" {
		t.Fatalf("모르는 동작이 저장했다: %v", kv)
	}
}

// **버킷이 지우기를 거부해도 글·상품 삭제는 성공으로 넘어간다** (A-309 와 같은 규칙).
// 행은 이미 갔다 — 500 을 보이면 운영자는 실패한 줄 알고 다시 누르고, 그때는 404 다.
// 작업 로그는 남는다.
func TestDeletesGoOnWhenTheBucketRefusesToDelete(t *testing.T) {
	d, pool := fixture(t, &fakeCaller{superuser: true, email: "root@example.com"})
	ctx := context.Background()
	bucket := fakes3.New("site")
	defer bucket.Close()
	remote := s3store.New(s3store.Config{Endpoint: bucket.URL, Region: "us-east-1", Bucket: "site",
		AccessKey: "AK", SecretKey: "SK", PathStyle: true}, nil)
	files := content.NewFiles(t.TempDir(), func(context.Context) (content.Remote, bool, error) { return remote, true, nil })
	d.Attachments = d.Content.AttachmentsIn(files)
	d.Images = d.Commerce.ImagesIn(files)

	boardID, err := d.Content.CreateBoard(ctx, content.Board{Slug: "free", Name: "자유", PerPage: 20}, content.PresetPublic)
	if err != nil {
		t.Fatal(err)
	}
	postID, err := d.Content.CreatePost(ctx, content.Post{BoardID: boardID, Title: "지울 글", Body: "본문"})
	if err != nil {
		t.Fatal(err)
	}
	png := "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR" + strings.Repeat("\x00", 40)
	if _, err := d.Attachments.Save(ctx, postID, "a.png", strings.NewReader(png)); err != nil {
		t.Fatal(err)
	}
	var productID string
	if err := pool.QueryRow(ctx, `INSERT INTO products (slug,name,base_price,is_visible)
		VALUES ('mat','매트',1000,true) RETURNING id`).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Images.Save(ctx, productID, "p.png", strings.NewReader(png), content.DefaultUploadLimits()); err != nil {
		t.Fatal(err)
	}
	if len(bucket.Keys()) != 2 {
		t.Fatalf("버킷 %v", bucket.Keys())
	}
	bucket.RejectCode = "AccessDenied"

	if rec := postAdmin(t, d.PostModerate, "/admin/posts", nil,
		url.Values{"post_id": {postID}, "action": {"delete"}}); rec.Code != http.StatusSeeOther && rec.Code != http.StatusOK {
		t.Fatalf("글 삭제 = HTTP %d (%s)", rec.Code, rec.Body.String())
	}
	if got := lastLog(t, pool); !strings.Contains(got, "글 삭제") {
		t.Errorf("작업 로그 %q", got)
	}
	if rec := postAdmin(t, d.ProductDelete, "/admin/products/"+productID+"/delete",
		map[string]string{"id": productID}, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("상품 삭제 = HTTP %d (%s)", rec.Code, rec.Body.String())
	}
	if got := lastLog(t, pool); !strings.Contains(got, "상품 삭제") {
		t.Errorf("작업 로그 %q", got)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM posts) + (SELECT count(*) FROM products)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("남은 행 %d (%v)", n, err)
	}
}

// 없는 키를 403 으로 답하는 버킷(AWS, ListBucket 없음)은 저장되지 않고, 무엇을 더할지 말한다.
func TestStorageAsksForTheListPermission(t *testing.T) {
	d, _ := superStorage(t)
	d.CheckStorage = func(context.Context, map[string]string) (string, error) {
		return StorageCodeListBucket, errors.New("missing key answered with AccessDenied")
	}
	rec := postAdmin(t, d.StorageSettingsSave, "/admin/settings/storage", nil, storageForm(nil))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "s3:ListBucket") ||
		!strings.Contains(rec.Body.String(), "목록") {
		t.Fatalf("HTTP %d (%s)", rec.Code, rec.Body.String())
	}
}

// openFails is a sealer whose sealed values no longer open — 설정 파일의 secret_key 가
// 바뀐 뒤의 상태다 (D60 「저장된 자격증명」).
type openFails struct{}

func (openFails) Seal(_, pt string) (string, error) { return "sealed:" + pt, nil }
func (openFails) Open(_, v string) (string, bool, error) {
	if strings.HasPrefix(v, "sealed:") {
		return "", true, errors.New("secret_key 가 바뀌었다")
	}
	return v, false, nil
}

// **시크릿이 열리지 않아도 A-210 에서 빠져나갈 수 있어야 한다.** 화면은 「다시 입력해
// 저장하세요」라고 하는데 그 저장이 500 이면 DB 를 손으로 고치는 것 말고는 길이 없다.
func TestStorageSaveWorksWhenTheSecretNoLongerOpens(t *testing.T) {
	d, checked := superStorage(t)
	ctx := context.Background()
	d.Content.UseSealer(openFails{})
	if err := d.Content.PutSettings(ctx, map[string]string{"storage.backend": "s3",
		"storage.s3.region": "us-east-1", "storage.s3.bucket": "ondolith-site",
		"storage.s3.access_key": "AKIDEXAMPLE", "storage.s3.secret_key": "OLD"}); err != nil {
		t.Fatal(err)
	}
	// 로컬로 되돌리기는 된다.
	if rec := postAdmin(t, d.StorageSettingsSave, "/admin/settings/storage", nil,
		storageForm(url.Values{"storage.backend": {""}, "storage.s3.secret_key": {""}})); rec.Code != http.StatusSeeOther {
		t.Fatalf("로컬로 되돌리기 = HTTP %d (%s)", rec.Code, rec.Body.String())
	}
	// 새 시크릿을 넣으면 그것으로 확인하고 저장한다.
	if rec := postAdmin(t, d.StorageSettingsSave, "/admin/settings/storage", nil,
		storageForm(url.Values{"storage.s3.secret_key": {"NEW-SECRET"}})); rec.Code != http.StatusSeeOther {
		t.Fatalf("새 시크릿으로 저장 = HTTP %d (%s)", rec.Code, rec.Body.String())
	}
	if n := len(*checked); n != 1 || (*checked)[0]["storage.s3.secret_key"] != "NEW-SECRET" {
		t.Fatalf("확인한 값 %v", *checked)
	}
	// 빈 시크릿 칸으로 S3 저장은 「입력하세요」다 — 열리지 않는 옛 값으로 확인하지 않는다.
	d.Content.UseSealer(openFails{})
	if err := d.Content.PutSettings(ctx, map[string]string{"storage.s3.secret_key": "OLD"}); err != nil {
		t.Fatal(err)
	}
	rec := postAdmin(t, d.StorageSettingsSave, "/admin/settings/storage", nil,
		storageForm(url.Values{"storage.s3.secret_key": {""}}))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "시크릿 키") {
		t.Fatalf("열리지 않는 시크릿 + 빈 칸 = HTTP %d (%s)", rec.Code, rec.Body.String())
	}
}

// 버킷을 바꾸거나 비우면 그 전 버킷의 파일이 보이지 않게 된다 — 작업 로그가 이전 값을 남긴다.
func TestStorageBucketChangeIsLogged(t *testing.T) {
	d, pool := fixture(t, &fakeCaller{superuser: true, email: "root@example.com"})
	d.CheckStorage = func(context.Context, map[string]string) (string, error) { return "", nil }
	if rec := postAdmin(t, d.StorageSettingsSave, "/admin/settings/storage", nil, storageForm(nil)); rec.Code != http.StatusSeeOther {
		t.Fatalf("HTTP %d", rec.Code)
	}
	if rec := postAdmin(t, d.StorageSettingsSave, "/admin/settings/storage", nil,
		url.Values{"storage.backend": {""}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("비우기 HTTP %d (%s)", rec.Code, rec.Body.String())
	}
	if got := lastLog(t, pool); !strings.Contains(got, "ondolith-site") || !strings.Contains(got, "(없음)") {
		t.Fatalf("작업 로그 %q — 버킷 이전 값이 없다", got)
	}
}

// **첨부 하나 지우기(A-309)는 파일이 남은 것과 지우지 못한 것을 가른다** (G8).
// 버킷이 파일 삭제를 거부하면 행은 이미 갔다 — 경고를 남기고 성공, 작업 로그에 「삭제」.
// 행 삭제 자체가 실패하면 아무것도 지워지지 않았다 — 500 이고 「삭제」 로그는 남지 않는다.
// 지금까지는 둘 다 성공으로 넘어갔다.
func TestAttachmentDeleteTellsALeftoverFileFromAFailure(t *testing.T) {
	d, pool := fixture(t, &fakeCaller{superuser: true, email: "root@example.com"})
	ctx := context.Background()
	bucket := fakes3.New("site")
	defer bucket.Close()
	remote := s3store.New(s3store.Config{Endpoint: bucket.URL, Region: "us-east-1", Bucket: "site",
		AccessKey: "AK", SecretKey: "SK", PathStyle: true}, nil)
	files := content.NewFiles(t.TempDir(), func(context.Context) (content.Remote, bool, error) { return remote, true, nil })
	d.Attachments = d.Content.AttachmentsIn(files)
	boardID, err := d.Content.CreateBoard(ctx, content.Board{Slug: "free", Name: "자유", PerPage: 20}, content.PresetPublic)
	if err != nil {
		t.Fatal(err)
	}
	postID, err := d.Content.CreatePost(ctx, content.Post{BoardID: boardID, Title: "첨부 글", Body: "본문"})
	if err != nil {
		t.Fatal(err)
	}
	png := "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR" + strings.Repeat("\x00", 40)
	left, err := d.Attachments.Save(ctx, postID, "left.png", strings.NewReader(png))
	if err != nil {
		t.Fatal(err)
	}
	kept, err := d.Attachments.Save(ctx, postID, "kept.png", strings.NewReader(png))
	if err != nil {
		t.Fatal(err)
	}
	del := func(id string) int {
		return postAdmin(t, d.AttachmentDelete, "/admin/attachments/delete", nil,
			url.Values{"attachment_id": {id}}).Code
	}
	rows := func(id string) int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM attachments WHERE id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	bucket.RejectCode = "AccessDenied"
	if code := del(left.ID); code != http.StatusSeeOther || rows(left.ID) != 0 {
		t.Errorf("버킷 거부 = HTTP %d, 남은 행 %d — want 303·0", code, rows(left.ID))
	}
	if got := lastLog(t, pool); !strings.Contains(got, "left.png") {
		t.Errorf("작업 로그 %q", got)
	}

	bucket.RejectCode = ""
	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION refuse_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'disk full'; END $$;
		CREATE TRIGGER refuse_delete BEFORE DELETE ON attachments FOR EACH ROW EXECUTE FUNCTION refuse_delete()`); err != nil {
		t.Fatal(err)
	}
	if code := del(kept.ID); code != http.StatusInternalServerError || rows(kept.ID) != 1 {
		t.Errorf("행 삭제 실패 = HTTP %d, 남은 행 %d — want 500·1", code, rows(kept.ID))
	}
	if got := lastLog(t, pool); strings.Contains(got, "kept.png") {
		t.Errorf("지우지 못한 첨부에 「삭제」 로그: %q", got)
	}
}
