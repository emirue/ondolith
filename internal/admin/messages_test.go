package admin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/emirue/ondolith/internal/commerce"
	"github.com/emirue/ondolith/internal/content"
	"github.com/emirue/ondolith/internal/theme"
)

// 운영자가 보는 거부 문구 (NFR-210). 표가 곧 문구 목록이다 — 패키지 접두어나
// 제약 이름, uuid 가 새면 깨진다.
func TestAdminRefusalsReadAsSentences(t *testing.T) {
	text := content.FieldSchema{Type: content.FieldText}
	choice := content.FieldSchema{Type: content.FieldSelect}
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"주소 형식", slugMessage(content.ErrSlugFormat), "주소에는 영문 소문자, 숫자, 하이픈(-)만 사용할 수 있으며, 영문 소문자나 숫자로 시작해야 합니다."},
		{"예약 주소", slugMessage(content.ErrSlugReserved), "시스템에서 사용하는 주소입니다. 다른 주소를 입력해 주세요."},
		{"쓰고 있는 주소", slugMessage(commerce.ErrSlugTaken), "이미 사용 중인 주소입니다. 다른 주소를 입력해 주세요."},
		{"주소 오류 아님", slugMessage(errors.New("db down")), ""},

		{"키 형식", fieldDefMessage(content.ErrFieldKeyFormat, text), "키는 영문 소문자로 시작해야 하며, 영문 소문자·숫자·밑줄(_)만 사용해 32자 이내로 입력해 주세요."},
		{"예약 키", fieldDefMessage(fmt.Errorf("%w: %q", content.ErrFieldKeyReserved, "email"), text), "기본 항목과 이름이 같은 키는 사용할 수 없습니다. 다른 키를 입력해 주세요."},
		{"라벨", fieldDefMessage(content.ErrFieldLabel, text), "라벨은 1자 이상 100자 이하로 입력해 주세요."},
		{"모르는 타입", fieldDefMessage(content.ValidateBoardField(content.FieldSchema{Key: "k", Label: "라벨", Type: "color"}), text), "지원하지 않는 타입입니다."},
		{"긴 선택지", fieldDefMessage(content.ErrFieldOptionsLong, choice), "선택지가 너무 많거나 깁니다. 선택지를 줄여 주세요."},
		{"선택지 없음", fieldDefMessage(content.ErrFieldOptions, choice), "select·multiselect 항목에는 선택지를 하나 이상 입력해 주세요."},
		{"선택지 남음", fieldDefMessage(content.ErrFieldOptions, text), "선택지는 select·multiselect 항목에만 입력할 수 있습니다."},
		{"항목 정의 오류 아님", fieldDefMessage(errors.New("db down"), text), ""},

		{"메뉴 순환", menuMessage(content.ErrMenuCycle), "자기 자신이나 하위 메뉴를 상위 메뉴로 지정할 수 없습니다."},
		{"상위 메뉴 없음", menuMessage(content.ErrMenuParent), "상위 메뉴를 찾을 수 없습니다. 페이지를 새로고침한 뒤 다시 시도해 주세요."},
		{"메뉴 오류 아님", menuMessage(errors.New("db down")), ""},

		{"상위 카테고리 없음", categoryMessage(fmt.Errorf("%w: 5b1e", commerce.ErrCategoryMissing)), "상위 카테고리를 찾을 수 없습니다. 페이지를 새로고침한 뒤 다시 시도해 주세요."},
		{"카테고리 순환", categoryMessage(fmt.Errorf("%w: 5b1e 는 9c0f 의 자손입니다", commerce.ErrCategoryCycle)), "자기 자신이나 하위 카테고리를 상위 카테고리로 지정할 수 없습니다."},
		{"카테고리 깊이", categoryMessage(commerce.ErrCategoryDepth), "카테고리는 최대 10단계까지 만들 수 있습니다."},
		{"카테고리 사용 중", categoryMessage(commerce.ErrCategoryInUse), "상품이나 하위 카테고리가 있는 카테고리는 삭제할 수 없습니다."},
		{"카테고리 주소", categoryMessage(commerce.ErrSlugTaken), "이미 사용 중인 주소입니다. 다른 주소를 입력해 주세요."},
		{"카테고리 오류 아님", categoryMessage(errors.New("db down")), ""},

		{"전이 불가", transitionMessage(commerce.CanTransition(commerce.StatusPaid, commerce.StatusDelivered, "A-506")), "「결제완료」 상태에서 「배송완료」 상태로 변경할 수 없습니다."},
		{"화면 밖 전이", transitionMessage(&commerce.TransitionError{Err: commerce.ErrActorNotAllowed, From: commerce.StatusDelivered, To: commerce.StatusRefunded, Actor: "P-506"}), "「배송완료」 상태에서 「환불」 상태로는 이 화면에서 변경할 수 없습니다."},
		{"모르는 상태", transitionMessage(commerce.ErrUnknownStatus), "알 수 없는 주문 상태입니다."},
		{"전이 오류 아님", transitionMessage(errors.New("db down")), ""},

		{"약관 빈칸", termsMessage(commerce.ErrTermsIncomplete), "종류, 버전, 본문을 모두 입력해 주세요."},
		{"약관 길이", termsMessage(commerce.ErrTermsTooLong), "종류는 50자, 버전은 20자, 본문은 20,000자까지 입력할 수 있습니다."},
		{"약관 오류 아님", termsMessage(errors.New("db down")), ""},

		{"base.html 없음", themeMessage(theme.ErrNoBase), "테마에 base.html 파일이 없습니다."},
		{"압축 안 base.html 없음", themeMessage(theme.ErrZipNoBase), "테마에 base.html 파일이 없습니다."},
		// 생산자를 직접 거친다 — 손으로 만든 오류는 생산자가 감싸는 방식을 바꿔도 통과한다.
		{"요구 버전", themeMessage(requiresErr(t)), "이 테마는 Ondolith 2.0.0 이상에서 사용할 수 있습니다. (현재 버전 1.4.2)"},
		{"theme.json", themeMessage(manifestErr(t)), "theme.json 파일의 형식이 올바르지 않습니다."},
		{"zip 아님", themeMessage(theme.Install(t.TempDir(), "t", bytes.NewReader([]byte("not a zip")), 9, false)), "zip 파일을 열 수 없습니다. 파일이 손상되지 않았는지 확인해 주세요."},
		{"압축 해제 크기", themeMessage(theme.ErrZipUnpackedTooLarge), "압축을 풀면 허용 크기(최대 20MB)를 넘습니다."},
		{"압축 크기", themeMessage(theme.ErrZipTooLarge), "압축 파일은 최대 20MB까지 올릴 수 있습니다."},
		{"압축 파일 수", themeMessage(theme.ErrZipTooMany), "압축 파일에는 파일을 최대 2000개까지 담을 수 있습니다."},
		{"압축 깊이", themeMessage(theme.ErrZipTooDeep), "압축 파일 안의 폴더 깊이는 최대 10단계입니다."},
		{"압축 항목", themeMessage(fmt.Errorf("%w: 상위 경로 %q", theme.ErrZipEntry, "../x")), "압축 파일에 허용되지 않는 항목(심볼릭 링크, 상위 폴더 경로 등)이 있습니다."},
		{"압축비", themeMessage(theme.ErrZipRatio), "압축을 풀면 크기가 지나치게 커지는 파일입니다."},
		{"압축 읽기", themeMessage(fmt.Errorf("%w: not a valid zip file", theme.ErrZipRead)), "zip 파일을 열 수 없습니다. 파일이 손상되지 않았는지 확인해 주세요."},
		{"테마 이름", themeMessage(theme.ErrThemeBadName), "테마 이름은 영문 소문자, 숫자, 하이픈(-)만 사용해 64자 이내로 입력해 주세요."},
		{"테마 오류 아님", themeMessage(errors.New("open /srv/themes: permission denied")), ""},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

