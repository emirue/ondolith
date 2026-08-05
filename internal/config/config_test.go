package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMissingFileReportsNotInstalled(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err = %v, want ErrNotInstalled", err)
	}
}

// FR-110. A config that exists but cannot be used must NOT look like "not
// installed" — otherwise a corrupt file drops the server back into the install
// wizard and anyone who can reach it re-points the site at their own database.
func TestLoadRefusesToTreatBrokenConfigAsUninstalled(t *testing.T) {
	tests := map[string]string{
		"잘린 JSON":         `{ "database_url": `,
		"JSON이 아님":        `this is not json at all`,
		"빈 파일":            ``,
		"database_url 없음": `{"site_name":"x"}`,
		"database_url 빈값": `{"database_url":"","site_name":"x"}`,
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ondolith.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil {
				t.Fatal("깨진 설정이 통과했다")
			}
			if errors.Is(err, ErrNotInstalled) {
				t.Fatal("깨진 설정을 미설치로 보고했다 — 사이트 재점유 경로")
			}
		})
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ondolith.json")
	want := &Config{
		DatabaseURL:   "postgres://u:p%40ss@127.0.0.1:5432/db?sslmode=require",
		SiteName:      "온돌 사이트",
		InstalledAt:   time.Date(2026, 7, 29, 4, 58, 16, 0, time.UTC),
		SecureCookies: true,
	}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.DatabaseURL != want.DatabaseURL {
		t.Errorf("DatabaseURL = %q, want %q", got.DatabaseURL, want.DatabaseURL)
	}
	if got.SiteName != want.SiteName {
		t.Errorf("SiteName = %q, want %q", got.SiteName, want.SiteName)
	}
	if !got.InstalledAt.Equal(want.InstalledAt) {
		t.Errorf("InstalledAt = %v, want %v", got.InstalledAt, want.InstalledAt)
	}
	if got.SecureCookies != want.SecureCookies {
		t.Errorf("SecureCookies = %v, want %v", got.SecureCookies, want.SecureCookies)
	}
}

// FR-105. The file holds the database password.
func TestSaveUsesOwnerOnlyPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ondolith.json")
	if err := Save(path, &Config{DatabaseURL: "postgres://x/y"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("권한 = %04o, want 0600", perm)
	}
}

// Save writes through a temp file and renames. A failed write must not leave a
// half-written config behind, and a successful one must not leave litter that
// a later glob could pick up.
func TestSaveLeavesNoTempFileBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ondolith.json")
	if err := Save(path, &Config{DatabaseURL: "postgres://x/y"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "ondolith.json" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("디렉터리 내용 = %v, want [ondolith.json]", names)
	}
}

func TestSaveOverwritesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ondolith.json")
	if err := Save(path, &Config{DatabaseURL: "postgres://old/db", SiteName: "옛 이름"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, &Config{DatabaseURL: "postgres://new/db", SiteName: "새 이름"}); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.SiteName != "새 이름" {
		t.Errorf("SiteName = %q, want 새 이름", got.SiteName)
	}
}

// NFR-304: 업그레이드는 바이너리만 바꾼다. 업로드·테마 디렉터리를 바이너리에
// 박으면 업그레이드가 바이너리를 옮길 때 파일은 따라가지 않는다.
func TestDirectoriesAreConfigurableAndResolveBesideTheConfig(t *testing.T) {
	// 비워 두면 설정 파일 옆이다 — 운영자가 설치 전체를 옮겨도 둘이 함께 간다.
	c := &Config{Path: "/opt/ondolith/ondolith.json"}
	if got := c.Uploads(); got != "/opt/ondolith/uploads" {
		t.Errorf("기본 업로드 경로 = %q", got)
	}
	if got := c.Themes(); got != "/opt/ondolith/themes" {
		t.Errorf("기본 테마 경로 = %q", got)
	}

	// 상대 경로도 설정 파일 기준이다. 프로세스의 작업 디렉터리 기준이면
	// systemd 로 띄웠을 때와 손으로 띄웠을 때가 달라진다.
	c = &Config{Path: "/opt/ondolith/ondolith.json", UploadDir: "data/files", ThemeDir: "skins"}
	if got := c.Uploads(); got != "/opt/ondolith/data/files" {
		t.Errorf("상대 업로드 경로 = %q", got)
	}
	if got := c.Themes(); got != "/opt/ondolith/skins" {
		t.Errorf("상대 테마 경로 = %q", got)
	}

	// 절대 경로는 그대로 쓴다 — 업로드를 별도 볼륨에 두는 것이 흔한 배치다.
	c = &Config{Path: "/opt/ondolith/ondolith.json", UploadDir: "/mnt/uploads", ThemeDir: "/mnt/themes"}
	if got := c.Uploads(); got != "/mnt/uploads" {
		t.Errorf("절대 업로드 경로 = %q", got)
	}
	if got := c.Themes(); got != "/mnt/themes" {
		t.Errorf("절대 테마 경로 = %q", got)
	}
}
