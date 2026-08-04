package content

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Post is one row of posts, plus the counts a list screen shows.
type Post struct {
	ID            string
	BoardID       string
	AuthorID      string // empty when the author is gone (SET NULL)
	AuthorName    string
	Title         string
	Body          string
	CustomFields  map[string]any
	Status        string
	IsPinned      bool
	IsSecret      bool
	ViewCount     int
	CommentCount  int
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
// canSecret decides whether other people's secret posts are visible. It is a
// parameter and not a filter the caller bolts on afterwards: a WHERE clause
// that is not in the statement cannot be forgotten by the next caller.
func (s *Store) ListPosts(ctx context.Context, boardID string, q ListQuery,
	viewerID string, canSecret bool,
) ([]Post, error) {
	// The ORDER BY comes from the allow list (listquery.go). Everything else is
	// a bind parameter.
	sql := `
		SELECT p.id, p.board_id, coalesce(p.author_id::text, ''), coalesce(u.display_name, ''),
		       p.title, p.body, p.custom_fields, p.status, p.is_pinned, p.is_secret,
		       p.view_count, p.created_at, p.updated_at,
		       (SELECT count(*) FROM comments c WHERE c.post_id = p.id),
		       EXISTS (SELECT 1 FROM attachments a WHERE a.post_id = p.id)
		FROM posts p
		LEFT JOIN users u ON u.id = p.author_id
		WHERE p.board_id = $1
		  AND p.status = 'published'
		  AND ($2 OR NOT p.is_secret OR p.author_id = $3)
		  AND ($4 = '' OR p.search_vector @@ to_tsquery('simple', $5))
		ORDER BY ` + q.OrderBy() + `
		LIMIT $6 OFFSET $7`

	// A prefix query is what actually matches Korean text: the stored token
	// carries the particle, so the exact term misses (D30 measured this).
	tsq := ""
	if q.Search != "" {
		tsq = toPrefixQuery(q.Search)
	}

	rows, err := s.pool.Query(ctx, sql, boardID, canSecret, nullIfEmpty(viewerID),
		q.Search, tsq, q.PerPage, q.Offset())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Post
	for rows.Next() {
		var p Post
		var raw []byte
		if err := rows.Scan(&p.ID, &p.BoardID, &p.AuthorID, &p.AuthorName,
			&p.Title, &p.Body, &raw, &p.Status, &p.IsPinned, &p.IsSecret,
			&p.ViewCount, &p.CreatedAt, &p.UpdatedAt,
			&p.CommentCount, &p.HasAttachment); err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &p.CustomFields); err != nil {
				return nil, err
			}
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CountPosts is the total for the pager. It is a second query, and the only
// one: a count cannot ride along with a LIMIT.
func (s *Store) CountPosts(ctx context.Context, boardID string, q ListQuery,
	viewerID string, canSecret bool,
) (int, error) {
	tsq := ""
	if q.Search != "" {
		tsq = toPrefixQuery(q.Search)
	}
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM posts p
		WHERE p.board_id = $1 AND p.status = 'published'
		  AND ($2 OR NOT p.is_secret OR p.author_id = $3)
		  AND ($4 = '' OR p.search_vector @@ to_tsquery('simple', $5))`,
		boardID, canSecret, nullIfEmpty(viewerID), q.Search, tsq).Scan(&n)
	return n, err
}

// PostByID reads one post. Secret posts are filtered in SQL, not after the
// fetch: a row that must not be shown should not leave the database (SC-1 4항).
func (s *Store) PostByID(ctx context.Context, id, viewerID string, canSecret bool) (*Post, error) {
	const q = `
		SELECT p.id, p.board_id, coalesce(p.author_id::text, ''), coalesce(u.display_name, ''),
		       p.title, p.body, p.custom_fields, p.status, p.is_pinned, p.is_secret,
		       p.view_count, p.created_at, p.updated_at,
		       (SELECT count(*) FROM comments c WHERE c.post_id = p.id),
		       EXISTS (SELECT 1 FROM attachments a WHERE a.post_id = p.id)
		FROM posts p
		LEFT JOIN users u ON u.id = p.author_id
		WHERE p.id = $1 AND ($2 OR NOT p.is_secret OR p.author_id = $3)`
	var p Post
	var raw []byte
	err := s.pool.QueryRow(ctx, q, id, canSecret, nullIfEmpty(viewerID)).
		Scan(&p.ID, &p.BoardID, &p.AuthorID, &p.AuthorName, &p.Title, &p.Body, &raw,
			&p.Status, &p.IsPinned, &p.IsSecret, &p.ViewCount, &p.CreatedAt, &p.UpdatedAt,
			&p.CommentCount, &p.HasAttachment)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p.CustomFields); err != nil {
			return nil, err
		}
	}
	return &p, nil
}

func (s *Store) CreatePost(ctx context.Context, p Post) (string, error) {
	fields, err := marshalFields(p.CustomFields)
	if err != nil {
		return "", err
	}
	const q = `
		INSERT INTO posts (board_id, author_id, title, body, custom_fields, is_secret)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`
	var id string
	err = s.pool.QueryRow(ctx, q, p.BoardID, nullIfEmpty(p.AuthorID),
		p.Title, p.Body, fields, p.IsSecret).Scan(&id)
	return id, err
}

// UpdatePost does not touch is_pinned or status: pinning and hiding are
// post.moderate, and letting an edit carry them would hand the author the
// moderator's power (the same split page.update/page.publish has).
func (s *Store) UpdatePost(ctx context.Context, id string, p Post) error {
	fields, err := marshalFields(p.CustomFields)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE posts SET title = $2, body = $3, custom_fields = $4, is_secret = $5,
		       updated_at = now()
		WHERE id = $1`, id, p.Title, p.Body, fields, p.IsSecret)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetPostFlags is the moderator's edit (A-307).
func (s *Store) SetPostFlags(ctx context.Context, id string, pinned bool, status string) error {
	if status != "published" && status != "hidden" {
		return errors.New("content: 알 수 없는 글 상태")
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE posts SET is_pinned = $2, status = $3, updated_at = now() WHERE id = $1`,
		id, pinned, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeletePost(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM posts WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// BumpViewCount increases the counter.
//
// SC-1 3항 allows this write on a GET because it carries no permission
// decision. Duplicate suppression is the caller's (the session remembers what
// it has already counted) — putting it here would need the store to know about
// sessions, and FR-305 keeps that out.
func (s *Store) BumpViewCount(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE posts SET view_count = view_count + 1 WHERE id = $1`, id)
	return err
}

// Comments reads a post's comments in ONE query, ordered so that a one-level
// reply tree can be assembled in memory. D30 caps replies at one level
// (parent_id is set at insert and no screen changes it), so no recursion.
func (s *Store) Comments(ctx context.Context, postID string) ([]Comment, error) {
	const q = `
		SELECT c.id, c.post_id, coalesce(c.parent_id::text, ''),
		       coalesce(c.author_id::text, ''), coalesce(u.display_name, ''),
		       c.body, coalesce(c.deleted_at, 'epoch'::timestamptz), c.created_at
		FROM comments c
		LEFT JOIN users u ON u.id = c.author_id
		WHERE c.post_id = $1
		ORDER BY coalesce(c.parent_id, c.id), c.created_at, c.id`
	rows, err := s.pool.Query(ctx, q, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Comment
	for rows.Next() {
		var c Comment
		var del time.Time
		if err := rows.Scan(&c.ID, &c.PostID, &c.ParentID, &c.AuthorID, &c.AuthorName,
			&c.Body, &del, &c.CreatedAt); err != nil {
			return nil, err
		}
		if !del.Equal(time.Unix(0, 0).UTC()) {
			c.DeletedAt = del
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) CreateComment(ctx context.Context, c Comment) (string, error) {
	const q = `
		INSERT INTO comments (post_id, parent_id, author_id, body)
		VALUES ($1, $2, $3, $4) RETURNING id`
	var id string
	err := s.pool.QueryRow(ctx, q, c.PostID, nullIfEmpty(c.ParentID),
		nullIfEmpty(c.AuthorID), c.Body).Scan(&id)
	return id, err
}

// DeleteComment is two-branched because the foreign key makes it so (D30).
//
// A comment with replies cannot be deleted — parent_id is NO ACTION, so the
// database refuses. That refusal is what produces the tombstone: the row stays,
// the body is emptied in the DATABASE (not hidden by a template `if`, because
// themes are third-party), and deleted_at marks it.
func (s *Store) DeleteComment(ctx context.Context, id string) error {
	var replies int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM comments WHERE parent_id = $1`, id).Scan(&replies); err != nil {
		return err
	}
	if replies > 0 {
		tag, err := s.pool.Exec(ctx,
			`UPDATE comments SET body = '', deleted_at = now(), updated_at = now()
			 WHERE id = $1 AND deleted_at IS NULL`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM comments WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
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
