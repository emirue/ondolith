package app

import (
	"errors"
	"fmt"

	"github.com/emirue/ondolith/internal/commerce"
	"github.com/emirue/ondolith/internal/content"
)

// 손님·회원 화면의 거부 문구 (NFR-210). err.Error() 는 로그용이다 — 패키지 접두어와
// 키 이름이 붙어 있어서, 그대로 내보내면 가입 화면에 `phone: content: 필수 필드` 가
// 나간다. 문구는 이 파일에 모은다: 화면마다 흩어 두면 같은 거부가 화면마다 다른
// 말이 되고, 검토할 때 한 번에 읽을 수 없다.
//
// 각 함수는 자기가 아는 거부가 아니면 "" 를 돌려준다. 그것은 사람이 고칠 수 있는
// 거부가 아니라 고장이므로, 호출자가 원인을 로그에 남기고 일반 문구를 쓴다.
// 거꾸로 여기서 문장이 나온 거부는 로그에 남기지 않는다 — 잘못된 입력을 되풀이하는
// 것만으로 로그를 불릴 수 있게 되기 때문이다.

// fieldsMessage is for content.ValidateCustomFields (P-103·P-108·P-205·P-206).
func fieldsMessage(err error) string {
	var fe content.FieldError
	if !errors.As(err, &fe) {
		return ""
	}
	if errors.Is(err, content.ErrUnknownField) {
		// 폼과 스키마가 어긋났다 — 화면을 연 뒤 운영자가 항목을 지웠거나, 폼을 고쳐 보냈다.
		return "입력 항목이 변경되었습니다. 페이지를 새로고침한 뒤 다시 입력해 주세요."
	}
	name := "「" + fe.Label + "」 항목"
	switch {
	case errors.Is(err, content.ErrFieldRequired):
		switch fe.Type {
		case content.FieldSelect:
			return name + "을 선택해 주세요."
		case content.FieldMultiselect:
			return name + "을 하나 이상 선택해 주세요."
		}
		return name + "을 입력해 주세요."
	case errors.Is(err, content.ErrFieldOption):
		return name + "은 목록에 있는 값 중에서 선택해 주세요."
	case errors.Is(err, content.ErrFieldType):
		switch fe.Type {
		case content.FieldNumber:
			return name + "에는 숫자를 입력해 주세요."
		case content.FieldDate:
			return name + "에 올바른 날짜를 입력해 주세요."
		case content.FieldURL:
			return name + "에는 http:// 또는 https:// 로 시작하는 주소를 입력해 주세요."
		}
	}
	return name + "의 값이 올바르지 않습니다."
}

// cartMessage is for the cart (P-401·P-403) and the order form (P-406).
func cartMessage(err error) string {
	subject := ""
	var item commerce.ItemError
	if errors.As(err, &item) {
		subject = "「" + item.Name + "」 상품"
	}
	var stock *commerce.StockError
	switch {
	case errors.As(err, &stock) && stock.Left <= 0:
		if subject != "" {
			return subject + "은 품절되었습니다."
		}
		return "품절된 상품입니다."
	case errors.As(err, &stock):
		left := fmt.Sprintf(" (남은 수량 %d개)", stock.Left)
		if stock.InCart > 0 {
			// 담기는 이미 담긴 수량과 합쳐 본다 — 그것을 말하지 않으면 남은 수량만큼
			// 담으려다 같은 거부를 다시 받는다.
			left = fmt.Sprintf(" (남은 수량 %d개, 장바구니에 이미 %d개)", stock.Left, stock.InCart)
		}
		if subject != "" {
			return subject + "의 재고가 부족합니다." + left
		}
		return "재고가 부족합니다." + left
	case errors.Is(err, commerce.ErrOutOfStock):
		return "재고가 부족합니다."
	case errors.Is(err, commerce.ErrNotSellable):
		if subject != "" {
			return subject + "은 현재 판매하지 않습니다."
		}
		return "현재 판매하지 않는 상품입니다."
	case errors.Is(err, commerce.ErrQuantityRange):
		return fmt.Sprintf("수량은 1개부터 %d개까지 선택할 수 있습니다.", commerce.MaxQuantity)
	case errors.Is(err, commerce.ErrAmountTooBig):
		return "한 번에 결제할 수 있는 금액(20억 원)을 넘습니다. 수량을 줄여 나눠 주문하세요."
	case errors.Is(err, commerce.ErrTermsRequired):
		return "필수 약관에 동의해 주세요."
	case errors.Is(err, commerce.ErrOrdererContact):
		return "주문자 이메일과 연락처를 입력해 주세요."
	}
	return ""
}

// uploadMessage is for attachments on a post (P-205·P-206).
func uploadMessage(err error) string {
	var limit content.LimitError
	switch {
	case errors.Is(err, content.ErrUploadNotAllowed):
		return "이 게시판에는 파일을 첨부할 수 없습니다."
	case errors.As(err, &limit) && errors.Is(err, content.ErrUploadTooMany):
		return fmt.Sprintf("파일은 글 하나에 최대 %d개까지 첨부할 수 있습니다.", limit.Limit)
	case errors.As(err, &limit) && errors.Is(err, content.ErrUploadTooLarge):
		return sizeLabel(limit.Limit) + " 이하의 파일만 첨부할 수 있습니다."
	case errors.Is(err, content.ErrUploadTooLarge):
		return "파일 크기가 너무 큽니다."
	case errors.Is(err, content.ErrUploadExt), errors.Is(err, content.ErrUploadContent):
		// 한 문장이다 — 어느 검증에서 걸렸는지 알려주지 않는다 (D19 P-205). 「내용이
		// 확장자와 다르다」는 위장 파일을 만드는 사람에게 다음 시도를 알려준다.
		return "허용되지 않는 파일 형식입니다."
	case errors.Is(err, content.ErrUploadEmpty):
		return "빈 파일은 첨부할 수 없습니다."
	case errors.Is(err, content.ErrUploadSetting):
		return "첨부 설정에 문제가 있어 파일을 저장하지 못했습니다. 사이트 관리자에게 문의해 주세요."
	}
	return ""
}

// sizeLabel writes a byte limit the way people read one: 10MB, 512KB.
func sizeLabel(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%dMB", n>>20)
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%dKB", n>>10)
	}
	return fmt.Sprintf("%d바이트", n)
}
