package content

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateEmail(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  string
		wantE error
	}{
		{"소문자화", "Admin@Example.COM", "admin@example.com", nil},
		{"앞뒤 공백 제거", "  a@b.com  ", "a@b.com", nil},
		{"정상", "user+tag@example.co.kr", "user+tag@example.co.kr", nil},
		{"빈 값", "", "", ErrEmailFormat},
		{"@ 없음", "nobody", "", ErrEmailFormat},
		{"도메인 없음", "a@", "", ErrEmailFormat},
		// `Name <a@b>` parses as an address but is not one. Storing it gives an
		// account nobody can log into.
		{"표시 이름이 붙은 형태", "홍길동 <a@b.com>", "", ErrEmailFormat},
		{"공백 포함", "a b@c.com", "", ErrEmailFormat},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateEmail(tc.in)
			if !errors.Is(err, tc.wantE) {
				t.Fatalf("err = %v, want %v", err, tc.wantE)
			}
			if got != tc.want {
				t.Errorf("= %q, want %q", got, tc.want)
			}
		})
	}
}

// Normalization must be idempotent: it runs on the way in and again on lookup,
// and a second pass that changes the value would make login fail for exactly
// the accounts that needed normalizing.
func TestNormalizeEmailIsIdempotent(t *testing.T) {
	for _, in := range []string{"A@B.COM", " x@y.z ", "already@lower.com"} {
		once := NormalizeEmail(in)
		if twice := NormalizeEmail(once); twice != once {
			t.Errorf("%q: 1회 %q, 2회 %q", in, once, twice)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want error
	}{
		{"정확히 최소 길이", strings.Repeat("a", MinPasswordLen), nil},
		{"최소 미만", strings.Repeat("a", MinPasswordLen-1), ErrPasswordShort},
		{"충분히 김", "correct-horse-battery-staple", nil},
		// bcrypt truncates past 72 bytes; accepting a longer one would ignore
		// the tail while the user believes it counted.
		{"72바이트 초과", strings.Repeat("a", 73), ErrPasswordLong},
		{"정확히 72바이트", strings.Repeat("a", 72), nil},
		// 10 Korean runes are 30 bytes: the floor counts runes, the ceiling
		// counts bytes, and mixing them up rejects valid passwords.
		{"한글 10자", strings.Repeat("가", 10), nil},
		{"한글 9자", strings.Repeat("가", 9), ErrPasswordShort},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidatePassword(tc.in); !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestValidateSlug(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want error
	}{
		{"정상", "about-us", nil},
		{"숫자 시작", "2026-notice", nil},
		{"대문자", "About", ErrSlugFormat},
		{"공백", "about us", ErrSlugFormat},
		{"경로 문자", "a/b", ErrSlugFormat},
		{"상위 경로", "..", ErrSlugFormat},
		{"하이픈 시작", "-about", ErrSlugFormat},
		{"빈 값", "", ErrSlugFormat},
		{"밑줄", "about_us", ErrSlugFormat},
		// A page at /admin would shadow the admin tree; one at /login would be
		// unreachable. Both read as "it did not save".
		{"예약 경로 admin", "admin", ErrSlugReserved},
		{"예약 경로 login", "login", ErrSlugReserved},
		{"예약 경로 sitemap.xml", "sitemap.xml", ErrSlugFormat},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateSlug(tc.in); !errors.Is(err, tc.want) {
				t.Errorf("ValidateSlug(%q) = %v, want %v", tc.in, err, tc.want)
			}
		})
	}
}

// Every reserved slug must be rejected by ValidateSlug — otherwise an entry in
// the list is decoration. `sitemap.xml` and `robots.txt` contain a dot and are
// caught by the format rule first, which is still a rejection.
func TestEveryReservedSlugIsRejected(t *testing.T) {
	for _, s := range ReservedSlugs {
		if err := ValidateSlug(s); err == nil {
			t.Errorf("예약 경로 %q 가 통과했다", s)
		}
	}
}

func TestCanTransition(t *testing.T) {
	tests := []struct {
		name     string
		from, to PageStatus
		want     error
	}{
		{"초안 → 발행", StatusDraft, StatusPublished, nil},
		{"발행 → 초안 (발행 취소)", StatusPublished, StatusDraft, nil},
		// Not a move. Reporting success would put a change in the audit log
		// that never happened.
		{"초안 → 초안", StatusDraft, StatusDraft, ErrTransitionBase},
		{"발행 → 발행", StatusPublished, StatusPublished, ErrTransitionBase},
		{"알 수 없는 출발 상태", PageStatus("archived"), StatusDraft, ErrStatusUnknown},
		{"알 수 없는 도착 상태", StatusDraft, PageStatus("deleted"), ErrStatusUnknown},
		{"빈 상태", PageStatus(""), StatusDraft, ErrStatusUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := CanTransition(tc.from, tc.to); !errors.Is(err, tc.want) {
				t.Errorf("CanTransition(%q, %q) = %v, want %v", tc.from, tc.to, err, tc.want)
			}
		})
	}
}

// The status set must match the CHECK in D30 exactly. A third status here would
// pass this package and fail the insert.
func TestStatusSetMatchesSchema(t *testing.T) {
	want := map[PageStatus]bool{StatusDraft: true, StatusPublished: true}
	if len(transitions) != len(want) {
		t.Fatalf("상태 %d종, want %d종 (D30 pages.status CHECK)", len(transitions), len(want))
	}
	for s := range transitions {
		if !want[s] {
			t.Errorf("D30 CHECK 에 없는 상태: %q", s)
		}
	}
}
