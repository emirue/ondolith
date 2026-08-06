package content

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func attachFixture(t *testing.T) (*Attachments, *Store, string, string) {
	t.Helper()
	s, _ := testStore(t)
	root := t.TempDir()
	boardID := seedBoard(t, s)
	postID := mkPost(t, s, boardID, "글")
	return s.AttachmentsIn(root), s, root, postID
}

func TestAttachmentRoundTrips(t *testing.T) {
	a, _, root, postID := attachFixture(t)
	ctx := context.Background()

	body := append(append([]byte(nil), pngBytes...), []byte(strings.Repeat("Z", 100))...)
	got, err := a.Save(ctx, postID, "사진.png", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if got.OriginalName != "사진.png" {
		t.Errorf("표시용 이름 = %q", got.OriginalName)
	}
	if got.MIMEType != "image/png" {
		t.Errorf("mime = %q — 클라이언트 주장이 아니라 매직바이트여야 한다", got.MIMEType)
	}
	if got.ByteSize != int64(len(body)) {
		t.Errorf("크기 = %d, want %d", got.ByteSize, len(body))
	}
	// DB 에는 경로·원본명·mime·크기만 남는다. 파일 내용은 디스크에만 있다.
	if strings.Contains(got.StoredPath, "사진") {
		t.Errorf("원본 이름이 저장 경로에 들어갔다: %q", got.StoredPath)
	}

	f, err := a.Open(&got)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	on := make([]byte, len(body)+10)
	n, _ := f.Read(on)
	if !bytes.Equal(on[:n], body) {
		t.Errorf("내려받은 내용이 다르다: %d 바이트", n)
	}

	// 웹루트 밖이라는 것은 곧 "이 디렉터리가 서빙되지 않는다"는 뜻이다. 여기서는
	// 파일이 설정된 루트 안에만 있다는 것으로 확인한다.
	var found int
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			found++
		}
		return nil
	})
	if found != 1 {
		t.Errorf("업로드 루트에 파일 %d개", found)
	}
}

// 검증에 걸린 업로드는 행도 파일도 남기지 않는다.
func TestRefusedAttachmentLeavesNoRowAndNoFile(t *testing.T) {
	a, s, root, postID := attachFixture(t)
	ctx := context.Background()

	if _, err := a.Save(ctx, postID, "shell.png", bytes.NewReader(phpBytes)); err == nil {
		t.Fatal("위장 파일이 저장됐다")
	}
	var rows int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM attachments`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("거부됐는데 %d행이 남았다", rows)
	}
	var files int
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			files++
		}
		return nil
	})
	if files != 0 {
		t.Errorf("거부됐는데 파일 %d개가 남았다", files)
	}
}

// 행 INSERT 가 실패하면 바이트도 지운다. 아무것도 가리키지 않는 파일은 나중에
// 살아 있는 첨부와 구분할 수 없다.
func TestFailedRowRemovesTheBytes(t *testing.T) {
	a, _, root, _ := attachFixture(t)
	ctx := context.Background()

	// 존재하지 않는 글 id → FK 위반.
	if _, err := a.Save(ctx, "00000000-0000-0000-0000-000000000000",
		"a.png", bytes.NewReader(pngBytes)); err == nil {
		t.Fatal("없는 글에 첨부가 붙었다")
	}
	var files int
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			files++
		}
		return nil
	})
	if files != 0 {
		t.Errorf("행이 없는데 파일 %d개가 남았다", files)
	}
}

// A-309: 디스크 삭제 실패를 행 삭제로 되돌리지 않는다. 행이 먼저 간다 —
// 방문자에게 보이는 실패(500 나는 다운로드)가 생길 수 없는 쪽이다.
func TestDeleteRemovesRowThenFile(t *testing.T) {
	a, s, root, postID := attachFixture(t)
	ctx := context.Background()

	got, err := a.Save(ctx, postID, "a.png", bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Delete(ctx, got.ID); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM attachments`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("행 %d개가 남았다", rows)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(got.StoredPath))); !os.IsNotExist(err) {
		t.Errorf("파일이 남았다: %v", err)
	}
	// 이미 없는 파일을 지우는 것은 오류가 아니다 — 앞선 실패로 고아 행이 남았을
	// 때 그 행을 정리할 수 없게 된다.
	if err := a.removeFile(got.StoredPath); err != nil {
		t.Errorf("없는 파일 삭제가 오류를 냈다: %v", err)
	}
}

