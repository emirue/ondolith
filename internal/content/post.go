package content

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/emirue/ondolith/internal/content/contentq"
)

// Post is one row of posts, plus the counts a list screen shows.
type Post struct {
	ID           string
	BoardID      string
	AuthorID     string // empty when the author is gone (SET NULL)
	AuthorName   string
	Title        string
	Body         string
	CustomFields map[string]any
	Status       string
	IsPinned     bool
	IsSecret     bool
	// int64 because D17 의 number 함수가 int64 를 받는다. 뷰 모델에서 변환하면
	// 그 변환을 잊은 화면만 조용히 다른 형식으로 나온다.
	ViewCount     int64
	CommentCount  int64
	HasAttachment bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Comment is one row of comments. A tombstone has DeletedAt set and an empty
// body — the database enforces that pairing (D30).
type Comment struct {
	ID         string
	PostID     string
	ParentID   string
	AuthorID   string
	AuthorName string
	Body       string
	DeletedAt  time.Time
	CreatedAt  time.Time
}

func (c Comment) IsTombstone() bool { return !c.DeletedAt.IsZero() }

// ListPosts reads one page.
//
// NFR-105: ONE query, whatever the page holds. The comment count and the
// attachment flag are lateral subqueries rather than a second round trip per
// row — that is the N+1 the requirement names, and it only shows up once a
// board has enough posts that nobody is testing on it any more.
//
// Secret posts ARE listed (FR-512, W2-24): the title, author and date show, and
// the body is what post.read_secret protects — PostByID refuses that. A board
// that hid them entirely would be unusable for the case they exist for, a Q&A
// board where you need to see your question is in the queue.
//
// So there is no viewer here and no canSecret: this query has no permission
// decision left to make. Search is the one that keeps the filter, because its
// results carry an excerpt of the body.
func (s *Store) ListPosts(ctx context.Context, boardID string, q ListQuery) ([]Post, error) {
	// The ORDER BY comes from the allow list (listquery.go). Everything else is
	// a bind parameter.
	//
	// **검색 절은 검색어가 있을 때만 붙는다.** `($2 = '' OR … @@ …)` 한 줄로
	// 두면 준비된 문장이 일반 계획으로 넘어간 뒤 OR 의 한쪽이 인덱스를 못 타
	// GIN 인덱스(posts_search_idx)가 버려지고, 게시판 전체를 훑으며 @@ 를
	// 평가한다 — SearchPosts 가 같은 조건을 OR 없이 쓰는 이유다. 두 문장이
	// 머리를 공유하지 않고 통째로 적혀 있는 것은 post_shape_test 가 문자열
	// 리터럴만 읽기 때문이다: 변수로 합치면 그 검사가 이 질의를 보지 못한다.
	if q.Search == "" {
		rows, err := s.pool.Query(ctx, `
			SELECT `+postListColumns+`
			FROM posts p
			LEFT JOIN users u ON u.id = p.author_id
			WHERE p.board_id = $1
			  AND p.status = 'published'
			ORDER BY `+q.OrderBy()+`
			LIMIT $2 OFFSET $3`, boardID, q.PerPage, q.Offset())
		if err != nil {
			return nil, err
		}
		return scanPosts(rows)
	}
	// A prefix query is what actually matches Korean text: the stored token
	// carries the particle, so the exact term misses (D30 measured this).
	rows, err := s.pool.Query(ctx, `
		SELECT `+postListColumns+`
		FROM posts p
		LEFT JOIN users u ON u.id = p.author_id
		WHERE p.board_id = $1
		  AND p.status = 'published'
		  AND p.search_vector @@ to_tsquery('simple', $2)
		ORDER BY `+q.OrderBy()+`
		LIMIT $3 OFFSET $4`, boardID, toPrefixQuery(q.Search), q.PerPage, q.Offset())
	if err != nil {
		return nil, err
	}
	return scanPosts(rows)
}

// CountPosts is the total for the pager. It counts exactly what ListPosts
// returns — a pager whose total disagrees with its pages tells the visitor
// there is a page that is not there. 검색 절은 ListPosts 와 같은 이유로 조건부다.
func (s *Store) CountPosts(ctx context.Context, boardID string, q ListQuery) (int64, error) {
	if q.Search == "" {
		return s.q.CountPosts(ctx, boardID)
	}
	return s.q.CountPostsSearch(ctx, contentq.CountPostsSearchParams{
		BoardID: boardID, Search: toPrefixQuery(q.Search)})
}

// SitemapEntry is all P-901 needs of a post: where it lives and when it changed.
type SitemapEntry struct {
	ID        string
	BoardID   string
	UpdatedAt time.Time
}

// SitemapPosts returns each board's newest published, non-secret posts — up to
// perBoard each — in ONE query. 게시판마다 ListPosts 를 부르면 게시판 수만큼의
// 왕복에 본문·커스텀 필드·댓글 수까지 실어 나른다; 크롤러가 자주 찾는 주소에
// 그 값은 쓰이지 않는다.
func (s *Store) SitemapPosts(ctx context.Context, boardIDs []string, perBoard int) ([]SitemapEntry, error) {
	if len(boardIDs) == 0 || perBoard <= 0 {
		return nil, nil
	}
	rows, err := s.q.SitemapPosts(ctx, contentq.SitemapPostsParams{
		BoardIds: boardIDs, PerBoard: int32(perBoard)})
	if err != nil {
		return nil, err
	}
	var out []SitemapEntry
	for _, r := range rows {
		out = append(out, SitemapEntry{ID: r.ID, BoardID: r.BoardID, UpdatedAt: r.UpdatedAt})
	}
	return out, nil
}

// PostByID reads one post. Secret posts are filtered in SQL, not after the
// fetch: a row that must not be shown should not leave the database (SC-1 4항).
func (s *Store) PostByID(ctx context.Context, id, viewerID string, canSecret bool) (*Post, error) {
	const q = `
		SELECT ` + postColumns + `
		FROM posts p
		LEFT JOIN users u ON u.id = p.author_id
		WHERE p.id = $1 AND ($2 OR NOT p.is_secret OR p.author_id = $3)`
	p, err := scanPost(s.pool.QueryRow(ctx, q, id, canSecret, nullIfEmpty(viewerID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) CreatePost(ctx context.Context, p Post) (string, error) {
	fields, err := marshalFields(p.CustomFields)
	if err != nil {
		return "", err
	}
	return s.q.CreatePost(ctx, contentq.CreatePostParams{
		BoardID: p.BoardID, AuthorID: strPtr(p.AuthorID), Title: p.Title, Body: p.Body,
		CustomFields: fields, IsSecret: p.IsSecret})
}

// UpdatePost does not touch is_pinned or status: pinning and hiding are
// post.moderate, and letting an edit carry them would hand the author the
// moderator's power (the same split page.update/page.publish has).
func (s *Store) UpdatePost(ctx context.Context, id string, p Post) error {
	fields, err := marshalFields(p.CustomFields)
	if err != nil {
		return err
	}
	return affected(s.q.UpdatePost(ctx, contentq.UpdatePostParams{
		ID: id, Title: p.Title, Body: p.Body, CustomFields: fields, IsSecret: p.IsSecret}))
}

// SetPostFlags is the moderator's edit (A-307).
func (s *Store) SetPostFlags(ctx context.Context, id string, pinned bool, status string) error {
	if status != "published" && status != "hidden" {
		return errors.New("content: 알 수 없는 글 상태")
	}
	return affected(s.q.SetPostFlags(ctx, contentq.SetPostFlagsParams{
		ID: id, IsPinned: pinned, Status: status}))
}

// DeletePost 는 글을 물리 삭제한다 (OPEN-40 결정, D30 3절).
//
// **첨부 실물은 지우지 않는다** — 이 타입은 업로드 경로를 모른다. 파일까지
// 지우는 것은 `Attachments.DeletePost` 이고, 화면은 그쪽을 부른다. 이 메서드는
// 첨부를 쓰지 않는 경로(P-210 툼스톤 정리 등)만 쓴다.
func (s *Store) DeletePost(ctx context.Context, id string) error {
	return affected(s.q.DeletePost(ctx, id))
}

// BumpViewCount increases the counter.
//
// SC-1 3항 allows this write on a GET because it carries no permission
// decision. Duplicate suppression is the caller's (the session remembers what
// it has already counted) — putting it here would need the store to know about
// sessions, and FR-305 keeps that out.
func (s *Store) BumpViewCount(ctx context.Context, id string) error {
	return s.q.BumpViewCount(ctx, id)
}

// commentRow is the shape Comments and ModerateComments share (fieldRow 와
// 같은 수법 — 질의마다 다른 생성 타입을 구조체 변환으로 한 곳에 모은다).
type commentRow struct {
	ID         string
	PostID     string
	ParentID   string
	AuthorID   string
	AuthorName string
	Body       string
	DeletedAt  time.Time
	CreatedAt  time.Time
}

// commentOf is the one place a comments row becomes a Comment. The query
// coalesces deleted_at to the epoch, so the epoch means "not deleted".
func commentOf(r commentRow) Comment {
	c := Comment{ID: r.ID, PostID: r.PostID, ParentID: r.ParentID, AuthorID: r.AuthorID,
		AuthorName: r.AuthorName, Body: r.Body, CreatedAt: r.CreatedAt}
	if !r.DeletedAt.Equal(time.Unix(0, 0).UTC()) {
		c.DeletedAt = r.DeletedAt
	}
	return c
}

// Comments reads a post's comments in ONE query, ordered so that a one-level
// reply tree can be assembled in memory. D30 caps replies at one level
// (parent_id is set at insert and no screen changes it), so no recursion.
func (s *Store) Comments(ctx context.Context, postID string) ([]Comment, error) {
	rows, err := s.q.Comments(ctx, postID)
	if err != nil {
		return nil, err
	}
	var out []Comment
	for _, r := range rows {
		out = append(out, commentOf(commentRow(r)))
	}
	return out, nil
}

func (s *Store) CreateComment(ctx context.Context, c Comment) (string, error) {
	return s.q.CreateComment(ctx, contentq.CreateCommentParams{
		PostID: c.PostID, ParentID: strPtr(c.ParentID), AuthorID: strPtr(c.AuthorID), Body: c.Body})
}

// DeleteComment is two-branched because the foreign key makes it so (D30).
//
// A comment with replies cannot be deleted — parent_id is NO ACTION, so the
// database refuses. That refusal is what produces the tombstone: the row stays,
// the body is emptied in the DATABASE (not hidden by a template `if`, because
// themes are third-party), and deleted_at marks it.
//
// 세어 보고 지우지 않는다. 세는 문장과 지우는 문장 사이에 답글이 달리면 DELETE
// 가 외래키에 걸려 500 이었다 — 외래키의 거절 자체가 판정이므로 먼저 지워 보고,
// 23503 이면 묘비로 간다.
func (s *Store) DeleteComment(ctx context.Context, id string) error {
	n, err := s.q.DeleteComment(ctx, id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return affected(s.q.TombstoneComment(ctx, id))
	}
	return affected(n, err)
}

func marshalFields(m map[string]any) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

// nullIfEmpty turns "" into a SQL NULL. An empty string in a uuid column is a
// type error at best and a row nobody can join at worst.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// toPrefixQuery builds the tsquery. Every term gets `:*` because the stored
// token carries the Korean particle — `게시판` misses a body that says
// `게시판을`, and `게시판:*` finds it (D30 measured both).
func toPrefixQuery(search string) string {
	var out []byte
	term := false
	for _, r := range search {
		// Only letters and digits survive: everything else is tsquery syntax,
		// and a visitor typing `&` or `!` must not compose a query.
		if isWordRune(r) {
			if !term && len(out) > 0 {
				out = append(out, " & "...)
			}
			out = append(out, string(r)...)
			term = true
			continue
		}
		if term {
			out = append(out, ":*"...)
			term = false
		}
	}
	if term {
		out = append(out, ":*"...)
	}
	return string(out)
}

func isWordRune(r rune) bool {
	switch {
	case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		return true
	case r > 0x7f:
		// Hangul, CJK and everything else non-ASCII. tsquery operators are all
		// ASCII, so nothing above this point can be one.
		return true
	}
	return false
}

// CommentByID reads one comment.
func (s *Store) CommentByID(ctx context.Context, id string) (*Comment, error) {
	r, err := s.q.CommentByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c := commentOf(commentRow{ID: r.ID, PostID: r.PostID, ParentID: r.ParentID,
		AuthorID: r.AuthorID, Body: r.Body, DeletedAt: r.DeletedAt, CreatedAt: r.CreatedAt})
	return &c, nil
}

// UpdateComment changes the body and nothing else. A tombstone is excluded in
// the WHERE clause: bringing back a body the author removed is the one edit
// that must not be possible.
func (s *Store) UpdateComment(ctx context.Context, id, body string) error {
	return affected(s.q.UpdateComment(ctx, contentq.UpdateCommentParams{ID: id, Body: body}))
}

// BoardByPost finds the board a post belongs to. P-209 and P-210 have no slug
// in their path, so the board — and its permission — is reached this way.
func (s *Store) BoardByPost(ctx context.Context, postID string) (*Board, error) {
	return oneBoard(s.q.BoardByPost(ctx, postID))
}

// SearchPosts is P-212. The readable board ids come from the caller; a board
// that is not in that list contributes nothing, because it is not in the WHERE
// clause (FR-510).
//
// Two lists rather than one flag: `post.read` and `post.read_secret` are
// granted per board, so "may I see secret posts here" has a different answer on
// each board a caller can read.
func (s *Store) SearchPosts(ctx context.Context, readable, secretIn []string,
	q ListQuery, viewerID string,
) ([]Post, error) {
	// 검사가 아니라 왕복 절약이다. 빈 목록은 `ANY('{}')` 로, 빈 검색어는
	// 빈 tsquery 로 어차피 0행이 된다 — 이 두 줄을 지워도 결과는 같고,
	// 그래서 여기에 규칙이 있는 척하지 않는다.
	if len(readable) == 0 || q.Search == "" {
		return nil, nil
	}
	sql := `
		SELECT ` + postColumns + `
		FROM posts p
		LEFT JOIN users u ON u.id = p.author_id
		WHERE p.board_id = ANY($1)
		  AND p.status = 'published'
		  AND (NOT p.is_secret OR p.board_id = ANY($2) OR p.author_id = $3)
		  AND p.search_vector @@ to_tsquery('simple', $4)
		ORDER BY ` + q.OrderBy() + `
		LIMIT $5 OFFSET $6`
	rows, err := s.pool.Query(ctx, sql, readable, secretIn,
		nullIfEmpty(viewerID), toPrefixQuery(q.Search), q.PerPage, q.Offset())
	if err != nil {
		return nil, err
	}
	return scanPosts(rows)
}

func (s *Store) CountSearchPosts(ctx context.Context, readable, secretIn []string,
	q ListQuery, viewerID string,
) (int64, error) {
	if len(readable) == 0 || q.Search == "" {
		return 0, nil // 위와 같은 이유 — 절약이지 검사가 아니다
	}
	return s.q.CountSearchPosts(ctx, contentq.CountSearchPostsParams{
		Readable: readable, SecretIn: secretIn, ViewerID: strPtr(viewerID),
		Search: toPrefixQuery(q.Search)})
}

// ModeratePosts is A-307's list: everything on one board, hidden and secret
// included. That is what moderating means — a moderator who cannot see the
// hidden post cannot un-hide it.
func (s *Store) ModeratePosts(ctx context.Context, boardID string, limit int) ([]Post, error) {
	const q = `
		SELECT ` + postListColumns + `
		FROM posts p
		LEFT JOIN users u ON u.id = p.author_id
		WHERE p.board_id = $1
		ORDER BY p.is_pinned DESC, p.created_at DESC, p.id DESC
		LIMIT $2`
	rows, err := s.pool.Query(ctx, q, boardID, limit)
	if err != nil {
		return nil, err
	}
	return scanPosts(rows)
}

// ModerateComments is A-308's list, newest first across one board.
func (s *Store) ModerateComments(ctx context.Context, boardID string, limit int) ([]Comment, error) {
	rows, err := s.q.ModerateComments(ctx, contentq.ModerateCommentsParams{
		BoardID: boardID, Limit: int32(limit)})
	if err != nil {
		return nil, err
	}
	var out []Comment
	for _, r := range rows {
		out = append(out, commentOf(commentRow(r)))
	}
	return out, nil
}

// postColumns is the SELECT list every post listing uses.
//
// 목록마다 베껴 적으면 컬럼 하나를 더할 때 한 곳을 빠뜨리고, 그 화면만 조용히
// 옛 모양으로 남는다 — 스캔 순서가 어긋나면 그때는 런타임 오류다.
//
// sqlc 불가: posts 를 Post 로 읽는 여섯 질의(ListPosts 둘·PostByID·SearchPosts·
// ModeratePosts·RecentPosts)와 scanPost 는 post_shape_test 가 이 파일의 AST 와
// 본문을 읽어 「postColumns 를 쓰는 SELECT 다섯 이상 + Scan 한 곳」을 요구한다.
// queries/post.sql 로 옮기면 그 검사가 질의를 못 찾아 실패한다 — 생성 코드는
// 열 목록과 스캔을 한 쌍으로 만들어 그 검사가 지키던 것을 타입으로 보장하므로,
// 검사를 거두는 커밋에서 이 여섯과 ListQuery.OrderBy 의 허용 목록을 함께 옮긴다
// (정렬은 D22 6절의 `ORDER BY CASE WHEN sqlc.arg('sort') = …` 로).
const postColumns = `
	p.id, p.board_id, coalesce(p.author_id::text, ''), coalesce(u.display_name, ''),
	p.title, p.body, p.custom_fields, p.status, p.is_pinned, p.is_secret,
	p.view_count, p.created_at, p.updated_at,
	(SELECT count(*) FROM comments c WHERE c.post_id = p.id),
	EXISTS (SELECT 1 FROM attachments a WHERE a.post_id = p.id)`

// postListColumns is postColumns with the body left out. 목록·최근 글·중재 목록은
// 본문을 그리지 않는데, 페이지마다 최대 100개의 본문이 DB→앱→GC 를 오갔다.
// 모양은 같아서 scanPost 가 그대로 읽는다.
const postListColumns = `
	p.id, p.board_id, coalesce(p.author_id::text, ''), coalesce(u.display_name, ''),
	p.title, '' AS body, p.custom_fields, p.status, p.is_pinned, p.is_secret,
	p.view_count, p.created_at, p.updated_at,
	(SELECT count(*) FROM comments c WHERE c.post_id = p.id),
	EXISTS (SELECT 1 FROM attachments a WHERE a.post_id = p.id)`

// postScanner is what pgx.Row and pgx.Rows share — 한 행을 읽는 것.
type postScanner interface{ Scan(dest ...any) error }

// scanPost reads one row shaped by postColumns.
//
// **컬럼 목록과 스캔 순서는 한 쌍이다.** 둘이 갈라지면 컴파일은 되고 런타임에
// 타입 오류가 나거나, 더 나쁘게는 같은 타입끼리 자리가 바뀌어 조용히 틀린
// 값이 나온다. 그래서 목록도 스캔도 각각 한 곳에만 둔다.
func scanPost(row postScanner) (Post, error) {
	var p Post
	var raw []byte
	if err := row.Scan(&p.ID, &p.BoardID, &p.AuthorID, &p.AuthorName,
		&p.Title, &p.Body, &raw, &p.Status, &p.IsPinned, &p.IsSecret,
		&p.ViewCount, &p.CreatedAt, &p.UpdatedAt,
		&p.CommentCount, &p.HasAttachment); err != nil {
		return p, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p.CustomFields); err != nil {
			return p, err
		}
	}
	return p, nil
}

// scanPosts reads rows shaped by postColumns.
func scanPosts(rows pgx.Rows) ([]Post, error) {
	defer rows.Close()
	var out []Post
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RecentPosts lists the newest published posts across the boards the caller may
// read — P-201 의 「최근 글」이다.
//
// **권한 술어가 검색과 같다.** 홈이 자기 조건을 따로 쓰면 그 한 줄이 어긋나는
// 날 비공개 게시판 글이 첫 화면에 뜬다 (D12 P-201). 읽을 수 있는 게시판이
// 없으면 빈 목록이다 — 「전부」로 읽지 않는다.
func (s *Store) RecentPosts(ctx context.Context, readable, secretIn []string,
	viewerID string, limit int,
) ([]Post, error) {
	if len(readable) == 0 || limit <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+postListColumns+`
		FROM posts p
		LEFT JOIN users u ON u.id = p.author_id
		WHERE p.board_id = ANY($1)
		  AND p.status = 'published'
		  AND (NOT p.is_secret OR p.board_id = ANY($2) OR p.author_id = $3)
		ORDER BY p.created_at DESC
		LIMIT $4`, readable, secretIn, nullIfEmpty(viewerID), limit)
	if err != nil {
		return nil, err
	}
	return scanPosts(rows)
}
