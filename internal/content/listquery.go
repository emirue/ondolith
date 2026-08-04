package content

import (
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ListQuery is a normalised board list request: what P-203 asks for, after the
// values a visitor controls have been clamped to something the database can be
// asked safely.
type ListQuery struct {
	// Sort is a column name that has already been checked against the allow
	// list. It is interpolated into SQL — that is exactly why nothing else may
	// reach it (D22 6절: an allow list, never escaping).
	Sort string
	// Desc is the direction. Kept separate from Sort so the allow list has one
	// entry per column instead of two.
	Desc bool
	// Page is 0-based and bounded.
	Page int
	// PerPage is bounded by the board's setting and by a hard ceiling.
	PerPage int
	// Search is the raw query string; the caller binds it as a parameter.
	Search string
	// After is the keyset cursor. Empty means the first page.
	After Cursor
}

// Cursor is the keyset position D30 measured: (is_pinned, created_at, id), all
// DESC, so the row comparison reaches the index. OFFSET fell to a sequential
// scan plus a 19,020-row sort at page 950; the same query as a keyset was an
// Index Only Scan touching 20 rows.
type Cursor struct {
	Pinned  bool
	Created time.Time
	ID      string
}

func (c Cursor) IsZero() bool { return c.ID == "" }

// sortColumns is the allow list. The value on the right is what goes into the
// ORDER BY clause; the key is what a URL may say.
//
// This is a map and not a `strings.Contains` check on purpose: an allow list
// answers "is this one of the things I wrote down", which stays true when
// somebody adds a column. Escaping answers "does this look dangerous", which
// stops being true the first time it is wrong.
var sortColumns = map[string]string{
	"created": "created_at",
	"views":   "view_count",
	"title":   "title",
}

// SortKeys lists what a URL may ask for, for the screen to render its options.
func SortKeys() []string { return []string{"created", "views", "title"} }

const (
	// MaxPerPage is the ceiling regardless of the board's setting. A visitor
	// asking for 10,000 rows is a denial of service with a query string.
	MaxPerPage = 100
	// MaxPage bounds OFFSET paging. Keyset paging has no such limit, but the
	// screen still offers numbered pages and a five-digit page number is a
	// crawler, not a reader.
	MaxPage = 1000
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
		if _, ok := sortColumns[key]; ok {
			out.Sort = key
			// A leading '-' is descending. Title reads better ascending, so an
			// explicit direction wins and the default follows the column.
			out.Desc = strings.HasPrefix(s, "-") || key != "title"
		}
	}

	if n, err := strconv.Atoi(q.Get("page")); err == nil {
		switch {
		case n < 1:
			out.Page = 0
		case n > MaxPage:
			out.Page = MaxPage - 1
		default:
			out.Page = n - 1 // URLs are 1-based, offsets are not
		}
	}

	out.PerPage = boardPerPage
	if n, err := strconv.Atoi(q.Get("per_page")); err == nil && n > 0 {
		out.PerPage = n
	}
	if out.PerPage < 1 {
		out.PerPage = 20
	}
	if out.PerPage > MaxPerPage {
		out.PerPage = MaxPerPage
	}

	out.Search = strings.TrimSpace(q.Get("q"))
	if r := []rune(out.Search); len(r) > MaxSearchRunes {
		out.Search = string(r[:MaxSearchRunes])
	}

	out.After = parseCursor(q.Get("after"))
	return out
}

// OrderBy renders the ORDER BY clause.
//
// The column comes from the allow list, so it is a constant this package wrote,
// not request text. Pinned posts lead regardless of the sort — that is what
// pinning means — and id is the tiebreaker that makes the keyset comparison
// total (D30).
func (l ListQuery) OrderBy() string {
	col, ok := sortColumns[l.Sort]
	if !ok {
		// Unreachable through ParseListQuery. Kept as a floor because this
		// string reaches SQL: a future caller building a ListQuery by hand must
		// not be able to choose the column.
		col = "created_at"
	}
	dir := "ASC"
	if l.Desc {
		dir = "DESC"
	}
	return "is_pinned DESC, " + col + " " + dir + ", id " + dir
}

// Offset is for the numbered-page path. Keyset paging uses After instead.
func (l ListQuery) Offset() int { return l.Page * l.PerPage }

// parseCursor reads `after` as pinned:unixnano:id. A malformed cursor is no
// cursor: it means the first page, which is the same thing a visitor sees on a
// link that has gone stale.
func parseCursor(s string) Cursor {
	if s == "" {
		return Cursor{}
	}
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 || parts[2] == "" {
		return Cursor{}
	}
	nano, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return Cursor{}
	}
	return Cursor{
		Pinned:  parts[0] == "1",
		Created: time.Unix(0, nano).UTC(),
		ID:      parts[2],
	}
}

// String renders a cursor for the next-page link.
func (c Cursor) String() string {
	if c.IsZero() {
		return ""
	}
	p := "0"
	if c.Pinned {
		p = "1"
	}
	return p + ":" + strconv.FormatInt(c.Created.UnixNano(), 10) + ":" + c.ID
}
