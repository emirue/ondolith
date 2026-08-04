package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/content"
	"github.com/emirue/ondolith/internal/migrations"
)

const dsnEnv = "ONDOLITH_TEST_DSN"

type fakeCaller struct {
	perms     map[string]bool
	id        string
	superuser bool
	reauth    bool
}

func (f fakeCaller) Can(p string) bool { return f.superuser || f.perms[p] }
func (f fakeCaller) UserID() string    { return f.id }
func (f fakeCaller) IsSuperuser() bool { return f.superuser }
func (f fakeCaller) NeedsReauth() bool { return f.reauth }

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
	d := &Deps{
		Content: content.NewStore(pool),
		Auth:    auth.NewStore(pool),
		Caller:  func(*http.Request) Caller { return c },
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
	d, _ := fixture(t, fakeCaller{perms: map[string]bool{"admin.access": true}})

	cases := map[string]http.HandlerFunc{
		"설정 조회":   d.SettingsForm,
		"설정 저장":   d.SettingsSave,
		"메일 조회":   d.MailSettingsForm,
		"페이지 목록":  d.PageList,
		"페이지 저장":  d.PageSave,
		"페이지 발행":  d.PagePublish,
		"페이지 삭제":  d.PageDelete,
		"사용자 비활성": d.UserDeactivate,
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
	d, _ := fixture(t, fakeCaller{perms: map[string]bool{"settings.update": true}})

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

// An empty secret box means "leave it alone": the form cannot show the current
// value, so empty is the normal state of a screen opened to change something
// else. Treating it as "erase" silently breaks mail on the next save.
func TestEmptySecretDoesNotEraseTheStoredOne(t *testing.T) {
	d, pool := fixture(t, fakeCaller{perms: map[string]bool{"settings.update": true}})
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
	d, _ := fixture(t, fakeCaller{perms: map[string]bool{"settings.update": true}})

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

// page.update must not be able to publish, or the separation of the two
// permissions is decorative.
func TestPublishNeedsItsOwnPermission(t *testing.T) {
	d, _ := fixture(t, fakeCaller{perms: map[string]bool{
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
	if rec := post(d.PageSave, "/admin/pages", url.Values{
		"slug": {"about"}, "title": {"회사"}, "body": {"본문"}}); rec.Code != http.StatusSeeOther {
		t.Errorf("page.update 로 저장이 안 된다: HTTP %d (%s)", rec.Code, rec.Body.String())
	}
}

// D15 5.3-1: destructive, so the password is re-confirmed. An open session is
// not the same as the operator being present.
func TestDeactivateRequiresReauth(t *testing.T) {
	d, _ := fixture(t, fakeCaller{perms: map[string]bool{"user.update": true}, reauth: true})
	rec := post(d.UserDeactivate, "/admin/users/x/deactivate", nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("재인증 없이 비활성화가 진행됐다: HTTP %d", rec.Code)
	}
}

// R6: only a superuser may switch off a superuser holder. Without it, revoking
// the role is blocked while turning off its holder is not — same end, other road.
func TestNonSuperuserCannotDeactivateASuperuserHolder(t *testing.T) {
	d, pool := fixture(t, fakeCaller{perms: map[string]bool{"user.update": true}, id: "me"})
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
	d, _ := fixture(t, fakeCaller{perms: map[string]bool{"settings.view": true}})
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
