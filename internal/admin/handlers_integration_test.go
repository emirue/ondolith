package admin

import (
	"bytes"
	"context"
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
	d := &Deps{
		Content:  store,
		Auth:     auth.NewStore(pool),
		Commerce: commerce.NewStore(pool),
		// 운영과 같이 채운다. 비워 두면 첨부를 지우는 경로가 nil 로 터지는데,
		// 그 사실이 테스트에서는 보이지 않는다.
		Attachments: store.AttachmentsIn(t.TempDir()),
		// **작업 로그도 운영과 같이 붙인다.** 비워 두면 d.log 가 조용히
		// 아무것도 안 하고, "로그에 남는다" 를 확인하는 검사가 전부 무의미해진다
		// (D15 7절이 요구하는 것이 바로 그 기록이다).
		OpLog: store.OpLog(),
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
	d, _ := fixture(t, &fakeCaller{perms: map[string]bool{"settings.update": true}})

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
	d, pool := fixture(t, &fakeCaller{perms: map[string]bool{"settings.update": true}})
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
	if rec := post(d.PageSave, "/admin/pages", url.Values{
		"slug": {"about"}, "title": {"회사"}, "body": {"본문"}}); rec.Code != http.StatusSeeOther {
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
func TestForcedResetEndsSessionsAndNeverSetsAPassword(t *testing.T) {
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
	var sent []string
	d.SendReset = func(email, token string) { sent = append(sent, email+"|"+token) }

	before, err := d.Auth.FindUserByID(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if rec := postID(d.UserResetPassword, target, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("재설정 강제 실패: HTTP %d (%s)", rec.Code, rec.Body.String())
	}

	// The hash is untouched: forcing a reset is not setting a password.
	var hash string
	if err := pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, target).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != "원래해시" {
		t.Errorf("관리자가 타인의 비밀번호를 바꿨다: %q", hash)
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

	// 중복은 DB UNIQUE 가 막는다.
	if rec := post(d.UserCreate, "/admin/users", url.Values{
		"email": {"new@example.com"}, "password": {"correct horse battery"}, "display_name": {"중복"},
	}); rec.Code != http.StatusConflict {
		t.Errorf("중복 이메일이 HTTP %d 로 통과했다", rec.Code)
	}
	// 짧은 비밀번호·빈 이름은 거부.
	for name, form := range map[string]url.Values{
		"짧은 비밀번호": {"email": {"a@b.com"}, "password": {"짧다"}, "display_name": {"가"}},
		"빈 이름":    {"email": {"c@d.com"}, "password": {"correct horse battery"}, "display_name": {"  "}},
	} {
		if rec := post(d.UserCreate, "/admin/users", form); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s가 HTTP %d 로 통과했다", name, rec.Code)
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
