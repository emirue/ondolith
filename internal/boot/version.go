package boot

import (
	"runtime/debug"
	"strings"
)

// coreModule is this module's path — what a site binary lists among its deps.
const coreModule = "github.com/emirue/ondolith"

// Versions is what a binary reports about itself (-version, 시스템 화면, 테마 requires).
type Versions struct {
	// Core is the core's version: the release stamp, the dependency version in a
	// site binary, or dev / dev+<커밋 12자리>.
	Core string
	// Site is "<모듈 경로> <버전 또는 커밋 12자리>" in a site binary, "" in the core's own.
	Site string
}

// Line is the -version output. 코어 릴리즈의 모양은 바꾸지 않는다 — scripts/verify-release.sh 가
// 이 줄로 대상 아키텍처에서 실제로 돌았는지 대조한다.
func (v Versions) Line(goos, goarch string) string {
	line := "ondolith " + v.Core + " " + goos + "/" + goarch
	if v.Site != "" {
		line += " · 사이트 " + v.Site
	}
	return line
}

// currentVersions resolves this process's versions from the stamp and the
// build info the Go toolchain recorded.
func currentVersions(stamped string) Versions {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		bi = nil
	}
	return resolveVersions(stamped, bi)
}

// resolveVersions is the decision on its own (D42 12절). `go test` builds record
// neither a VCS revision nor dependency versions, so a test driving the binary
// cannot tell the branches apart — this takes the build info as a value.
//
// 사이트 실행 파일에서는 main 이 사이트의 것이라 -X main.version 이 코어에 닿지 않는다. 그 대신 go.mod 가
// 고정한 의존 버전이 빌드 정보에 있다. replace 로 로컬 코어를 끼우면 그 버전은 거짓이라 dev 다.
func resolveVersions(stamped string, bi *debug.BuildInfo) Versions {
	if stamped == "" {
		stamped = "dev"
	}
	if bi == nil {
		return Versions{Core: formatVersion(stamped, "")}
	}
	rev := revision(bi)
	if bi.Main.Path == coreModule || bi.Main.Path == "" {
		return Versions{Core: formatVersion(stamped, rev)}
	}
	v := Versions{Core: "dev", Site: bi.Main.Path + " " + siteVersion(bi.Main.Version, rev)}
	for _, d := range bi.Deps {
		if d.Path == coreModule && d.Replace == nil && d.Version != "" && d.Version != "(devel)" {
			v.Core = d.Version
		}
	}
	return v
}

// siteVersion is the site module's own version, or its commit when it has no tag.
func siteVersion(version, rev string) string {
	if version != "" && version != "(devel)" {
		return version
	}
	if len(rev) >= 12 {
		return rev[:12]
	}
	return "dev"
}

// formatVersion prefers the ldflags value and falls back to the VCS revision,
// so an unreleased build still identifies itself.
func formatVersion(v, rev string) string {
	if v != "dev" {
		return v
	}
	if len(rev) >= 12 {
		return v + "+" + rev[:12]
	}
	return v
}

func revision(bi *debug.BuildInfo) string {
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" {
			return strings.TrimSpace(s.Value)
		}
	}
	return ""
}
