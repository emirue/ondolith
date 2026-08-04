package auth

import (
	"slices"
	"testing"
)

// The seven rules W1-04's grant model rests on (D15 2.1, 2.4, 1.1). Each is a
// separate case because they fail independently: an implication bug opens every
// screen, while a scope bug opens exactly one board.
func TestCanOn(t *testing.T) {
	const (
		notice = BoardID("notice")
		free   = BoardID("free")
	)

	tests := []struct {
		name  string
		super bool
		gives []Grant
		perm  string
		board BoardID
		want  bool
	}{
		{
			name:  "전역 부여는 어느 게시판에서도 통과",
			gives: []Grant{{Permission: "post.read", Board: Global}},
			perm:  "post.read", board: free, want: true,
		},
		{
			name:  "전역 부여는 게시판 없는 자원에도 통과",
			gives: []Grant{{Permission: "settings.update", Board: Global}},
			perm:  "settings.update", board: Global, want: true,
		},
		{
			name:  "스코프 부여는 그 게시판만 통과",
			gives: []Grant{{Permission: "post.write", Board: free}},
			perm:  "post.write", board: free, want: true,
		},
		{
			name:  "스코프 부여는 다른 게시판을 통과시키지 않는다",
			gives: []Grant{{Permission: "post.write", Board: free}},
			perm:  "post.write", board: notice, want: false,
		},
		{
			// A grant scoped to a board says nothing about a resource that has
			// no board. Answering true here would let a per-board grant reach
			// site-wide screens.
			name:  "스코프 부여는 게시판 없는 질문에 답하지 않는다",
			gives: []Grant{{Permission: "post.write", Board: free}},
			perm:  "post.write", board: Global, want: false,
		},
		{
			name:  "미부여는 거부",
			gives: []Grant{{Permission: "post.read", Board: free}},
			perm:  "post.write", board: free, want: false,
		},
		{
			name:  "부여가 하나도 없으면 거부 (fail-closed)",
			gives: nil,
			perm:  "post.read", board: free, want: false,
		},
		{
			name:  "superuser 는 전건 통과",
			super: true, gives: nil,
			perm: "anything.at_all", board: notice, want: true,
		},
		{
			name:  "superuser 는 게시판 없는 질문도 통과",
			super: true, gives: nil,
			perm: "settings.update", board: Global, want: true,
		},
		{
			// D15 2.1: 함의 없음. This is the case that silently opens
			// everything if someone "helpfully" adds a hierarchy.
			name:  "함의 없음 — board.manage 가 board.view 를 통과시키지 않는다",
			gives: []Grant{{Permission: "board.manage", Board: Global}},
			perm:  "board.view", board: Global, want: false,
		},
		{
			name:  "같은 권한의 전역·스코프 부여가 섞여도 전역이 이긴다",
			gives: []Grant{{Permission: "post.read", Board: notice}, {Permission: "post.read", Board: Global}},
			perm:  "post.read", board: free, want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPermissions(tc.super, tc.gives)
			if got := p.CanOn(tc.perm, tc.board); got != tc.want {
				t.Errorf("CanOn(%q, %q) = %v, want %v", tc.perm, tc.board, got, tc.want)
			}
		})
	}
}

// A nil set must refuse rather than panic. Handlers reach for the request's
// permissions before anything has put them there in exactly one situation —
// a bug — and that situation must fail closed, not crash the server.
func TestNilPermissionsRefuses(t *testing.T) {
	var p *Permissions
	if p.CanOn("post.read", "free") {
		t.Error("nil 권한 집합이 통과시켰다")
	}
	if p.Can("settings.update") {
		t.Error("nil 권한 집합이 통과시켰다")
	}
}

// Can is CanOn with no board. Keeping them consistent matters because handlers
// for site-wide screens use the short form.
func TestCanIsCanOnWithoutBoard(t *testing.T) {
	p := NewPermissions(false, []Grant{{Permission: "settings.update", Board: Global}})
	if !p.Can("settings.update") {
		t.Error("전역 부여인데 Can 이 거부했다")
	}
	scoped := NewPermissions(false, []Grant{{Permission: "post.write", Board: "free"}})
	if scoped.Can("post.write") {
		t.Error("스코프 부여인데 Can 이 통과시켰다")
	}
}

// D15 1.1. anonymous is the floor, not a ceiling: leaving it out of an
// authenticated request is what produces "open to anonymous, invisible to
// members".
func TestEffectiveRoles(t *testing.T) {
	tests := []struct {
		name     string
		authed   bool
		assigned []string
		want     []string
	}{
		{
			name: "익명 요청은 {anonymous}", authed: false, assigned: nil,
			want: []string{"anonymous"},
		},
		{
			name: "익명 요청은 부여가 있어도 무시한다", authed: false, assigned: []string{"operator"},
			want: []string{"anonymous"},
		},
		{
			name: "인증 요청은 {anonymous, member} 를 포함한다", authed: true, assigned: nil,
			want: []string{"anonymous", "member"},
		},
		{
			name: "인증 요청은 user_roles 를 더한다", authed: true, assigned: []string{"editor"},
			want: []string{"anonymous", "member", "editor"},
		},
		{
			name: "암묵 역할이 중복 부여돼도 한 번만", authed: true, assigned: []string{"member", "anonymous", "operator"},
			want: []string{"anonymous", "member", "operator"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EffectiveRoles(tc.authed, tc.assigned)
			if !slices.Equal(got, tc.want) {
				t.Errorf("EffectiveRoles(%v, %v) = %v, want %v", tc.authed, tc.assigned, got, tc.want)
			}
		})
	}
}
