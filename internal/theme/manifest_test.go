package theme

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func themeDir(t *testing.T, manifest string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "base.html"), []byte("{{block \"content\" .}}{{end}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if manifest != "" {
		if err := os.WriteFile(filepath.Join(dir, "theme.json"), []byte(manifest), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// D17: activating a theme this binary is too old for breaks every page —
// including A-202, the screen that would switch back. Refusing beats repairing
// by hand.
func TestThemeRequiringANewerOndolithIsRefused(t *testing.T) {
	dir := themeDir(t, `{"name":"미래테마","requires":"2.0.0"}`)

	warn, err := ValidateThemeDir(dir, "1.9.3")
	if !errors.Is(err, ErrRequiresNewer) {
		t.Fatalf("err = %v, want ErrRequiresNewer", err)
	}
	if !strings.Contains(err.Error(), "2.0.0") || !strings.Contains(err.Error(), "1.9.3") {
		t.Errorf("어느 쪽이 부족한지 알 수 없는 메시지다: %v", err)
	}
	if warn != "" {
		t.Errorf("거부하면서 경고도 냈다: %q", warn)
	}
}

func TestThemeRequiringAnOlderOrEqualOndolithIsAccepted(t *testing.T) {
	for _, req := range []string{"1.0.0", "1.9.3", "1.9", "1", "v1.9.3"} {
		dir := themeDir(t, `{"requires":"`+req+`"}`)
		if _, err := ValidateThemeDir(dir, "1.9.3"); err != nil {
			t.Errorf("requires=%q 가 거부됐다: %v", req, err)
		}
	}
}

// D17: a development build skips the comparison and warns. Being unable to try
// a theme while writing it is worse than a theme that misbehaves.
func TestDevBuildWarnsInsteadOfRefusing(t *testing.T) {
	dir := themeDir(t, `{"requires":"99.0.0"}`)

	for _, cur := range []string{"dev", "dev+abc123def456", "vdev"} {
		warn, err := ValidateThemeDir(dir, cur)
		if err != nil {
			t.Errorf("개발 빌드 %q 에서 거부됐다: %v", cur, err)
		}
		if warn == "" {
			t.Errorf("개발 빌드 %q 에서 경고가 없다 — 조용히 넘어갔다", cur)
			continue
		}
		// The reason has to be the right one. An unparseable version warns too,
		// and telling the operator "버전을 해석할 수 없다" when the truth is
		// "개발 빌드라 건너뛴다" sends them to look at the wrong thing.
		if !strings.Contains(warn, "개발 빌드") {
			t.Errorf("개발 빌드 %q 인데 다른 이유로 경고했다: %q", cur, warn)
		}
	}
}

// An unparseable running version cannot be compared. Refusing there would brick
// a legitimately built binary over a version string, so it warns like dev does.
func TestUnparseableCurrentVersionWarnsInsteadOfRefusing(t *testing.T) {
	dir := themeDir(t, `{"requires":"2.0.0"}`)

	warn, err := ValidateThemeDir(dir, "nightly-2026-08-04")
	if err != nil {
		t.Errorf("해석 불가한 현재 버전에서 거부됐다: %v", err)
	}
	if warn == "" {
		t.Error("경고 없이 통과했다")
	}
}

// theme.json is optional: base.html is the only required file (D17).
func TestMissingManifestMeansNoMinimum(t *testing.T) {
	if _, err := ValidateThemeDir(themeDir(t, ""), "0.0.1"); err != nil {
		t.Errorf("theme.json 없는 테마가 거부됐다: %v", err)
	}
}

// A `requires` nobody can read is a bug in the theme, not a pass. Treating it
// as "no minimum" would let a typo silently disable the floor.
func TestUnreadableRequiresIsRefused(t *testing.T) {
	for _, bad := range []string{"latest", "1.2.3.4", "", "1.x", "-1.0.0"} {
		if bad == "" {
			continue // absent is legal; covered above
		}
		if _, err := ValidateThemeDir(themeDir(t, `{"requires":"`+bad+`"}`), "1.0.0"); err == nil {
			t.Errorf("해석 불가한 requires=%q 가 통과했다", bad)
		}
	}
}

func TestBrokenManifestJSONIsRefused(t *testing.T) {
	if _, err := ValidateThemeDir(themeDir(t, `{"requires":`), "1.0.0"); err == nil {
		t.Error("깨진 theme.json 이 통과했다")
	}
}

func TestVersionComparisonOrdersByComponent(t *testing.T) {
	tests := []struct {
		a, b string
		want bool // a older than b
	}{
		{"1.0.0", "2.0.0", true},
		{"1.9.0", "1.10.0", true}, // not string order
		{"1.2.9", "1.2.10", true},
		{"2.0.0", "1.9.9", false},
		{"1.2.3", "1.2.3", false},
	}
	for _, tc := range tests {
		a, ok := parseVersion(tc.a)
		if !ok {
			t.Fatalf("parseVersion(%q) 실패", tc.a)
		}
		b, ok := parseVersion(tc.b)
		if !ok {
			t.Fatalf("parseVersion(%q) 실패", tc.b)
		}
		if got := olderThan(a, b); got != tc.want {
			t.Errorf("olderThan(%s, %s) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
