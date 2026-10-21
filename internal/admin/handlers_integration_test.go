package admin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/commerce"
	"github.com/emirue/ondolith/internal/content"
	"github.com/emirue/ondolith/internal/migrations"
)

const dsnEnv = "ONDOLITH_TEST_DSN"

type fakeCaller struct {
	perms map[string]bool
	// scoped[board][perm] — 스코프 권한은 게시판마다 답이 다르다 (D15 2.4).
	scoped    map[string]map[string]bool
	id        string
	email     string
	superuser bool
	reauth    bool
	// password 는 재인증에서 통과하는 값이다. 비어 있으면 어떤 입력도 통과하지
	// 못한다 — 재인증을 만족시킬 수 없는 계정이다.
	password string
}

func (f *fakeCaller) Can(p string) bool { return f.superuser || f.perms[p] }
func (f *fakeCaller) CanOn(p string, board auth.BoardID) bool {
	if f.superuser || f.perms[p] {
		return true
	}
	return f.scoped[string(board)][p]
}
func (f *fakeCaller) Email() string     { return f.email }
func (f *fakeCaller) UserID() string    { return f.id }
func (f *fakeCaller) IsSuperuser() bool { return f.superuser }
func (f *fakeCaller) NeedsReauth() bool { return f.reauth }

// ConfirmReauth 는 운영의 adminCaller 처럼 **성공을 남긴다** (그쪽은 세션에
// 찍는다). 남기지 않으면 이 stub 은 운영이 만들 수 없는 상태를 시험하게 된다.
func (f *fakeCaller) ConfirmReauth(p string) bool {
	if f.password == "" || p != f.password {
		return false
	}
	f.reauth = false
	return true
}

func fixture(t *testing.T, c Caller) (*Deps, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s 미설정 — 통합 테스트를 건너뜁니다 (make test-integration)", dsnEnv)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, s := range []string{"DROP SCHEMA public CASCADE", "CREATE SCHEMA public"} {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { db.Close() })
	if err := migrations.Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := content.NewStore(pool)
	// **`operation_logs.actor_user_id` 는 uuid 다.** `"u1"` 같은 임의 문자열은
	// 22P02 로 실패하고, 그 실패를 운영 코드가 삼킨다 — 그래서 이 픽스처에서는
	// 모든 작업 로그가 조용히 사라지고 있었다.
	//
	// 사용자 행을 만들어 붙이지는 않는다: 사용자 수를 세는 검사들이 그 한 행에
	// 걸린다. 빈 값은 NULL 로 저장되고 (D30 이 익명 액터를 허용한다), 감사
	// 스냅샷인 `actor_email` 은 그대로 남는다.
	if f, ok := c.(*fakeCaller); ok && f.id != "" && !looksLikeUUIDForTest(f.id) {
		f.id = ""
	}
	// 업로드 루트는 운영처럼 첨부와 상품 이미지가 함께 쓴다.
	uploads := t.TempDir()
	shop := commerce.NewStore(pool)
	d := &Deps{
		Content:  store,
		Auth:     auth.NewStore(pool),
		Commerce: shop,
		// 운영과 같이 채운다. 비워 두면 첨부를 지우는 경로가 nil 로 터지는데,
		// 그 사실이 테스트에서는 보이지 않는다.
		Attachments: store.AttachmentsIn(uploads),
		Images:      shop.ImagesIn(uploads),
		Media:       store.MediaIn(uploads),
		// **작업 로그도 운영과 같이 붙인다.** 비워 두면 d.log 가 조용히
		// 아무것도 안 하고, "로그에 남는다" 를 확인하는 검사가 전부 무의미해진다
		// (D15 7절이 요구하는 것이 바로 그 기록이다).
		OpLog: store.OpLog(),
		// 운영과 같이 PG 가 있다. 비우면 환불 실행(A-507·A-511)이 「PG 미설정」
		// 502 로 끝나고, 돈이 나가는 경로를 검사하는 테스트가 전부 그 502 를 본다.
		Gateway: func() commerce.Gateway { return reconcileGateway{status: commerce.PaymentApproved} },
		// 운영과 같이 채운다. 비우면 A-206 이 콜백을 빈 주소로 그리고,
		// 관리자가 그것을 프로바이더 콘솔에 붙여넣으면 연동이 안 된다.
		BaseURL: func(r *http.Request) string {
			scheme := "http"
			if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
				scheme = "https"
			}
			return scheme + "://" + r.Host
		},
		// **작업 로그 기록 실패를 테스트 실패로 만든다.** 운영에서는 삼키는
		// 것이 맞지만(변경은 이미 일어났다), 테스트에서 삼키면 "로그에 남는다"
		// 는 단언이 전부 헛돈다 — 실제로 이 픽스처는 actor id 가 uuid 가
		// 아니라서 모든 기록이 조용히 실패하고 있었다.
		Logger: slog.New(failOnAudit{t: t}),
		Caller: func(*http.Request) Caller { return c },
		Render: func(w http.ResponseWriter, _ *http.Request, name string, code int, data any) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(name))
			if m, ok := data.(map[string]any); ok {
				for k, v := range m {
					if s, ok := v.(string); ok {
						_, _ = w.Write([]byte("|" + k + "=" + s))
					}
					if mm, ok := v.(map[string]string); ok {
						for kk, vv := range mm {
							_, _ = w.Write([]byte("|" + kk + "=" + vv))
						}
					}
				}
			}
		},
		Version: "v1.2.3",
		Migrations: func(context.Context) ([]string, int, error) {
			return []string{"00001_init", "00002_rbac"}, 0, nil
		},
	}
	return d, pool
}

func post(h http.HandlerFunc, target string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func get(h http.HandlerFunc, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// Every handler checks its own permission even though the tree gate ran: the
// gate saw `/admin/...` and cannot know this request is a delete (D15 4.2).
func TestEachHandlerChecksItsOwnPermission(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"admin.access": true}})

	cases := map[string]http.HandlerFunc{
		"설정 조회":   d.SettingsForm,
		"설정 저장":   d.SettingsSave,
		"메일 조회":   d.MailSettingsForm,
		"페이지 목록":  d.PageList,
		"페이지 저장":  d.PageSave,
		"페이지 발행":  d.PagePublish,
		"페이지 삭제":  d.PageDelete,
		"사용자 비활성": d.UserDeactivate,
		"사용자 활성":  d.UserActivate,
		"시스템 정보":  d.System,
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			rec := post(h, "/admin/x", nil)
			if rec.Code != http.StatusForbidden {
				t.Errorf("admin.access 만 있는데 HTTP %d, want 403", rec.Code)
			}
		})
	}
}

// FR-708: the secret is written once and never travels back. "Masked in the UI"
// is not the same as "never sent" — the value would be in the HTML source.
func TestSecretsAreNeverRedisplayed(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{superuser: true}) // SMTP 저장은 superuser 만 (R7)

	post(d.MailSettingsSave, "/admin/settings/mail", url.Values{
		"mail.smtp_host":     {"smtp.example.com"},
		"mail.smtp_user":     {"postmaster"},
		"mail.smtp_password": {"s3cr3t-smtp-password"},
		"mail.tls_mode":      {"starttls"},
	})

	rec := get(d.MailSettingsForm, "/admin/settings/mail")
	if strings.Contains(rec.Body.String(), "s3cr3t-smtp-password") {
		t.Errorf("SMTP 비밀번호가 화면에 다시 표시됐다: %s", rec.Body.String())
	}
	// ...while the non-secret fields do come back, or the form is unusable.
	if !strings.Contains(rec.Body.String(), "smtp.example.com") {
		t.Errorf("일반 설정이 표시되지 않았다: %s", rec.Body.String())
	}
}

// **Turnstile 키는 A-201 에서 넣는다** (FR-216). 비밀키는 다시 보이지 않고, 빈 칸은
// 「그대로 두라」이고, 바꾸면 작업 로그에 남되 값은 남지 않는다. 길이는 서버가 막는다.
func TestTurnstileKeysOnSiteSettings(t *testing.T) {
	d, pool := fixture(t, &fakeCaller{perms: map[string]bool{"settings.update": true}, email: "op@example.com"})
	ctx := context.Background()

	if rec := post(d.SettingsSave, "/admin/settings", url.Values{
		"turnstile.site_key": {"0x4AAAAAAAsitekey"}, "turnstile.secret_key": {"0x4AAAAAAA-SUPERSECRET"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("저장 = HTTP %d (%s)", rec.Code, rec.Body.String())
	}
	body := get(d.SettingsForm, "/admin/settings").Body.String()
	if strings.Contains(body, "SUPERSECRET") {
		t.Errorf("Turnstile 비밀키가 화면에 다시 표시됐다")
	}
	if !strings.Contains(body, "0x4AAAAAAAsitekey") {
		t.Errorf("사이트 키가 보이지 않는다: %s", body)
	}

	post(d.SettingsSave, "/admin/settings", url.Values{
		"turnstile.site_key": {"0x4AAAAAAAsitekey"}, "turnstile.secret_key": {""}, "site.name": {"온돌"}})
	kv, err := d.Content.Settings(ctx, "turnstile.secret_key")
	if err != nil || kv["turnstile.secret_key"] != "0x4AAAAAAA-SUPERSECRET" {
		t.Errorf("빈 칸이 저장된 비밀키를 지웠다: %q (%v)", kv["turnstile.secret_key"], err)
	}

	var n int
	var leaked bool
	if err := pool.QueryRow(ctx, `SELECT count(*), coalesce(bool_or(summary LIKE '%SUPERSECRET%'), false)
		FROM operation_logs WHERE target_id = 'turnstile'`).Scan(&n, &leaked); err != nil {
		t.Fatal(err)
	}
	if n != 1 || leaked {
		t.Errorf("Turnstile 변경 작업 로그 %d건(want 1), 키가 남았나 %v", n, leaked)
	}

	for _, k := range []string{"turnstile.site_key", "turnstile.secret_key"} {
		if rec := post(d.SettingsSave, "/admin/settings", url.Values{k: {strings.Repeat("k", 201)}}); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s 201자 = HTTP %d, want 422", k, rec.Code)
		}
	}
}

// **GA4 측정 ID 는 형식이 맞을 때만 저장한다** (FR-714). 이 값은 공개 화면의 `<script>` 에
// 들어간다 — 「G- + 영숫자」(Google 문서) 밖의 값을 받으면 오타는 조용히 측정을 끄고, 이상한
// 값은 스크립트 주소를 바꾼다. 붙여넣은 공백·소문자는 고쳐 저장하고, 빈 값은 끄기다.
func TestAnalyticsIDMustLookLikeGA4(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"settings.update": true}})
	ctx := context.Background()
	saved := func() string {
		kv, err := d.Content.Settings(ctx, "analytics.ga4_id")
		if err != nil {
			t.Fatal(err)
		}
		return kv["analytics.ga4_id"]
	}
	if rec := post(d.SettingsSave, "/admin/settings", url.Values{"analytics.ga4_id": {" g-psw1my7hb4 "}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("맞는 ID = HTTP %d", rec.Code)
	}
	if got := saved(); got != "G-PSW1MY7HB4" {
		t.Errorf("저장된 ID = %q, want G-PSW1MY7HB4", got)
	}
	for _, bad := range []string{"UA-12345-1", "G-", "G-PSW1\"><script>", "PSW1MY7HB4", "G-" + strings.Repeat("A", 40)} {
		if rec := post(d.SettingsSave, "/admin/settings", url.Values{"analytics.ga4_id": {bad}}); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%q = HTTP %d, want 422", bad, rec.Code)
		}
	}
	if got := saved(); got != "G-PSW1MY7HB4" {
		t.Errorf("거부한 값이 저장됐다: %q", got)
	}
	post(d.SettingsSave, "/admin/settings", url.Values{"analytics.ga4_id": {""}})
	if got := saved(); got != "" {
		t.Errorf("빈 값으로 끌 수 없다: %q", got)
	}
}

