package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/emirue/ondolith/internal/content"
)

// ---- A-210 파일 저장소 설정 (FR-717) ---------------------------------------------

// migrateBudget bounds one press of 「옮기기」 — WriteTimeout(60초, D60 「연결 타임아웃」)
// 안에 응답까지 끝나야 한다. 남으면 다시 누른다.
const migrateBudget = 40 * time.Second

// checkBudget bounds 연결 확인: 시험 객체 쓰기·읽기·지우기 세 번.
const checkBudget = 20 * time.Second

// StorageCodeListBucket is the code CheckStorage reports when the bucket answers
// a missing key with a refusal instead of NoSuchKey — on AWS, the key lacks
// s3:ListBucket. 서비스 코드(AccessDenied)를 그대로 보이면 「권한이 없다」로만 읽혀
// 무엇을 더할지 모른다.
const StorageCodeListBucket = "s3:ListBucket"

// migrateGrace is how long a file already being sent may run past
// migrateBudget. 합이 WriteTimeout(60초) 안이어야 운영자가 빈 응답을 보지 않는다.
const migrateGrace = 15 * time.Second

var (
	s3RegionRe = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	// S3 버킷 이름 규칙: 3~63자, 소문자·숫자·점·하이픈, 처음과 끝은 소문자·숫자.
	s3BucketRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
)

// StorageSettingsForm is A-210 GET.
func (d *Deps) StorageSettingsForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "settings.update"); !ok {
		return
	}
	d.renderStorage(w, r, http.StatusOK, "", "")
}

// StorageSettingsSave is A-210 POST — 저장, 연결 확인(`action=verify`), 옮기기
// (`action=migrate`). **셋 다 superuser + 재인증이다** (D15 5.1 R8): 저장소를 바꾸는
// 사람은 그 뒤의 모든 파일을, 옮기기는 이미 있는 파일까지 받는다. 연결 확인도 폼의
// 엔드포인트로 서버가 접속하므로 같은 문을 지난다.
func (d *Deps) StorageSettingsSave(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "settings.update")
	if !ok {
		return
	}
	if !c.IsSuperuser() {
		d.renderStorage(w, r, http.StatusForbidden, "파일 저장소 설정은 최고 관리자만 바꿀 수 있습니다.", "")
		return
	}
	if !reauthOK(c, r) {
		d.renderStorage(w, r, http.StatusForbidden, "비밀번호를 다시 입력하세요.", "")
		return
	}
	if err := r.ParseForm(); err != nil {
		d.refuse(w, r, http.StatusBadRequest, "잘못된 요청입니다.")
		return
	}
	action := r.PostFormValue("action")
	if action == "migrate" {
		d.migrateStorage(w, r, c)
		return
	}
	if action != "" && action != "verify" {
		d.renderStorage(w, r, http.StatusUnprocessableEntity, "알 수 없는 동작입니다.", "")
		return
	}

	kv := map[string]string{}
	for _, k := range content.StorageSettingKeys {
		kv[k] = strings.TrimSpace(r.PostFormValue(k))
	}
	dropBlankSecrets(kv)
	if kv[content.SettingStoragePathStyle] != "" {
		kv[content.SettingStoragePathStyle] = "1"
	}
	if msg := storageSettingsProblem(kv); msg != "" {
		d.renderStorage(w, r, http.StatusUnprocessableEntity, msg, "")
		return
	}
	// **열리지 않는 시크릿은 없는 것으로 읽는다** (SettingsForForm). 엄격하게 읽으면 설정 파일의
	// secret_key 가 바뀐 사이트에서 이 저장이 500 이 되어, 화면이 「다시 입력해 저장하세요」라고
	// 하는 바로 그 길이 막힌다 (D60 「저장된 자격증명」).
	saved, _, err := d.Content.SettingsForForm(r.Context(), content.StorageSettingKeys...)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	// 확인은 **폼의 값으로** 한다 — 저장 전에 맞는지 알아야 한다. 비워 둔 시크릿만 저장된 것을 쓴다.
	merged := map[string]string{}
	for k, v := range saved {
		merged[k] = v
	}
	for k, v := range kv {
		merged[k] = v
	}
	if action == "verify" || kv[content.SettingStorageBackend] == "s3" {
		if msg := storageMissing(merged); msg != "" {
			d.renderStorage(w, r, http.StatusUnprocessableEntity, msg, "")
			return
		}
		if !d.storageCheck(w, r, c, merged) {
			return
		}
		if action == "verify" {
			d.renderStorage(w, r, http.StatusOK, "", "저장소에 시험 파일을 쓰고, 읽고, 지웠습니다. 이 값으로 저장할 수 있습니다.")
			return
		}
	}

	if err := d.Content.PutSettings(r.Context(), kv); err != nil {
		d.serverError(w, r, err)
		return
	}
	// 행선지가 바뀐 것은 이전·새 값으로 남긴다. 버킷을 바꾸거나 비우면 그 전 버킷의 파일이
	// 보이지 않게 되므로, 무엇이었는지가 기록에 있어야 한다. 키는 남기지 않는다 (D15 7절).
	summary := "파일 저장소 설정 변경: 저장 위치 " + backendName(saved[content.SettingStorageBackend]) +
		" → " + backendName(kv[content.SettingStorageBackend])
	for _, f := range []struct{ key, name string }{
		{content.SettingStorageBucket, "버킷"}, {content.SettingStorageEndpoint, "엔드포인트"},
	} {
		if saved[f.key] != kv[f.key] {
			summary += ", " + f.name + " " + orNone(saved[f.key]) + " → " + orNone(kv[f.key])
		}
	}
	d.log(r, c, "settings.update", "settings", "storage", summary)
	http.Redirect(w, r, "/admin/settings/storage", http.StatusSeeOther)
}

