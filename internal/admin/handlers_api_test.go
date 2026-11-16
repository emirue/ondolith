package admin

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/apikeys"
)

// **A-213 커스텀 API** (D42 6절): 최고 관리자가 사이트 API 를 켜고 끄며, 앱 키를 발급하고 폐기한다.
//   - 켜기·끄기는 재확인 없이(되돌릴 수 있다), 발급·폐기는 재확인(자격증명 — D15 5.3-1)
//   - **발급한 키는 그 응답 화면에 한 번만 보인다.** 세션 알림(FR-724)은 DB 세션 표에 평문으로 남고 백업에도
//     실리므로 쓰지 않는다. 작업 로그에도 원문이 없다. 응답은 no-store
//   - 이 실행 파일에 붙은 사이트 API 목록을 보인다
func TestCustomAPIScreen(t *testing.T) {
	op := &fakeCaller{perms: map[string]bool{"settings.update": true}, email: "op@example.com"}
	d, pool := fixture(t, op)
	ctx := context.Background()
	d.APIKeys = apikeys.New(pool)
	d.SiteAPI = []string{"GET /api/site/v1/notes", "POST /api/site/v1/notes"}
	var got map[string]any
	d.Render = func(w http.ResponseWriter, _ *http.Request, _ string, code int, data any) {
		got, _ = data.(map[string]any)
		w.WriteHeader(code)
	}
	enabled := func() string {
		kv, err := d.Content.Settings(ctx, SettingAPIEnabled)
		if err != nil {
			t.Fatal(err)
		}
		return kv[SettingAPIEnabled]
	}
	logged := func(like string) int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM operation_logs WHERE summary LIKE $1`, like).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// 운영자(최고 관리자 아님)는 보기만.
	if rec := get(d.APISettingsForm, "/admin/settings/api"); rec.Code != http.StatusOK {
		t.Fatalf("운영자의 보기 = HTTP %d", rec.Code)
	}
	if rec := post(d.APIToggle, "/admin/settings/api", url.Values{"on": {"1"}}); rec.Code != http.StatusForbidden || enabled() != "" {
		t.Fatalf("운영자의 켜기 = HTTP %d, 설정 %q", rec.Code, enabled())
	}

	su := &fakeCaller{superuser: true, reauth: true, email: "su@example.com", password: "correct horse battery"}
	d.Caller = func(*http.Request) Caller { return su }

	// 켜기·끄기: 재확인 없이, 알림과 작업 로그.
	if rec := post(d.APIToggle, "/admin/settings/api", url.Values{"on": {"1"}}); rec.Code != http.StatusSeeOther || enabled() != "1" {
		t.Fatalf("켜기 = HTTP %d, 설정 %q", rec.Code, enabled())
	}
	wantNotice(t, d, "커스텀 API 를 켰습니다 — 발급한 앱 키로 /api/site/ 를 부를 수 있습니다.")
	if rec := post(d.APIToggle, "/admin/settings/api", url.Values{}); rec.Code != http.StatusSeeOther || enabled() != "" {
		t.Fatalf("끄기 = HTTP %d, 설정 %q", rec.Code, enabled())
	}
	wantNotice(t, d, "커스텀 API 를 껐습니다 — /api/site/ 는 모두 404 입니다.")
	if logged("커스텀 API %") != 2 {
		t.Errorf("켜기·끄기 작업 로그 %d건, want 2", logged("커스텀 API %"))
	}

	// 발급: 재확인이 없으면 403, 이름이 틀리면 422.
	if rec := post(d.APIKeyIssue, "/admin/settings/api/keys", url.Values{"name": {"flutter"}}); rec.Code != http.StatusForbidden {
		t.Fatalf("재확인 없이 발급 = HTTP %d, want 403", rec.Code)
	}
	if rec := post(d.APIKeyIssue, "/admin/settings/api/keys", url.Values{"name": {" "}, "password": {su.password}}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("빈 이름 = HTTP %d, want 422", rec.Code)
	}
	rec := post(d.APIKeyIssue, "/admin/settings/api/keys", url.Values{"name": {"flutter-android"}, "password": {su.password}})
	raw, _ := got["Issued"].(string)
	if rec.Code != http.StatusOK || !strings.HasPrefix(raw, apikeys.Prefix) || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("발급 = HTTP %d, 키 %q, Cache-Control %q — 그 화면에 한 번, 저장되지 않게", rec.Code, raw, rec.Header().Get("Cache-Control"))
	}
	for _, n := range su.notices {
		if strings.Contains(n, raw) {
			t.Errorf("세션 알림에 키 원문이 실렸다: %q", n)
		}
	}
	var leaked int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operation_logs WHERE position($1 in summary) > 0`, raw).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 || logged("앱 키 발급: flutter-android") != 1 {
		t.Errorf("작업 로그: 원문 %d건(want 0), 발급 기록 %d건(want 1)", leaked, logged("앱 키 발급: flutter-android"))
	}
	if _, ok, _ := d.APIKeys.Lookup(ctx, raw); !ok {
		t.Fatal("발급한 키로 찾을 수 없다")
	}

	// 보기: 키 목록과 이 실행 파일의 사이트 API.
	get(d.APISettingsForm, "/admin/settings/api")
	keys, _ := got["Keys"].([]apikeys.Key)
	routes, _ := got["Routes"].([]string)
	if len(keys) != 1 || keys[0].Name != "flutter-android" || len(routes) != 2 || got["Issued"] != nil {
		t.Errorf("보기 = 키 %+v, 라우트 %v, 발급 %v — 다시 열면 원문은 없다", keys, routes, got["Issued"])
	}

	// 폐기: 재확인, 알림, 작업 로그. 없는 키는 404. 발급 때 넣은 비밀번호로 열린 15분 창을 닫고 본다.
	su.reauth = true
	if rec := post(d.APIKeyRevoke, "/admin/settings/api/keys/revoke", url.Values{"id": {keys[0].ID}}); rec.Code != http.StatusForbidden {
		t.Fatalf("재확인 없이 폐기 = HTTP %d, want 403", rec.Code)
	}
	if rec := post(d.APIKeyRevoke, "/admin/settings/api/keys/revoke", url.Values{"id": {keys[0].ID}, "password": {su.password}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("폐기 = HTTP %d, want 303", rec.Code)
	}
	wantNotice(t, d, "앱 키 「flutter-android」를 폐기했습니다 — 그 키로 오는 요청은 401 입니다.")
	if _, ok, _ := d.APIKeys.Lookup(ctx, raw); ok || logged("앱 키 폐기: flutter-android") != 1 {
		t.Errorf("폐기 뒤 찾음 %v, 작업 로그 %d건", ok, logged("앱 키 폐기: flutter-android"))
	}
	if rec := post(d.APIKeyRevoke, "/admin/settings/api/keys/revoke", url.Values{"id": {keys[0].ID}, "password": {su.password}}); rec.Code != http.StatusNotFound {
		t.Errorf("두 번 폐기 = HTTP %d, want 404", rec.Code)
	}
}