// **A-201 저장은 작업 로그에 남는다** (D19 A-201 「저장 성공 → 남김」, D15 7절).
//
// 사이트 유형과 Turnstile 만 남기고 나머지는 흔적이 없었다. GA4 측정 ID 는 방문 기록이
// 어느 GA 속성으로 가는지를 정한다 — `settings.update` 를 가진 운영자가 자기 속성으로
// 바꿔도 아무도 몰랐다 (2026-10-21 점검). 그래서 GA4 는 값까지 남긴다(공개값이다).
func TestSiteSettingsSaveIsLogged(t *testing.T) {
	d, pool := fixture(t, &fakeCaller{perms: map[string]bool{"settings.update": true}, email: "op@example.com"})
	ctx := context.Background()
	summaries := func() []string {
		rows, err := pool.Query(ctx, `SELECT summary FROM operation_logs WHERE target_id = 'site' ORDER BY created_at`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}

	post(d.SettingsSave, "/admin/settings", url.Values{"analytics.ga4_id": {"G-AAAA1111"}})
	post(d.SettingsSave, "/admin/settings", url.Values{"analytics.ga4_id": {"G-BBBB2222"}, "site.name": {"새 이름"}})
	post(d.SettingsSave, "/admin/settings", url.Values{"analytics.ga4_id": {"G-BBBB2222"}}) // 바뀐 것 없음

	got := summaries()
	if len(got) != 2 {
		t.Fatalf("작업 로그 %d건 %q, want 2 (바뀐 저장만)", len(got), got)
	}
	if !strings.Contains(got[0], "G-AAAA1111") {
		t.Errorf("첫 GA4 지정이 값 없이 남았다: %q", got[0])
	}
	if !strings.Contains(got[1], "G-AAAA1111 → G-BBBB2222") || !strings.Contains(got[1], "site.name") {
		t.Errorf("GA4 교체·이름 변경이 남지 않았다: %q", got[1])
	}
	if strings.Contains(got[1], "새 이름") {
		t.Errorf("GA4 밖의 값까지 남겼다: %q", got[1])
	}
}

// **A-201 은 D19 가 적은 검사를 한다** — 사이트 이름 1~100자(공백만은 거부), 메타 설명
// 300자, OG 이미지 http·https 또는 내부 경로. 명세에만 있고 구현이 없었다 (2026-10-21
// 점검). OG 이미지는 `<meta content>` 에 이스케이프돼 들어가 XSS 는 아니지만,
// `javascript:`·`//남의호스트` 같은 값이 링크 미리보기에 그대로 실렸다.
func TestSiteSettingsValidateTheD19Fields(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"settings.update": true}})
	for _, bad := range []url.Values{
		{"site.name": {"   "}},
		{"site.name": {strings.Repeat("가", 101)}},
		{"site.meta_description": {strings.Repeat("a", 301)}},
		{"site.og_image": {"javascript:alert(1)"}},
		{"site.og_image": {"//evil.example/x.png"}},
		{"site.og_image": {"ftp://example.com/x.png"}},
		{"site.og_image": {`/\evil.example/x.png`}},
	} {
		if rec := post(d.SettingsSave, "/admin/settings", bad); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%v = HTTP %d, want 422", bad, rec.Code)
		}
	}
	for _, ok := range []url.Values{
		{"site.name": {strings.Repeat("가", 100)}},
		{"site.meta_description": {strings.Repeat("가", 300)}},
		{"site.og_image": {"https://cdn.example.com/og.png"}},
		{"site.og_image": {"/media/0e7f0c3a-0000-4000-8000-000000000000"}},
		{"site.og_image": {""}},
	} {
		if rec := post(d.SettingsSave, "/admin/settings", ok); rec.Code != http.StatusSeeOther {
			t.Errorf("%v = HTTP %d, want 303 (%s)", ok, rec.Code, rec.Body.String())
		}
	}
}

// **지도 키는 A-201 에서 넣는다** (FR-716). 네이버 ncpKeyId·카카오 JavaScript 키·구글 API
// 키·Map ID 는 페이지에 실려 나가는 공개값이라 봉인하지 않지만, `<script>` 의 속성이 되므로
// 영문·숫자·`-`·`_` 만 받는다. 붙여넣은 앞뒤 공백은 떼고, 빈 값은 그 지도를 끈다.
func TestMapKeysOnSiteSettings(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"settings.update": true}})
	ctx := context.Background()
	keys := []string{"map.naver_key", "map.kakao_key", "map.google_key", "map.google_map_id"}
	saved := func() map[string]string {
		kv, err := d.Content.Settings(ctx, keys...)
		if err != nil {
			t.Fatal(err)
		}
		return kv
	}
	if rec := post(d.SettingsSave, "/admin/settings", url.Values{
		"map.naver_key": {" abc123naver "}, "map.kakao_key": {"0123456789abcdef0123456789abcdef"},
		"map.google_key": {"AIzaSyA-test_key"}, "map.google_map_id": {"8e0a97af9386fef"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("지도 키 저장 = HTTP %d", rec.Code)
	}
	if kv := saved(); kv["map.naver_key"] != "abc123naver" || kv["map.kakao_key"] != "0123456789abcdef0123456789abcdef" ||
		kv["map.google_key"] != "AIzaSyA-test_key" || kv["map.google_map_id"] != "8e0a97af9386fef" {
		t.Errorf("저장된 지도 키 = %v", kv)
	}
	for _, bad := range []url.Values{
		{"map.kakao_key": {`abc" onload="alert(1)`}},
		{"map.naver_key": {"abc def"}},
		{"map.google_key": {strings.Repeat("k", 101)}},
		{"map.google_map_id": {"<script>"}},
	} {
		if rec := post(d.SettingsSave, "/admin/settings", bad); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%v = HTTP %d, want 422", bad, rec.Code)
		}
	}
	if kv := saved(); kv["map.kakao_key"] != "0123456789abcdef0123456789abcdef" {
		t.Errorf("거부한 값이 저장됐다: %v", kv)
	}
	post(d.SettingsSave, "/admin/settings", url.Values{"map.kakao_key": {""}})
	if kv := saved(); kv["map.kakao_key"] != "" {
		t.Errorf("빈 값으로 끌 수 없다: %v", kv)
	}
}

// **회원가입은 A-201 에서 닫고 다시 연다** (FR-217). 빈 값이 「받음」이다.
func TestSignupCloseOnSiteSettings(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"settings.update": true}})
	saved := func() string {
		kv, err := d.Content.Settings(context.Background(), "auth.signup_closed")
		if err != nil {
			t.Fatal(err)
		}
		return kv["auth.signup_closed"]
	}
	for _, want := range []string{"1", ""} {
		if rec := post(d.SettingsSave, "/admin/settings", url.Values{"auth.signup_closed": {want}}); rec.Code != http.StatusSeeOther {
			t.Fatalf("auth.signup_closed=%q 저장 = HTTP %d", want, rec.Code)
		}
		if got := saved(); got != want {
			t.Errorf("auth.signup_closed = %q, want %q", got, want)
		}
	}
}

// 관리자 페이지 편집(A-302)도 에디터의 「지도 넣기」 가 쓸 키를 받는다 (FR-716).
func TestPageFormCarriesMapKeys(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"page.create": true, "page.update": true}})
	if err := d.Content.PutSettings(context.Background(), map[string]string{"map.kakao_key": "kakaokey123"}); err != nil {
		t.Fatal(err)
	}
	if body := get(d.PageForm, "/admin/pages/new").Body.String(); !strings.Contains(body, "map.kakao_key=kakaokey123") {
		t.Errorf("페이지 편집에 지도 키가 없다: %s", body)
	}
}

// An empty secret box means "leave it alone": the form cannot show the current
// value, so empty is the normal state of a screen opened to change something
// else. Treating it as "erase" silently breaks mail on the next save.
func TestEmptySecretDoesNotEraseTheStoredOne(t *testing.T) {
	d, pool := fixture(t, &fakeCaller{superuser: true}) // SMTP 저장은 superuser 만 (R7)
	ctx := context.Background()

	post(d.MailSettingsSave, "/admin/settings/mail", url.Values{
		"mail.smtp_host": {"smtp.example.com"}, "mail.smtp_password": {"keep-me-please"},
		"mail.tls_mode": {"starttls"},
	})
	post(d.MailSettingsSave, "/admin/settings/mail", url.Values{
		"mail.smtp_host": {"smtp2.example.com"}, "mail.smtp_password": {""},
		"mail.tls_mode": {"tls"},
	})

	kv, err := content.NewStore(pool).Settings(ctx, "mail.smtp_password", "mail.smtp_host")
	if err != nil {
		t.Fatal(err)
	}
	if kv["mail.smtp_password"] != "keep-me-please" {
		t.Errorf("빈 칸이 저장된 비밀번호를 지웠다: %q", kv["mail.smtp_password"])
	}
	if kv["mail.smtp_host"] != "smtp2.example.com" {
		t.Errorf("일반 필드가 저장되지 않았다: %q", kv["mail.smtp_host"])
	}
}