// storageCheck runs 연결 확인 and draws its failure. It reports whether the
// check passed. 응답 본문은 그리지 않는다 — 서비스의 오류 코드만 (D60 「파일 저장소」).
func (d *Deps) storageCheck(w http.ResponseWriter, r *http.Request, c Caller, kv map[string]string) bool {
	if d.CheckStorage == nil {
		d.refuse(w, r, http.StatusNotFound, "연결 확인을 쓸 수 없습니다.")
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), checkBudget)
	defer cancel()
	code, err := d.CheckStorage(ctx, kv)
	switch {
	case err == nil:
		d.log(r, c, "settings.update", "settings", "storage", "파일 저장소 연결 확인: 성공")
		return true
	case code != "":
		d.log(r, c, "settings.update", "settings", "storage", "파일 저장소 연결 확인: 거부 "+code)
		d.renderStorage(w, r, http.StatusUnprocessableEntity,
			"저장소가 거부했습니다 ("+code+"). "+storageRefusalHint(code)+" 저장하지 않았습니다.", "")
	default:
		d.Logger.Warn("파일 저장소 연결 확인", "err", err)
		d.log(r, c, "settings.update", "settings", "storage", "파일 저장소 연결 확인: 닿지 못함")
		d.renderStorage(w, r, http.StatusBadGateway,
			"저장소에 닿지 못했습니다. 엔드포인트 주소와 네트워크를 확인하세요. 저장하지 않았습니다.", "")
	}
	return false
}

// migrateStorage is 「옮기기」: local uploads go to the bucket (content.Files.Migrate).
func (d *Deps) migrateStorage(w http.ResponseWriter, r *http.Request, c Caller) {
	if d.Files == nil {
		d.refuse(w, r, http.StatusNotFound, "옮기기를 쓸 수 없습니다.")
		return
	}
	until := time.Now().Add(migrateBudget)
	// 시한은 새 파일을 시작하지 않는 선이고, 이미 보내는 파일은 이 ctx 가 끊는다 — 끊긴 파일은
	// 실패로 세고 로컬에 남는다.
	ctx, cancel := context.WithDeadline(r.Context(), until.Add(migrateGrace))
	defer cancel()
	res, err := d.Files.Migrate(ctx, until)
	if errors.Is(err, content.ErrStorageLocal) {
		d.renderStorage(w, r, http.StatusUnprocessableEntity, "먼저 S3 로 저장하세요 — 옮길 곳이 없습니다.", "")
		return
	}
	summary := fmt.Sprintf("파일 저장소 옮기기: %d개 옮김, %d개 실패, %d개 남음", res.Moved, res.Failed, res.Left)
	if res.Unreadable > 0 {
		summary += fmt.Sprintf(", 읽지 못한 폴더 %d곳 — 끝나지 않았다", res.Unreadable)
	}
	if err != nil && res.Failed == 0 && res.Unreadable == 0 {
		summary += " — 중단됨"
	}
	d.log(r, c, "settings.update", "settings", "storage", summary)
	if err != nil {
		d.Logger.Warn("파일 저장소 옮기기", "err", err)
	}
	switch {
	case res.Unreadable > 0:
		// 「0개 실패」로 끝내면 운영자는 다 옮긴 줄 알고 업로드 폴더를 정리한다.
		d.renderStorage(w, r, http.StatusInternalServerError, fmt.Sprintf(
			"%d개를 옮겼지만 업로드 폴더 중 %d곳을 읽지 못해 그 안의 파일은 옮기지 않았습니다. 서버 로그에서 폴더를 확인해 권한을 고친 뒤 다시 누르세요.",
			res.Moved, res.Unreadable), "")
		return
	case err != nil && res.Failed == 0:
		d.renderStorage(w, r, http.StatusInternalServerError,
			"업로드 폴더나 저장소 설정을 읽지 못해 옮기지 못했습니다. 서버 로그를 확인하세요.", "")
		return
	case err != nil:
		d.renderStorage(w, r, http.StatusBadGateway, fmt.Sprintf(
			"%d개를 옮겼고 %d개는 옮기지 못했습니다 — 옮기지 못한 파일은 로컬에 남아 그대로 보입니다. 잠시 후 다시 누르세요.",
			res.Moved, res.Failed), "")
		return
	}
	notice := fmt.Sprintf("%d개를 버킷으로 옮겼습니다.", res.Moved)
	if res.Left > 0 {
		notice += fmt.Sprintf(" %d개가 남았습니다 — 한 번에 40초까지 옮기고, 1분 안에 쓰인 파일은 쓰는 중일 수 있어 다음 번으로 미룹니다. 다시 누르세요.", res.Left)
	} else if res.Moved == 0 {
		notice = "옮길 로컬 파일이 없습니다."
	}
	d.renderStorage(w, r, http.StatusOK, "", notice)
}