func requiresErr(t *testing.T) error {
	t.Helper()
	_, err := theme.CheckRequires("2.0.0", "1.4.2")
	return err
}

func manifestErr(t *testing.T) error {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "theme.json"), []byte(`{"name":`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := theme.ReadManifest(dir)
	return err
}

// 문구 함수가 모르는 오류는 고장이다 — 원인은 로그로 가고 화면에는 일반 문장만 나간다.
func TestUnknownAdminFailureIsLoggedNotShown(t *testing.T) {
	var buf bytes.Buffer
	d := &Deps{Logger: slog.New(slog.NewTextHandler(&buf, nil))}
	cause := errors.New(`ERROR: new row violates check constraint "x" (SQLSTATE 23514)`)
	got := d.refusal(httptest.NewRequest(http.MethodPost, "/admin/terms", nil), termsMessage(cause), cause)
	if got != "요청을 처리하지 못했습니다. 잠시 후 다시 시도해 주세요." {
		t.Errorf("화면 문구 = %q", got)
	}
	if !strings.Contains(buf.String(), "SQLSTATE 23514") || !strings.Contains(buf.String(), "/admin/terms") {
		t.Errorf("원인이 로그에 없다: %s", buf.String())
	}
	// 문장이 있는 거부는 로그에 남기지 않는다 — 잘못된 입력을 되풀이해 로그를 불릴 수 없게.
	buf.Reset()
	if got := d.refusal(httptest.NewRequest(http.MethodPost, "/admin/terms", nil),
		termsMessage(commerce.ErrTermsIncomplete), commerce.ErrTermsIncomplete); got != "종류, 버전, 본문을 모두 입력해 주세요." || buf.Len() != 0 {
		t.Errorf("알려진 거부 = %q, 로그 %q", got, buf.String())
	}
}

