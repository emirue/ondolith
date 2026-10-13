package theme

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var (
	// ErrRequiresNewer means the theme asked for an Ondolith newer than this one.
	ErrRequiresNewer = errors.New("테마가 더 높은 Ondolith 버전을 요구합니다")
	// ErrManifest means theme.json is there but cannot be read as D17 describes.
	ErrManifest = errors.New("theme: theme.json 형식이 올바르지 않습니다")
)

// RequiresError is ErrRequiresNewer with both versions, for the sentence A-202
// shows the operator.
type RequiresError struct {
	Need, Have string
}

func (e *RequiresError) Error() string {
	return fmt.Sprintf("%v: %s 이상 필요, 현재 %s", ErrRequiresNewer, e.Need, e.Have)
}
func (e *RequiresError) Unwrap() error { return ErrRequiresNewer }

// Manifest is theme.json (D17). Only the keys the core acts on are read: name,
// version and author are for display and gate nothing.
type Manifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Author  string `json:"author"`
	// Requires is the minimum Ondolith version. Absent means no minimum.
	Requires string `json:"requires"`
}

// ReadManifest reads theme.json from a theme directory.
//
// A missing file is not an error: base.html is the only required file (D17),
// so a theme without a manifest is a legal theme with no minimum version.
func ReadManifest(dir string) (Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, "theme.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Manifest{}, nil
	}
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, fmt.Errorf("%w: %w", ErrManifest, err)
	}
	return m, nil
}

// parseVersion takes the leading major.minor.patch and drops everything after
// it. D17 fixes the comparison at three numbers on purpose: pre-release and
// build-metadata ordering is precision theme compatibility does not need, and
// a rule nobody can predict is worse than a coarse one.
//
// Missing components read as zero, so "2" and "2.0.0" compare equal.
func parseVersion(s string) ([3]int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "+-"); i >= 0 {
		s = s[:i]
	}
	var out [3]int
	if s == "" {
		return out, false
	}
	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func olderThan(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// IsDevBuild reports the `dev` prefix D17 uses to mean "skip the comparison".
func IsDevBuild(current string) bool {
	return strings.HasPrefix(strings.TrimPrefix(current, "v"), "dev")
}

// CheckRequires compares a theme's `requires` against the running version.
//
// It returns a warning string instead of an error for the cases where the
// comparison cannot be made — a development build, or a version string this
// binary was stamped with that does not parse. Refusing there would make a
// theme unusable while it is being written, which D17 rules out explicitly.
// A warning nobody surfaces is not a warning, so the caller gets the text.
func CheckRequires(requires, current string) (warn string, err error) {
	if requires == "" {
		return "", nil
	}
	req, ok := parseVersion(requires)
	if !ok {
		return "", fmt.Errorf("%w: requires 값이 major.minor.patch 형식이 아닙니다: %q", ErrManifest, requires)
	}
	if IsDevBuild(current) {
		return fmt.Sprintf("개발 빌드(%s)라 테마 요구 버전 %s 검사를 건너뜁니다", current, requires), nil
	}
	cur, ok := parseVersion(current)
	if !ok {
		return fmt.Sprintf("현재 버전 %q 을 해석할 수 없어 테마 요구 버전 %s 검사를 건너뜁니다", current, requires), nil
	}
	if olderThan(cur, req) {
		return "", &RequiresError{Need: requires, Have: current}
	}
	return "", nil
}
