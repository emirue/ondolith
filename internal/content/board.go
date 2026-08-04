package content

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrSlugTakenBoard = errors.New("content: 이미 사용 중인 게시판 주소입니다")
	ErrBoardInUse     = errors.New("content: 글이 남아 있는 게시판입니다")
)

// Board is one row of boards (D30).
type Board struct {
	ID               string
	Slug             string
	Name             string
	Skin             string
	AllowAttachments bool
	AllowComments    bool
	AllowSecret      bool
	PerPage          int
}

// CreateBoard writes the board and its preset grants in ONE transaction.
//
// D14 4.2 requires this. A board that exists with no grants is invisible to
// everyone including the person who just made it, and the screen that fixes it
// is a different screen — so the operator's next move is to create a second
// board, believing the first one failed. Either both rows land or neither does.
func (s *Store) CreateBoard(ctx context.Context, b Board, preset BoardPreset) (string, error) {
	grants, err := PresetGrants(preset)
	if err != nil {
		return "", err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	const insert = `
		INSERT INTO boards (slug, name, skin, allow_attachments, allow_comments, allow_secret, per_page)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`
	var id string
	err = tx.QueryRow(ctx, insert, b.Slug, b.Name, b.Skin,
		b.AllowAttachments, b.AllowComments, b.AllowSecret, b.PerPage).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return "", ErrSlugTakenBoard
	}
	if err != nil {
		return "", err
	}

	// The role and permission are looked up by key inside the same statement.
	// Reading their ids first would be two round trips and a window in which a
	// role could be deleted between the read and the write.
	const grant = `
		INSERT INTO role_permissions (role_id, permission_id, board_id)
		SELECT r.id, p.id, $3 FROM roles r, permissions p
		WHERE r.key = $1 AND p.key = $2 AND p.is_scoped`
	for _, g := range grants {
		tag, err := tx.Exec(ctx, grant, g.Role, g.Permission, id)
		if err != nil {
			return "", err
		}
		// SELECT-driven INSERT writes nothing when the WHERE matches nothing —
		// a renamed role or a permission that is not scoped would silently
		// produce a board with fewer grants than the preset promised.
		if tag.RowsAffected() != 1 {
			return "", fmt.Errorf("content: 프리셋 부여 실패 (%s / %s): 역할 또는 스코프 권한이 없다",
				g.Role, g.Permission)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Store) Boards(ctx context.Context) ([]Board, error) {
	const q = `
		SELECT id, slug, name, skin, allow_attachments, allow_comments, allow_secret, per_page
		FROM boards ORDER BY name, id`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Board
	for rows.Next() {
		var b Board
		if err := rows.Scan(&b.ID, &b.Slug, &b.Name, &b.Skin,
			&b.AllowAttachments, &b.AllowComments, &b.AllowSecret, &b.PerPage); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) BoardBySlug(ctx context.Context, slug string) (*Board, error) {
	const q = `
		SELECT id, slug, name, skin, allow_attachments, allow_comments, allow_secret, per_page
		FROM boards WHERE slug = $1`
	var b Board
	err := s.pool.QueryRow(ctx, q, slug).Scan(&b.ID, &b.Slug, &b.Name, &b.Skin,
		&b.AllowAttachments, &b.AllowComments, &b.AllowSecret, &b.PerPage)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &b, err
}

// UpdateBoard changes settings. It does not touch the slug: the slug is in
// every link anyone has saved, and D19 A-305 keeps it out of the edit form.
func (s *Store) UpdateBoard(ctx context.Context, id string, b Board) error {
	const q = `
		UPDATE boards SET name = $2, skin = $3, allow_attachments = $4,
		       allow_comments = $5, allow_secret = $6, per_page = $7, updated_at = now()
		WHERE id = $1`
	tag, err := s.pool.Exec(ctx, q, id, b.Name, b.Skin,
		b.AllowAttachments, b.AllowComments, b.AllowSecret, b.PerPage)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteBoard removes a board, its posts and its scoped grants — all by
// CASCADE (D30 3-1).
//
// The count is checked first and reported, rather than letting the delete
// succeed silently: A-305 has a confirmation step, and "이 게시판의 글 128건도
// 함께 삭제됩니다" is the only thing that makes that step mean anything.
func (s *Store) DeleteBoard(ctx context.Context, id string, force bool) error {
	if !force {
		var posts int
		if err := s.pool.QueryRow(ctx,
			`SELECT count(*) FROM posts WHERE board_id = $1`, id).Scan(&posts); err != nil {
			return err
		}
		if posts > 0 {
			return fmt.Errorf("%w: 글 %d건", ErrBoardInUse, posts)
		}
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM boards WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// BoardFields reads one board's custom field schema, in display order.
func (s *Store) BoardFields(ctx context.Context, boardID string) ([]FieldSchema, error) {
	const q = `
		SELECT key, label, field_type, is_required, show_in_list, options, sort_order
		FROM board_fields WHERE board_id = $1 ORDER BY sort_order, key`
	rows, err := s.pool.Query(ctx, q, boardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FieldSchema
	for rows.Next() {
		var f FieldSchema
		var opts []string
		if err := rows.Scan(&f.Key, &f.Label, &f.Type, &f.Required, &f.ShowInList, &opts, &f.Sort); err != nil {
			return nil, err
		}
		f.Options = opts
		out = append(out, f)
	}
	return out, rows.Err()
}

// SaveBoardField inserts or updates one field definition.
//
// The reserved-key check runs here as well as in the handler: this is the last
// place before the database, and the database deliberately does not hold the
// list (D30 — it would grow with every column added).
func (s *Store) SaveBoardField(ctx context.Context, boardID string, f FieldSchema) error {
	if err := ValidateFieldKey(f.Key); err != nil {
		return err
	}
	opts := f.Options
	if opts == nil {
		opts = []string{}
	}
	const q = `
		INSERT INTO board_fields (board_id, key, label, field_type, is_required, show_in_list, options, sort_order)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (board_id, key) DO UPDATE SET
			label = EXCLUDED.label, field_type = EXCLUDED.field_type,
			is_required = EXCLUDED.is_required, show_in_list = EXCLUDED.show_in_list,
			options = EXCLUDED.options, sort_order = EXCLUDED.sort_order, updated_at = now()`
	_, err := s.pool.Exec(ctx, q, boardID, f.Key, f.Label, f.Type,
		f.Required, f.ShowInList, opts, f.Sort)
	return err
}

// DeleteBoardField removes a definition. The values already stored in
// posts.custom_fields are left alone — D14 3절 규칙 4 makes deleting a field
// stop it being shown, not destroy what people wrote.
func (s *Store) DeleteBoardField(ctx context.Context, boardID, key string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM board_fields WHERE board_id = $1 AND key = $2`, boardID, key)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
