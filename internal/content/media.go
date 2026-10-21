package content

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/emirue/ondolith/internal/content/contentq"
)

// Media is one picture uploaded from the editor (FR-514, 00027).
type Media struct {
	ID         string
	UploaderID string
	// StoredPath 는 본문용 파일이다. 썸네일은 그 옆의 `-thumb` 다.
	StoredPath string
	MIMEType   string
	ThumbMIME  string
	Width      int
	Height     int
	ByteSize   int64
	CreatedAt  time.Time
}

// MediaStore keeps editor pictures where uploads live (Files — upload_dir 또는 버킷, FR-717).
type MediaStore struct {
	store *Store
	files *Files
}

// MediaIn binds the media table to the upload storage.
func (s *Store) MediaIn(files *Files) *MediaStore { return &MediaStore{store: s, files: files} }

// Save validates, processes and stores one picture.
//
// 검증은 첨부와 같은 함수다 (확장자 허용목록 + 내용 감지, NFR-206) — 이미지로
// 좁힌 상한(ImageOnly)으로. 그 뒤 ProcessImage 가 줄이고 세우고 EXIF 를 버린다.
// **원본은 저장하지 않는다.** 행은 마지막에 쓴다: 파일 없는 행은 깨진 사진으로
// 보이고, 행 없는 파일은 아무에게도 안 보인다 (첨부와 같은 순서).
func (m *MediaStore) Save(ctx context.Context, uploaderID, name string, r io.Reader,
	limits UploadLimits, now time.Time) (Media, error) {

	mime, body, err := ValidateUpload(name, r, limits.ImageOnly())
	if err != nil {
		return Media{}, err
	}
	data, err := io.ReadAll(io.LimitReader(body, limits.MaxBytes+1))
	if err != nil {
		return Media{}, err
	}
	if int64(len(data)) > limits.MaxBytes {
		return Media{}, fmt.Errorf("%w: %d 바이트 초과", ErrUploadTooLarge, limits.MaxBytes)
	}
	p, err := ProcessImage(data, mime)
	if err != nil {
		return Media{}, err
	}

	rel := path.Join("media", now.UTC().Format("2006/01"), newUUID())
	if err := m.files.Write(ctx, rel, p.Display); err != nil {
		return Media{}, err
	}
	if err := m.files.Write(ctx, rel+"-thumb", p.Thumb); err != nil {
		_ = m.files.Remove(ctx, rel)
		return Media{}, err
	}
	thumbMIME := "image/jpeg"
	if p.MIME != "image/jpeg" { // png·gif 의 썸네일은 png 다 (투명도)
		thumbMIME = "image/png"
	}
	row, err := m.store.q.CreateMedia(ctx, contentq.CreateMediaParams{
		UploaderID: strPtr(uploaderID), StoredPath: rel, MimeType: p.MIME, ThumbMime: thumbMIME,
		Width: int32(p.Width), Height: int32(p.Height), ByteSize: int64(len(p.Display))})
	if err != nil {
		_ = m.files.Remove(ctx, rel)
		_ = m.files.Remove(ctx, rel+"-thumb")
		return Media{}, err
	}
	return Media{ID: row.ID, UploaderID: uploaderID, StoredPath: rel, MIMEType: p.MIME,
		ThumbMIME: thumbMIME, Width: p.Width, Height: p.Height, ByteSize: int64(len(p.Display)),
		CreatedAt: row.CreatedAt}, nil
}

// ByID reads one picture's row, or ErrNotFound.
func (m *MediaStore) ByID(ctx context.Context, id string) (Media, error) {
	r, err := m.store.q.MediaByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Media{}, ErrNotFound
	}
	if err != nil {
		return Media{}, err
	}
	return Media{ID: r.ID, UploaderID: r.UploaderID, StoredPath: r.StoredPath, MIMEType: r.MimeType,
		ThumbMIME: r.ThumbMime, Width: int(r.Width), Height: int(r.Height), ByteSize: r.ByteSize,
		CreatedAt: r.CreatedAt}, nil
}

// Open opens the display file, or the thumbnail.
func (m *MediaStore) Open(ctx context.Context, md Media, thumb bool) (io.ReadSeekCloser, error) {
	rel := md.StoredPath
	if thumb {
		rel += "-thumb"
	}
	return m.files.Open(ctx, rel)
}