// FR-710: a closed vocabulary. An unknown value leaves the router assembling a
// tree nobody described.
func TestSiteTypeIsAClosedVocabulary(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"settings.update": true}})

	if rec := post(d.SettingsSave, "/admin/settings", url.Values{
		"site.type": {"marketplace"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("알 수 없는 사이트 유형이 HTTP %d 로 통과했다", rec.Code)
	}
	for _, v := range []string{"cms", "shop"} {
		if rec := post(d.SettingsSave, "/admin/settings", url.Values{
			"site.type": {v}}); rec.Code != http.StatusSeeOther {
			t.Errorf("%s 가 거부됐다: HTTP %d", v, rec.Code)
		}
	}
}

// **각 위젯은 그 데이터에 대한 권한이 있을 때만 조회한다** (D13 A-101, FR-702).
//
// 「그릴 때 숨긴다」로는 부족하다 — 데이터를 가져다 놓고 템플릿에서 가리면,
// 그 템플릿을 고치는 날 편집자에게 매출이 보인다. 그래서 **묻지 않았는지**를
// 본다: 렌더러가 받은 데이터에 그 키가 아예 없어야 한다.
func TestDashboardWidgetsFollowPermissions(t *testing.T) {
	for _, tc := range []struct {
		name       string
		perms      map[string]bool
		wantPosts  bool
		wantOrders bool
	}{
		{"권한 없음 — 위젯도 없다", map[string]bool{"admin.access": true}, false, false},
		{"글만", map[string]bool{"admin.access": true, "post.view": true}, true, false},
		{"주문만", map[string]bool{"admin.access": true, "order.view": true}, false, true},
		{"둘 다", map[string]bool{"admin.access": true, "post.view": true, "order.view": true}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _ := fixture(t, &fakeCaller{perms: tc.perms, id: "u1"})
			var data map[string]any
			d.Render = func(_ http.ResponseWriter, _ *http.Request, _ string, _ int, v any) {
				data, _ = v.(map[string]any)
			}
			d.Dashboard(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/admin", nil))

			if _, has := data["Posts"]; has != tc.wantPosts {
				t.Errorf("Posts 위젯 존재=%v, want %v — 권한: %v", has, tc.wantPosts, tc.perms)
			}
			if _, has := data["Orders"]; has != tc.wantOrders {
				t.Errorf("Orders 위젯 존재=%v, want %v — 권한 없이 매출이 보인다", has, tc.wantOrders)
			}
		})
	}
}

// 관리자 배색도 닫힌 어휘다 (A-201). 모르는 값은 `data-admin-theme` 에 그대로
// 찍히고 admin.css 의 어느 블록과도 매치되지 않아 콘솔이 기본으로 보인다 —
// **저장은 됐는데 아무 일도 안 일어나는 상태**이고, 화면은 아무 말도 하지 않는다.
func TestAdminThemeIsAClosedVocabulary(t *testing.T) {
	d, pool := fixture(t, &fakeCaller{perms: map[string]bool{"settings.update": true}})

	if rec := post(d.SettingsSave, "/admin/settings", url.Values{
		"admin.theme": {"1z"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("알 수 없는 배색이 HTTP %d 로 통과했다", rec.Code)
	}

	// **빈 값은 유효하다.** 그것이 「고르지 않았다」이고, 곧 기본(1b)이다 —
	// 저장 못 하게 막으면 한번 고른 뒤 기본으로 되돌릴 방법이 없어진다.
	for _, v := range []string{"", "1a", "1b", "1c", "1d", "1e"} {
		if rec := post(d.SettingsSave, "/admin/settings", url.Values{
			"admin.theme": {v}}); rec.Code != http.StatusSeeOther {
			t.Errorf("배색 %q 가 거부됐다: HTTP %d", v, rec.Code)
		}
	}

	// **저장이 실제로 남아야 한다.** 이 버그의 원래 모습이 「부팅 때 읽는 키인데
	// 그것을 쓰는 폼이 없다」였다 — 폼을 붙였는데 값이 안 닿으면 같은 자리다.
	post(d.SettingsSave, "/admin/settings", url.Values{"admin.theme": {"1d"}})
	var got string
	if err := pool.QueryRow(context.Background(),
		`SELECT value FROM settings WHERE key = 'admin.theme'`).Scan(&got); err != nil {
		t.Fatalf("배색이 저장되지 않았다: %v", err)
	}
	if got != "1d" {
		t.Errorf("저장된 배색 = %q, want 1d", got)
	}
}

// page.update must not be able to publish, or the separation of the two
// permissions is decorative.
func TestPublishNeedsItsOwnPermission(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{
		"page.view": true, "page.update": true, // no page.publish, no page.delete
	}})

	if rec := post(d.PagePublish, "/admin/pages/1/publish", url.Values{
		"status": {"published"}}); rec.Code != http.StatusForbidden {
		t.Errorf("page.update 만으로 발행됐다: HTTP %d", rec.Code)
	}
	if rec := post(d.PageDelete, "/admin/pages/1/delete", nil); rec.Code != http.StatusForbidden {
		t.Errorf("page.update 만으로 삭제됐다: HTTP %d", rec.Code)
	}
	// ...and editing itself still works.
	//
	// **경로 변수를 실어 부른다.** 라우트는 `/admin/pages/{id}` 이므로 운영에서
	// `id` 가 비는 일은 없고, 비워 두면 PageSave 가 「새로 만들기」로 읽어
	// page.create 를 요구한다 (D15 2.2). 픽스처가 라우트와 달랐던 것이지
	// 핸들러가 틀린 것이 아니다.
	id := mkPage(t, d)
	req := httptest.NewRequest(http.MethodPost, "/admin/pages/"+id,
		strings.NewReader(url.Values{
			"slug": {"about"}, "title": {"회사"}, "body": {"본문"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	d.PageSave(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("page.update 로 저장이 안 된다: HTTP %d (%s)", rec.Code, rec.Body.String())
	}
}

// D15 5.3-1: destructive, so the password is re-confirmed. An open session is
// not the same as the operator being present.
func TestDeactivateRequiresReauth(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"user.update": true}, reauth: true})
	rec := post(d.UserDeactivate, "/admin/users/x/deactivate", nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("재인증 없이 비활성화가 진행됐다: HTTP %d", rec.Code)
	}
}

// R6: only a superuser may switch off a superuser holder. Without it, revoking
// the role is blocked while turning off its holder is not — same end, other road.
func TestNonSuperuserCannotDeactivateASuperuserHolder(t *testing.T) {
	d, pool := fixture(t, &fakeCaller{perms: map[string]bool{"user.update": true}, id: "me"})
	ctx := context.Background()

	target, err := d.Auth.CreateUser(ctx, "admin@example.com", "h", "관리자")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE key='admin'`,
		target); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/users/"+target+"/deactivate", nil)
	req.SetPathValue("id", target)
	rec := httptest.NewRecorder()
	d.UserDeactivate(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("비-superuser 가 superuser 보유자를 비활성화했다: HTTP %d", rec.Code)
	}
	u, err := d.Auth.FindUserByID(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if !u.IsActive {
		t.Error("거부됐는데 계정이 비활성화됐다")
	}
}

// C5: the DSN carries the database password, and an admin screen is exactly
// where a screenshot comes from.
func TestSystemScreenDoesNotShowTheDSN(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"settings.view": true}})
	rec := get(d.System, "/admin/system")

	body := rec.Body.String()
	for _, banned := range []string{"postgres://", "password=", "sslmode", os.Getenv(dsnEnv)} {
		if banned == "" {
			continue
		}
		if strings.Contains(body, banned) {
			t.Errorf("시스템 화면에 DSN 조각이 있다 (%q): %s", banned, body)
		}
	}
	if !strings.Contains(body, "v1.2.3") {
		t.Errorf("버전이 표시되지 않았다: %s", body)
	}
}

// A-401 needs user.view. D15 2.2 lists it separately from user.update, and a
// screen that accepted either would merge two permissions the design keeps apart.
func TestUserListNeedsUserView(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"user.update": true}})
	if rec := get(d.UserList, "/admin/users"); rec.Code != http.StatusForbidden {
		t.Errorf("user.update 만으로 목록이 열렸다: HTTP %d", rec.Code)
	}
}

// The list reads the columns the screen draws and nothing else. A password hash
// that never leaves the database cannot leak from a template someone edits later.
func TestUserListDoesNotCarryCredentials(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"user.view": true}})
	ctx := context.Background()

	const hash = "$2a$12$notarealhashbutdistinctive"
	if _, err := d.Auth.CreateUser(ctx, "a@example.com", hash, "가"); err != nil {
		t.Fatal(err)
	}
	rows, err := d.Auth.ListUsers(ctx, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d행, want 1행", len(rows))
	}
	// Assert on the value handed to the template, not on what this fixture
	// happens to print: %+v walks the struct, so a field added to UserRow later
	// fails here instead of shipping the hash to every admin screen.
	var handed string
	d.Render = func(_ http.ResponseWriter, _ *http.Request, _ string, _ int, data any) {
		handed = fmt.Sprintf("%+v", data)
	}
	get(d.UserList, "/admin/users")
	if handed == "" {
		t.Fatal("렌더가 호출되지 않았다")
	}
	if strings.Contains(handed, hash) {
		t.Errorf("목록이 템플릿에 비밀번호 해시를 넘겼다: %s", handed)
	}
	if rows[0].Email != "a@example.com" || rows[0].DisplayName != "가" {
		t.Errorf("표시할 열이 비었다: %+v", rows[0])
	}
}

// Roles come back with the row. One query per user would turn the screen most
// likely to grow into N+1 the moment it does.
func TestUserListCarriesRolesWithoutASecondQuery(t *testing.T) {
	d, pool := fixture(t, &fakeCaller{perms: map[string]bool{"user.view": true}})
	ctx := context.Background()

	id, err := d.Auth.CreateUser(ctx, "op@example.com", "h", "운영")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE key='operator'`,
		id); err != nil {
		t.Fatal(err)
	}
	plain, err := d.Auth.CreateUser(ctx, "plain@example.com", "h", "일반")
	if err != nil {
		t.Fatal(err)
	}

	rows, err := d.Auth.ListUsers(ctx, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, r := range rows {
		got[r.ID] = r.Roles
	}
	if len(got[id]) != 1 || got[id][0] != "operator" {
		t.Errorf("역할이 함께 오지 않았다: %v", got[id])
	}
	// A user with no role must still appear — an INNER JOIN would drop them.
	if _, ok := got[plain]; !ok {
		t.Error("역할 없는 사용자가 목록에서 사라졌다")
	}
	if len(got[plain]) != 0 {
		t.Errorf("역할 없는 사용자에 역할이 붙었다: %v", got[plain])
	}
}

// An unbounded list is one seed script away from loading every account to draw
// one screen.
func TestUserListIsBounded(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"user.view": true}})
	ctx := context.Background()

	for i := range userPageSize + 5 {
		if _, err := d.Auth.CreateUser(ctx,
			fmt.Sprintf("u%d@example.com", i), "h", fmt.Sprintf("사용자%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := d.Auth.ListUsers(ctx, userPageSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != userPageSize {
		t.Errorf("%d행 반환 — 상한 %d 이 걸리지 않았다", len(rows), userPageSize)
	}
	next, err := d.Auth.ListUsers(ctx, userPageSize, userPageSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 5 {
		t.Errorf("두 번째 쪽 %d행, want 5행", len(next))
	}
	// The pages must not overlap, or paging silently hides accounts.
	first := map[string]bool{}
	for _, r := range rows {
		first[r.ID] = true
	}
	for _, r := range next {
		if first[r.ID] {
			t.Errorf("두 쪽에 같은 사용자가 나온다: %s", r.ID)
		}
	}
}

// ?page= has to reach the query. A screen that draws page 1 while fetching page
// 0 hides every account past the first screenful and looks like it worked.
func TestUserListPagingReachesTheQuery(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"user.view": true}})
	ctx := context.Background()

	for i := range userPageSize + 3 {
		if _, err := d.Auth.CreateUser(ctx,
			fmt.Sprintf("p%02d@example.com", i), "h", "쪽"); err != nil {
			t.Fatal(err)
		}
	}
	var shown [][]auth.UserRow
	d.Render = func(_ http.ResponseWriter, _ *http.Request, _ string, _ int, data any) {
		m, ok := data.(map[string]any)
		if !ok {
			t.Fatalf("data 가 map 이 아니다: %T", data)
		}
		rows, ok := m["Users"].([]auth.UserRow)
		if !ok {
			t.Fatalf("Users 가 []auth.UserRow 가 아니다: %T", m["Users"])
		}
		shown = append(shown, rows)
	}

	get(d.UserList, "/admin/users")
	get(d.UserList, "/admin/users?page=1")
	if len(shown) != 2 {
		t.Fatalf("렌더 %d회, want 2회", len(shown))
	}
	if len(shown[0]) != userPageSize {
		t.Errorf("1쪽 %d행, want %d행", len(shown[0]), userPageSize)
	}
	if len(shown[1]) != 3 {
		t.Errorf("2쪽 %d행, want 3행 — page 파라미터가 쿼리에 닿지 않았다", len(shown[1]))
	}
	first := map[string]bool{}
	for _, r := range shown[0] {
		first[r.ID] = true
	}
	for _, r := range shown[1] {
		if first[r.ID] {
			t.Errorf("2쪽에 1쪽 사용자가 다시 나온다: %s", r.Email)
		}
	}
}

// postID drives an A-402 action against one target.
func postID(h http.HandlerFunc, id string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/admin/users/"+id, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// D19 A-402 gives every action its own permission row. Reaching the screen with
// user.update must not carry delete, create or forced reset with it — otherwise
// the four rows describe one permission wearing four names.
func TestEachAccountActionNeedsItsOwnPermission(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"user.update": true}, id: "me"})
	ctx := context.Background()
	target, err := d.Auth.CreateUser(ctx, "t@example.com", "h", "대상")
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]http.HandlerFunc{
		"삭제":       d.UserDelete,
		"비밀번호 재설정": d.UserResetPassword,
		"생성":       d.UserCreate,
	}
	for name, h := range cases {
		if rec := postID(h, target, url.Values{}); rec.Code != http.StatusForbidden {
			t.Errorf("user.update 만으로 %s가 HTTP %d 로 통과했다", name, rec.Code)
		}
	}
	u, err := d.Auth.FindUserByID(ctx, target)
	if err != nil {
		t.Fatalf("거부됐어야 할 삭제가 실제로 지웠다: %v", err)
	}
	if !u.IsActive {
		t.Error("거부됐는데 상태가 바뀌었다")
	}
}

// D15 5.3-1: all three destructive actions re-confirm the password. An open
// session is not the same as the operator being present.
func TestAllDestructiveAccountActionsRequireReauth(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{
		perms: map[string]bool{"user.update": true, "user.delete": true, "user.reset_password": true},
		id:    "me", reauth: true,
	})
	ctx := context.Background()
	target, err := d.Auth.CreateUser(ctx, "t@example.com", "h", "대상")
	if err != nil {
		t.Fatal(err)
	}

	for name, h := range map[string]http.HandlerFunc{
		"비활성":      d.UserDeactivate,
		"활성":       d.UserActivate, // 막아 둔 계정을 여는 것이다
		"삭제":       d.UserDelete,
		"비밀번호 재설정": d.UserResetPassword,
	} {
		if rec := postID(h, target, nil); rec.Code != http.StatusForbidden {
			t.Errorf("재인증 없이 %s가 HTTP %d 로 진행됐다", name, rec.Code)
		}
	}
	if _, err := d.Auth.FindUserByID(ctx, target); err != nil {
		t.Errorf("재인증 없이 삭제됐다: %v", err)
	}
}

