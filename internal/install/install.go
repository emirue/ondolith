// Package install serves the browser-based setup wizard.
//
// The install tree and the operating tree are separate route trees. This
// package is mounted only while the site is uninstalled; once Complete
// succeeds the caller swaps in the operating handler and this tree is gone.
package install

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/bcrypt"

	"github.com/emirue/ondolith/internal/config"
	"github.com/emirue/ondolith/internal/migrations"
)

//go:embed templates/*.html
var templatesFS embed.FS

// minPasswordLen is the floor for the administrator password. Deliberately
// modest: this is a self-hosted product and a wizard that rejects the
// operator's password three times gets a weaker password, not a stronger one.
const minPasswordLen = 10

// connectTimeout bounds the database connection attempt so a wrong host does
// not hang the wizard.
const connectTimeout = 10 * time.Second

type handler struct {
	configPath  string
	log         *slog.Logger
	onInstalled func(*config.Config) error
	tmpl        *template.Template

	mu   sync.Mutex
	done bool
}

// New returns the install-mode route tree. onInstalled is called with the
// freshly written config once the database is ready; it is responsible for
// bringing up the operating tree.
func New(configPath string, log *slog.Logger, onInstalled func(*config.Config) error) (http.Handler, error) {
	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	h := &handler{configPath: configPath, log: log, onInstalled: onInstalled, tmpl: tmpl}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /install", h.show)
	mux.HandleFunc("POST /install", h.submit)
	// Everything else: there is no site yet, so there is nowhere else to go.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/install", http.StatusSeeOther)
	})

	// Even the installer gets CSRF protection: without it a page the operator
	// happens to be visiting could POST this form and claim the site.
	return http.NewCrossOriginProtection().Handler(mux), nil
}

// sslModes are libpq's sslmode values, in increasing order of strictness.
var sslModes = []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"}

// form holds the wizard's input. Passwords are never echoed back.
type form struct {
	DBHost     string
	DBPort     string
	DBUser     string
	DBPassword string
	DBName     string
	DBSSLMode  string
	SiteName   string
	AdminEmail string
	AdminPW    string
	AdminPW2   string

	Error string
}

// SSLModes is called from the template to render the sslmode <select>.
func (*form) SSLModes() []string { return sslModes }

func (h *handler) show(w http.ResponseWriter, r *http.Request) {
	h.render(w, http.StatusOK, &form{
		DBHost:    "127.0.0.1",
		DBPort:    "5432",
		DBName:    "ondolith",
		DBSSLMode: "disable",
	})
}

func (h *handler) render(w http.ResponseWriter, code int, f *form) {
	f.AdminPW, f.AdminPW2, f.DBPassword = "", "", ""
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if err := h.tmpl.ExecuteTemplate(w, "install.html", f); err != nil {
		h.log.Error("설치 화면 렌더링 실패", "err", err)
	}
}

