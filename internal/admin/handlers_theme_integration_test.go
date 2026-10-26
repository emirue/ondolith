package admin

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/theme"
)

// D41 의 화면 셋: 설정형 hero(글·숫자·고르기·사진), 모음형 partners·logos.
const themeAdminJSON = `{"screens": [
  {"key": "hero", "title": "메인 배너", "kind": "settings", "fields": [
    {"key": "headline", "label": "큰 제목", "type": "text", "required": true},
    {"key": "count", "label": "숫자", "type": "number"},
    {"key": "tone", "label": "글자색", "type": "select", "options": ["밝게", "어둡게"], "default": "밝게"},
    {"key": "photo", "label": "배경 사진", "type": "image"}]},
  {"key": "partners", "title": "협력사", "kind": "collection", "fields": [
    {"key": "name", "label": "이름", "type": "text", "required": true},
    {"key": "logo", "label": "로고", "type": "image"}]},
  {"key": "logos", "title": "로고 띠", "kind": "collection", "fields": [
    {"key": "name", "label": "이름", "type": "text"}]}
]}`

func themeFixture(t *testing.T, c Caller) (*Deps, *pgxpool.Pool) {
	t.Helper()
	d, pool := fixture(t, c)
	screens, warns := theme.ParseAdmin([]byte(themeAdminJSON))
	if len(warns) != 0 {
		t.Fatal(warns)
	}
	d.ThemeScreens = func() []theme.Screen { return screens }
	return d, pool
}

// themeReq sends to a theme handler with the path values the mux would set.
func themeReq(h http.HandlerFunc, method, target string, body *bytes.Buffer, ctype string, pv ...string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, body)
		req.Header.Set("Content-Type", ctype)
	}
	for i := 0; i+1 < len(pv); i += 2 {
		req.SetPathValue(pv[i], pv[i+1])
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func formBody(v url.Values) (*bytes.Buffer, string) {
	return bytes.NewBufferString(v.Encode()), "application/x-www-form-urlencoded"
}

// multipartBody writes fields and, for each file key, a small JPEG (or raw bytes when size > 0).
func multipartBody(t *testing.T, fields url.Values, files map[string]int) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, vs := range fields {
		for _, v := range vs {
			_ = mw.WriteField(k, v)
		}
	}
	for k, size := range files {
		fw, _ := mw.CreateFormFile(k, "사진.jpg")
		if size > 0 {
			_, _ = fw.Write(bytes.Repeat([]byte("a"), size))
			continue
		}
		if err := jpeg.Encode(fw, image.NewRGBA(image.Rect(0, 0, 40, 30)), nil); err != nil {
			t.Fatal(err)
		}
	}
	_ = mw.Close()
	return &body, mw.FormDataContentType()
}

// 화면 단위 권한 (D41 4절). 판정은 처리기가 한다 — 메뉴를 숨기는 것은 UX 다.
func TestThemeScreenNeedsTheScreenScope(t *testing.T) {
	scoped := &fakeCaller{screens: map[string]bool{"partners": true}}
	d, _ := themeFixture(t, scoped)
	if rec := themeReq(d.ThemeScreen, "GET", "/admin/theme/hero", nil, "", "key", "hero"); rec.Code != http.StatusForbidden {
		t.Errorf("다른 화면 부여로 hero = HTTP %d, want 403", rec.Code)
	}
	if rec := themeReq(d.ThemeScreen, "GET", "/admin/theme/partners", nil, "", "key", "partners"); rec.Code != http.StatusOK {
		t.Errorf("부여받은 화면 = HTTP %d", rec.Code)
	}
	b, ct := formBody(url.Values{"headline": {"x"}})
	if rec := themeReq(d.ThemeScreenSave, "POST", "/admin/theme/hero", b, ct, "key", "hero"); rec.Code != http.StatusForbidden {
		t.Errorf("다른 화면 부여로 저장 = HTTP %d, want 403", rec.Code)
	}

	d.Caller = func(*http.Request) Caller { return &fakeCaller{} }
	if rec := themeReq(d.ThemeScreen, "GET", "/admin/theme/partners", nil, "", "key", "partners"); rec.Code != http.StatusForbidden {
		t.Errorf("부여 없이 = HTTP %d, want 403", rec.Code)
	}

	d.Caller = func(*http.Request) Caller {
		return &fakeCaller{perms: map[string]bool{auth.ThemeContentPermission: true}}
	}
	if rec := themeReq(d.ThemeScreen, "GET", "/admin/theme/hero", nil, "", "key", "hero"); rec.Code != http.StatusOK {
		t.Errorf("전역 부여로 hero = HTTP %d", rec.Code)
	}
	if rec := themeReq(d.ThemeScreen, "GET", "/admin/theme/nope", nil, "", "key", "nope"); rec.Code != http.StatusNotFound {
		t.Errorf("선언되지 않은 키 = HTTP %d, want 404", rec.Code)
	}
	// 종류가 맞지 않는 경로도 없는 것이다.
	b, ct = formBody(url.Values{"name": {"x"}})
	if rec := themeReq(d.ThemeScreenSave, "POST", "/admin/theme/partners", b, ct, "key", "partners"); rec.Code != http.StatusNotFound {
		t.Errorf("모음형에 설정 저장 = HTTP %d, want 404", rec.Code)
	}
	if rec := themeReq(d.ThemeItemForm, "GET", "/admin/theme/hero/new", nil, "", "key", "hero"); rec.Code != http.StatusNotFound {
		t.Errorf("설정형에 행 추가 폼 = HTTP %d, want 404", rec.Code)
	}
}

