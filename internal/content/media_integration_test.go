package content

import (
	"bytes"
	"context"
	"errors"
	"image"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// **에디터로 올린 사진은 줄인 본문용과 썸네일로 저장되고, 행이 그 둘을 가리킨다** (FR-514).
func TestMediaSaveStoresDisplayAndThumbnail(t *testing.T) {
	s, _ := testStore(t)
	root := t.TempDir()
	m := s.MediaIn(LocalFiles(root))
	ctx := context.Background()

	md, err := m.Save(ctx, "", "거실.jpg", bytes.NewReader(encodeJPEG(t, halves(3000, 2000))),
		DefaultUploadLimits(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if md.Width != 1920 || md.Height != 1280 || md.MIMEType != "image/jpeg" {
		t.Errorf("저장된 사진 %dx%d %s, want 1920x1280 image/jpeg", md.Width, md.Height, md.MIMEType)
	}
	// 본문 사진도 첨부와 같은 권한이다 — 앱 계정만 읽는다 (D60 3절).
	if fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(md.StoredPath))); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("본문 사진 권한 %v (%v), want 0600", fi.Mode().Perm(), err)
	}
	got, err := m.ByID(ctx, md.ID)
	if err != nil || got.StoredPath != md.StoredPath {
		t.Fatalf("ByID = %+v (%v)", got, err)
	}
	for _, thumb := range []bool{false, true} {
		f, err := m.Open(ctx, got, thumb)
		if err != nil {
			t.Fatalf("파일 열기(썸네일 %v): %v", thumb, err)
		}
		b, _ := io.ReadAll(f)
		f.Close()
		cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
		want := 1920
		if thumb {
			want = MediaThumbEdge
		}
		if err != nil || cfg.Width != want {
			t.Errorf("썸네일 %v 의 폭 %d (%v), want %d", thumb, cfg.Width, err, want)
		}
	}
}

// 그림이 아니거나 너무 큰 것은 저장하지 않고, **디스크에 아무것도 남기지 않는다** —
// 행 없는 파일은 나중에 살아 있는 사진과 구분할 수 없다.
func TestMediaSaveRefusesAndLeavesNothing(t *testing.T) {
	s, pool := testStore(t)
	root := t.TempDir()
	m := s.MediaIn(LocalFiles(root))
	ctx := context.Background()

	cases := []struct {
		name, file string
		body       []byte
		want       error
	}{
		{"문서", "견적.pdf", []byte("%PDF-1.4"), ErrUploadExt},
		{"그림인 척하는 글", "사진.jpg", []byte("<html>not a picture</html>"), ErrUploadContent},
		{"빈 파일", "빈.jpg", nil, ErrUploadEmpty},
	}
	for _, tc := range cases {
		if _, err := m.Save(ctx, "", tc.file, bytes.NewReader(tc.body), DefaultUploadLimits(), time.Now()); !errors.Is(err, tc.want) {
			t.Errorf("%s = %v, want %v", tc.name, err, tc.want)
		}
	}
	small := DefaultUploadLimits()
	small.MaxBytes = 100
	if _, err := m.Save(ctx, "", "큰.jpg", bytes.NewReader(encodeJPEG(t, halves(200, 200))), small, time.Now()); !errors.Is(err, ErrUploadTooLarge) {
		t.Errorf("상한 초과 = %v, want ErrUploadTooLarge", err)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM media`).Scan(&n); err != nil || n != 0 {
		t.Errorf("media 행 %d (%v), want 0", n, err)
	}
	var files []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, strings.TrimPrefix(p, root))
		}
		return nil
	})
	if len(files) != 0 {
		t.Errorf("거부한 업로드가 파일을 남겼다: %v", files)
	}
}
