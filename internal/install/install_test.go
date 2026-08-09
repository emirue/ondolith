package install

import (
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func validForm() *form {
	return &form{
		DBHost:     "127.0.0.1",
		DBPort:     "5432",
		DBUser:     "ondolith",
		DBPassword: "pw",
		DBName:     "ondolith",
		DBSSLMode:  "disable",
		SiteName:   "테스트 사이트",
		AdminEmail: "admin@example.com",
		AdminPW:    "correct-horse-battery",
		AdminPW2:   "correct-horse-battery",
	}
}

func TestValidateAcceptsGoodInput(t *testing.T) {
	if err := validForm().validate(); err != nil {
		t.Fatalf("valid form rejected: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	tests := map[string]func(*form){
		"빈 호스트":      func(f *form) { f.DBHost = "" },
		"빈 사용자":      func(f *form) { f.DBUser = "" },
		"빈 DB 이름":    func(f *form) { f.DBName = "" },
		"빈 사이트 이름":   func(f *form) { f.SiteName = "" },
		"숫자가 아닌 포트":  func(f *form) { f.DBPort = "postgres" },
		"포트 0":       func(f *form) { f.DBPort = "0" },
		"포트 범위 초과":   func(f *form) { f.DBPort = "65536" },
		"알 수 없는 SSL": func(f *form) { f.DBSSLMode = "yes-please" },
		"잘못된 이메일":    func(f *form) { f.AdminEmail = "admin@" },
		"짧은 비밀번호":    func(f *form) { f.AdminPW, f.AdminPW2 = "short", "short" },
		"비밀번호 불일치":   func(f *form) { f.AdminPW2 = "something-else" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			f := validForm()
			mutate(f)
			if err := f.validate(); err == nil {
				t.Fatal("입력이 거부되어야 하는데 통과했다")
			}
		})
	}
}

// A password with ':', '@' and '/' is the case that string concatenation gets
// wrong: it silently produces a DSN pointing at the wrong host.
func TestDSNEscapesAwkwardPassword(t *testing.T) {
	f := validForm()
	f.DBPassword = "p:a@ss/word?x"

	u, err := url.Parse(f.dsn())
	if err != nil {
		t.Fatalf("dsn is not a valid URL: %v", err)
	}
	if got := u.Host; got != "127.0.0.1:5432" {
		t.Errorf("host = %q, want 127.0.0.1:5432", got)
	}
	pw, _ := u.User.Password()
	if pw != f.DBPassword {
		t.Errorf("password = %q, want %q", pw, f.DBPassword)
	}
	if got := strings.TrimPrefix(u.Path, "/"); got != f.DBName {
		t.Errorf("dbname = %q, want %q", got, f.DBName)
	}
	if got := u.Query().Get("sslmode"); got != "disable" {
		t.Errorf("sslmode = %q, want disable", got)
	}
}

func TestDSNIPv6Host(t *testing.T) {
	f := validForm()
	f.DBHost = "::1"

	u, err := url.Parse(f.dsn())
	if err != nil {
		t.Fatalf("dsn is not a valid URL: %v", err)
	}
	if got := u.Host; got != "[::1]:5432" {
		t.Errorf("host = %q, want [::1]:5432", got)
	}
}

// The wizard shows the operator the real database error (FR-107) — it is the
// one screen where that is allowed, because the operator is the only audience
// and they are who can fix a wrong host. The DSN travels inside those errors,
// and the DSN carries the database password, which C5 forbids putting anywhere
// it can be read later (D15 7절, D22 4절).
//
// Redaction is currently pgx's doing (`pgconn.redactPW`), not ours, and pgx's
// own source calls that "perhaps it would be better only return a static
// string" — a property we depend on but do not own. Without this test a pgx
// upgrade could put the password on the page and every check would stay green.
//
// No database needed: the failure is a refused connection, which is the same
// path a wrong host takes.
//
// Both assertions were checked by breaking the code (M4):
//   - db password: making provision wrap cfg.DatabaseURL into the error fails it
//   - admin password: it guards the PAIR of template and render(). The template
//     has no `value=` on the password inputs today, so removing render()'s
//     clearing alone changes nothing; adding `value="{{.AdminPW}}"` alone is
//     still safe because render() clears. Doing both fails this test, which is
//     the regression it exists for — someone adds the echo "so the operator
//     does not retype everything" and the clearing goes with a later cleanup.
func TestConnectFailureNeverShowsDatabasePassword(t *testing.T) {
	const dbPassword = "correct-horse-battery-DBPW"
	const adminPassword = "correct-horse-battery-ADMINPW"

	// A port that nothing is listening on: bind one, read it back, release it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()

	w := newWizard(t)
	rec := w.post(url.Values{
		"db_host":                {"127.0.0.1"},
		"db_port":                {port},
		"db_user":                {"ondolith"},
		"db_password":            {dbPassword},
		"db_name":                {"ondolith"},
		"db_sslmode":             {"disable"},
		"site_name":              {"테스트 사이트"},
		"admin_email":            {"admin@example.com"},
		"admin_password":         {adminPassword},
		"admin_password_confirm": {adminPassword},
	})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("HTTP %d, want 400 — 접속이 실패했어야 한다. 본문: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// The error must actually be about the connection, or this test would pass
	// on a form-validation rejection that never builds a DSN at all.
	if !strings.Contains(body, "연결할 수 없습니다") {
		t.Fatalf("접속 실패 사유가 안 보인다 — 다른 이유로 거부됐다면 이 테스트는 아무것도 검증하지 않는다. 본문: %s", body)
	}
	if strings.Contains(body, dbPassword) {
		t.Errorf("DB 비밀번호가 화면에 노출됐다 (C5 위반). 본문: %s", body)
	}
	if strings.Contains(body, adminPassword) {
		t.Errorf("관리자 비밀번호가 화면에 노출됐다 (C5 위반). 본문: %s", body)
	}
}

// NFR-212. 관리자의 공개 표시 이름에 **이메일이 새면 안 된다.**
//
// 설치가 `display_name` 에 관리자 이메일을 그대로 넣고 있었다. 그러면 관리자가
// 쓴 첫 글의 작성자 줄에 주소가 걸리고, 방문자 누구나 그것을 읽는다 — 브라우저
// 스크린샷의 게시물 화면에 `admin@example.com` 이 그대로 찍혀 있었다. 로그인
// 응답이 계정의 존재를 감추는 것(FR-201)도 그 줄 하나로 무의미해진다.
func TestAdminDisplayNameNeverLeaksTheEmail(t *testing.T) {
	f := validForm()
	f.AdminName = ""
	if got := f.displayName(); got == "" {
		t.Fatal("표시 이름이 비었다 — 화면에 이름 없는 작성자가 나온다")
	}
	if got := f.displayName(); strings.Contains(got, "@") ||
		strings.Contains(got, f.AdminEmail) {
		t.Errorf("표시 이름에 이메일이 들어갔다: %q", got)
	}

	f.AdminName = "온돌 운영자"
	if got := f.displayName(); got != "온돌 운영자" {
		t.Errorf("입력한 이름을 쓰지 않았다: %q", got)
	}
}

// NFR-211. 설치 화면도 **프레임에 들어가면 안 된다.**
//
// DB 비밀번호와 관리자 비밀번호를 받는 폼이다 — 클릭재킹의 대가가 운영 트리보다
// 크지, 작지 않다. 코드를 공용(httpsec)으로 옮겨도 **배선은 트리마다 따로**라
// 한쪽만 감기는 일이 생긴다. 그래서 트리마다 자기 검사를 둔다.
func TestInstallTreeSetsSecurityHeaders(t *testing.T) {
	h, err := New(t.TempDir()+"/ondolith.json", slog.New(slog.DiscardHandler), nil)
	if err != nil {
		t.Fatalf("설치 핸들러를 만들지 못했다: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/install", nil))

	for _, c := range []struct{ header, want string }{
		{"X-Frame-Options", "DENY"},
		{"X-Content-Type-Options", "nosniff"},
	} {
		if got := rec.Header().Get(c.header); got != c.want {
			t.Errorf("%s = %q, want %q", c.header, got, c.want)
		}
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors") {
		t.Errorf("CSP 에 frame-ancestors 가 없다: %q",
			rec.Header().Get("Content-Security-Policy"))
	}
}