func (h *handler) submit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "잘못된 요청입니다.", http.StatusBadRequest)
		return
	}
	f := &form{
		DBHost:     strings.TrimSpace(r.PostFormValue("db_host")),
		DBPort:     strings.TrimSpace(r.PostFormValue("db_port")),
		DBUser:     strings.TrimSpace(r.PostFormValue("db_user")),
		DBPassword: r.PostFormValue("db_password"),
		DBName:     strings.TrimSpace(r.PostFormValue("db_name")),
		DBSSLMode:  strings.TrimSpace(r.PostFormValue("db_sslmode")),
		SiteName:   strings.TrimSpace(r.PostFormValue("site_name")),
		AdminEmail: strings.ToLower(strings.TrimSpace(r.PostFormValue("admin_email"))),
		AdminPW:    r.PostFormValue("admin_password"),
		AdminPW2:   r.PostFormValue("admin_password_confirm"),
	}

	if err := f.validate(); err != nil {
		f.Error = err.Error()
		h.render(w, http.StatusBadRequest, f)
		return
	}

	// One installation at a time, and only one ever.
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.done {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	cfg := &config.Config{
		DatabaseURL:   f.dsn(),
		SiteName:      f.SiteName,
		InstalledAt:   time.Now().UTC(),
		SecureCookies: requestIsHTTPS(r),
	}

	if err := h.provision(r.Context(), cfg, f); err != nil {
		// The operator is the only audience here and they are the one who can
		// fix a bad host or a missing database, so show them the real error.
		f.Error = err.Error()
		h.render(w, http.StatusBadRequest, f)
		return
	}

	if err := config.Save(h.configPath, cfg); err != nil {
		f.Error = fmt.Sprintf("설정 파일을 저장하지 못했습니다: %v", err)
		h.render(w, http.StatusInternalServerError, f)
		return
	}

	if err := h.onInstalled(cfg); err != nil {
		f.Error = fmt.Sprintf("운영 모드 전환에 실패했습니다: %v", err)
		h.render(w, http.StatusInternalServerError, f)
		return
	}

	h.done = true
	h.log.Info("설치 완료", "site", cfg.SiteName, "admin", f.AdminEmail)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// provision connects to the database, applies migrations and creates the
// first administrator. It leaves nothing behind on failure except whatever
// migrations already committed, which are re-runnable.
func (h *handler) provision(ctx context.Context, cfg *config.Config, f *form) error {
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("데이터베이스 접속 설정이 올바르지 않습니다: %w", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("데이터베이스에 연결할 수 없습니다: %w", err)
	}

	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	if err := migrations.Run(ctx, db); err != nil {
		return fmt.Errorf("마이그레이션에 실패했습니다: %w", err)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(f.AdminPW), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("비밀번호를 처리하지 못했습니다: %w", err)
	}

	// **계정과 역할을 한 문장에서 만든다.**
	//
	// `is_admin` 만 켜는 것으로는 관리자가 되지 않는다. 권한은 역할이 정하고
	// (D15), `is_admin` 은 Phase 0 의 잔재로 W2-01 이 지울 컬럼이다.
	// 00003 의 백필(`WHERE u.is_admin`)은 **마이그레이션 시점에 한 번** 도는데
	// 설치는 마이그레이션을 먼저 돌리고 계정을 나중에 만든다 — 그 순서 때문에
	// 백필은 늘 빈 테이블을 보고, 새로 설치한 사이트의 유일한 관리자는 역할이
	// 없는 채로 남아 `/admin` 에서 「권한이 없습니다」를 본다. 되돌릴 화면이
	// 관리자 화면 안에 있으므로 그 사이트는 손댈 방법이 없다.
	//
	// CTE 로 묶는 이유: 계정만 만들고 역할 부여에서 실패하면 정확히 위의 잠긴
	// 상태가 되고, 재설치는 「이미 계정이 존재한다」로 거부된다. 둘은 함께
	// 성립하거나 함께 없어야 한다.
	const q = `WITH created AS (
	               INSERT INTO users (email, password_hash, display_name, is_admin)
	               VALUES ($1, $2, $3, true)
	               ON CONFLICT (email) DO NOTHING
	               RETURNING id
	           )
	           INSERT INTO user_roles (user_id, role_id)
	           SELECT created.id, r.id FROM created, roles r WHERE r.key = 'admin'`
	tag, err := pool.Exec(ctx, q, f.AdminEmail, string(hash), f.AdminEmail)
	if err != nil {
		return fmt.Errorf("관리자 계정을 만들지 못했습니다: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("이미 %s 계정이 존재하는 데이터베이스입니다. 빈 데이터베이스를 사용하거나 다른 이메일을 입력하세요", f.AdminEmail)
	}
	return nil
}

func (f *form) validate() error {
	switch {
	case f.DBHost == "":
		return errors.New("데이터베이스 호스트를 입력하세요.")
	case f.DBUser == "":
		return errors.New("데이터베이스 사용자를 입력하세요.")
	case f.DBName == "":
		return errors.New("데이터베이스 이름을 입력하세요.")
	case f.SiteName == "":
		return errors.New("사이트 이름을 입력하세요.")
	}

	port, err := strconv.Atoi(f.DBPort)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("포트는 1에서 65535 사이의 숫자여야 합니다.")
	}

	if !slices.Contains(sslModes, f.DBSSLMode) {
		return errors.New("SSL 모드 값이 올바르지 않습니다.")
	}

	if _, err := mail.ParseAddress(f.AdminEmail); err != nil {
		return errors.New("관리자 이메일 형식이 올바르지 않습니다.")
	}
	if len([]rune(f.AdminPW)) < minPasswordLen {
		return fmt.Errorf("관리자 비밀번호는 %d자 이상이어야 합니다.", minPasswordLen)
	}
	if f.AdminPW != f.AdminPW2 {
		return errors.New("관리자 비밀번호가 서로 다릅니다.")
	}
	return nil
}

// dsn builds the connection URL through net/url so that passwords containing
// ':', '@' or '/' survive.
func (f *form) dsn() string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(f.DBUser, f.DBPassword),
		Host:   net.JoinHostPort(f.DBHost, f.DBPort),
		Path:   "/" + f.DBName,
	}
	q := u.Query()
	q.Set("sslmode", f.DBSSLMode)
	u.RawQuery = q.Encode()
	return u.String()
}

// requestIsHTTPS decides the Secure cookie flag. X-Forwarded-Proto is only
// meaningful behind a trusted reverse proxy; getting it wrong writes a wrong
// value into the config, which the operator can edit. It is not a trust
// boundary — nothing is authorised on the strength of this.
func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