// storageSettingsProblem checks the formats D19 A-210 names. "" means the
// values may be stored. 빈 값은 여기서 보지 않는다 — S3 에 필요한지는 storageMissing.
func storageSettingsProblem(kv map[string]string) string {
	if b := kv[content.SettingStorageBackend]; b != "" && b != "s3" {
		return "저장 위치 값이 올바르지 않습니다."
	}
	for _, k := range content.StorageSettingKeys {
		if len(kv[k]) > 200 {
			return "값이 너무 깁니다 (200자 이내)."
		}
	}
	if e := kv[content.SettingStorageEndpoint]; e != "" {
		u, err := url.Parse(e)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
			u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
			return "엔드포인트는 http(s)://호스트[:포트] 모양이어야 합니다 — 경로·질의·사용자 정보 없이."
		}
		kv[content.SettingStorageEndpoint] = u.Scheme + "://" + u.Host
	}
	if v := kv[content.SettingStorageRegion]; v != "" && !s3RegionRe.MatchString(v) {
		return "리전은 소문자·숫자·하이픈입니다 (예: ap-northeast-2, auto)."
	}
	if v := kv[content.SettingStorageBucket]; v != "" && !s3BucketRe.MatchString(v) {
		return "버킷 이름은 3~63자의 소문자·숫자·점·하이픈이고, 처음과 끝은 소문자나 숫자입니다."
	}
	if v := kv[content.SettingStorageAccessKey]; strings.ContainsAny(v, " \t\r\n") {
		return "액세스 키에 공백이 있습니다."
	}
	return ""
}

// storageMissing names the empty field S3 needs, after the saved secret was
// merged in. "" means nothing is missing.
func storageMissing(kv map[string]string) string {
	for _, f := range []struct{ key, name string }{
		{content.SettingStorageRegion, "리전"}, {content.SettingStorageBucket, "버킷"},
		{content.SettingStorageAccessKey, "액세스 키"}, {content.SettingStorageSecretKey, "시크릿 키"},
	} {
		if kv[f.key] == "" {
			return f.name + "을(를) 입력하세요 — S3 에 필요합니다."
		}
	}
	return ""
}

// storageRefusalHint says what the operator should look at for a refusal code.
func storageRefusalHint(code string) string {
	switch code {
	case "NoSuchBucket":
		return "버킷이 없습니다 — 이름과 리전을 확인하세요."
	case "InvalidAccessKeyId":
		return "액세스 키를 저장소가 모릅니다."
	case "SignatureDoesNotMatch":
		return "시크릿 키가 액세스 키와 맞지 않습니다."
	case "AccessDenied":
		return "이 키에 버킷의 읽기·쓰기·지우기 권한이 없습니다."
	case "PermanentRedirect", "AuthorizationHeaderMalformed":
		return "리전이 버킷의 리전과 다릅니다."
	case StorageCodeListBucket:
		return "없는 파일을 물으면 「없다」 대신 거부로 답합니다 — 키에 버킷의 목록 권한(s3:ListBucket)을 더하세요. " +
			"이 권한이 없으면 지워진 파일이 「없음」(404) 대신 오류(500)로 보입니다."
	}
	return "엔드포인트·리전·버킷·키를 확인하세요."
}

func backendName(v string) string {
	if v == "s3" {
		return "S3"
	}
	return "로컬"
}

func (d *Deps) renderStorage(w http.ResponseWriter, r *http.Request, code int, msg, notice string) {
	d.renderSettingsNotice(w, r, "admin/storage.html", content.StorageSettingKeys, code, msg, notice)
}
