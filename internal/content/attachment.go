package content

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
)

// Attachment is one row of attachments (D30).
type Attachment struct {
	ID           string
	PostID       string
	StoredPath   string
	OriginalName string
	MIMEType     string
	ByteSize     int64
	CreatedAt    time.Time
}

// Attachments is the store's view of the upload directory.
//
// The directory is configuration (NFR-304): an upgrade replaces the binary and
// nothing else, so a path compiled in would move with the binary while the
// files stayed put.
type Attachments struct {
	store *Store
	root  string
}

func (s *Store) AttachmentsIn(root string) *Attachments {
	return &Attachments{store: s, root: root}
}

// Save validates, writes the file and records the row.
//
// The row is written LAST. A row pointing at a file that is not there renders
// a broken download on a page that otherwise works; a file with no row is
// invisible litter that a later sweep can find. Of the two failure modes only
// one is silent to the visitor, so the write order picks the other.
func (a *Attachments) Save(ctx context.Context, postID, name string, r io.Reader) (Attachment, error) {
	stored, err := StoreUpload(a.root, name, r, time.Now())
	if err != nil {
		return Attachment{}, err
	}

	const q = `
		INSERT INTO attachments (post_id, stored_path, original_name, mime_type, byte_size)
		VALUES ($1, $2, $3, $4, $5) RETURNING id, created_at`
	var out Attachment
	err = a.store.pool.QueryRow(ctx, q, postID, stored.StoredPath,
		stored.OriginalName, stored.MIMEType, stored.ByteSize).Scan(&out.ID, &out.CreatedAt)
	if err != nil {
		// The row did not land, so the bytes must not stay: nothing will ever
		// point at them and nobody could tell them from a live attachment.
		_ = a.removeFile(stored.StoredPath)
		return Attachment{}, err
	}
	out.PostID = postID
	out.StoredPath = stored.StoredPath
	out.OriginalName = stored.OriginalName
	out.MIMEType = stored.MIMEType
	out.ByteSize = stored.ByteSize
	return out, nil
}

func (a *Attachments) List(ctx context.Context, postID string) ([]Attachment, error) {
	const q = `
		SELECT id, post_id, stored_path, original_name, mime_type, byte_size, created_at
		FROM attachments WHERE post_id = $1 ORDER BY created_at, id`
	rows, err := a.store.pool.Query(ctx, q, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Attachment
	for rows.Next() {
		var at Attachment
		if err := rows.Scan(&at.ID, &at.PostID, &at.StoredPath, &at.OriginalName,
			&at.MIMEType, &at.ByteSize, &at.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, at)
	}
	return out, rows.Err()
}

func (a *Attachments) ByID(ctx context.Context, id string) (*Attachment, error) {
	const q = `
		SELECT id, post_id, stored_path, original_name, mime_type, byte_size, created_at
		FROM attachments WHERE id = $1`
	var at Attachment
	err := a.store.pool.QueryRow(ctx, q, id).Scan(&at.ID, &at.PostID, &at.StoredPath,
		&at.OriginalName, &at.MIMEType, &at.ByteSize, &at.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &at, err
}

// Open returns the file for download.
//
// The path comes from the database, where a CHECK constrains it to
// `YYYY/MM/<uuid>` — but it is still opened through os.Root, because "the
// database validated it" is one migration away from being false and the escape
// check costs nothing (NFR-201: do not write the check by hand).
func (a *Attachments) Open(at *Attachment) (*os.File, error) {
	rt, err := os.OpenRoot(a.root)
	if err != nil {
		return nil, err
	}
	defer rt.Close()
	return rt.Open(filepath.FromSlash(at.StoredPath))
}

// Delete removes the row and then the file.
//
// A-309 decided the disk delete does not roll back the row: a file that
// survives its row is litter, while a row that survives its file is a download
// that 500s. The row goes first so the failure the visitor can see is the one
// that cannot happen.
func (a *Attachments) Delete(ctx context.Context, id string) error {
	at, err := a.ByID(ctx, id)
	if err != nil {
		return err
	}
	tag, err := a.store.pool.Exec(ctx, `DELETE FROM attachments WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	// Reported, not returned: the row is already gone and the caller cannot
	// undo it. The file is now orphaned, which A-309 accepts.
	return a.removeFile(at.StoredPath)
}

func (a *Attachments) removeFile(rel string) error {
	rt, err := os.OpenRoot(a.root)
	if err != nil {
		return err
	}
	defer rt.Close()
	err = rt.Remove(filepath.FromSlash(rel))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