func TestThemeSettingsSaveMergesAndLogs(t *testing.T) {
	d, pool := themeFixture(t, &fakeCaller{superuser: true, email: "op@example.com"})
	ctx := context.Background()
	// 옛 테마가 남긴 칸 — 이 선언에는 없다.
	if err := d.Content.SaveThemeSettings(ctx, "hero", map[string]any{"gone": "옛 값"}); err != nil {
		t.Fatal(err)
	}
	b, ct := formBody(url.Values{"headline": {"새 제목"}, "count": {"7"}, "tone": {"어둡게"}})
	rec := themeReq(d.ThemeScreenSave, "POST", "/admin/theme/hero", b, ct, "key", "hero")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/theme/hero" {
		t.Fatalf("저장 = HTTP %d → %q (%s)", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	got, _ := d.Content.ThemeSettings(ctx, "hero")
	if got["headline"] != "새 제목" || got["count"] != 7.0 || got["tone"] != "어둡게" || got["gone"] != "옛 값" {
		t.Errorf("저장값 = %#v", got)
	}
	var action, target, summary string
	if err := pool.QueryRow(ctx, `SELECT action, target_type || ':' || target_id, summary FROM operation_logs
		ORDER BY created_at DESC LIMIT 1`).Scan(&action, &target, &summary); err != nil {
		t.Fatal(err)
	}
	if action != "theme.edit_content" || target != "theme_screen:hero" || strings.Contains(summary, "새 제목") {
		t.Errorf("작업 로그 = %s %s %q (값은 남기지 않는다)", action, target, summary)
	}
}

func TestThemeSettingsRefusalKeepsTheForm(t *testing.T) {
	d, _ := themeFixture(t, &fakeCaller{superuser: true})
	var captured map[string]any
	d.Render = func(w http.ResponseWriter, _ *http.Request, _ string, code int, data any) {
		captured, _ = data.(map[string]any)
		w.WriteHeader(code)
	}
	b, ct := formBody(url.Values{"headline": {""}, "count": {"열"}})
	rec := themeReq(d.ThemeScreenSave, "POST", "/admin/theme/hero", b, ct, "key", "hero")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("거부 = HTTP %d, want 422", rec.Code)
	}
	if got, _ := d.Content.ThemeSettings(context.Background(), "hero"); len(got) != 0 {
		t.Errorf("거부된 폼이 저장됐다: %#v", got)
	}
	inputs, _ := captured["Inputs"].([]themeInput)
	byKey := map[string]themeInput{}
	for _, in := range inputs {
		byKey[in.Field.Key] = in
	}
	if byKey["count"].Value != "열" || byKey["count"].Error == "" || byKey["headline"].Error == "" {
		t.Errorf("적은 값과 칸 옆 오류가 남아야 한다: %+v", byKey)
	}
	if captured["Error"] == nil {
		t.Error("머리 오류 문구가 없다")
	}
}

