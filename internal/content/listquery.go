package content

import (
	"net/url"
	"strconv"
	"strings"
)

// ListQuery is a normalised board list request: what P-203 asks for, after the
// values a visitor controls have been clamped to something the database can be
// asked safely.
type ListQuery struct {
	// Sort is a key that has already been checked against the allow list. It
	// reaches SQL as a bind parameter that an ORDER BY CASE chain compares
	// (D22 6절) — the column names live in queries/post.sql, not here.
	Sort string
	// Desc is the direction. Kept separate from Sort so the allow list has one
	// entry per column instead of two.
	Desc bool
	// Page is 0-based and bounded.
	Page int
	// PerPage is bounded by the board's setting and by a hard ceiling.
	PerPage int
	// perPageAsked is whether the URL chose PerPage — then the pager's links
	// carry it, or page 4 of 50 rows would link to page 5 of 20.
	perPageAsked bool
	// Search is the raw query string; the caller binds it as a parameter.
	Search string
	// ViewerID·CanSecret 은 **검색에서** 읽을 수 없는 비밀글을 빼는 데 쓴다.
	// URL 에서 오지 않는다 — 핸들러가 행위자로 채운다. 비워 두면 익명·권한 없음,
	// 즉 남의 비밀글이 검색에 걸리지 않는 쪽이다(닫힌 기본값).
	ViewerID  string
	CanSecret bool
}

// sortKeys is the allow list: what a URL may say. Each key is one CASE branch
// in the ORDER BY of queries/post.sql (ListPosts·ListPostsSearch·SearchPosts);
// a key not in that chain sorts nothing, so the set here and the chain there
// must name the same keys.
//
// This is a set and not a `strings.Contains` check on purpose: an allow list
// answers "is this one of the things I wrote down", which stays true when
// somebody adds a key. Escaping answers "does this look dangerous", which
// stops being true the first time it is wrong.
var sortKeys = map[string]bool{
	"created": true,
	"views":   true,
	"title":   true,
}

// SortKeys lists what a URL may ask for, for the screen to render its options.
func SortKeys() []string { return []string{"created", "views", "title"} }

const (
	// MaxPerPage is the ceiling regardless of the board's setting. A visitor
	// asking for 10,000 rows is a denial of service with a query string.
	MaxPerPage = 100
	// MaxPage bounds the lists that do not count their rows (상품 목록·상품 검색·
	// 주문 내역, 「한 행 더 읽어」 다음을 안다). A three-digit page number there is
	// a crawler, not a reader.
	MaxPage = 100
	// MaxListPage bounds the counted lists (게시판 P-203·통합 검색 P-212) only so
	// that OFFSET stays inside int32 at MaxPerPage. Their pager numbers every page,
	// and a reader looking for an old post walks back as far as the board goes.
	MaxListPage = 1_000_000
	// MaxSearchRunes bounds the search term. A megabyte of text in a tsquery is
	// work the database does before it can refuse.
	MaxSearchRunes = 100
)

// ParseListQuery normalises a request.
//
// Out-of-range values are CLAMPED, not refused (W2-08). A visitor who edits the
// page number into nonsense should see the last page, not an error — the error
// teaches nothing and the clamp is what every crawler and stale bookmark needs.
// An unknown sort key falls back to the default rather than failing for the
// same reason.
//
// boardPerPage is the board's own setting; 0 means "not configured".
func ParseListQuery(q url.Values, boardPerPage int) ListQuery {
	out := ListQuery{Sort: "created", Desc: true}

	if s := q.Get("sort"); s != "" {
		key := strings.TrimPrefix(s, "-")
		if sortKeys[key] {
			out.Sort = key
			// A leading '-' is descending. Title reads better ascending, so an
			// explicit direction wins and the default follows the column.
			out.Desc = strings.HasPrefix(s, "-") || key != "title"
		}
	}

	out.Search = strings.TrimSpace(q.Get("q"))
	if r := []rune(out.Search); len(r) > MaxSearchRunes {
		out.Search = string(r[:MaxSearchRunes])
	}

	if n, err := strconv.Atoi(q.Get("page")); err == nil {
		switch {
		case n < 1:
			out.Page = 0
		case n > out.MaxPages():
			out.Page = out.MaxPages() - 1
		default:
			out.Page = n - 1 // URLs are 1-based, offsets are not
		}
	}

	out.PerPage = boardPerPage
	if n, err := strconv.Atoi(q.Get("per_page")); err == nil && n > 0 {
		out.PerPage, out.perPageAsked = n, true
	}
	if out.PerPage < 1 {
		out.PerPage = 20
	}
	if out.PerPage > MaxPerPage {
		out.PerPage = MaxPerPage
	}

	return out
}

// PerPageParam is the `per_page` value for the pager's links — "" when the URL
// left it to the board's setting.
func (l ListQuery) PerPageParam() string {
	if !l.perPageAsked {
		return ""
	}
	return strconv.Itoa(l.PerPage)
}

// MaxPages is the last page number this list may ask for. A list with a search
// term keeps MaxPage: its OFFSET still sorts every match it skips (본문까지 —
// 검색 질의는 인덱스만으로 쪽을 고르지 못한다), so a deep page of a common word
// would sort them all.
func (l ListQuery) MaxPages() int {
	if l.Search != "" {
		return MaxPage
	}
	return MaxListPage
}

// sortKey is what the query's ORDER BY CASE chain is handed.
//
// Unknown keys are unreachable through ParseListQuery. Kept as a floor because
// a future caller building a ListQuery by hand must not be able to turn the
// sort off: a key the chain does not name matches no CASE, and the list would
// come back in pinned-then-id order with nobody noticing.
func (l ListQuery) sortKey() string {
	if sortKeys[l.Sort] {
		return l.Sort
	}
	return "created"
}

// Offset is the rows before this page.
func (l ListQuery) Offset() int { return l.Page * l.PerPage }

// SortParam is the `sort` value ParseListQuery reads back as this sort, for the
// pager's links — "" for the default. Without it the next page falls back to
// newest-first.
func (l ListQuery) SortParam() string {
	switch key := l.sortKey(); {
	case key == "created" && l.Desc:
		return ""
	case key == "title" && l.Desc:
		return "-title"
	default:
		return key
	}
}
