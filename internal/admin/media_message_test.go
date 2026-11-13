package admin

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/content"
)

// 사진을 올리다 실패하면 사람이 고칠 수 있는 말을 한다. 서버 탓(빈 문자열)과 사람이 고칠 것을 섞지 않는다.
func TestEditorMessageTellsWhatToFix(t *testing.T) {
	limits := content.UploadLimits{MaxBytes: 10 << 20}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, ""},
		{fmt.Errorf("x: %w", content.ErrUploadExt), "jpg·png·gif·webp"},
		{fmt.Errorf("x: %w", content.ErrUploadTooLarge), "10 MB"},
		{&http.MaxBytesError{Limit: 1}, "10 MB"},
		{fmt.Errorf("x: %w", content.ErrImageTooLarge), "4,000만 화소"},
		// 메모리가 모자라 거부한 사진은 줄여 올리면 된다 — 서버 오류(500)로 보이면 안 된다.
		{fmt.Errorf("x: %w", content.ErrImageNoMemory), "줄여"},
		{fmt.Errorf("x: %w", content.ErrImageDecode), "읽을 수 없습니다"},
		{fmt.Errorf("디스크가 꽉 찼다"), ""},
	} {
		got := editorMessage(tc.err, limits)
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("editorMessage(%v) = %q, want %q 포함", tc.err, got, tc.want)
		}
	}
}
