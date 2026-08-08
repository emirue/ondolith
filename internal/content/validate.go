// Package content holds the rules that decide whether input is acceptable and
// how a page moves between states. Everything here is pure: no database, no
// request. That is what lets D19's tables be checked without a server, and it
// keeps the same rule from being written slightly differently in each handler.
package content

import (
	"errors"
	"net/mail"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	ErrEmailFormat    = errors.New("content: 이메일 형식이 올바르지 않습니다")
	ErrPasswordShort  = errors.New("content: 비밀번호가 너무 짧습니다")
	ErrPasswordLong   = errors.New("content: 비밀번호가 너무 깁니다")
	ErrSlugFormat     = errors.New("content: 슬러그는 소문자·숫자·하이픈만 쓸 수 있습니다")
	ErrSlugReserved   = errors.New("content: 예약된 경로와 충돌합니다")
	ErrStatusUnknown  = errors.New("content: 알 수 없는 상태입니다")
	ErrTransitionBase = errors.New("content: 허용되지 않는 상태 전이입니다")
)

// Password bounds. The floor is NFR-208; the ceiling is bcrypt's, not a policy
// choice — bcrypt silently truncates past 72 bytes, so a longer password would
// have its tail ignored while the user believes it counted.
const (
	MinPasswordLen  = 10
	MaxPasswordByte = 72
)

// NormalizeEmail lowercases and trims. Storage and comparison both go through
// here because the schema has no citext: the invariant "emails are stored
// lower-cased" only holds if one function owns it (D30 3절).
func NormalizeEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// ValidateEmail normalizes first, then parses. Returning the normalized form
// means callers cannot accidentally store the raw input.
func ValidateEmail(s string) (string, error) {
	n := NormalizeEmail(s)
	if n == "" {
		return "", ErrEmailFormat
	}
	addr, err := mail.ParseAddress(n)
	if err != nil || addr.Address != n {
		// ParseAddress accepts `Name <a@b>`; a display name in a login field is
		// not an address and would store something nobody can log in with.
		return "", ErrEmailFormat
	}
	return n, nil
}

// ValidatePassword checks length only. D60 deliberately declines composition
// rules: they push people toward `Password1!` and a note on the monitor.
func ValidatePassword(s string) error {
	if utf8.RuneCountInString(s) < MinPasswordLen {
		return ErrPasswordShort
	}
	if len(s) > MaxPasswordByte {
		return ErrPasswordLong
	}
	return nil
}

// slugPattern mirrors the CHECK on boards.slug (D30). Keeping the two in step
// matters because a value that passes here and fails there surfaces as a 500.
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ReservedSlugs are the path segments the router owns. A page whose slug
// collides either shadows a system route or is永 unreachable — both look like
// the page "did not save" to whoever typed it (D19 A-302).
//
// This list is a hand-copy of the routes and D19 records that as an open item:
// it should be generated from the route table, or it drifts as routes are
// added. Until then it is one list, in one place, so the drift is at least
// findable.
var ReservedSlugs = []string{
	"admin", "login", "logout", "signup", "password", "auth", "me", "verify",
	"board", "comments", "attachments", "search", "shop", "cart", "checkout",
	"orders", "webhooks", "static", "install", "sitemap.xml", "robots.txt",
}

var reservedSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(ReservedSlugs))
	for _, s := range ReservedSlugs {
		m[s] = struct{}{}
	}
	return m
}()

// ValidateSlug checks format then collision. Order matters only for the error
// message, but the message is what the operator acts on.
func ValidateSlug(s string) error {
	if !slugPattern.MatchString(s) {
		return ErrSlugFormat
	}
	if _, taken := reservedSet[s]; taken {
		return ErrSlugReserved
	}
	return nil
}

// PageStatus is the closed set from the pages.status CHECK (D30).
type PageStatus string

const (
	StatusDraft     PageStatus = "draft"
	StatusPublished PageStatus = "published"
)

// transitions is the whole state graph: draft ⇄ published and nothing else.
// Written as data rather than as an if-chain so that adding a state is a change
// to this table and not to a condition somebody forgets to extend.
var transitions = map[PageStatus]map[PageStatus]bool{
	StatusDraft:     {StatusPublished: true},
	StatusPublished: {StatusDraft: true},
}

// CanTransition reports whether from → to is allowed. Publishing is a separate
// permission from editing (page.publish vs page.update, D15 2.2), so the
// handler that calls this is not the handler that saves the body — which is
// exactly why the rule lives here instead of inside either of them.
//
// A transition to the same status is refused: it is not a move, and treating it
// as success would let "publish" report done on an already-published page while
// the audit log records a change that did not happen.
func CanTransition(from, to PageStatus) error {
	if _, ok := transitions[from]; !ok {
		return ErrStatusUnknown
	}
	if _, ok := transitions[to]; !ok {
		return ErrStatusUnknown
	}
	if !transitions[from][to] {
		return ErrTransitionBase
	}
	return nil
}

// uuidPattern 은 형식만 본다. 존재 여부는 저장소가 답한다.
var uuidPattern = regexp.MustCompile(
	`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsUUID reports whether s could be a row id.
//
// **형식이 깨진 값과 없는 값은 다르다.** 없는 UUID 는 `WHERE id = $1` 이 0행을
// 내고 핸들러가 404 로 옮긴다. 형식이 깨진 값은 `uuid` 컬럼과 비교되는 순간
// PostgreSQL 이 **22P02** 로 터지고, 그 오류는 어느 도메인 오류와도 일치하지
// 않아 500 이 된다 — 잘못된 입력이 서버 고장으로 보고되고, 로그와 경보를
// 오염시킨다.
//
// 이 저장소에서 같은 부류가 세 번 나왔다: 반품 폼의 빈 `item_id`, 그것을 고친
// **바로 그 커밋에서 새로 만든** 카테고리 삭제, 그리고 장바구니의 `variant_id`.
// 판정을 한 곳에 두는 이유가 그것이다.
func IsUUID(s string) bool { return uuidPattern.MatchString(s) }