// refusalShown checks a refused request's page: it says want, and none of what
// err.Error() carries — a package prefix or a database error — reached it.
func refusalShown(t *testing.T, page, want string) {
	t.Helper()
	if !strings.Contains(page, want) {
		t.Errorf("화면에 %q 가 없다", want)
	}
	// `content:` 는 한글이 뒤따를 때만 본다 — 관리자 화면은 admin.css 를 <style> 로
	// 싣고, 거기 `justify-content:` 가 있다.
	if m := leakedPrefix.FindString(page); m != "" {
		t.Errorf("화면에 내부 오류 문자열 %q 가 나왔다", m)
	}
	for _, leak := range []string{"commerce:", "theme:", "SQLSTATE"} {
		if strings.Contains(page, leak) {
			t.Errorf("화면에 내부 오류 문자열 %q 가 나왔다", leak)
		}
	}
}

var leakedPrefix = regexp.MustCompile(`content: \p{Hangul}`)

// 문구가 실제 화면까지 간다 — 페이지 주소(A-302)·약관(A-207)·게시판 삭제(A-305).
func TestAdminRefusalsReachTheirScreens(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{
		"page.create": true, "settings.update": true, "board.manage": true}}
	d, pool := fixture(t, caller)
	ctx := context.Background()

	rec := postAdmin(t, d.PageSave, "/admin/pages/new", nil, url.Values{
		"slug": {"admin"}, "title": {"소개"}, "status": {"draft"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("예약 주소 = HTTP %d, want 422", rec.Code)
	}
	refusalShown(t, rec.Body.String(), "시스템에서 사용하는 주소입니다. 다른 주소를 입력해 주세요.")

	rec = postAdmin(t, d.TermsAdd, "/admin/terms", nil, url.Values{
		"kind": {"service"}, "version": {"1.0"}, "body": {strings.Repeat("가", 20001)},
		"effective_at": {time.Now().Add(24 * time.Hour).Format("2006-01-02")}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("긴 약관 = HTTP %d, want 422", rec.Code)
	}
	refusalShown(t, rec.Body.String(), "종류는 50자, 버전은 20자, 본문은 20,000자까지 입력할 수 있습니다.")

	var board string
	if err := pool.QueryRow(ctx,
		`INSERT INTO boards (slug, name) VALUES ('kept', '남는 글') RETURNING id`).Scan(&board); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO posts (board_id, title, body) VALUES ($1, '글', '본문')`, board); err != nil {
		t.Fatal(err)
	}
	// 확인 없이 온 요청에는 확인 화면이 이 문구와 함께 나간다.
	rec = postAdmin(t, d.BoardDelete, "/admin/boards/"+board+"/delete", map[string]string{"id": board}, url.Values{})
	if rec.Code != http.StatusConflict {
		t.Errorf("글이 남은 게시판 삭제 = HTTP %d, want 409", rec.Code)
	}
	refusalShown(t, rec.Body.String(), "글이 남아 있는 게시판입니다.")
}

// **관리자에게 보이는 문구는 화면과 같은 말을 쓴다.** 템플릿은 「주소」·「공개」로 맞췄는데,
// 페이지 저장 거부가 「이미 사용 중인 슬러그입니다.」, 상품 삭제 거부가 「노출을 끄세요.」였다.
func TestAdminMessagesUseScreenWords(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	literal := regexp.MustCompile(`"[^"\n]*(슬러그|노출을)[^"\n]*"`)
	scanned := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		for _, m := range literal.FindAll(b, -1) {
			t.Errorf("%s: %s — 화면은 「주소」·「공개」라고 부른다", f, m)
		}
	}
	if scanned < 10 {
		t.Fatalf("소스를 %d 개밖에 못 읽었다 — 검사가 헛돌았다", scanned)
	}
}
