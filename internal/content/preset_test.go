package content

import (
	"errors"
	"testing"
)

// 스코프 부여는 is_scoped=true 인 6개에만 허용된다 (D15 2.4). 프리셋이 그
// 밖의 권한을 넣으면 부여 핸들러가 거부하므로 게시판 생성 전체가 실패한다 —
// 그 실패는 A-305 를 처음 쓰는 사람에게 일어난다.
var scopedPermissions = map[string]bool{
	"post.read": true, "post.write": true, "post.moderate": true,
	"post.read_secret": true, "comment.write": true, "comment.moderate": true,
}

var knownRoles = map[string]bool{
	"anonymous": true, "member": true, "editor": true, "operator": true, "admin": true,
}

func TestEveryPresetGrantsSomething(t *testing.T) {
	for _, p := range Presets() {
		g, err := PresetGrants(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		// fail-closed 는 기본값이지 프리셋이 아니다. 빈 프리셋은 화면이
		// "설정했다"고 말하면서 아무것도 안 한 상태다.
		if len(g) == 0 {
			t.Errorf("%s 프리셋이 부여 행을 하나도 만들지 않는다", p)
		}
	}
}

func TestPresetGrantsUseOnlyScopedPermissionsAndRealRoles(t *testing.T) {
	for _, p := range Presets() {
		g, _ := PresetGrants(p)
		for _, row := range g {
			if !scopedPermissions[row.Permission] {
				t.Errorf("%s: %q 는 게시판 스코프 권한이 아니다", p, row.Permission)
			}
			if !knownRoles[row.Role] {
				t.Errorf("%s: %q 는 존재하지 않는 역할이다", p, row.Role)
			}
		}
	}
}

// 프리셋마다 누가 읽을 수 있는지가 정확히 달라야 한다. 셋이 같은 결과를 내면
// 화면의 선택지가 장식이다.
func TestPresetsDifferInWhoCanRead(t *testing.T) {
	readers := func(p BoardPreset) map[string]bool {
		out := map[string]bool{}
		g, _ := PresetGrants(p)
		for _, row := range g {
			if row.Permission == "post.read" {
				out[row.Role] = true
			}
		}
		return out
	}

	pub, mem, priv := readers(PresetPublic), readers(PresetMembers), readers(PresetPrivate)

	if !pub["anonymous"] {
		t.Error("공개 게시판을 익명이 못 읽는다")
	}
	// 없는 것이 곧 거부다 (D15 P1). anonymous 행이 있으면 로그인 없이 읽힌다.
	if mem["anonymous"] {
		t.Error("회원전용 게시판에 anonymous 읽기 행이 있다")
	}
	if priv["anonymous"] || priv["member"] {
		t.Errorf("비공개 게시판을 일반 회원이 읽는다: %v", priv)
	}
	if !priv["operator"] {
		t.Error("비공개 게시판을 운영자도 못 읽는다 — 첫 스팸을 지울 사람이 없다")
	}
}

// 익명 쓰기는 어떤 프리셋에도 없다. 스팸의 기본값이 되어서는 안 된다.
func TestNoPresetLetsAnonymousWrite(t *testing.T) {
	for _, p := range Presets() {
		g, _ := PresetGrants(p)
		for _, row := range g {
			if row.Role != "anonymous" {
				continue
			}
			if row.Permission != "post.read" {
				t.Errorf("%s: 익명에게 %q 를 준다", p, row.Permission)
			}
		}
	}
}

// post.read_secret 은 남의 비밀글을 읽는 권한이다 (D15 2.4). 드롭다운의
// 기본값이 아니라 의도적인 부여여야 한다.
func TestNoPresetGrantsSecretReading(t *testing.T) {
	for _, p := range Presets() {
		g, _ := PresetGrants(p)
		for _, row := range g {
			if row.Permission == "post.read_secret" {
				t.Errorf("%s 프리셋이 %s 에게 비밀글 열람을 준다", p, row.Role)
			}
		}
	}
}

// 모든 프리셋에 조정자가 있다. 아무도 조정할 수 없는 게시판은 첫 스팸에
// superuser 를 불러야 한다.
func TestEveryPresetHasAModerator(t *testing.T) {
	for _, p := range Presets() {
		g, _ := PresetGrants(p)
		var hasPost, hasComment bool
		for _, row := range g {
			switch row.Permission {
			case "post.moderate":
				hasPost = true
			case "comment.moderate":
				hasComment = true
			}
		}
		if !hasPost || !hasComment {
			t.Errorf("%s: 조정 권한이 없다 (post=%v comment=%v)", p, hasPost, hasComment)
		}
	}
}

// 오타는 "부여 없음"이 아니라 오류다. 조용히 넘어가면 아무도 못 들어가는
// 게시판이 만들어지고 이유를 말해 주는 것이 없다.
func TestUnknownPresetIsRefused(t *testing.T) {
	if _, err := PresetGrants("전체공개"); !errors.Is(err, ErrUnknownPreset) {
		t.Errorf("알 수 없는 프리셋이 통과했다: %v", err)
	}
	if _, err := PresetGrants(""); !errors.Is(err, ErrUnknownPreset) {
		t.Errorf("빈 프리셋이 통과했다: %v", err)
	}
}

// 같은 행이 두 번 들어가면 role_permissions 의 UNIQUE 가 게시판 생성
// 트랜잭션을 통째로 실패시킨다.
func TestPresetHasNoDuplicateRows(t *testing.T) {
	for _, p := range Presets() {
		g, _ := PresetGrants(p)
		seen := map[Grant]bool{}
		for _, row := range g {
			if seen[row] {
				t.Errorf("%s: 중복 행 %+v", p, row)
			}
			seen[row] = true
		}
	}
}

// 반환된 슬라이스를 고쳐도 다음 호출에 영향이 없어야 한다. 프리셋 표를
// 호출자가 바꿔 버리면 그 다음 게시판부터 권한이 달라진다.
func TestPresetGrantsAreACopy(t *testing.T) {
	first, _ := PresetGrants(PresetPublic)
	first[0] = Grant{Role: "admin", Permission: "post.moderate"}
	second, _ := PresetGrants(PresetPublic)
	if second[0] == (Grant{Role: "admin", Permission: "post.moderate"}) {
		t.Error("호출자가 프리셋 표를 바꿨다")
	}
}