func TestThemeSettingsPicture(t *testing.T) {
	d, pool := themeFixture(t, &fakeCaller{superuser: true})
	ctx := context.Background()
	b, ct := multipartBody(t, url.Values{"headline": {"제목"}}, map[string]int{"photo": 0})
	if rec := themeReq(d.ThemeScreenSave, "POST", "/admin/theme/hero", b, ct, "key", "hero"); rec.Code != http.StatusSeeOther {
		t.Fatalf("사진 저장 = HTTP %d (%s)", rec.Code, rec.Body.String())
	}
	got, _ := d.Content.ThemeSettings(ctx, "hero")
	photo, _ := got["photo"].(map[string]any)
	if photo["w"] != 40.0 || photo["h"] != 30.0 {
		t.Fatalf("사진 값 = %#v", got["photo"])
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM media WHERE id = $1`, photo["id"]).Scan(&n)
	if n != 1 {
		t.Errorf("media 행이 없다: %v", photo["id"])
	}

	// 파일도 빼기도 없으면 유지.
	b, ct = multipartBody(t, url.Values{"headline": {"제목2"}}, nil)
	themeReq(d.ThemeScreenSave, "POST", "/admin/theme/hero", b, ct, "key", "hero")
	if got, _ := d.Content.ThemeSettings(ctx, "hero"); got["photo"] == nil {
		t.Error("사진 없이 저장했더니 사진이 사라졌다")
	}
	// 빼기.
	b, ct = multipartBody(t, url.Values{"headline": {"제목3"}, "photo" + theme.ClearSuffix: {"1"}}, nil)
	themeReq(d.ThemeScreenSave, "POST", "/admin/theme/hero", b, ct, "key", "hero")
	if got, _ := d.Content.ThemeSettings(ctx, "hero"); got["photo"] != nil {
		t.Errorf("사진 빼기가 안 됐다: %#v", got["photo"])
	}

	// 거부된 폼은 사진 파일을 남기지 않는다 — 검사가 저장보다 먼저다.
	_, _ = pool.Exec(ctx, `DELETE FROM media`)
	b, ct = multipartBody(t, url.Values{"headline": {""}}, map[string]int{"photo": 0})
	if rec := themeReq(d.ThemeScreenSave, "POST", "/admin/theme/hero", b, ct, "key", "hero"); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("필수 칸이 빈 폼 = HTTP %d", rec.Code)
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM media`).Scan(&n)
	if n != 0 {
		t.Errorf("거부된 폼이 사진 %d장을 남겼다", n)
	}

	// 사진이 아닌 파일은 칸 옆 문구로 거부된다.
	b, ct = multipartBody(t, url.Values{"headline": {"제목"}}, map[string]int{"photo": 100})
	if rec := themeReq(d.ThemeScreenSave, "POST", "/admin/theme/hero", b, ct, "key", "hero"); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("사진 아닌 파일 = HTTP %d, want 422", rec.Code)
	}
}

// W6-28 의 교훈: 본문 상한은 파싱보다 먼저 건다 (D41 6절). 한 장 상한을 1 MiB 로 낮추면
// 사진 칸 하나인 화면의 상한은 2 MiB 다.
func TestThemeUploadBodyIsCappedBeforeParse(t *testing.T) {
	d, _ := themeFixture(t, &fakeCaller{superuser: true})
	ctx := context.Background()
	if err := d.Content.PutSettings(ctx, map[string]string{"upload.max_bytes": "1048576"}); err != nil {
		t.Fatal(err)
	}
	b, ct := multipartBody(t, url.Values{"headline": {"제목"}}, map[string]int{"photo": 3 << 20})
	rec := themeReq(d.ThemeScreenSave, "POST", "/admin/theme/hero", b, ct, "key", "hero")
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("상한을 넘는 본문 = HTTP %d, want 413", rec.Code)
	}
	if got, _ := d.Content.ThemeSettings(ctx, "hero"); len(got) != 0 {
		t.Errorf("상한을 넘는 본문이 저장됐다: %#v", got)
	}
}