// 글을 지우면 첨부 행도 CASCADE 로 간다 (D30). 파일은 남는다 — A-309 가
// 고아 파일을 허용한 그 자리다.
func TestDeletingAPostCascadesAttachmentRows(t *testing.T) {
	a, s, _, postID := attachFixture(t)
	ctx := context.Background()
	if _, err := a.Save(ctx, postID, "a.png", bytes.NewReader(pngBytes)); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePost(ctx, postID); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM attachments`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("글을 지웠는데 첨부 %d행이 남았다", rows)
	}
}

// 다운로드 경로는 DB 에서 오지만 그래도 os.Root 를 통과한다. "DB 가 검증했다"는
// 마이그레이션 하나 만에 거짓이 된다.
func TestOpenRefusesAPathOutsideTheRoot(t *testing.T) {
	a, _, _, _ := attachFixture(t)
	// `../` 를 실제로 읽히는 파일까지 올라가게 만든다. 얕게 두면 탈출에
	// 성공해도 없는 경로라 열리지 않아, os.Root 를 빼는 변이가 안 잡힌다.
	deep := strings.Repeat("../", 24) + "etc/passwd"
	for _, p := range []string{deep, "../../etc/passwd", "/etc/passwd", "2026/08/../../../x"} {
		if f, err := a.Open(&Attachment{StoredPath: p}); err == nil {
			f.Close()
			t.Errorf("루트 밖 경로가 열렸다: %q", p)
		}
	}
}

func TestListIsOrderedAndScopedToThePost(t *testing.T) {
	a, s, _, postID := attachFixture(t)
	ctx := context.Background()
	other := mkPost(t, s, seedBoardSlug(t, s, "notice"), "다른 글")

	for range 3 {
		if _, err := a.Save(ctx, postID, "a.png", bytes.NewReader(pngBytes)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Save(ctx, other, "b.png", bytes.NewReader(pngBytes)); err != nil {
		t.Fatal(err)
	}

	got, err := a.List(ctx, postID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("%d개, want 3개 — 다른 글의 첨부가 섞였다", len(got))
	}
	for _, at := range got {
		if at.PostID != postID {
			t.Errorf("다른 글의 첨부다: %s", at.ID)
		}
	}
}

func seedBoardSlug(t *testing.T, s *Store, slug string) string {
	t.Helper()
	id, err := s.CreateBoard(context.Background(), newBoard(slug, slug), PresetPublic)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// **글을 지우면 첨부 파일도 사라진다** (OPEN-40 결정).
//
// 행은 CASCADE 로 가지만 파일은 따라가지 않는다. 정리 잡이 없으므로
// (NFR-103) 여기서 지우지 않으면 그 파일들은 영원히 남고, 글이 사라졌으니
// A-309 목록에도 나오지 않아 아무도 찾지 못한다.
func TestDeletingAPostRemovesItsFiles(t *testing.T) {
	a, s, root, postID := attachFixture(t)
	ctx := context.Background()
	var stored []string
	for _, name := range []string{"a.png", "b.png"} {
		at, err := a.Save(ctx, postID, name, bytes.NewReader(pngBytes))
		if err != nil {
			t.Fatal(err)
		}
		stored = append(stored, at.StoredPath)
	}
	for _, rel := range stored {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("업로드 직후 파일이 없다: %v", err)
		}
	}

	if err := a.DeletePost(ctx, postID); err != nil {
		t.Fatal(err)
	}

	var rows int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM attachments`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("글을 지웠는데 첨부 %d행이 남았다", rows)
	}
	for _, rel := range stored {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("첨부 파일 %s 가 남았다 (err=%v) — 아무도 찾지 못하는 쓰레기다", rel, err)
		}
	}
}

