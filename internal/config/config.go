// Package config reads and writes Ondolith's on-disk configuration.
//
// The presence of a readable config file is the installed flag: if it is
// missing, the server boots into install mode.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrNotInstalled reports that no config file exists yet.
var ErrNotInstalled = errors.New("config: not installed")

// Config is the whole of Ondolith's persistent configuration. It holds the
// database password, so it is written with 0600 permissions.
type Config struct {
	DatabaseURL string    `json:"database_url"`
	SiteName    string    `json:"site_name"`
	InstalledAt time.Time `json:"installed_at"`

	// SecureCookies sets the Secure flag on session cookies. Detected at
	// install time from the request; edit this file to correct it if the
	// server later moves behind (or out from behind) TLS.
	SecureCookies bool `json:"secure_cookies"`

	// SiteURL is the origin that emailed links (password reset, address
	// verification) point at. Captured at install time from the operator's
	// own request; edit this file if the domain or scheme changes later.
	//
	// It is deliberately NOT derived from each request's Host header: a reset
	// mail is sent to the victim, and a forged Host would make its link point
	// at the attacker's server (reset poisoning). The install request is the
	// one Host we can trust — the operator typed it.
	SiteURL string `json:"site_url"`

	// SecretKey seals the credentials stored in the settings table (PG 시크릿
	// 키, SMTP 비밀번호, 소셜 client_secret): base64 of 32 random bytes,
	// generated at install. It lives HERE and not in the database because the
	// database is what it protects — a leaked dump opens nothing without this
	// file. Back the file up with the database (D72 5절); without it the
	// stored credentials cannot be read and must be typed again.
	SecretKey string `json:"secret_key"`

	// UploadDir holds attachments. It is configuration and not a fixed path
	// because NFR-304 says an upgrade replaces the binary and nothing else —
	// a path compiled in would move with the binary, and the files would not.
	//
	// It lives OUTSIDE the web root: nothing serves it as a directory, and a
	// download goes through a handler that checks the parent post's permission
	// (D60 §3). Empty means "uploads/" beside the config file.
	UploadDir string `json:"upload_dir"`

	// ThemeDir is where disk themes live, for the same reason.
	ThemeDir string `json:"theme_dir"`

	// BackupDir holds the automatic backups (FR-715). Empty means "backups/"
	// beside the config file. 업로드 폴더 안이면 안 된다 — BackupsInsideUploads.
	BackupDir string `json:"backup_dir"`

	// ProxyIPHeader names the header a reverse proxy on THIS machine sets to the
	// client's address (nginx: `proxy_set_header X-Real-IP $remote_addr;`). Empty
	// means none: the client is RemoteAddr.
	//
	// It is read only when the connection comes from loopback — i.e. from that
	// proxy. A client reaching the app directly cannot choose its rate-limit
	// bucket by sending the header. Without it, behind the proxy every visitor
	// is 127.0.0.1 and each per-IP limit becomes one site-wide limit (D72).
	ProxyIPHeader string `json:"proxy_ip_header,omitempty"`

	// Path is where this config was read from. It is not serialised — it is
	// where the file IS, not something the file says — and it is what relative
	// UploadDir/ThemeDir resolve against.
	Path string `json:"-"`
}

// Uploads resolves the upload directory, relative to the config file's own
// directory when the setting is a relative path. An operator who moves the
// installation moves both together.
func (c *Config) Uploads() string {
	return resolveDir(c.Path, c.UploadDir, "uploads")
}

// Themes resolves the theme directory the same way.
func (c *Config) Themes() string {
	return resolveDir(c.Path, c.ThemeDir, "themes")
}

// Backups resolves the automatic-backup directory the same way.
func (c *Config) Backups() string {
	return resolveDir(c.Path, c.BackupDir, "backups")
}

// BackupsInsideUploads reports whether the backup directory is the upload
// directory or under it. 업로드 폴더는 백업에 통째로 들어가므로 그 안의 백업 폴더는
// 다음 백업이 이전 백업을 싣게 만든다 — 부팅이 거부한다.
func (c *Config) BackupsInsideUploads() bool {
	rel, err := filepath.Rel(c.Uploads(), c.Backups())
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func resolveDir(configPath, set, fallback string) string {
	if set == "" {
		set = fallback
	}
	if filepath.IsAbs(set) {
		return set
	}
	return filepath.Join(filepath.Dir(configPath), set)
}

// Load reads the config at path. It returns ErrNotInstalled if the file does
// not exist, which callers must distinguish from a genuine read failure — a
// corrupt or unreadable config must not silently restart the install wizard
// and let a passer-by re-point the site at their own database.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotInstalled
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("config: %s has no database_url", path)
	}
	// Remember where this came from. Uploads() and Themes() resolve relative
	// paths against it, so that moving the whole installation moves them too.
	c.Path = path
	return &c, nil
}

// Save writes the config atomically with 0600 permissions.
func Save(path string, c *Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".ondolith-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