func TestThemeItemsAddEditOrderDelete(t *testing.T) {
	d, pool := themeFixture(t, &fakeCaller{superuser: true})
	ctx := context.Background()
	add := func(name, sort string) {
		t.Helper()
		b, ct := multipartBody(t, url.Values{"name": {name}, "_sort": {sort}}, nil)
		rec := themeReq(d.ThemeItemSave, "POST", "/admin/theme/partners/new", b, ct, "key", "partners")
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/theme/partners" {
			t.Fatalf("행 추가 = HTTP %d → %q (%s)", rec.Code, rec.Header().Get("Location"), rec.Body.String())
		}
	}
	add("가", "2")
	add("나", "1")
	items, _ := d.Content.ThemeItems(ctx, "partners")
	if len(items) != 2 || items[0].Data["name"] != "나" {
		t.Fatalf("순서 칸대로: %+v", items)
	}
	id := items[1].ID

	var captured map[string]any
	render := d.Render
	d.Render = func(w http.ResponseWriter, _ *http.Request, _ string, code int, data any) {
		captured, _ = data.(map[string]any)
		w.WriteHeader(code)
	}
	themeReq(d.ThemeItemForm, "GET", "/admin/theme/partners/new", nil, "", "key", "partners")
	if captured["Sort"] != 3 {
		t.Errorf("새 행의 순서 칸 = %v, want 3 (맨 뒤)", captured["Sort"])
	}
	d.Render = render

	b, ct := multipartBody(t, url.Values{"name": {"다"}, "_sort": {"0"}}, nil)
	if rec := themeReq(d.ThemeItemSave, "POST", "/admin/theme/partners/"+id, b, ct, "key", "partners", "id", id); rec.Code != http.StatusSeeOther {
		t.Fatalf("행 수정 = HTTP %d (%s)", rec.Code, rec.Body.String())
	}
	items, _ = d.Content.ThemeItems(ctx, "partners")
	if items[0].Data["name"] != "다" {
		t.Errorf("수정한 행이 앞으로 와야 한다: %+v", items)
	}

	// 다른 화면의 행 id 로는 닿지 않는다.
	b, ct = multipartBody(t, url.Values{"name": {"x"}}, nil)
	if rec := themeReq(d.ThemeItemSave, "POST", "/admin/theme/logos/"+id, b, ct, "key", "logos", "id", id); rec.Code != http.StatusNotFound {
		t.Errorf("다른 화면의 행 수정 = HTTP %d, want 404", rec.Code)
	}
	if rec := themeReq(d.ThemeItemDelete, "POST", "/admin/theme/logos/"+id+"/delete", nil, "", "key", "logos", "id", id); rec.Code != http.StatusNotFound {
		t.Errorf("다른 화면의 행 삭제 = HTTP %d, want 404", rec.Code)
	}

	if rec := themeReq(d.ThemeItemDelete, "POST", "/admin/theme/partners/"+id+"/delete", nil, "", "key", "partners", "id", id); rec.Code != http.StatusSeeOther {
		t.Errorf("행 삭제 = HTTP %d", rec.Code)
	}
	if items, _ = d.Content.ThemeItems(ctx, "partners"); len(items) != 1 {
		t.Errorf("지운 뒤 %d행", len(items))
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM operation_logs WHERE action = 'theme.edit_content' AND target_type = 'theme_item'`).Scan(&n)
	if n != 4 {
		t.Errorf("행 작업 로그 %d건, want 4 (추가 둘·수정·삭제)", n)
	}

	// 필수 칸이 빈 행은 거부.
	b, ct = multipartBody(t, url.Values{"name": {""}}, nil)
	if rec := themeReq(d.ThemeItemSave, "POST", "/admin/theme/partners/new", b, ct, "key", "partners"); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("필수 칸이 빈 행 = HTTP %d, want 422", rec.Code)
	}
}

func TestThemeItemLimitIsRefused(t *testing.T) {
	d, pool := themeFixture(t, &fakeCaller{superuser: true})
	if _, err := pool.Exec(context.Background(), `INSERT INTO theme_items (screen_key, data)
		SELECT 'logos', '{}' FROM generate_series(1, 200)`); err != nil {
		t.Fatal(err)
	}
	b, ct := multipartBody(t, url.Values{"name": {"x"}}, nil)
	rec := themeReq(d.ThemeItemSave, "POST", "/admin/theme/logos/new", b, ct, "key", "logos")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "200") {
		t.Errorf("상한 = HTTP %d %s", rec.Code, rec.Body.String())
	}
	// 꽉 찬 모음에 사진을 올려도 사진 파일이 남지 않는다 — 세는 것이 사진 저장보다 먼저다.
	if _, err := pool.Exec(context.Background(), `INSERT INTO theme_items (screen_key, data)
		SELECT 'partners', '{}' FROM generate_series(1, 200)`); err != nil {
		t.Fatal(err)
	}
	b, ct = multipartBody(t, url.Values{"name": {"x"}}, map[string]int{"logo": 0})
	if rec := themeReq(d.ThemeItemSave, "POST", "/admin/theme/partners/new", b, ct, "key", "partners"); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("꽉 찬 모음에 사진과 함께 = HTTP %d", rec.Code)
	}
	var n int
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM media`).Scan(&n)
	if n != 0 {
		t.Errorf("넘친 요청이 사진 %d장을 남겼다", n)
	}
}

