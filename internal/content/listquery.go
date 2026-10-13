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
// in the ORDER BY of queries/post.sql (ListPosts, the two search shapes of each search);
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
	// MaxListPage bounds the board list without a search term (P-203) only so
	// that OFFSET stays inside int32 at MaxPerPage. A search stops at
	// SearchCountCap instead (MaxPages). Their pager numbers every page,
	// and a reader looking for an old post walks back as far as the board goes.
	MaxListPage = 1_000_000
	// SearchCountCap is how far a search counts its matches. 흔한 낱말은 맞는 글이
	// 수십만이고 다 세면 표를 끝까지 읽는다(100만 건에서 960ms). 넘으면 화면이
	// 「10,000건 이상」이라 쓰고, 쪽도 거기까지다 — 그 너머는 검색어를 좁힐 일이다.
	SearchCountCap = 10_000
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

	// 쪽 크기를 쪽 번호보다 먼저 정한다 — 검색의 쪽 상한(MaxPages)이 쪽 크기에 달렸다.
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

// MaxPages is the last page number this list may ask for. A search counts its
// matches only up to SearchCountCap, so its pages end there.
func (l ListQuery) MaxPages() int {
	if l.Search != "" {
		per := max(l.PerPage, 1) // 손으로 만든 ListQuery 에서도 0 으로 나누지 않는다
		return max(1, (SearchCountCap+per-1)/per)
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