// **첨부 상한은 설정값이다** (OPEN-41 결정): 파일당 크기와 글당 개수.
func TestUploadLimitsComeFromSettings(t *testing.T) {
	a, s, _, postID := attachFixture(t)
	ctx := context.Background()
	put := func(kv map[string]string) {
		t.Helper()
		if err := s.PutSettings(ctx, kv); err != nil {
			t.Fatal(err)
		}
	}

	// 글당 개수: 1개로 낮추면 두 번째가 막힌다.
	put(map[string]string{SettingUploadMaxPerPost: "1"})
	if _, err := a.Save(ctx, postID, "a.png", bytes.NewReader(pngBytes)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Save(ctx, postID, "b.png", bytes.NewReader(pngBytes)); !errors.Is(err, ErrUploadTooMany) {
		t.Fatalf("두 번째 첨부 = %v, want ErrUploadTooMany", err)
	}
	// 막힌 업로드가 디스크에 다녀가지도 않았다 — 상한의 목적이 그 사용량이다.
	var rows int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM attachments`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("첨부 %d행, want 1", rows)
	}

	// 파일당 크기: 내용보다 작게 잡으면 막힌다.
	put(map[string]string{SettingUploadMaxPerPost: "10",
		SettingUploadMaxBytes: strconv.Itoa(len(pngBytes) - 1)})
	if _, err := a.Save(ctx, postID, "c.png", bytes.NewReader(pngBytes)); !errors.Is(err, ErrUploadTooLarge) {
		t.Fatalf("상한보다 큰 파일 = %v, want ErrUploadTooLarge", err)
	}

	// 이상한 값은 기본값으로 물러서지 않고 오류다.
	put(map[string]string{SettingUploadMaxBytes: "0"})
	if _, err := a.Save(ctx, postID, "d.png", bytes.NewReader(pngBytes)); !errors.Is(err, ErrUploadSetting) {
		t.Fatalf("0 바이트 상한 = %v, want ErrUploadSetting", err)
	}
}

// **확장자는 뺄 수만 있고 더할 수는 없다.**
//
// 권고는 "허용 목록을 설정값으로" 였지만, 자유 목록이면 `.svg` 를 폼 한 칸으로
// 되살릴 수 있다 — D60 이 SVG 를 뺀 이유와 그때 함께 열기로 한 첨부 전용 서빙
// 경로가 무효가 된다.
func TestDenyExtSubtractsButCannotAdd(t *testing.T) {
	a, s, _, postID := attachFixture(t)
	ctx := context.Background()

	// 뺄 수 있다.
	if err := s.PutSettings(ctx, map[string]string{SettingUploadDenyExt: "png, .zip"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Save(ctx, postID, "a.png", bytes.NewReader(pngBytes)); !errors.Is(err, ErrUploadExt) {
		t.Fatalf("거부 목록의 png = %v, want ErrUploadExt", err)
	}

	// 더할 수는 없다. 목록에 적어도 내장 허용목록 밖은 그대로 거부된다.
	if err := s.PutSettings(ctx, map[string]string{SettingUploadDenyExt: ""}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"x.svg", "x.html", "x.php"} {
		if _, err := a.Save(ctx, postID, name, bytes.NewReader(pngBytes)); !errors.Is(err, ErrUploadExt) {
			t.Errorf("%s = %v, want ErrUploadExt", name, err)
		}
	}
	// 빼지 않은 것은 여전히 된다.
	if _, err := a.Save(ctx, postID, "ok.png", bytes.NewReader(pngBytes)); err != nil {
		t.Errorf("거부 목록을 비웠는데 png 가 막혔다: %v", err)
	}
}