// D19: 자기 계정 비활성화·삭제는 거부. Locking yourself out needs no permission
// to be a catastrophe, and deleting yourself removes the actor the log names.
func TestCannotOperateOnYourOwnAccount(t *testing.T) {
	d, _ := fixture(t, nil)
	ctx := context.Background()
	me, err := d.Auth.CreateUser(ctx, "me@example.com", "h", "나")
	if err != nil {
		t.Fatal(err)
	}
	d.Caller = func(*http.Request) Caller {
		return &fakeCaller{perms: map[string]bool{
			"user.update": true, "user.delete": true, "user.reset_password": true,
		}, id: me}
	}

	for name, h := range map[string]http.HandlerFunc{
		"비활성": d.UserDeactivate,
		"삭제":  d.UserDelete,
	} {
		if rec := postID(h, me, nil); rec.Code != http.StatusForbidden {
			t.Errorf("자기 계정 %s가 HTTP %d 로 통과했다", name, rec.Code)
		}
	}
	u, err := d.Auth.FindUserByID(ctx, me)
	if err != nil {
		t.Fatalf("자기 자신을 지웠다: %v", err)
	}
	if !u.IsActive {
		t.Error("자기 자신을 비활성화했다")
	}
}

// 5.2 applies to deletion exactly as it does to deactivation: the two reach the
// same end state, so guarding one and not the other guards neither.
func TestLastSuperuserCannotBeDeleted(t *testing.T) {
	d, pool := fixture(t, nil)
	ctx := context.Background()
	me, _ := d.Auth.CreateUser(ctx, "me@example.com", "h", "나")
	only, _ := d.Auth.CreateUser(ctx, "only@example.com", "h", "유일한관리자")
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE key='admin'`,
		only); err != nil {
		t.Fatal(err)
	}
	d.Caller = func(*http.Request) Caller {
		return &fakeCaller{perms: map[string]bool{"user.delete": true}, id: me, superuser: true}
	}

	if rec := postID(d.UserDelete, only, nil); rec.Code != http.StatusConflict {
		t.Errorf("마지막 관리자 삭제가 HTTP %d 로 통과했다", rec.Code)
	}
	if _, err := d.Auth.FindUserByID(ctx, only); err != nil {
		t.Errorf("마지막 관리자가 지워졌다: %v", err)
	}
}

// D19: 타인의 비밀번호를 설정하지 않는다 — 재설정 강제만 있다. The administrator
// never learns a credential they could then use as that user.
//
// **옛 비밀번호는 비운다** (D15 5.4). 탈취를 되돌리는 동작인데 해시를 그대로 두면,
// 비밀번호를 아는 탈취자가 세션이 끊긴 그 자리에서 다시 로그인하고 소셜 계정까지 다시
// 붙인다 (2026-10-21 재점검). 비우는 것은 설정하는 것이 아니다 — 관리자는 아무것도 모른다.
func TestForcedResetEndsSessionsAndNeverSetsAPassword(t *testing.T) {
	d, pool := fixture(t, nil)
	ctx := context.Background()
	me, _ := d.Auth.CreateUser(ctx, "me@example.com", "h", "나")
	oldHash, err := auth.HashPassword("taker-knows-this")
	if err != nil {
		t.Fatal(err)
	}
	target, err := d.Auth.CreateUser(ctx, "t@example.com", oldHash, "대상")
	if err != nil {
		t.Fatal(err)
	}
	d.Caller = func(*http.Request) Caller {
		return &fakeCaller{perms: map[string]bool{"user.reset_password": true}, id: me, superuser: true}
	}
	var sent []string
	d.SendReset = func(email, token string) { sent = append(sent, email+"|"+token) }

	before, err := d.Auth.FindUserByID(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	mailReady(t, d)
	if rec := postID(d.UserResetPassword, target, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("재설정 강제 실패: HTTP %d (%s)", rec.Code, rec.Body.String())
	}

	// The hash is cleared, not replaced: nothing the administrator could know.
	var hash string
	if err := pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, target).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != "" {
		t.Errorf("재설정 강제 뒤 해시 %q — 비우지 않았거나 관리자가 새 값을 넣었다", hash)
	}
	if _, err := d.Auth.Authenticate(ctx, "t@example.com", "taker-knows-this"); err == nil {
		t.Error("재설정 강제 뒤에도 옛 비밀번호로 로그인된다 — 탈취자가 그대로 돌아온다")
	}
	// ...and every existing session is over (D15 5.4).
	after, err := d.Auth.FindUserByID(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if !after.SessionsValidFrom.After(before.SessionsValidFrom) {
		t.Errorf("세션 컷오프가 앞당겨지지 않았다: %v → %v",
			before.SessionsValidFrom, after.SessionsValidFrom)
	}
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "t@example.com|") {
		t.Errorf("재설정 링크가 대상에게 가지 않았다: %v", sent)
	}
	// The raw token exists only in the mail: the table holds a hash.
	raw := strings.TrimPrefix(sent[0], "t@example.com|")
	var stored int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM password_reset_tokens WHERE token_hash::text LIKE $1`,
		"%"+raw+"%").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Error("토큰 원문이 그대로 저장됐다")
	}
}

// mailReady 는 A-205 에 SMTP 호스트를 넣는다 — 재설정 강제는 메일이 나갈 때만 한다.
func mailReady(t *testing.T, d *Deps) {
	t.Helper()
	if err := d.Content.PutSettings(context.Background(), map[string]string{"mail.smtp_host": "smtp.example.com"}); err != nil {
		t.Fatal(err)
	}
}

// **메일을 보낼 수 없는 사이트에서는 재설정 강제를 하지 않는다.** 강제는 비밀번호를 비우고
// 소셜 연결을 끊은 뒤 재설정 메일로 주인을 돌려보내는데, 메일이 안 나가면 그 계정은 들어갈
// 길이 하나도 없다 (2026-10-21 3차 점검).
func TestForcedResetNeedsMail(t *testing.T) {
	d, pool := fixture(t, nil)
	ctx := context.Background()
	me, _ := d.Auth.CreateUser(ctx, "me@example.com", "h", "나")
	target, err := d.Auth.CreateUser(ctx, "t@example.com", "원래해시", "대상")
	if err != nil {
		t.Fatal(err)
	}
	d.Caller = func(*http.Request) Caller {
		return &fakeCaller{perms: map[string]bool{"user.reset_password": true}, id: me, superuser: true}
	}
	if rec := postID(d.UserResetPassword, target, nil); rec.Code != http.StatusConflict {
		t.Fatalf("메일 없는 재설정 강제 = HTTP %d, want 409", rec.Code)
	}
	var hash string
	if err := pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, target).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != "원래해시" {
		t.Errorf("거부했는데 해시가 %q 가 됐다 — 계정이 잠겼다", hash)
	}
}

// **재설정 강제는 소셜 연결도 끊는다** (D15 5.4). 탈취를 되돌리는 동작인데, 연결이
// 남으면 탈취한 사람이 붙여 둔 소셜 계정으로 다시 들어온다 — 세션만 끊고 「되돌렸다」고
// 말하던 것이 2026-10-21 점검에서 확인됐다. 주인은 재설정 뒤 P-111 에서 다시 잇는다.
func TestForcedResetAlsoCutsSocialLinks(t *testing.T) {
	d, pool := fixture(t, nil)
	ctx := context.Background()
	me, _ := d.Auth.CreateUser(ctx, "me@example.com", "h", "나")
	target, err := d.Auth.CreateUser(ctx, "t@example.com", "h", "대상")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"google", "kakao"} {
		if err := d.Auth.LinkSocial(ctx, target, p, p+"-attacker"); err != nil {
			t.Fatal(err)
		}
	}
	d.Caller = func(*http.Request) Caller {
		return &fakeCaller{perms: map[string]bool{"user.reset_password": true}, id: me, superuser: true}
	}
	mailReady(t, d)
	if rec := postID(d.UserResetPassword, target, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("재설정 강제 실패: HTTP %d (%s)", rec.Code, rec.Body.String())
	}
	if links, err := d.Auth.SocialAccounts(ctx, target); err != nil || len(links) != 0 {
		t.Errorf("재설정 강제 뒤 소셜 연결 %+v (%v) — 그 연결로 다시 들어온다", links, err)
	}
	var summary string
	if err := pool.QueryRow(ctx, `SELECT summary FROM operation_logs WHERE action = 'user.reset_password'`).Scan(&summary); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "소셜 연결 2") {
		t.Errorf("작업 로그가 끊은 연결을 말하지 않는다: %q", summary)
	}
}

// 비활성 계정에 대한 재설정 강제는 거부하지 않는다 — 복귀 절차의 일부다 (D19).
func TestForcedResetWorksOnAnInactiveAccount(t *testing.T) {
	d, _ := fixture(t, nil)
	ctx := context.Background()
	me, _ := d.Auth.CreateUser(ctx, "me@example.com", "h", "나")
	target, _ := d.Auth.CreateUser(ctx, "t@example.com", "h", "대상")
	if err := d.Auth.SetActive(ctx, target, false); err != nil {
		t.Fatal(err)
	}
	d.Caller = func(*http.Request) Caller {
		return &fakeCaller{perms: map[string]bool{"user.reset_password": true}, id: me, superuser: true}
	}
	d.SendReset = func(string, string) {}

	mailReady(t, d)
	if rec := postID(d.UserResetPassword, target, nil); rec.Code != http.StatusSeeOther {
		t.Errorf("비활성 계정 재설정이 HTTP %d 로 거부됐다", rec.Code)
	}
}

