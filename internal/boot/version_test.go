package boot

import (
	"runtime/debug"
	"testing"
)

// **사이트 실행 파일에서도 코어 버전이 정확하다** ([D42](../../docs/42-site-extension.md) 12절).
//
// 코어 릴리즈는 -ldflags 로 버전을 찍는다. 사이트 저장소가 코어를 Go 모듈로 가져와 빌드하면 그 값이 비어 `dev` 가
// 되고, 테마 requires 검사(FR-313)가 개발 빌드로 보고 건너뛴다. 사이트 실행 파일의 코어 버전은 빌드 정보의 의존
// 모듈 버전이다. `go test` 빌드에는 의존 버전이 기록되지 않아, 실행 파일을 거쳐서는 이 분기를 물 수 없다 — 그래서
// 빌드 정보를 받는 순수 함수로 시험한다.
func TestResolveVersions(t *testing.T) {
	const rev = "80813d3fffaa1234"
	vcs := []debug.BuildSetting{{Key: "vcs.revision", Value: rev}}
	core := debug.Module{Path: coreModule, Version: "(devel)"}
	site := func(version string, deps ...*debug.Module) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Path: "example.com/juwon", Version: version}, Deps: deps, Settings: vcs}
	}
	for _, tc := range []struct {
		name    string
		stamped string
		bi      *debug.BuildInfo
		want    Versions
	}{
		{"코어 릴리즈", "v0.8.0", &debug.BuildInfo{Main: core, Settings: vcs}, Versions{Core: "v0.8.0"}},
		{"코어를 소스에서 빌드", "dev", &debug.BuildInfo{Main: core, Settings: vcs}, Versions{Core: "dev+80813d3fffaa"}},
		{"태그된 코어를 쓰는 사이트", "", site("v1.2.0", &debug.Module{Path: coreModule, Version: "v0.8.0"}),
			Versions{Core: "v0.8.0", Site: "example.com/juwon v1.2.0"}},
		{"사이트에 태그가 없으면 커밋", "", site("(devel)", &debug.Module{Path: coreModule, Version: "v0.8.0"}),
			Versions{Core: "v0.8.0", Site: "example.com/juwon 80813d3fffaa"}},
		{"로컬 코어로 바꿔 끼운 사이트는 dev", "", site("v1.2.0", &debug.Module{Path: coreModule, Version: "v0.8.0",
			Replace: &debug.Module{Path: "../ondolith"}}), Versions{Core: "dev", Site: "example.com/juwon v1.2.0"}},
		{"의존 목록에 코어가 없으면 dev", "", site("v1.2.0"), Versions{Core: "dev", Site: "example.com/juwon v1.2.0"}},
		{"go.work 로 빌드해 의존 판이 (devel)", "", site("v1.2.0", &debug.Module{Path: coreModule, Version: "(devel)"}),
			Versions{Core: "dev", Site: "example.com/juwon v1.2.0"}},
		{"주 모듈 경로가 없으면 코어로 본다", "v0.8.0", &debug.BuildInfo{Settings: vcs}, Versions{Core: "v0.8.0"}},
		{"빌드 정보가 없으면 찍힌 값", "", nil, Versions{Core: "dev"}},
		{"빌드 정보가 없는 릴리즈", "v0.8.0", nil, Versions{Core: "v0.8.0"}},
	} {
		if got := resolveVersions(tc.stamped, tc.bi); got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// `-version` 한 줄. 코어 릴리즈의 모양은 바뀌지 않는다 — scripts/verify-release.sh 가 그 줄을 대조한다.
func TestVersionLine(t *testing.T) {
	if got, want := (Versions{Core: "v0.8.0"}).Line("linux", "amd64"), "ondolith v0.8.0 linux/amd64"; got != want {
		t.Errorf("코어: %q, want %q", got, want)
	}
	if got, want := (Versions{Core: "v0.8.0", Site: "example.com/juwon v1.2.0"}).Line("linux", "arm64"),
		"ondolith v0.8.0 linux/arm64 · 사이트 example.com/juwon v1.2.0"; got != want {
		t.Errorf("사이트: %q, want %q", got, want)
	}
}
