package commerce

import (
	"errors"
	"fmt"
	"math/bits"
)

var (
	// ErrDiscountTooLarge 는 할인이 상품 합계를 넘는 경우다. 넘으면 총액이
	// 음수가 되고, 음수 총액은 "돈을 돌려주는 주문" 이다.
	ErrDiscountTooLarge = errors.New("commerce: 할인이 상품 합계를 넘습니다")
	// ErrRefundQuantity 는 소진 수량을 넘겨 환불하려는 경우다.
	ErrRefundQuantity = errors.New("commerce: 환불 수량이 남은 수량을 넘습니다")
)

// Apportion splits `discount` across lines in proportion to their amounts.
//
// **최대 나머지법이다.** 비례 배분을 정수로 내림하면 합계가 할인액보다 작아지고,
// 그 차이가 어디로 갔는지 아무도 모른다. 남은 몫은 나머지가 큰 품목부터 1원씩
// 준다 — 결과의 합은 언제나 `discount` 와 정확히 같다.
//
// 동점이면 앞 품목이 가져간다. 순서가 정해져 있지 않으면 같은 주문을 두 번
// 계산했을 때 배분이 달라지고, 그러면 스냅샷이라는 말이 무의미해진다.
//
// 반환값의 i번째는 lines[i] 에 배분된 할인액이다. 각 값은 그 품목 금액을 넘지
// 않는다 — 넘으면 그 품목의 환불액이 음수가 된다.
func Apportion(lines []int, discount int) ([]int, error) {
	if discount < 0 {
		return nil, fmt.Errorf("%w: %d", ErrPriceNegative, discount)
	}
	total := 0
	for _, l := range lines {
		if l < 0 {
			return nil, fmt.Errorf("%w: 품목 금액 %d", ErrPriceNegative, l)
		}
		total += l
	}
	if discount > total {
		return nil, fmt.Errorf("%w: 할인 %d, 합계 %d", ErrDiscountTooLarge, discount, total)
	}
	out := make([]int, len(lines))
	if discount == 0 || total == 0 {
		return out, nil
	}

	// 1단계: 내림 배분. 나머지를 함께 기억한다.
	//
	// `l * discount` 는 최대 10^10 × 10^10 = 10^20 이고 int64 상한은 약
	// 9.2×10^18 이다 — **넘친다.** 나눗셈을 먼저 하는 손재주(`l/total*discount
	// + (l%total)*discount/total`)도 두 번째 항에서 같은 이유로 넘친다.
	// 128비트 곱셈·나눗셈이 정확하고 짧다.
	assigned := 0
	rem := make([]int, len(lines))
	for i, l := range lines {
		hi, lo := bits.Mul64(uint64(l), uint64(discount))
		var q, r uint64
		if uint64(l) == uint64(total) {
			// bits.Div64 는 몫이 64비트를 넘으면 패닉한다. l == total 이면
			// 몫이 정확히 discount 이므로 나눌 필요가 없다.
			q, r = uint64(discount), 0
		} else {
			q, r = bits.Div64(hi, lo, uint64(total))
		}
		out[i] = int(q)
		rem[i] = int(r)
		assigned += int(q)
	}

	// 2단계: 남은 몫을 나머지가 큰 순서로 1원씩. 동점이면 앞 품목.
	left := discount - assigned
	for ; left > 0; left-- {
		best := -1
		for i := range lines {
			if rem[i] == 0 {
				continue
			}
			if best < 0 || rem[i] > rem[best] {
				best = i
			}
		}
		if best < 0 {
			// 나머지가 전부 0 인데 남은 몫이 있다 — 산술이 깨진 것이다.
			// 조용히 버리면 배분 합계가 할인액과 달라진다.
			return nil, fmt.Errorf("commerce: 할인 배분이 %d 원 남았다", left)
		}
		out[best]++
		rem[best] = 0
	}

	// 배분 결과가 품목 금액을 넘지 않는다는 것을 여기서 다시 확인하지 않는다.
	// 비례 배분이라 수학적으로 넘을 수 없고, 변이를 넣어도 아무 테스트가 울지
	// 않았다 — 그 보장을 지는 것은 discount_test.go 의 성질 테스트다 (무작위
	// 2000 조합에서 `배분 ≤ 품목 금액` 과 `합계 == 할인액`을 단언한다).
	return out, nil
}

// RefundableAmount is what refunding `qty` more units of one line is worth.
//
// 식은 `floor(할인후금액 × (소진+qty) / 수량) − floor(할인후금액 × 소진 / 수량)` 다.
//
// 단순히 `단가 × qty` 로 하면 할인이 붙은 품목에서 합이 안 맞는다: 13,000원짜리
// 2개에 1,000원이 할인됐다면 할인후 금액은 25,000원인데, 한 개씩 두 번 환불하며
// 12,500원씩 주려면 반올림이 필요하고 그 반올림이 매번 같으리라는 보장이 없다.
//
// 이 식은 **단조**이고 누적이 정확하다 — 전 수량을 어떤 순서로 나눠 환불해도
// 합계가 할인후 금액과 1원도 다르지 않다.
func RefundableAmount(lineAmount, discount, quantity, settled, qty int) (int, error) {
	if quantity < 1 {
		return 0, fmt.Errorf("%w: 주문 수량 %d", ErrQuantityRange, quantity)
	}
	if qty < 1 {
		return 0, fmt.Errorf("%w: 환불 수량 %d", ErrQuantityRange, qty)
	}
	if settled < 0 || settled+qty > quantity {
		return 0, fmt.Errorf("%w: 소진 %d + 요청 %d > 주문 %d",
			ErrRefundQuantity, settled, qty, quantity)
	}
	net := lineAmount - discount
	if net < 0 {
		return 0, fmt.Errorf("%w: 할인 %d 이 금액 %d 를 넘는다", ErrPriceNegative, discount, lineAmount)
	}
	return share(net, settled+qty, quantity) - share(net, settled, quantity), nil
}

// share is floor(net * n / q).
//
// 곱이 넘치지 않는다: net 은 금액 상한(100억) 이하이고 n 은 수량 상한(999)
// 이하라 곱이 10^13 이다. int64 상한 9.2×10^18 과 다섯 자리 차이다.
//
// Apportion 쪽은 사정이 다르다 — 거기서는 금액 × 할인액이라 10^20 이 되어
// 실제로 넘친다. 그래서 그쪽만 나눗셈을 먼저 한다.
func share(net, n, q int) int { return net * n / q }