// D19 A-402 create: email lowercased, password hashed, duplicates refused by
// the database, and no role comes with it.
func TestUserCreateValidatesAndAssignsNoRole(t *testing.T) {
	d, pool := fixture(t, &fakeCaller{perms: map[string]bool{"user.create": true}, id: "me"})
	ctx := context.Background()

	if rec := post(d.UserCreate, "/admin/users", url.Values{
		"email": {"NEW@Example.COM"}, "password": {"correct horse battery"}, "display_name": {"새 사용자"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("생성 실패: HTTP %d (%s)", rec.Code, rec.Body.String())
	}
	var email, hash string
	if err := pool.QueryRow(ctx,
		`SELECT email, password_hash FROM users WHERE display_name = '새 사용자'`).Scan(&email, &hash); err != nil {
		t.Fatal(err)
	}
	if email != "new@example.com" {
		t.Errorf("이메일이 소문자화되지 않았다: %q", email)
	}
	if strings.Contains(hash, "correct horse") || !strings.HasPrefix(hash, "$2") {
		t.Errorf("비밀번호가 bcrypt 해시로 저장되지 않았다: %q", hash)
	}
	var roles int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM user_roles ur JOIN users u ON u.id = ur.user_id
		 WHERE u.email = 'new@example.com'`).Scan(&roles); err != nil {
		t.Fatal(err)
	}
	if roles != 0 {
		t.Errorf("생성만으로 역할 %d개가 붙었다 — user.create 가 곧 권한 부여가 된다", roles)
	}
	// 관리자가 만든 계정은 **인증된 채로** 만든다 — 관리자가 보증한 주소이고, 인증을
	// 켠 사이트에서 미인증이면 로그인조차 되지 않는다 (FR-214).
	var verified bool
	if err := pool.QueryRow(ctx,
		`SELECT email_verified_at IS NOT NULL FROM users WHERE email = 'new@example.com'`).Scan(&verified); err != nil {
		t.Fatal(err)
	}
	if !verified {
		t.Error("관리자가 만든 계정이 미인증이다 — 인증 켬에서 로그인할 수 없다")
	}

	// 중복은 DB UNIQUE 가 막는다.
	if rec := post(d.UserCreate, "/admin/users", url.Values{
		"email": {"new@example.com"}, "password": {"correct horse battery"}, "display_name": {"중복"},
	}); rec.Code != http.StatusConflict {
		t.Errorf("중복 이메일이 HTTP %d 로 통과했다", rec.Code)
	}
	// 짧은 비밀번호·빈 이름·이메일 형식은 거부. 문구는 사람이 읽는 말이다 — 가입(P-103)과 같은 문장이고,
	// 내부 오류 문자열(`content: …`)이 아니다.
	for name, tc := range map[string]struct {
		form url.Values
		want string
	}{
		"짧은 비밀번호": {url.Values{"email": {"a@b.com"}, "password": {"짧다"}, "display_name": {"가"}}, "비밀번호는 10자 이상이어야 합니다."},
		"빈 이름":    {url.Values{"email": {"c@d.com"}, "password": {"correct horse battery"}, "display_name": {"  "}}, "표시 이름은 1~50자입니다."},
		"이메일 형식":  {url.Values{"email": {"not-an-email"}, "password": {"correct horse battery"}, "display_name": {"가"}}, "이메일 형식이 올바르지 않습니다."},
	} {
		rec := post(d.UserCreate, "/admin/users", tc.form)
		if body := rec.Body.String(); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(body, "Error="+tc.want) {
			t.Errorf("%s: HTTP %d %q, want 422 「%s」", name, rec.Code, body, tc.want)
		}
	}
}

// A-402 상세는 user.update 뒤에 있고, 자격증명은 화면에 없다.
func TestUserDetailNeedsUpdateAndCarriesNoCredentials(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"user.view": true}})
	target, err := d.Auth.CreateUser(context.Background(), "t@example.com", "$2a$12$distinctivehash", "대상")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/users/"+target, nil)
	req.SetPathValue("id", target)
	rec := httptest.NewRecorder()
	d.UserDetail(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("user.view 만으로 상세가 열렸다: HTTP %d", rec.Code)
	}

	d.Caller = func(*http.Request) Caller { return &fakeCaller{perms: map[string]bool{"user.update": true}} }
	var handed string
	d.Render = func(w http.ResponseWriter, _ *http.Request, _ string, code int, data any) {
		w.WriteHeader(code)
		handed = fmt.Sprintf("%+v", data)
	}
	rec = httptest.NewRecorder()
	d.UserDetail(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("상세 조회 실패: HTTP %d", rec.Code)
	}
	if strings.Contains(handed, "$2a$12$distinctivehash") {
		t.Errorf("상세가 템플릿에 비밀번호 해시를 넘겼다: %s", handed)
	}
}

// D15 5.3-1: 테마 업로드는 계정 탈취의 마지막 단계다 — 실행되는 파일을
// 올리는 일이므로 비밀번호를 다시 확인한다. 세션이 열려 있는 것과 운영자가
// 그 자리에 있는 것은 다르다.
func TestThemeUploadRequiresReauth(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{
		perms: map[string]bool{"theme.upload": true}, id: "me", reauth: true,
	})
	var called bool
	d.InstallTheme = func(string, io.ReaderAt, int64, bool) error {
		called = true
		return nil
	}

	rec := post(d.ThemeUpload, "/admin/themes/upload", nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("재인증 없이 업로드가 HTTP %d 로 진행됐다", rec.Code)
	}
	if called {
		t.Error("재인증 전에 설치가 호출됐다")
	}
}

// **A-307 의 글 삭제도 첨부 실물까지 지운다** (OPEN-40 결정).
//
// P-207 과 같은 규칙이다. 화면이 둘이니 배선도 둘이고, 한쪽만 고치면 나머지
// 한쪽으로 지운 글의 파일이 조용히 쌓인다 — 정리 잡이 없다 (NFR-103).
func TestAdminPostDeleteRemovesTheAttachmentFile(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"admin.access": true, "post.moderate": true},
		id: "u1", email: "op@example.com"}
	d, _ := fixture(t, caller)
	ctx := context.Background()

	boardID, err := d.Content.CreateBoard(ctx,
		content.Board{Slug: "free", Name: "자유", PerPage: 20}, content.PresetPublic)
	if err != nil {
		t.Fatal(err)
	}
	postID, err := d.Content.CreatePost(ctx,
		content.Post{BoardID: boardID, Title: "지울 글", Body: "본문"})
	if err != nil {
		t.Fatal(err)
	}
	at, err := d.Attachments.Save(ctx, postID, "사진.png",
		bytes.NewReader([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"+strings.Repeat("\x00", 40))))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Attachments.Open(&at); err != nil {
		t.Fatalf("업로드 직후 파일이 없다: %v", err)
	}

	rec := postAdmin(t, d.PostModerate, "/admin/posts", nil,
		url.Values{"post_id": {postID}, "action": {"delete"}})
	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusOK {
		t.Fatalf("삭제 = HTTP %d (%q)", rec.Code, rec.Body.String())
	}

	if _, err := d.Attachments.Open(&at); !os.IsNotExist(err) {
		t.Errorf("첨부 파일이 남았다 (err=%v) — 글이 없어 아무도 찾지 못한다", err)
	}
}

// **활성 테마는 덮어쓸 수 없다 (409). 비활성은 덮어쓴다** (OPEN-42 결정).
//
// 활성 테마를 덮어쓰는 동안 사이트가 그 디렉터리를 그리고 있고, 새 zip 에
// 옛 파셜이 없으면 그 순간의 방문자는 오류 더미를 본다.
func TestThemeUploadRefusesToOverwriteTheActiveTheme(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"theme.upload": true}, id: "me"})
	ctx := context.Background()
	if err := d.Content.PutSettings(ctx, map[string]string{"theme.active": "live"}); err != nil {
		t.Fatal(err)
	}
	var installed []string
	d.InstallTheme = func(name string, _ io.ReaderAt, _ int64, replace bool) error {
		if !replace {
			t.Errorf("%q 를 replace=false 로 설치했다 — 비활성은 덮어써야 한다", name)
		}
		installed = append(installed, name)
		return nil
	}

	if rec := postThemeZip(t, d, "live"); rec.Code != http.StatusConflict {
		t.Errorf("활성 테마 재업로드 = HTTP %d, want 409 (%q)", rec.Code, rec.Body.String())
	}
	if len(installed) != 0 {
		t.Errorf("활성 테마인데 설치가 %v 로 진행됐다", installed)
	}

	// 비활성 테마는 덮어쓴다 — 위 단언이 "무엇이든 막힌다" 를 본 것이 아니다.
	if rec := postThemeZip(t, d, "spare"); rec.Code != http.StatusSeeOther {
		t.Errorf("비활성 테마 재업로드 = HTTP %d, want 303 (%q)", rec.Code, rec.Body.String())
	}
	if len(installed) != 1 || installed[0] != "spare" {
		t.Errorf("설치된 것 %v, want [spare]", installed)
	}
}

// postThemeZip posts A-203 의 멀티파트 폼. urlencoded 로는 핸들러가 파일 앞에서
// 멈춰서, 이름을 읽는 지점까지 가지 못한다.
func postThemeZip(t *testing.T, d *Deps, name string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("name", name); err != nil {
		t.Fatal(err)
	}
	fw, err := mw.CreateFormFile("theme", name+".zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte("PK\x05\x06" + strings.Repeat("\x00", 18))); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/themes/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	d.ThemeUpload(rec, req)
	return rec
}

// **메일(SMTP)이 비어 있으면 대시보드가 알린다** (FR-708, D13 A-205).
//
// 새 설치는 이메일 인증이 켜진 채로 시작한다(FR-214). SMTP 를 넣기 전에는
// 가입한 회원이 인증 메일을 받지 못하고, 재설정도 「보냈습니다」라고만 답한다
// (계정 열거 방지라 화면이 실패를 말할 수 없다). 그 사실을 아는 자리가 필요하다.
func TestDashboardWarnsWhenMailIsNotConfigured(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"admin.access": true}, id: "u1"}
	d, _ := fixture(t, caller)
	ctx := context.Background()

	var warning string
	d.Render = func(_ http.ResponseWriter, _ *http.Request, _ string, _ int, data any) {
		warning = ""
		if m, ok := data.(map[string]any); ok {
			if s, ok := m["MailWarning"].(string); ok {
				warning = s
			}
		}
	}
	call := func() {
		d.Dashboard(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/admin", nil))
	}

	call()
	if !strings.Contains(warning, "메일") {
		t.Errorf("SMTP 가 비어 있는데 경고하지 않았다: %q", warning)
	}
	if err := d.Content.PutSettings(ctx, map[string]string{"mail.smtp_host": "smtp.example.com"}); err != nil {
		t.Fatal(err)
	}
	call()
	if warning != "" {
		t.Errorf("SMTP 를 넣었는데도 경고한다: %q — 위 단언이 「늘 경고한다」였다", warning)
	}
}

// **shop 모드에서 사업자 정보가 비면 대시보드가 알린다** (FR-711, W3-33).
//
// 저장을 막지 않기로 했으므로 (설치 직후는 항상 비어 있다) 알리는 자리가
// 없으면 그 결정은 "아무도 모르는 채 빠져 있다" 가 된다.
func TestDashboardWarnsAboutMissingBusinessInfoInShopMode(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"admin.access": true}, id: "u1"}
	d, _ := fixture(t, caller)
	ctx := context.Background()

	var warning string
	d.Render = func(_ http.ResponseWriter, _ *http.Request, _ string, _ int, data any) {
		warning = ""
		if m, ok := data.(map[string]any); ok {
			if s, ok := m["Warning"].(string); ok {
				warning = s
			}
		}
	}
	call := func() {
		req := httptest.NewRequest(http.MethodGet, "/admin", nil)
		d.Dashboard(httptest.NewRecorder(), req)
	}

	// cms 모드에는 표시 의무가 없다.
	call()
	if warning != "" {
		t.Errorf("cms 모드인데 경고했다: %q", warning)
	}

	if err := d.Content.PutSettings(ctx, map[string]string{"site.type": "shop"}); err != nil {
		t.Fatal(err)
	}
	call()
	if !strings.Contains(warning, "사업자등록번호") {
		t.Errorf("shop 모드에서 빈 항목을 알리지 않았다: %q", warning)
	}

	// 여덟 항목을 다 채우면 조용해진다 — 위 단언이 "늘 경고한다" 가 아니다.
	full := map[string]string{}
	for _, k := range commerce.BusinessKeys {
		full[k] = "값"
	}
	if err := d.Content.PutSettings(ctx, full); err != nil {
		t.Fatal(err)
	}
	call()
	if warning != "" {
		t.Errorf("다 채웠는데 경고한다: %q", warning)
	}
}

// **배포된 약관을 고치려는 요청을 조용히 무시하지 않고 거부한다** (D19 A-207).
//
// 무시하면 운영자는 본문이 고쳐졌다고 믿는다. `order_agreements` 가 가리키는
// 본문이 바뀌면 동의 이력이 거짓이 되고, FR-619 의 "나중에 재현된다" 가 깨진다.
func TestTermsRefusesEditingAPublishedVersion(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"settings.update": true},
		id: "u1", email: "op@example.com"}
	d, pool := fixture(t, caller)
	ctx := context.Background()

	id, err := d.Commerce.AddTerms(ctx, commerce.Terms{Kind: "service", Version: "1.0",
		Body: "원래 본문", EffectiveAt: time.Now(), Required: true}, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	for _, field := range []string{"id", "terms_id"} {
		rec := postAdmin(t, d.TermsAdd, "/admin/terms", nil, url.Values{
			field: {id}, "kind": {"service"}, "version": {"1.0"},
			"body": {"고친 본문"}, "effective_at": {time.Now().Format("2006-01-02")}})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s 필드 = HTTP %d, want 422", field, rec.Code)
		}
	}

	// 본문이 그대로다.
	var body string
	if err := pool.QueryRow(ctx, `SELECT body FROM terms WHERE id = $1`, id).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body != "원래 본문" {
		t.Errorf("본문이 %q 로 바뀌었다", body)
	}

	// 새 버전은 들어간다 — 위 단언이 "무엇이든 막힌다" 가 아니다.
	rec := postAdmin(t, d.TermsAdd, "/admin/terms", nil, url.Values{
		"kind": {"service"}, "version": {"1.1"}, "body": {"개정 본문"},
		"effective_at": {time.Now().Format("2006-01-02")}, "is_required": {"on"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("새 버전 추가 = HTTP %d (%q)", rec.Code, rec.Body.String())
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM terms`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("약관 %d행, want 2", n)
	}
}

// failOnAudit turns a swallowed audit-log failure into a test failure.
type failOnAudit struct{ t *testing.T }

func (h failOnAudit) Enabled(context.Context, slog.Level) bool { return true }
func (h failOnAudit) Handle(_ context.Context, r slog.Record) error {
	if strings.Contains(r.Message, "작업 로그 기록 실패") {
		var detail []string
		r.Attrs(func(a slog.Attr) bool { detail = append(detail, a.String()); return true })
		h.t.Errorf("작업 로그가 기록되지 않았다: %v", detail)
	}
	return nil
}
func (h failOnAudit) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h failOnAudit) WithGroup(string) slog.Handler      { return h }

