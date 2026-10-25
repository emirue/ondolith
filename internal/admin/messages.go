package admin

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/emirue/ondolith/internal/commerce"
	"github.com/emirue/ondolith/internal/content"
	"github.com/emirue/ondolith/internal/theme"
)

// 관리자 화면의 거부 문구 (NFR-210). err.Error() 는 로그용이다 — 패키지 접두어,
// 상위 카테고리의 uuid, DB 제약 이름이 그대로 실린다. 문구는 이 파일에 모은다.
//
// 각 함수는 자기가 아는 거부가 아니면 "" 를 돌려준다. 그것은 고장이므로 호출자가
// 원인을 로그에 남기고 일반 문구를 쓴다 (D22 3절: 사용자에게는 일반 메시지, 로그에는
// 원인 전부).

// refusal is msg when one of the functions below had a sentence for err, and
// otherwise logs err and says only that it did not work.
func (d *Deps) refusal(r *http.Request, msg string, err error) string {
	if msg != "" {
		return msg
	}
	d.logFailure(r, err)
	return "요청을 처리하지 못했습니다. 잠시 후 다시 시도해 주세요."
}

// slugMessage is for page and category addresses (A-302·A-509).
func slugMessage(err error) string {
	switch {
	case errors.Is(err, content.ErrSlugFormat):
		return "주소에는 영문 소문자, 숫자, 하이픈(-)만 사용할 수 있으며, 영문 소문자나 숫자로 시작해야 합니다."
	case errors.Is(err, content.ErrSlugReserved):
		return "시스템에서 사용하는 주소입니다. 다른 주소를 입력해 주세요."
	case errors.Is(err, commerce.ErrSlugTaken):
		return "이미 사용 중인 주소입니다. 다른 주소를 입력해 주세요."
	}
	return ""
}

// fieldDefMessage is for a custom field definition (A-306·A-406). f is what was
// submitted: whether the options are missing or extra depends on its type.
func fieldDefMessage(err error, f content.FieldSchema) string {
	switch {
	case errors.Is(err, content.ErrFieldKeyFormat):
		return "키는 영문 소문자로 시작해야 하며, 영문 소문자·숫자·밑줄(_)만 사용해 32자 이내로 입력해 주세요."
	case errors.Is(err, content.ErrFieldKeyReserved):
		return "기본 항목과 이름이 같은 키는 사용할 수 없습니다. 다른 키를 입력해 주세요."
	case errors.Is(err, content.ErrFieldLabel):
		return "라벨은 1자 이상 100자 이하로 입력해 주세요."
	case errors.Is(err, content.ErrFieldTypeUnknown):
		return "지원하지 않는 타입입니다."
	case errors.Is(err, content.ErrFieldOptionsLong):
		return "선택지가 너무 많거나 깁니다. 선택지를 줄여 주세요."
	case errors.Is(err, content.ErrFieldOptions):
		if f.Type == content.FieldSelect || f.Type == content.FieldMultiselect {
			return "select·multiselect 항목에는 선택지를 하나 이상 입력해 주세요."
		}
		return "선택지는 select·multiselect 항목에만 입력할 수 있습니다."
	}
	return ""
}

// menuMessage is for A-204.
func menuMessage(err error) string {
	switch {
	case errors.Is(err, content.ErrMenuCycle):
		return "자기 자신이나 하위 메뉴를 상위 메뉴로 지정할 수 없습니다."
	case errors.Is(err, content.ErrMenuParent):
		return "상위 메뉴를 찾을 수 없습니다. 페이지를 새로고침한 뒤 다시 시도해 주세요."
	}
	return ""
}

// categoryMessage is for A-509.
func categoryMessage(err error) string {
	switch {
	case errors.Is(err, commerce.ErrCategoryMissing):
		return "상위 카테고리를 찾을 수 없습니다. 페이지를 새로고침한 뒤 다시 시도해 주세요."
	case errors.Is(err, commerce.ErrCategoryCycle):
		return "자기 자신이나 하위 카테고리를 상위 카테고리로 지정할 수 없습니다."
	case errors.Is(err, commerce.ErrCategoryDepth):
		return fmt.Sprintf("카테고리는 최대 %d단계까지 만들 수 있습니다.", commerce.MaxCategoryDepth)
	case errors.Is(err, commerce.ErrCategoryInUse):
		return "상품이나 하위 카테고리가 있는 카테고리는 삭제할 수 없습니다."
	}
	return slugMessage(err)
}