// A-404 의 화면 스코프 부여 (D41 4절). 불변식은 auth.ValidateScreenGrant 하나다.
func TestGrantPermissionWithAThemeScreen(t *testing.T) {
	d, pool := themeFixture(t, nil)
	ctx := context.Background()
	me, err := d.Auth.CreateUser(ctx, "me@example.com", "h", "나")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE key='admin'`, me); err != nil {
		t.Fatal(err)
	}
	d.Caller = func(*http.Request) Caller {
		return &fakeCaller{perms: map[string]bool{"role.manage": true}, id: me, superuser: true}
	}
	grant := func(v url.Values) int {
		return post(d.RoleGrantPermission, "/admin/roles/permissions", v).Code
	}
	if code := grant(url.Values{"role": {"editor"}, "permission": {auth.ThemeContentPermission}, "theme_screen": {"partners"}}); code != http.StatusSeeOther {
		t.Fatalf("화면 부여 = HTTP %d", code)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM role_permissions WHERE theme_screen = 'partners'`).Scan(&n)
	if n != 1 {
		t.Errorf("화면 부여 행 %d, want 1", n)
	}
	for name, v := range map[string]url.Values{
		"다른 권한에 화면":  {"role": {"editor"}, "permission": {"page.view"}, "theme_screen": {"partners"}},
		"선언되지 않은 화면": {"role": {"editor"}, "permission": {auth.ThemeContentPermission}, "theme_screen": {"nope"}},
		"화면 키 형식":    {"role": {"editor"}, "permission": {auth.ThemeContentPermission}, "theme_screen": {"Bad"}},
	} {
		if code := grant(v); code != http.StatusUnprocessableEntity {
			t.Errorf("%s = HTTP %d, want 422", name, code)
		}
	}
	var board string
	if err := pool.QueryRow(ctx, `INSERT INTO boards (slug, name) VALUES ('free', '자유') RETURNING id`).Scan(&board); err != nil {
		t.Fatal(err)
	}
	if code := grant(url.Values{"role": {"editor"}, "permission": {auth.ThemeContentPermission},
		"theme_screen": {"partners"}, "board_id": {board}}); code != http.StatusUnprocessableEntity {
		t.Errorf("게시판과 화면을 함께 = HTTP %d, want 422", code)
	}
}

// 리뷰 #1: 처리기가 받는 요청은 미들웨어가 WithContext 로 만든 사본이라 서버의 자동 정리(원본만
// 본다)가 닿지 않는다. 8 MiB 를 넘는 파일은 $TMPDIR/multipart-* 로 쓰이고, 지우지 않으면 쌓인다.
func TestThemeUploadTempFilesAreRemoved(t *testing.T) {
	d, _ := themeFixture(t, &fakeCaller{superuser: true})
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	b, ct := multipartBody(t, url.Values{"headline": {"제목"}}, map[string]int{"photo": 9 << 20})
	if rec := themeReq(d.ThemeScreenSave, "POST", "/admin/theme/hero", b, ct, "key", "hero"); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("사진 아닌 큰 파일 = HTTP %d", rec.Code)
	}
	left, _ := os.ReadDir(tmp)
	if len(left) != 0 {
		t.Errorf("업로드 임시 파일 %d 개가 남았다", len(left))
	}
}

// 리뷰 #2: 사진 크기만큼의 상한은 multipart 에만 준다. urlencoded 본문은 net/http 가 상한 안에서
// 통째로 메모리에 읽으므로, 사진 칸 열 개면 요청 하나에 200 MiB 를 읽게 된다.
func TestThemeURLEncodedBodyKeepsTheSmallCap(t *testing.T) {
	d, _ := themeFixture(t, &fakeCaller{superuser: true})
	b, ct := formBody(url.Values{"headline": {strings.Repeat("가", 1<<20)}})
	if rec := themeReq(d.ThemeScreenSave, "POST", "/admin/theme/hero", b, ct, "key", "hero"); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("1 MiB 를 넘는 urlencoded 본문 = HTTP %d, want 413", rec.Code)
	}
}