// looksLikeUUIDForTest mirrors the shape check the store uses.
func looksLikeUUIDForTest(s string) bool {
	return len(s) == 36 && s[8] == '-' && s[13] == '-' && s[18] == '-' && s[23] == '-'
}

// **PG 시크릿 키는 저장된 뒤 어떤 화면에도 다시 오지 않는다** (A-209, D19 A-205).
//
// 값을 다시 보내면 관리자 화면을 여는 것 자체가 자격증명 노출이고, "화면에서
// 가렸다" 는 "보낸 적 없다" 와 다르다.
func TestPaymentSecretIsNeverRedisplayed(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"settings.update": true},
		id: "", email: "op@example.com", password: "correct horse battery"}
	d, _ := fixture(t, caller)
	ctx := context.Background()

	rec := postAdmin(t, d.PaymentSettingsSave, "/admin/settings/payment", nil, url.Values{
		"pg.provider": {"toss"}, "pg.client_key": {"test_ck_public"},
		"pg.secret_key": {"test_sk_SUPERSECRET"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("저장 = HTTP %d (%q)", rec.Code, rec.Body.String())
	}

	// 실제로 저장됐다.
	kv, err := d.Content.Settings(ctx, "pg.secret_key", "pg.client_key", "pg.provider")
	if err != nil {
		t.Fatal(err)
	}
	if kv["pg.secret_key"] != "test_sk_SUPERSECRET" {
		t.Fatalf("시크릿이 저장되지 않았다: %q", kv["pg.secret_key"])
	}

	// 화면에는 오지 않는다.
	var shown map[string]string
	var saved map[string]bool
	d.Render = func(_ http.ResponseWriter, _ *http.Request, _ string, _ int, data any) {
		m, _ := data.(map[string]any)
		shown, _ = m["Settings"].(map[string]string)
		saved, _ = m["SecretSaved"].(map[string]bool)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/settings/payment", nil)
	d.PaymentSettingsForm(httptest.NewRecorder(), req)

	for k, v := range shown {
		if strings.Contains(v, "SUPERSECRET") {
			t.Errorf("%s 로 시크릿이 화면에 왔다", k)
		}
	}
	if _, ok := shown["pg.secret_key"]; ok {
		t.Error("시크릿 키가 표시 대상에 들어 있다")
	}
	if !saved["pg.secret_key"] {
		t.Error("저장 여부조차 알려주지 않았다 — 운영자가 설정했는지 알 수 없다")
	}
	// 공개 키는 보여도 된다. 결제창이 브라우저에서 쓴다.
	if shown["pg.client_key"] != "test_ck_public" {
		t.Errorf("클라이언트 키 %q — 공개 키는 보여야 한다", shown["pg.client_key"])
	}
}

// **빈 시크릿은 「그대로 두라」이지 「지우라」가 아니다.**
//
// 화면이 현재 값을 보여줄 수 없으므로 빈 칸이 정상 상태다. 지워 버리면
// 다른 항목을 고치러 들어온 운영자가 결제를 꺼뜨린다.
func TestEmptyPaymentSecretDoesNotEraseTheStoredOne(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"settings.update": true},
		id: "", email: "op@example.com"}
	d, _ := fixture(t, caller)
	ctx := context.Background()

	if err := d.Content.PutSettings(ctx, map[string]string{
		"pg.provider": "toss", "pg.secret_key": "test_sk_KEEP"}); err != nil {
		t.Fatal(err)
	}
	rec := postAdmin(t, d.PaymentSettingsSave, "/admin/settings/payment", nil, url.Values{
		"pg.provider": {"toss"}, "pg.client_key": {"test_ck_new"}, "pg.secret_key": {""}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("HTTP %d (%q)", rec.Code, rec.Body.String())
	}
	kv, err := d.Content.Settings(ctx, "pg.secret_key", "pg.client_key")
	if err != nil {
		t.Fatal(err)
	}
	if kv["pg.secret_key"] != "test_sk_KEEP" {
		t.Errorf("시크릿이 %q 로 바뀌었다 — 빈 칸이 지우기가 됐다", kv["pg.secret_key"])
	}
	if kv["pg.client_key"] != "test_ck_new" {
		t.Errorf("클라이언트 키가 저장되지 않았다: %q", kv["pg.client_key"])
	}
}

// 등록되지 않은 결제사는 422 다. 자유 문자열을 어댑터 선택에 쓰면 "결제가
// 조용히 안 되는" 사이트가 된다.
func TestPaymentProviderIsAnAllowList(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"settings.update": true},
		id: "", email: "op@example.com"}
	d, _ := fixture(t, caller)

	// 앞뒤 공백은 다듬는다 (운영자가 붙여넣는다) — 거부 대상이 아니다.
	for _, bad := range []string{"stripe", "TOSS", "../toss", "toss;drop"} {
		rec := postAdmin(t, d.PaymentSettingsSave, "/admin/settings/payment", nil,
			url.Values{"pg.provider": {bad}})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("결제사 %q = HTTP %d, want 422", bad, rec.Code)
		}
	}
	kv, err := d.Content.Settings(context.Background(), "pg.provider")
	if err != nil {
		t.Fatal(err)
	}
	if kv["pg.provider"] != "" {
		t.Errorf("거부된 값이 저장됐다: %q", kv["pg.provider"])
	}
}