// transitionMessage is for A-506. D19 names both statuses in the sentence.
func transitionMessage(err error) string {
	var te *commerce.TransitionError
	switch {
	case errors.As(err, &te) && errors.Is(err, commerce.ErrActorNotAllowed):
		return fmt.Sprintf("「%s」 상태에서 「%s」 상태로는 이 화면에서 변경할 수 없습니다.", te.From, te.To)
	case errors.As(err, &te):
		return fmt.Sprintf("「%s」 상태에서 「%s」 상태로 변경할 수 없습니다.", te.From, te.To)
	case errors.Is(err, commerce.ErrTransitionNotAllowed), errors.Is(err, commerce.ErrActorNotAllowed):
		return "현재 주문 상태에서는 선택한 상태로 변경할 수 없습니다."
	case errors.Is(err, commerce.ErrUnknownStatus):
		return "알 수 없는 주문 상태입니다."
	}
	return ""
}

// termsMessage is for A-207's new version form.
func termsMessage(err error) string {
	switch {
	case errors.Is(err, commerce.ErrTermsIncomplete):
		return "종류, 버전, 본문을 모두 입력해 주세요."
	case errors.Is(err, commerce.ErrTermsTooLong):
		return "종류는 50자, 버전은 20자, 본문은 20,000자까지 입력할 수 있습니다."
	}
	return ""
}

// msgBoardInUse heads A-305's delete confirmation (board-delete.html): a board
// with posts is deleted only after this screen's checkbox.
const msgBoardInUse = "글이 남아 있는 게시판입니다. 삭제하면 글, 댓글, 첨부 파일이 모두 함께 삭제됩니다. 아래에서 확인해 주세요."

// themeRefusal is A-202·A-203's sentence: what failed, then why. A fault with no
// sentence gets refusal's own, which already says it failed — prefixing it would
// say so twice.
func (d *Deps) themeRefusal(r *http.Request, failed string, err error) string {
	if msg := themeMessage(err); msg != "" {
		return failed + " " + msg
	}
	return d.refusal(r, "", err)
}

// themeMessage is for activating (A-202) and uploading (A-203) a theme.
func themeMessage(err error) string {
	var req *theme.RequiresError
	switch {
	case errors.Is(err, theme.ErrNoBase), errors.Is(err, theme.ErrZipNoBase):
		return "테마에 base.html 파일이 없습니다."
	case errors.As(err, &req):
		return fmt.Sprintf("이 테마는 Ondolith %s 이상에서 사용할 수 있습니다. (현재 버전 %s)", req.Need, req.Have)
	case errors.Is(err, theme.ErrManifest):
		return "theme.json 파일의 형식이 올바르지 않습니다."
	case errors.Is(err, theme.ErrZipTooLarge):
		return fmt.Sprintf("압축 파일은 최대 %dMB까지 올릴 수 있습니다.", theme.MaxZipBytes>>20)
	case errors.Is(err, theme.ErrZipUnpackedTooLarge):
		return fmt.Sprintf("압축을 풀면 허용 크기(최대 %dMB)를 넘습니다.", theme.MaxTotalBytes>>20)
	case errors.Is(err, theme.ErrZipTooMany):
		return fmt.Sprintf("압축 파일에는 파일을 최대 %d개까지 담을 수 있습니다.", theme.MaxEntries)
	case errors.Is(err, theme.ErrZipTooDeep):
		return fmt.Sprintf("압축 파일 안의 폴더 깊이는 최대 %d단계입니다.", theme.MaxDepth)
	case errors.Is(err, theme.ErrZipEntry):
		return "압축 파일에 허용되지 않는 항목(심볼릭 링크, 상위 폴더 경로 등)이 있습니다."
	case errors.Is(err, theme.ErrZipRatio):
		return "압축을 풀면 크기가 지나치게 커지는 파일입니다."
	case errors.Is(err, theme.ErrZipRead):
		return "zip 파일을 열 수 없습니다. 파일이 손상되지 않았는지 확인해 주세요."
	case errors.Is(err, theme.ErrThemeBadName):
		return "테마 이름은 영문 소문자, 숫자, 하이픈(-)만 사용해 64자 이내로 입력해 주세요."
	}
	return ""
}
