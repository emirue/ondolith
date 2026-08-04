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
