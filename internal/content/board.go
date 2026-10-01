package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/emirue/ondolith/internal/content/contentq"
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

// boardOf is the one place a boards row becomes a Board (pageOf 와 같은 이유).
func boardOf(r contentq.Board) Board {
	return Board{ID: r.ID, Slug: r.Slug, Name: r.Name, Skin: r.Skin,
		AllowAttachments: r.AllowAttachments, AllowComments: r.AllowComments,
		AllowSecret: r.AllowSecret, PerPage: int(r.PerPage)}
}

func oneBoard(r contentq.Board, err error) (*Board, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	b := boardOf(r)
	return &b, nil
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
	q := s.q.WithTx(tx)

	id, err := q.CreateBoard(ctx, contentq.CreateBoardParams{
		Slug: b.Slug, Name: b.Name, Skin: b.Skin,
		AllowAttachments: b.AllowAttachments, AllowComments: b.AllowComments,
		AllowSecret: b.AllowSecret, PerPage: int32(b.PerPage)})
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
	for _, g := range grants {
		n, err := q.GrantBoardPreset(ctx, contentq.GrantBoardPresetParams{
			BoardID: id, Role: g.Role, Permission: g.Permission})
		if err != nil {
			return "", err
		}
		// SELECT-driven INSERT writes nothing when the WHERE matches nothing —
		// a renamed role or a permission that is not scoped would silently
		// produce a board with fewer grants than the preset promised.
		if n != 1 {
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
	rows, err := s.q.Boards(ctx)
	if err != nil {
		return nil, err
	}
	var out []Board
	for _, r := range rows {
		out = append(out, boardOf(r))
	}
	return out, nil
}

func (s *Store) BoardBySlug(ctx context.Context, slug string) (*Board, error) {
	return oneBoard(s.q.BoardBySlug(ctx, slug))
}

// UpdateBoard changes settings. It does not touch the slug: the slug is in
// every link anyone has saved, and D19 A-305 keeps it out of the edit form.
func (s *Store) UpdateBoard(ctx context.Context, id string, b Board) error {
	return affected(s.q.UpdateBoard(ctx, contentq.UpdateBoardParams{
		ID: id, Name: b.Name, Skin: b.Skin,
		AllowAttachments: b.AllowAttachments, AllowComments: b.AllowComments,
		AllowSecret: b.AllowSecret, PerPage: int32(b.PerPage)}))
}

// DeleteBoard removes a board, its posts and its scoped grants — all by
// CASCADE (D30 3-1).
//
// The count is checked first and reported, rather than letting the delete
// succeed silently: A-305 has a confirmation step, and "이 게시판의 글 128건도
// 함께 삭제됩니다" is the only thing that makes that step mean anything.
func (s *Store) DeleteBoard(ctx context.Context, id string, force bool) error {
	if !force {
		posts, err := s.q.CountBoardPosts(ctx, id)
		if err != nil {
			return err
		}
		if posts > 0 {
			return fmt.Errorf("%w: 글 %d건", ErrBoardInUse, posts)
		}
	}
	return affected(s.q.DeleteBoard(ctx, id))
}

// fieldRow is the shape board_fields and user_fields share. sqlc 는 질의마다
// 행 타입을 따로 내므로, 필드가 같은 구조체 변환 `fieldRow(r)` 로 한 곳에 모은다.
type fieldRow struct {
	Key        string
	Label      string
	FieldType  string
	IsRequired bool
	ShowInList bool
	Options    []byte
	SortOrder  int32
}

// fieldOf turns a row into a FieldSchema. options 는 jsonb 라 생성 코드가
// []byte 로 준다 — 풀어서 []string 으로.
func fieldOf(r fieldRow) (FieldSchema, error) {
	f := FieldSchema{Key: r.Key, Label: r.Label, Type: FieldType(r.FieldType),
		Required: r.IsRequired, ShowInList: r.ShowInList, Sort: int(r.SortOrder)}
	if len(r.Options) > 0 {
		if err := json.Unmarshal(r.Options, &f.Options); err != nil {
			return f, err
		}
	}
	return f, nil
}

// fieldOptions is the jsonb value for FieldSchema.Options: never SQL NULL, an
// empty list when nothing was given.
func fieldOptions(opts []string) ([]byte, error) {
	if opts == nil {
		opts = []string{}
	}
	return json.Marshal(opts)
}

// BoardFields reads one board's custom field schema, in display order.
func (s *Store) BoardFields(ctx context.Context, boardID string) ([]FieldSchema, error) {
	rows, err := s.q.BoardFields(ctx, boardID)
	if err != nil {
		return nil, err
	}
	var out []FieldSchema
	for _, r := range rows {
		f, err := fieldOf(fieldRow(r))
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
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
	opts, err := fieldOptions(f.Options)
	if err != nil {
		return err
	}
	return s.q.SaveBoardField(ctx, contentq.SaveBoardFieldParams{
		BoardID: boardID, Key: f.Key, Label: f.Label, FieldType: string(f.Type),
		IsRequired: f.Required, ShowInList: f.ShowInList, Options: opts, SortOrder: int32(f.Sort)})
}

// DeleteBoardField removes a definition. The values already stored in
// posts.custom_fields are left alone — D14 3절 규칙 4 makes deleting a field
// stop it being shown, not destroy what people wrote.
func (s *Store) DeleteBoardField(ctx context.Context, boardID, key string) error {
	return affected(s.q.DeleteBoardField(ctx, contentq.DeleteBoardFieldParams{BoardID: boardID, Key: key}))
}

// BoardByID is A-305's read.
func (s *Store) BoardByID(ctx context.Context, id string) (*Board, error) {
	return oneBoard(s.q.BoardByID(ctx, id))
}
