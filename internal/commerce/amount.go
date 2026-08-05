package commerce

import (
	"errors"
	"fmt"
)

// 금액은 전부 정수 minor unit 이다. 이 파일에 float32·float64 가 없는 것이
// 규칙이고, amount_test.go 의 타입 검사가 그것을 매 빌드 확인한다 — 부동소수는
// 어디선가 반올림되고, 그 어디선가는 매번 다른 곳이다 (D30 「금액」).

var (
	ErrQuantityRange = errors.New("commerce: 수량이 범위를 벗어났습니다")
	ErrPriceNegative = errors.New("commerce: 금액이 음수입니다")
	ErrAmountTooBig  = errors.New("commerce: 금액이 상한을 넘었습니다")
)

// maxAmount is 100억 원. 그 위는 사람이 저지른 것이지 주문이 아니다.
//
// 상한이 있는 이유는 오버플로 때문이 아니라(int64 는 넉넉하다) 수량 필드에
// 들어온 큰 수가 합계를 통해 PG 승인 요청까지 흘러가는 것을 여기서 끊기
// 위해서다.
const maxAmount = 10_000_000_000

// maxQuantity 는 한 품목당 상한이다. D19 P-405 의 폼 상한과 같은 값이어야
// 하는데, 폼 검증은 브라우저가 건너뛸 수 있으므로 계산 경로에도 둔다.
const maxQuantity = 999

// Line is one order line's inputs. 가격은 상품·조합에서 읽은 값이고 클라이언트가
// 보낸 값이 아니다 — 그 구분이 이 구조체에 `Price` 필드가 있는 이유이자,
// 폼 바인딩 구조체에는 없는 이유다 (D19 P-405 「받지 않는 필드」).
type Line struct {
	BasePrice  int // products.base_price
	PriceDelta int // product_variants.price_delta (음수 가능)
	Quantity   int
}

// UnitPrice is base + delta, which is what order_items.unit_price snapshots.
//
// 음수는 거부한다. D30 이 price_delta 에 음수를 허용하는 것은 낮은 등급 옵션이
// 기본가보다 싼 것이 정상이기 때문이고, 합이 음수가 되는 것은 그 이유의 범위
// 밖이다 — 허용하면 조합 하나가 주문 총액을 깎는 쿠폰이 된다.
func (l Line) UnitPrice() (int, error) {
	if l.BasePrice < 0 {
		return 0, fmt.Errorf("%w: 기본가 %d", ErrPriceNegative, l.BasePrice)
	}
	unit := l.BasePrice + l.PriceDelta
	if unit < 0 {
		return 0, fmt.Errorf("%w: 기본가 %d + 차액 %d", ErrPriceNegative, l.BasePrice, l.PriceDelta)
	}
	return unit, nil
}

// Amount is the line total — order_items.line_amount 의 생성 컬럼과 같은 식이다.
func (l Line) Amount() (int, error) {
	if l.Quantity < 1 || l.Quantity > maxQuantity {
		return 0, fmt.Errorf("%w: %d (1~%d)", ErrQuantityRange, l.Quantity, maxQuantity)
	}
	unit, err := l.UnitPrice()
	if err != nil {
		return 0, err
	}
	amount := unit * l.Quantity
	if amount > maxAmount {
		return 0, fmt.Errorf("%w: %d", ErrAmountTooBig, amount)
	}
	return amount, nil
}

// Shipping is D50 「Phase 3 정책값」's whole model: one free-shipping threshold.
//
// 도서산간 추가비는 없다. 그것은 우편번호 표를 필요로 하고 그 표는 갱신 대상
// 이라, FR-613 이 `선택` 인 이유가 그것이다.
type Shipping struct {
	FlatFee       int // shipping.flat_fee
	FreeThreshold int // shipping.free_threshold — 0 이면 항상 유료
}

// Fee returns the shipping charge for a given goods subtotal.
//
// 경계는 "미만이면 부과" 다 (D50). 기준액과 정확히 같으면 무료 — `>` 로 쓰면
// 5만원 기준에서 5만원짜리 주문이 배송비를 무는데, 그것은 화면이 "5만원 이상
// 무료배송" 이라고 적어 둔 것과 다르다.
func (s Shipping) Fee(goods int) (int, error) {
	if s.FlatFee < 0 || s.FreeThreshold < 0 {
		return 0, fmt.Errorf("%w: 배송비 %d / 기준액 %d", ErrPriceNegative, s.FlatFee, s.FreeThreshold)
	}
	if s.FreeThreshold > 0 && goods >= s.FreeThreshold {
		return 0, nil
	}
	return s.FlatFee, nil
}

// Total is the number that goes into orders.total_amount, and the only number
// FR-607 compares against the PG's callback.
//
// 반환값이 (상품합계, 배송비, 총액) 셋인 이유: 주문서에 배송비를 따로 보여줘야
// 하는데, 총액만 돌려주면 화면이 배송비를 **다시 계산**하게 되고 그 순간 같은
// 규칙이 두 곳에 생긴다.
func Total(lines []Line, ship Shipping) (goods, fee, total int, err error) {
	if len(lines) == 0 {
		return 0, 0, 0, errors.New("commerce: 주문할 품목이 없습니다")
	}
	for _, l := range lines {
		amount, aerr := l.Amount()
		if aerr != nil {
			return 0, 0, 0, aerr
		}
		goods += amount
	}
	// 여기에 상품합계 상한 검사를 따로 두지 않는다. 배송비가 음수일 수 없으므로
	// goods > maxAmount 이면 total > maxAmount 이고, 아래 검사가 같은 것을 잡는다
	// — 변이를 넣어도 어떤 테스트도 울지 않아서 지웠다. 남겨 두면 지키는 것이
	// 있는 것처럼 보인다.
	fee, err = ship.Fee(goods)
	if err != nil {
		return 0, 0, 0, err
	}
	total = goods + fee
	if total > maxAmount {
		return 0, 0, 0, fmt.Errorf("%w: 총액 %d", ErrAmountTooBig, total)
	}
	return goods, fee, total, nil
}

// CancelRefund is what a cancellation gives back.
//
// D50: 부분 취소로 배송비를 환불하지 않는다. 배송이 나갔으면 그 비용은 발생했고,
// 부분 취소마다 비례 배분하면 남은 주문이 무료배송 기준을 밑도는 순간 배송비를
// 다시 청구해야 하는데 그 청구 화면은 없다.
func CancelRefund(itemsRefunded, fee int, whole bool) (int, error) {
	if itemsRefunded < 0 || fee < 0 {
		return 0, fmt.Errorf("%w: 품목 %d / 배송비 %d", ErrPriceNegative, itemsRefunded, fee)
	}
	if whole {
		return itemsRefunded + fee, nil
	}
	return itemsRefunded, nil
}