// 자격증명 교체는 이후 모든 결제의 수취인을 바꾼다 — 재인증을 요구한다.
func TestPaymentSettingsRequireReauth(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"settings.update": true},
		reauth: true, id: "", email: "op@example.com", password: "correct horse battery"}
	d, _ := fixture(t, caller)

	rec := postAdmin(t, d.PaymentSettingsSave, "/admin/settings/payment", nil,
		url.Values{"pg.provider": {"toss"}, "pg.secret_key": {"test_sk_X"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("재인증 없이 = HTTP %d, want 403", rec.Code)
	}
	kv, err := d.Content.Settings(context.Background(), "pg.secret_key")
	if err != nil {
		t.Fatal(err)
	}
	if kv["pg.secret_key"] != "" {
		t.Error("재인증 없이 자격증명이 저장됐다")
	}

	// 비밀번호를 함께 보내면 통과한다.
	rec = postAdmin(t, d.PaymentSettingsSave, "/admin/settings/payment", nil, url.Values{
		"pg.provider": {"toss"}, "pg.secret_key": {"test_sk_X"},
		"password": {"correct horse battery"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("재인증 뒤 = HTTP %d (%q)", rec.Code, rec.Body.String())
	}
}

// **SMTP 설정은 superuser 만 바꾼다** (D15 5.1 R7).
//
// 비밀번호 재설정 토큰은 이 SMTP 로 나간다. `settings.update` 만 가진 운영자가
// 호스트를 자기 서버로 돌리고 superuser 이메일로 재설정을 요청하면 토큰을 받아
// superuser 가 된다 — R6 이 막은 「계정 조작은 superuser 만」을 메일 경로로
// 우회하는 길이었다. 재인증·작업 로그도 없어서 되돌려 놓으면 흔적이 남지 않았다.
func TestMailSettingsAreSuperuserOnlyReauthedAndLogged(t *testing.T) {
	op := &fakeCaller{perms: map[string]bool{"settings.update": true}, email: "op@example.com"}
	d, pool := fixture(t, op)
	ctx := context.Background()

	rec := postAdmin(t, d.MailSettingsSave, "/admin/settings/mail", nil, url.Values{
		"mail.smtp_host": {"attacker.example"}, "mail.tls_mode": {"none"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("settings.update 만 있는 운영자의 SMTP 저장 = HTTP %d, want 403", rec.Code)
	}
	if kv, _ := d.Content.Settings(ctx, "mail.smtp_host"); kv["mail.smtp_host"] != "" {
		t.Fatalf("운영자가 SMTP 호스트를 바꿨다: %q", kv["mail.smtp_host"])
	}

	// superuser 도 재인증 창이 지났으면 비밀번호를 다시 넣어야 한다.
	su := &fakeCaller{superuser: true, reauth: true, email: "su@example.com",
		password: "correct horse battery"}
	d.Caller = func(*http.Request) Caller { return su }
	rec = postAdmin(t, d.MailSettingsSave, "/admin/settings/mail", nil, url.Values{
		"mail.smtp_host": {"smtp.example.com"}, "mail.tls_mode": {"starttls"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("재인증 없이 = HTTP %d, want 403", rec.Code)
	}
	rec = postAdmin(t, d.MailSettingsSave, "/admin/settings/mail", nil, url.Values{
		"mail.smtp_host": {"smtp.example.com"}, "mail.tls_mode": {"starttls"},
		"password": {"correct horse battery"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("재인증 뒤 = HTTP %d (%q)", rec.Code, rec.Body.String())
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operation_logs
		WHERE action = 'settings.update' AND target_id = 'mail'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("SMTP 변경 작업 로그 %d건, want 1", n)
	}
}

// **SMTP 설정은 형식을 검사한다** (D19 A-205). 검사하지 않으면 포트 「abc」·발신
// 「not-an-email」이 저장되고, 그 뒤의 재설정·인증 메일은 화면 아무 데도 드러나지
// 않은 채 전부 실패한다. 비운 설정은 받는다 — A-205 「설정이 비어 있는 상태 저장:
// 거부하지 않는다」.
func TestMailSettingsRefuseMalformedValues(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{superuser: true})
	ctx := context.Background()
	ok := url.Values{"mail.smtp_host": {"smtp.example.com"}, "mail.smtp_port": {"587"},
		"mail.tls_mode": {"starttls"}, "mail.from_address": {"noreply@example.com"}}
	if rec := post(d.MailSettingsSave, "/admin/settings/mail", ok); rec.Code != http.StatusSeeOther {
		t.Fatalf("정상 값 = HTTP %d (%q)", rec.Code, rec.Body.String())
	}

	bad := map[string][2]string{
		"포트 글자":    {"mail.smtp_port", "abc"},
		"포트 0":     {"mail.smtp_port", "0"},
		"포트 범위 밖":  {"mail.smtp_port", "70000"},
		"발신 주소 형식": {"mail.from_address", "not-an-email"},
		"호스트에 공백":  {"mail.smtp_host", "smtp example.com"},
		"호스트에 스킴":  {"mail.smtp_host", "smtp://smtp.example.com"},
		"호스트에 포트":  {"mail.smtp_host", "smtp.example.com:587"},
	}
	for name, kv := range bad {
		t.Run(name, func(t *testing.T) {
			form := url.Values{}
			for k, v := range ok {
				form[k] = v
			}
			form.Set(kv[0], kv[1])
			if rec := post(d.MailSettingsSave, "/admin/settings/mail", form); rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("%s=%q → HTTP %d, want 422", kv[0], kv[1], rec.Code)
			}
			if got, _ := d.Content.Settings(ctx, kv[0]); got[kv[0]] != ok.Get(kv[0]) {
				t.Errorf("거부했는데 %s 가 %q 로 바뀌었다", kv[0], got[kv[0]])
			}
		})
	}

	// 발신 주소는 소문자로 저장한다 (D19 A-205, D30 3절).
	form := url.Values{}
	for k, v := range ok {
		form[k] = v
	}
	form.Set("mail.from_address", "NoReply@Example.COM")
	if rec := post(d.MailSettingsSave, "/admin/settings/mail", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("대문자 발신 주소 = HTTP %d", rec.Code)
	}
	if got, _ := d.Content.Settings(ctx, "mail.from_address"); got["mail.from_address"] != "noreply@example.com" {
		t.Errorf("발신 주소 = %q, want 소문자", got["mail.from_address"])
	}

	// 비운 설정은 받는다 — 메일을 끄는 방법이다. IP 호스트, 밑줄이 든 Docker
	// compose 서비스 이름(리졸버가 해석한다)도 정상이다.
	for _, host := range []string{"", "10.0.0.25", "mail_relay"} {
		empty := url.Values{"mail.smtp_host": {host}, "mail.smtp_port": {""},
			"mail.tls_mode": {"none"}, "mail.from_address": {""}}
		if rec := post(d.MailSettingsSave, "/admin/settings/mail", empty); rec.Code != http.StatusSeeOther {
			t.Errorf("호스트 %q·빈 포트·빈 발신 = HTTP %d, want 303", host, rec.Code)
		}
	}
}

// **메일 설정 시험 발송** (D13·D19 A-205, D15 4.3-2).
//
// 없던 동안 운영자는 저장한 SMTP 가 맞는지 알 방법이 없었다 — 재설정·인증
// 메일의 실패는 공개 화면이 말할 수 없다 (FR-207). 받는 주소는 **로그인한
// 관리자 본인**뿐이다: 주소를 받으면 우리 SMTP 가 임의 주소로 쏘는 중계가 된다.
func TestMailTestSendsOnlyToTheAdminItself(t *testing.T) {
	su := &fakeCaller{superuser: true, email: "su@example.com"}
	d, pool := fixture(t, su)
	ctx := context.Background()
	var sentTo []string
	var fail error
	d.SendMailTest = func(_ context.Context, to string) error {
		sentTo = append(sentTo, to)
		return fail
	}
	d.Limiter = auth.NewLimiter()
	d.Limits = auth.DefaultLimits()
	var shown map[string]any
	d.Render = func(w http.ResponseWriter, _ *http.Request, _ string, code int, data any) {
		w.WriteHeader(code)
		shown, _ = data.(map[string]any)
	}

	// 폼이 다른 주소를 실어 와도 무시한다.
	rec := post(d.MailTest, "/admin/settings/mail/test", url.Values{"to": {"victim@example.com"}})
	if rec.Code != http.StatusOK || len(sentTo) != 1 || sentTo[0] != "su@example.com" {
		t.Fatalf("HTTP %d, 받는 주소 %v — want 200·관리자 본인 한 번", rec.Code, sentTo)
	}
	if n, _ := shown["Notice"].(string); !strings.Contains(n, "su@example.com") {
		t.Errorf("성공 안내에 받는 주소가 없다: %q", n)
	}

	// 실패는 SMTP 원인을 보이고 작업 로그에 남는다 (D19 A-205 「테스트 발송 실패: 남김」).
	fail = errors.New("535 Authentication Credentials Invalid")
	post(d.MailTest, "/admin/settings/mail/test", nil)
	if e, _ := shown["Error"].(string); !strings.Contains(e, "발송에 실패했습니다") || !strings.Contains(e, "535") {
		t.Errorf("실패 안내 = %q, want 「발송에 실패했습니다: …535…」", e)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operation_logs
		WHERE action = 'mail.failed' AND actor_email = 'su@example.com'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("시험 발송 실패 작업 로그 %d건, want 1", n)
	}

	// 계정당 5회/시간 (D15 4.3-2) — 남의 SMTP 를 우리가 대신 두드리는 요청이다.
	fail = nil
	for i := 0; i < 10 && len(sentTo) < 5; i++ { // 상한: 한도가 일찍 막아도 멈춘다
		post(d.MailTest, "/admin/settings/mail/test", nil)
	}
	if rec := post(d.MailTest, "/admin/settings/mail/test", nil); rec.Code != http.StatusTooManyRequests || len(sentTo) != 5 {
		t.Errorf("여섯 번째 = HTTP %d·발송 %d회, want 429·5회", rec.Code, len(sentTo))
	}

	// 재인증 창이 지난 superuser 는 비밀번호를 다시 넣어야 보낸다 (저장과 같은 문).
	stale := &fakeCaller{superuser: true, reauth: true, email: "late@example.com", password: "correct horse battery"}
	d.Caller = func(*http.Request) Caller { return stale }
	before := len(sentTo)
	if rec := post(d.MailTest, "/admin/settings/mail/test", nil); rec.Code != http.StatusForbidden || len(sentTo) != before {
		t.Errorf("재인증 없이 = HTTP %d·발송 %d회, want 403·0회", rec.Code, len(sentTo)-before)
	}
	if rec := post(d.MailTest, "/admin/settings/mail/test", url.Values{"password": {"correct horse battery"}}); rec.Code != http.StatusOK || len(sentTo) != before+1 {
		t.Errorf("재인증 뒤 = HTTP %d·발송 %d회, want 200·1회 — 위 단언이 「늘 거부」였다", rec.Code, len(sentTo)-before)
	}

	// 저장과 같은 문: superuser 만 (D15 5.1 R7).
	op := &fakeCaller{perms: map[string]bool{"settings.update": true}, email: "op@example.com"}
	d.Caller = func(*http.Request) Caller { return op }
	before = len(sentTo)
	if rec := post(d.MailTest, "/admin/settings/mail/test", nil); rec.Code != http.StatusForbidden || len(sentTo) != before {
		t.Errorf("settings.update 만 있는 운영자 = HTTP %d·발송 %d회, want 403·0회", rec.Code, len(sentTo)-before)
	}
}

// **작업 로그에 키 값이 남지 않는다** (D15 7절, D22 4절). 로그는 지워지지
// 않으므로 (append-only) 한 번 새면 영구히 남는다.
func TestPaymentSettingsLogNeverCarriesTheKey(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"settings.update": true},
		id: "", email: "op@example.com"}
	d, pool := fixture(t, caller)

	postAdmin(t, d.PaymentSettingsSave, "/admin/settings/payment", nil, url.Values{
		"pg.provider": {"toss"}, "pg.client_key": {"test_ck_public"},
		"pg.secret_key": {"test_sk_SUPERSECRET"}})

	var summary, target string
	if err := pool.QueryRow(context.Background(),
		`SELECT summary, COALESCE(target_id,'') FROM operation_logs
		 ORDER BY created_at DESC LIMIT 1`).Scan(&summary, &target); err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"SUPERSECRET", "test_sk"} {
		if strings.Contains(summary+target, leak) {
			t.Errorf("작업 로그에 키가 남았다: %q / %q", summary, target)
		}
	}
	if !strings.Contains(summary, "결제 설정") {
		t.Errorf("변경 사실이 남지 않았다: %q", summary)
	}
}

// **길이 상한을 서버가 건다** (D19 A-209: 200자).
//
// `settings.value` 는 무제한 `text` 라, HTML 의 `maxlength` 만으로는 스크립트
// POST 를 막지 못한다 — 100KB 짜리 키가 저장되고 이후 매 GET 마다 폼 속성으로
// 다시 나간다.
func TestPaymentKeysHaveAServerSideLengthLimit(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"settings.update": true},
		id: "", email: "op@example.com"}
	d, _ := fixture(t, caller)
	ctx := context.Background()

	long := strings.Repeat("k", 201)
	for _, field := range []string{"pg.client_key", "pg.secret_key"} {
		rec := postAdmin(t, d.PaymentSettingsSave, "/admin/settings/payment", nil,
			url.Values{"pg.provider": {"toss"}, field: {long}})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s 201자 = HTTP %d, want 422", field, rec.Code)
		}
	}
	kv, err := d.Content.Settings(ctx, "pg.client_key", "pg.secret_key")
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range kv {
		if len(v) > 200 {
			t.Errorf("%s 가 %d자로 저장됐다", k, len(v))
		}
	}

	// 200자는 통과한다 — 위 단언이 "긴 값은 전부 막힌다" 가 아니라는 것.
	ok := strings.Repeat("k", 200)
	rec := postAdmin(t, d.PaymentSettingsSave, "/admin/settings/payment", nil,
		url.Values{"pg.provider": {"toss"}, "pg.client_key": {ok}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("200자 = HTTP %d, want 303", rec.Code)
	}
}

// **A-206 은 콜백 URL 을 받지 않는다** (D19 A-206 받지 않는 필드).
//
// 입력받으면 인가 코드를 공격자 서버로 돌리는 경로가 된다. 무시하지 않고
// 거부한다 — 무시는 조용해서 공격 시도를 놓친다.
func TestSocialSettingsRefusesCallbackAndEndpointFields(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"settings.update": true},
		id: "", email: "op@example.com"}
	d, _ := fixture(t, caller)

	for _, field := range []string{"callback_url", "callback", "redirect_uri",
		"authorize_url", "token_url", "scope"} {
		rec := postAdmin(t, d.SocialSettingsSave, "/admin/settings/social", nil,
			url.Values{"provider": {"google"}, "client_id": {"id"},
				"client_secret": {"secret"}, field: {"https://evil.example.com/cb"}})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s 필드 = HTTP %d, want 422", field, rec.Code)
		}
	}
	kv, err := d.Content.Settings(context.Background(),
		auth.SocialSettingKey("google", "client_id"))
	if err != nil {
		t.Fatal(err)
	}
	if kv[auth.SocialSettingKey("google", "client_id")] != "" {
		t.Error("거부된 요청이 저장됐다")
	}
}

// **자격증명 없이 활성화할 수 없다** (D19 A-206 거부 조건).
// 켜면 로그인 버튼이 즉시 깨지고, 사용자는 로그인이 고장 난 것으로 읽는다.
func TestSocialCannotBeEnabledWithoutCredentials(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"settings.update": true},
		id: "", email: "op@example.com"}
	d, _ := fixture(t, caller)
	ctx := context.Background()

	rec := postAdmin(t, d.SocialSettingsSave, "/admin/settings/social", nil,
		url.Values{"provider": {"google"}, "enabled": {"on"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("키 없이 활성화 = HTTP %d, want 422", rec.Code)
	}

	// 키를 넣으면 켜진다.
	rec = postAdmin(t, d.SocialSettingsSave, "/admin/settings/social", nil,
		url.Values{"provider": {"google"}, "client_id": {"cid"},
			"client_secret": {"csecret"}, "enabled": {"on"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("키와 함께 = HTTP %d (%q)", rec.Code, rec.Body.String())
	}
	kv, err := d.Content.Settings(ctx, auth.SocialSettingKey("google", "enabled"))
	if err != nil {
		t.Fatal(err)
	}
	if kv[auth.SocialSettingKey("google", "enabled")] == "" {
		t.Error("켜지지 않았다")
	}

	// **이미 저장된 시크릿이 있으면 빈 칸으로도 켤 수 있다.** 빈 칸은
	// 「그대로 두라」이므로, 다른 항목만 고치러 온 관리자가 막히면 안 된다.
	rec = postAdmin(t, d.SocialSettingsSave, "/admin/settings/social", nil,
		url.Values{"provider": {"google"}, "client_id": {"cid"}, "enabled": {"on"}})
	if rec.Code != http.StatusSeeOther {
		t.Errorf("저장된 시크릿이 있는데 = HTTP %d", rec.Code)
	}
}

// **시크릿은 저장 후 화면에 다시 오지 않는다** (D19 A-206).
func TestSocialSecretIsNeverRedisplayed(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"settings.update": true},
		id: "", email: "op@example.com"}
	d, _ := fixture(t, caller)

	postAdmin(t, d.SocialSettingsSave, "/admin/settings/social", nil,
		url.Values{"provider": {"naver"}, "client_id": {"cid"},
			"client_secret": {"NAVER_SUPERSECRET"}})

	var configs []auth.SocialConfig
	var callbacks map[string]string
	d.Render = func(_ http.ResponseWriter, _ *http.Request, _ string, _ int, data any) {
		m, _ := data.(map[string]any)
		configs, _ = m["Providers"].([]auth.SocialConfig)
		callbacks, _ = m["Callbacks"].(map[string]string)
	}
	req := httptest.NewRequest(http.MethodGet, "https://shop.example.com/admin/settings/social", nil)
	req.Host = "shop.example.com"
	d.SocialSettingsForm(httptest.NewRecorder(), req)

	found := false
	for _, c := range configs {
		if c.Provider != "naver" {
			continue
		}
		found = true
		if !c.HasSecret {
			t.Error("저장 여부조차 알려주지 않았다")
		}
		if strings.Contains(c.ClientID, "SUPERSECRET") {
			t.Error("시크릿이 클라이언트 ID 자리로 나왔다")
		}
	}
	if !found {
		t.Fatal("naver 설정이 화면에 없다")
	}
	// **콜백은 서버가 계산해 보여준다.** 입력란이 아니다.
	if callbacks["naver"] != "https://shop.example.com/auth/naver/callback" {
		t.Errorf("콜백 %q — 서버가 계산한 값이 아니다", callbacks["naver"])
	}
}

// 목록 밖 프로바이더는 422 다. 목록은 코드가 정한다 (D15 P1).
func TestSocialProviderIsAnAllowList(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"settings.update": true},
		id: "", email: "op@example.com"}
	d, _ := fixture(t, caller)

	for _, bad := range []string{"", "facebook", "GOOGLE", "../google"} {
		rec := postAdmin(t, d.SocialSettingsSave, "/admin/settings/social", nil,
			url.Values{"provider": {bad}, "client_id": {"id"}, "client_secret": {"s"}})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("프로바이더 %q = HTTP %d, want 422", bad, rec.Code)
		}
	}
}

