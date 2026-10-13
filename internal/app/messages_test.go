package app

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/commerce"
	"github.com/emirue/ondolith/internal/content"
)

// 손님·회원이 보는 거부 문구 (NFR-210). 표가 곧 문구 목록이다 — 문장을 바꾸면
// 여기서 같이 바뀌고, 패키지 접두어(`content:`·`commerce:`)나 키 이름이 새면 깨진다.
func TestVisitorRefusalsReadAsSentences(t *testing.T) {
	field := func(typ content.FieldType, err error) error {
		return content.FieldError{Key: "phone", Label: "연락처", Type: typ, Err: err}
	}
	wrapType := func(typ content.FieldType) error {
		return field(typ, fmt.Errorf("%w: 내부 설명", content.ErrFieldType))
	}
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"필수 글자", fieldsMessage(field(content.FieldText, content.ErrFieldRequired)), "「연락처」 항목을 입력해 주세요."},
		{"필수 선택", fieldsMessage(field(content.FieldSelect, content.ErrFieldRequired)), "「연락처」 항목을 선택해 주세요."},
		{"필수 여러 개", fieldsMessage(field(content.FieldMultiselect, content.ErrFieldRequired)), "「연락처」 항목을 하나 이상 선택해 주세요."},
		{"선택지 밖", fieldsMessage(field(content.FieldSelect, content.ErrFieldOption)), "「연락처」 항목은 목록에 있는 값 중에서 선택해 주세요."},
		{"숫자 아님", fieldsMessage(wrapType(content.FieldNumber)), "「연락처」 항목에는 숫자를 입력해 주세요."},
		{"날짜 아님", fieldsMessage(wrapType(content.FieldDate)), "「연락처」 항목에 올바른 날짜를 입력해 주세요."},
		{"주소 아님", fieldsMessage(wrapType(content.FieldURL)), "「연락처」 항목에는 http:// 또는 https:// 로 시작하는 주소를 입력해 주세요."},
		{"모르는 항목", fieldsMessage(content.FieldError{Key: "x", Err: content.ErrUnknownField}), "입력 항목이 변경되었습니다. 페이지를 새로고침한 뒤 다시 입력해 주세요."},
		{"항목 오류 아님", fieldsMessage(errors.New("db down")), ""},

		{"재고 부족", cartMessage(commerce.Sellable{ProductVisible: true, VariantVisible: true, Stock: 1}.CheckAvailable(2)), "재고가 부족합니다. (남은 수량 1개)"},
		{"이미 담긴 수량", cartMessage(&commerce.StockError{Want: 6, Left: 5, InCart: 3}), "재고가 부족합니다. (남은 수량 5개, 장바구니에 이미 3개)"},
		{"품절", cartMessage(commerce.Sellable{ProductVisible: true, VariantVisible: true}.CheckAvailable(1)), "품절된 상품입니다."},
		{"판매 안 함", cartMessage(commerce.ErrNotSellable), "현재 판매하지 않는 상품입니다."},
		{"수량 범위", cartMessage(commerce.Sellable{ProductVisible: true, VariantVisible: true, Stock: 5}.CheckAvailable(0)), "수량은 1개부터 999개까지 선택할 수 있습니다."},
		{"주문 품목 재고", cartMessage(commerce.ItemError{Name: "티셔츠", Err: &commerce.StockError{Want: 3, Left: 1}}), "「티셔츠」 상품의 재고가 부족합니다. (남은 수량 1개)"},
		{"주문 품목 품절", cartMessage(commerce.ItemError{Name: "티셔츠", Err: &commerce.StockError{Want: 1}}), "「티셔츠」 상품은 품절되었습니다."},
		{"주문 품목 판매 안 함", cartMessage(commerce.ItemError{Name: "티셔츠", Err: commerce.ErrNotSellable}), "「티셔츠」 상품은 현재 판매하지 않습니다."},
		{"약관", cartMessage(commerce.ErrTermsRequired), "필수 약관에 동의해 주세요."},
		{"주문자 연락처", cartMessage(commerce.ErrOrdererContact), "주문자 이메일과 연락처를 입력해 주세요."},
		{"장바구니 오류 아님", cartMessage(errors.New("db down")), ""},

		{"첨부 안 받음", uploadMessage(content.ErrUploadNotAllowed), "이 게시판에는 파일을 첨부할 수 없습니다."},
		{"첨부 개수", uploadMessage(content.LimitError{Err: content.ErrUploadTooMany, Limit: 5}), "파일은 글 하나에 최대 5개까지 첨부할 수 있습니다."},
		{"첨부 크기", uploadMessage(content.LimitError{Err: content.ErrUploadTooLarge, Limit: 10 << 20}), "10MB 이하의 파일만 첨부할 수 있습니다."},
		{"첨부 크기 KB", uploadMessage(content.LimitError{Err: content.ErrUploadTooLarge, Limit: 512 << 10}), "512KB 이하의 파일만 첨부할 수 있습니다."},
		// 확장자와 내용 불일치는 같은 문장이다 — 어느 검증인지 알려주지 않는다 (D19 P-205).
		{"확장자", uploadMessage(fmt.Errorf("%w: %q", content.ErrUploadExt, ".exe")), "허용되지 않는 파일 형식입니다."},
		{"내용 불일치", uploadMessage(fmt.Errorf("%w: text/plain", content.ErrUploadContent)), "허용되지 않는 파일 형식입니다."},
		{"빈 파일", uploadMessage(content.ErrUploadEmpty), "빈 파일은 첨부할 수 없습니다."},
		{"설정 오류", uploadMessage(content.ErrUploadSetting), "첨부 설정에 문제가 있어 파일을 저장하지 못했습니다. 사이트 관리자에게 문의해 주세요."},
		{"첨부 오류 아님", uploadMessage(errors.New("disk full")), ""},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, tc.got, tc.want)
		}
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
