package content

import (
	"errors"
	"fmt"
	"sort"
)

// BoardPreset is the permission shape a board is created with (A-305).
//
// A preset is a starting point, not a mode: the rows it writes are ordinary
// role_permissions rows that A-404 can edit afterwards. Storing the preset name
// on the board would make it a second source of truth about who may read the
// board, and the two would disagree the first time somebody edits the grants
// (D15 2.4 — scope is expressed by role_permissions.board_id and nothing else).
type BoardPreset string

const (
	PresetPublic  BoardPreset = "공개"
	PresetMembers BoardPreset = "회원전용"
	PresetPrivate BoardPreset = "비공개"
)

var ErrUnknownPreset = errors.New("content: 알 수 없는 권한 프리셋")

// Grant is one row to write into role_permissions, scoped to the new board.
type Grant struct {
	Role       string
	Permission string
}

// presetGrants is OPEN-12's answer: the exact rows each preset writes.
//
// Three rules shaped it.
//
//  1. Every preset writes at least one row. Fail-closed is the default state of
//     a board nobody granted anything on — it is not a preset, and offering
//     "비공개" as an empty grant set would make the screen claim to have done
//     something it did not.
//  2. operator gets the moderation permissions on every preset, including
//     비공개. A board nobody can moderate needs a superuser to fix its first
//     spam post, and D15 2.5 already gives operator moderation globally — the
//     scoped rows make the intent visible on A-404 rather than implied.
//  3. post.read_secret is never in a preset. It reads other people's secret
//     posts (D15 2.4), and that belongs to a deliberate grant, not a dropdown
//     default.
var presetGrants = map[BoardPreset][]Grant{
	// 공개: 누구나 읽고, 회원이 쓴다. 익명 쓰기는 프리셋에 없다 — 스팸의
	// 기본값이 되어서는 안 된다.
	PresetPublic: {
		{Role: "anonymous", Permission: "post.read"},
		{Role: "member", Permission: "post.read"},
		{Role: "member", Permission: "post.write"},
		{Role: "member", Permission: "comment.write"},
		{Role: "operator", Permission: "post.moderate"},
		{Role: "operator", Permission: "comment.moderate"},
	},
	// 회원전용: 로그인해야 보인다. anonymous 행이 아예 없다 — 없는 것이 곧
	// 거부다 (D15 P1 fail-closed).
	PresetMembers: {
		{Role: "member", Permission: "post.read"},
		{Role: "member", Permission: "post.write"},
		{Role: "member", Permission: "comment.write"},
		{Role: "operator", Permission: "post.moderate"},
		{Role: "operator", Permission: "comment.moderate"},
	},
	// 비공개: 운영진만. editor 가 읽고 쓰는 이유는 이 프리셋이 "내부 게시판"에
	// 쓰이기 때문이다 — 아무도 못 읽는 게시판은 만들 이유가 없다.
	PresetPrivate: {
		{Role: "editor", Permission: "post.read"},
		{Role: "editor", Permission: "post.write"},
		{Role: "editor", Permission: "comment.write"},
		{Role: "operator", Permission: "post.read"},
		{Role: "operator", Permission: "post.moderate"},
		{Role: "operator", Permission: "comment.moderate"},
	},
}

// Presets lists the allowed values for A-305's dropdown, in a fixed order.
func Presets() []BoardPreset {
	return []BoardPreset{PresetPublic, PresetMembers, PresetPrivate}
}

// PresetGrants returns the rows to write with the board, in the same
// transaction (D14 4.2). An unknown name is refused rather than treated as
// "no grants": a typo would otherwise create a board nobody can reach and no
// message would say why.
func PresetGrants(p BoardPreset) ([]Grant, error) {
	g, ok := presetGrants[p]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownPreset, p)
	}
	out := append([]Grant(nil), g...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Role != out[j].Role {
			return out[i].Role < out[j].Role
		}
		return out[i].Permission < out[j].Permission
	})
	return out, nil
}