// mkPage 는 수정 대상 한 건을 만든다. 만드는 데 권한 검사를 거치지 않도록
// 스토어를 직접 쓴다 — 여기서 보려는 것은 수정 경로의 권한이지 생성이 아니다.
func mkPage(t *testing.T, d *Deps) string {
	t.Helper()
	id, err := d.Content.CreatePage(context.Background(), content.Page{
		Slug: "seed", Title: "씨앗", Body: "본문",
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// **「다음」은 다음 쪽이 실제로 있을 때만 낸다** (A-401).
//
// 앞 판은 조건 없이 그렸다 — 사용자가 한 명뿐인 사이트도 무한히 다음 쪽을
// 제시했고, 링크를 따라가는 검사가 35쪽까지 걸어가 요청 제한에 걸렸다.
// 행 수만 보는 검사(TestUserListIsBounded)는 이것을 잡지 못한다: 반환 행 수는
// `HasNext` 를 늘 true 로 되돌려도 똑같다.
func TestUserListOffersNextOnlyWhenThereIsOne(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"user.view": true}})
	ctx := context.Background()

	var got []bool
	var rows []int
	d.Render = func(_ http.ResponseWriter, _ *http.Request, _ string, _ int, data any) {
		m := data.(map[string]any)
		got = append(got, m["HasNext"].(bool))
		rows = append(rows, len(m["Users"].([]auth.UserRow)))
	}

	// 한 쪽에 딱 맞게 채운다 — 경계다. 다음 쪽은 비어 있으므로 「다음」이
	// 없어야 한다. `>=` 로 잘못 쓰면 여기서만 틀린다.
	for i := range userPageSize {
		if _, err := d.Auth.CreateUser(ctx,
			fmt.Sprintf("n%02d@example.com", i), "h", "쪽"); err != nil {
			t.Fatal(err)
		}
	}
	d.UserList(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/admin/users", nil))

	// 한 명 더 넣으면 다음 쪽이 생긴다.
	if _, err := d.Auth.CreateUser(ctx, "overflow@example.com", "h", "넘침"); err != nil {
		t.Fatal(err)
	}
	d.UserList(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/admin/users", nil))

	// 마지막 쪽에서는 다시 없다.
	d.UserList(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/admin/users?page=1", nil))

	if len(got) != 3 {
		t.Fatalf("화면이 %d 번 그려졌다 — 검사가 헛돌았다", len(got))
	}
	// 행 수도 함께 본다: 한 행 더 읽어 판정하므로, 그 여분이 화면으로 새면
	// 한 쪽에 51행이 나온다.
	for i, want := range []int{userPageSize, userPageSize, 1} {
		if rows[i] != want {
			t.Errorf("%d번째: %d행, want %d행 — 판정용으로 더 읽은 행이 화면에 샜다",
				i+1, rows[i], want)
		}
	}
	for i, want := range []bool{false, true, false} {
		if got[i] != want {
			t.Errorf("%d번째: HasNext=%v, want %v", i+1, got[i], want)
		}
	}
}

type verifyingGateway struct {
	reconcileGateway
	err   error
	calls int
}

func (g *verifyingGateway) VerifyCredentials(context.Context) error { g.calls++; return g.err }

// A-209 「연결 확인」: 저장된 키가 맞는지 관리자 화면에서 안다. 값은 드러나지
// 않고, 결과는 작업 로그에 남는다.
func TestPaymentVerifyReportsWithoutRevealingTheKey(t *testing.T) {
	d, pool := fixture(t, &fakeCaller{perms: map[string]bool{"settings.update": true}, id: "", email: "op@example.com"})
	ctx := context.Background()
	if err := d.Content.PutSettings(ctx, map[string]string{"pg.provider": "toss", "pg.secret_key": "test_sk_live_value"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		err  error
		code int
		text string
	}{
		{"성공", nil, http.StatusOK, "받아들였습니다"},
		{"키 거부", commerce.ErrGatewayCredentials, http.StatusUnprocessableEntity, "거부했습니다"},
		{"불명", commerce.ErrPaymentUnknown, http.StatusBadGateway, "닿지 못했거나"},
	} {
		gw := &verifyingGateway{err: tc.err}
		d.Gateway = func() commerce.Gateway { return gw }
		rec := postAdmin(t, d.PaymentSettingsSave, "/admin/settings/payment", nil,
			url.Values{"action": {"verify"}})
		if rec.Code != tc.code {
			t.Errorf("[%s] HTTP %d, want %d: %s", tc.name, rec.Code, tc.code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), tc.text) {
			t.Errorf("[%s] 본문에 %q 가 없다: %s", tc.name, tc.text, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "test_sk_live_value") {
			t.Errorf("[%s] 시크릿 값이 화면에 실렸다", tc.name)
		}
		if gw.calls != 1 {
			t.Errorf("[%s] 결제사에 %d번 물었다", tc.name, gw.calls)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operation_logs WHERE summary LIKE '결제 설정 연결 확인%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("연결 확인이 작업 로그에 %d건, want 3", n)
	}
	// 결제사가 없으면 묻지 않는다.
	d.Gateway = func() commerce.Gateway { return nil }
	rec := postAdmin(t, d.PaymentSettingsSave, "/admin/settings/payment", nil, url.Values{"action": {"verify"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("결제사 없음 → HTTP %d, want 422", rec.Code)
	}
}

// **비활성 계정을 다시 살린다** (D19 A-402, FR-704). 비활성화만 있고 되돌릴 길이 없어,
// 탈퇴 안내의 「다시 이용하시려면 관리자에게 문의」가 거짓이었다 — 관리자도 살릴 수
// 없었다. 두 동작 모두 끝나면 상세 화면으로 돌아와 바뀐 상태를 보인다.
func TestDeactivatedAccountCanBeReactivated(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"user.update": true}, id: "me"})
	ctx := context.Background()
	target, err := d.Auth.CreateUser(ctx, "back@example.com", "h", "돌아온 회원")
	if err != nil {
		t.Fatal(err)
	}
	run := func(h http.HandlerFunc, action string) {
		req := httptest.NewRequest(http.MethodPost, "/admin/users/"+target+"/"+action, nil)
		req.SetPathValue("id", target)
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/users/"+target {
			t.Fatalf("%s = HTTP %d → %q, want 303 → 상세", action, rec.Code, rec.Header().Get("Location"))
		}
	}
	active := func() bool {
		u, err := d.Auth.FindUserByID(ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		return u.IsActive
	}
	run(d.UserDeactivate, "deactivate")
	if active() {
		t.Fatal("비활성화가 반영되지 않았다")
	}
	run(d.UserActivate, "activate")
	if !active() {
		t.Error("다시 활성화되지 않았다")
	}
}

// R6 는 활성화에도 같다 — 비-superuser 가 비활성 superuser 보유자를 되살리지 못한다.
func TestNonSuperuserCannotReactivateASuperuserHolder(t *testing.T) {
	d, pool := fixture(t, &fakeCaller{perms: map[string]bool{"user.update": true}, id: "me"})
	ctx := context.Background()
	target, err := d.Auth.CreateUser(ctx, "admin2@example.com", "h", "관리자")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE key='admin'`, target); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET is_active = false WHERE id = $1`, target); err != nil {
		t.Fatal(err)
	}
	if rec := postID(d.UserActivate, target, nil); rec.Code != http.StatusForbidden {
		t.Errorf("비-superuser 가 superuser 보유자를 되살렸다: HTTP %d", rec.Code)
	}
	if u, _ := d.Auth.FindUserByID(ctx, target); u.IsActive {
		t.Error("거부됐는데 계정이 활성화됐다")
	}
}

// **계정 조작은 작업 로그에 남는다** (D15 7절, D19 A-402 「생성·비활성·삭제·재설정 성공 —
// 남김」). 다섯 동작 모두 아무 기록도 남기지 않았다.
func TestAccountOperationsAreLogged(t *testing.T) {
	d, pool := fixture(t, &fakeCaller{perms: map[string]bool{"user.create": true, "user.update": true,
		"user.delete": true, "user.reset_password": true}, id: "me", email: "op@example.com", password: "pw"})
	ctx := context.Background()
	if rec := post(d.UserCreate, "/admin/users/new", url.Values{"email": {"new@example.com"},
		"password": {"correct horse battery"}, "display_name": {"새 회원"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("생성 = HTTP %d (%s)", rec.Code, rec.Body.String())
	}
	u, _, err := d.Auth.FindActiveUserByEmail(ctx, "new@example.com")
	if err != nil {
		t.Fatal(err)
	}
	mailReady(t, d)
	for _, h := range []http.HandlerFunc{d.UserDeactivate, d.UserActivate, d.UserResetPassword, d.UserDelete} {
		if rec := postID(h, u.ID, url.Values{"password": {"pw"}}); rec.Code != http.StatusSeeOther {
			t.Fatalf("계정 조작 = HTTP %d (%s)", rec.Code, rec.Body.String())
		}
	}
	rows, err := pool.Query(ctx, `SELECT summary FROM operation_logs WHERE target_type = 'user' ORDER BY created_at, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		got = append(got, s)
	}
	want := []string{"사용자 생성: new@example.com", "사용자 비활성화", "사용자 활성화", "재설정 강제 (세션 종료·로그인 수단 회수·소셜 연결 0개 해제·재설정 메일 발송)", "사용자 삭제"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("작업 로그 = %v, want %v", got, want)
	}
}

// **사용자 목록에서 이메일·이름으로 찾는다** (D19 A-401). 50명씩 이전·다음뿐이라 고객이
// 전화로 이메일을 불러 줘도 찾을 길이 넘기기뿐이었다. % · _ · \ 는 글자 그대로 찾는다.
func TestUserListSearchesByEmailOrName(t *testing.T) {
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"user.view": true}})
	ctx := context.Background()
	for _, u := range [][2]string{{"kim@example.com", "김온돌"}, {"lee@example.com", "이바닥"}, {"under_score@example.com", "밑줄"}, {"back@example.com", `역\슬래시`}} {
		if _, err := d.Auth.CreateUser(ctx, u[0], "h", u[1]); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	d.Render = func(w http.ResponseWriter, _ *http.Request, _ string, code int, v any) {
		got = nil
		for _, u := range v.(map[string]any)["Users"].([]auth.UserRow) {
			got = append(got, u.Email)
		}
		w.WriteHeader(code)
	}
	for q, want := range map[string]string{"lee": "lee@example.com", "김온": "kim@example.com",
		"_": "under_score@example.com", "%": "", `\`: "back@example.com"} {
		d.UserList(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/admin/users?q="+url.QueryEscape(q), nil))
		if strings.Join(got, ",") != want {
			t.Errorf("검색 %q = %v, want %q", q, got, want)
		}
	}
}

// **관리자 화면의 거절은 관리자 레이아웃 안에서 말한다.** http.Error 는 레이아웃 없는 흰
// 화면 한 줄이라(「자기 계정에는 이 작업을 할 수 없습니다.」 등), 운영자는 어디서 무엇이
// 거절됐는지 보고 돌아갈 길이 없었다.
func TestAdminRefusalsRenderInsideTheLayout(t *testing.T) {
	d, _ := fixture(t, nil)
	ctx := context.Background()
	me, err := d.Auth.CreateUser(ctx, "me@example.com", "h", "나")
	if err != nil {
		t.Fatal(err)
	}
	d.Caller = func(*http.Request) Caller {
		return &fakeCaller{perms: map[string]bool{"user.update": true}, id: me}
	}
	rec := postID(d.UserDeactivate, me, nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "admin/refusal.html") ||
		!strings.Contains(rec.Body.String(), "자기 계정에는 이 작업을 할 수 없습니다.") {
		t.Errorf("자기 계정 비활성화 = HTTP %d %q — 관리자 거절 화면이어야 한다", rec.Code, rec.Body.String())
	}
	// 권한이 없을 때도 같다.
	d.Caller = func(*http.Request) Caller { return &fakeCaller{perms: map[string]bool{}, id: me} }
	if rec := postID(d.UserDeactivate, me, nil); !strings.Contains(rec.Body.String(), "admin/refusal.html") {
		t.Errorf("권한 없음 = %q — 관리자 거절 화면이어야 한다", rec.Body.String())
	}
}
