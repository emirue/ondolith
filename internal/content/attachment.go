package content

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	limits, err := a.Limits(ctx)
	if err != nil {
		return Attachment{}, err
	}
	// **개수를 먼저 센다.** 파일을 쓴 뒤에 세면 상한을 넘긴 그 파일이 디스크에
	// 다녀갔다 지워지는데, 상한의 목적이 바로 그 디스크 사용이다.
	var n int
	if err := a.store.pool.QueryRow(ctx,
		`SELECT count(*) FROM attachments WHERE post_id = $1`, postID).Scan(&n); err != nil {
		return Attachment{}, err
	}
	if n >= limits.MaxPerPost {
		return Attachment{}, fmt.Errorf("%w: 글당 %d개", ErrUploadTooMany, limits.MaxPerPost)
	}

	stored, err := StoreUpload(a.root, name, r, time.Now(), limits)
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

// DeletePost removes the post and then the files its attachments left behind.
//
// **첨부 행은 CASCADE 로 함께 지워지지만 파일은 따라가지 않는다** (D30). 정리
// 잡이 없으므로 (NFR-103) 여기서 지우지 않으면 그 파일들은 영원히 남는다 —
// 글이 지워졌으니 A-309 목록에도 나오지 않아 아무도 찾지 못한다.
//
// 경로를 **먼저 읽는다**: 행이 사라진 뒤에는 어떤 파일이 그 글의 것이었는지
// 알 방법이 없다. 파일 삭제 실패는 보고만 하고 되돌리지 않는다 — A-309 가
// 정한 것과 같은 이유로, 행보다 파일이 오래 남는 것은 쓰레기이고 그 반대는
// 500 을 내는 다운로드다.
func (a *Attachments) DeletePost(ctx context.Context, postID string) error {
	rows, err := a.store.pool.Query(ctx,
		`SELECT stored_path FROM attachments WHERE post_id = $1`, postID)
	if err != nil {
		return err
	}
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		paths = append(paths, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	if err := a.store.DeletePost(ctx, postID); err != nil {
		return err
	}
	var firstErr error
	for _, p := range paths {
		if err := a.removeFile(p); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Limits reads A-309's upload bounds (OPEN-41 결정). 설정이 없으면 기본값이다.
//
// 값이 이상하면 **기본값으로 물러서지 않고 오류를 낸다**: 상한을 0 으로 적어
// 둔 사이트가 조용히 20 MiB 를 허용하면, 운영자는 자기가 정한 값이 걸려
// 있다고 믿는다.
func (a *Attachments) Limits(ctx context.Context) (UploadLimits, error) {
	l := DefaultUploadLimits()
	kv, err := a.store.Settings(ctx,
		SettingUploadMaxBytes, SettingUploadMaxPerPost, SettingUploadDenyExt)
	if err != nil {
		return l, err
	}
	if v := kv[SettingUploadMaxBytes]; v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return l, fmt.Errorf("%w: %s=%q", ErrUploadSetting, SettingUploadMaxBytes, v)
		}
		l.MaxBytes = n
	}
	if v := kv[SettingUploadMaxPerPost]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return l, fmt.Errorf("%w: %s=%q", ErrUploadSetting, SettingUploadMaxPerPost, v)
		}
		l.MaxPerPost = n
	}
	if v := kv[SettingUploadDenyExt]; v != "" {
		l.Denied = map[string]bool{}
		for _, e := range strings.Split(v, ",") {
			e = strings.ToLower(strings.TrimSpace(e))
			if e == "" {
				continue
			}
			if !strings.HasPrefix(e, ".") {
				e = "." + e
			}
			l.Denied[e] = true
		}
	}
	return l, nil
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

// BoardAttachments is A-309's list: every attachment on one board, newest
// first. The board scopes it because post.moderate does (D15 2.4) — "every
// attachment on the site" is a list nobody has permission for as a whole.
func (s *Store) BoardAttachments(ctx context.Context, boardID string, limit int) ([]Attachment, error) {
	const q = `
		SELECT a.id, a.post_id, a.stored_path, a.original_name, a.mime_type,
		       a.byte_size, a.created_at
		FROM attachments a
		JOIN posts p ON p.id = a.post_id
		WHERE p.board_id = $1
		ORDER BY a.created_at DESC, a.id DESC
		LIMIT $2`
	rows, err := s.pool.Query(ctx, q, boardID, limit)
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
