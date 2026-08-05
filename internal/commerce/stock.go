package commerce

import (
	"errors"
	"fmt"
)

// 재고 규칙. 저장소가 아니라 규칙만 여기 있다 — 실제 차감은 `stock = stock + $1`
// 델타 갱신이고 (D30 product_variants), 절대값 UPDATE 경로를 만들지 않는 이유는
// 델타 두 건이 순서와 무관하게 둘 다 맞기 때문이다.

var (
	// ErrOutOfStock is D50's "백오더 없음": 재고 0 이면 담기도 주문도 거부한다.
	ErrOutOfStock = errors.New("commerce: 재고가 부족합니다")
	// ErrNotSellable is a variant that is hidden or belongs to a hidden product.
	ErrNotSellable = errors.New("commerce: 판매 중인 상품이 아닙니다")
	// ErrReservationMissing is 예약 해제인데 예약이 없는 경우 — 그냥 통과시키면
	// 재고가 조용히 늘어난다.
	ErrReservationMissing = errors.New("commerce: 해제할 예약이 없습니다")
)

// Sellable is what a variant must satisfy before it can be added or ordered.
//
// 상품과 조합 둘 다 본다. 조합만 보면 상품을 숨긴 뒤에도 조합 URL 을 아는
// 사람은 계속 살 수 있다 — A-503 이 "안 보이게" 를 누른 것과 다른 결과다.
type Sellable struct {
	ProductVisible bool
	VariantVisible bool
	Stock          int
}

// CheckAvailable reports whether `want` units can be taken right now.
//
// 재고 검사는 여기서 한 번, DB 의 `CHECK (stock >= 0)` 에서 다시 한 번 한다.
// 이쪽은 사용자에게 이유를 말해 주기 위한 것이고, 동시 요청을 실제로 막는 것은
// 저쪽이다 — 이 함수만 믿으면 두 요청이 같은 잔량을 보고 둘 다 통과한다.
func (s Sellable) CheckAvailable(want int) error {
	if !s.ProductVisible || !s.VariantVisible {
		return ErrNotSellable
	}
	if want < 1 || want > maxQuantity {
		return fmt.Errorf("%w: %d (1~%d)", ErrQuantityRange, want, maxQuantity)
	}
	if s.Stock < want {
		return fmt.Errorf("%w: 요청 %d, 재고 %d", ErrOutOfStock, want, s.Stock)
	}
	return nil
}

// MergeQuantity is what happens when a guest cart meets a member cart at login
// (D50 「비회원 장바구니 병합」).
//
// 더하되 재고 상한에서 자른다. 한쪽을 버리면 "왜 없어졌지" 가 되고, 자르지
// 않으면 합친 결과가 곧바로 재고를 넘는 장바구니가 된다 — 그 장바구니는 주문
// 화면에서야 거부되고, 사용자는 무엇을 줄여야 하는지 모른다.
func MergeQuantity(guest, member, stock int) int {
	sum := guest + member
	if sum > stock {
		sum = stock
	}
	if sum > maxQuantity {
		sum = maxQuantity
	}
	if sum < 0 {
		return 0
	}
	return sum
}

// StockDelta is one movement of stock, as it is applied: `stock = stock + N`.
type StockDelta struct {
	VariantID string
	Delta     int
}

// Reserve returns the deltas for taking `qty` units — 주문 생성(P-406)과 교환
// 접수(P-512)가 같은 것을 쓴다.
//
// 교환 접수에 예약이 필요한 이유는 D14 「교환 재고」다: 새 조합을 잡아 두지
// 않으면 수거하는 동안 그 조합이 팔려 나가고, 재발송할 물건이 없어진다.
func Reserve(variantID string, qty int) ([]StockDelta, error) {
	if qty < 1 {
		return nil, fmt.Errorf("%w: %d", ErrQuantityRange, qty)
	}
	return []StockDelta{{VariantID: variantID, Delta: -qty}}, nil
}

// Release returns the deltas for giving `qty` units back.
//
// 결제 실패(P-409), 취소, 교환 거부·취소가 부른다. D14 는 "거부·취소 시 반드시
// 푼다. 풀지 않으면 재고가 조용히 잠긴다" 고 적었다 — 잠긴 재고는 오류를 내지
// 않고 판매만 멈추므로, 사람이 A-503 에서 숫자를 보고서야 알아챈다.
//
// reserved 는 실제로 잡아 둔 수량이다. 그것을 넘겨 푸는 요청은 거부한다 —
// 통과시키면 없던 재고가 생기고, 그것이 다음 주문을 받는다.
func Release(variantID string, qty, reserved int) ([]StockDelta, error) {
	if qty < 1 {
		return nil, fmt.Errorf("%w: %d", ErrQuantityRange, qty)
	}
	if reserved < qty {
		return nil, fmt.Errorf("%w: 해제 %d, 예약 %d", ErrReservationMissing, qty, reserved)
	}
	return []StockDelta{{VariantID: variantID, Delta: qty}}, nil
}

// PickupRestock is deliberately empty — D50 「자동 재입고」.
//
// 수거 확인은 재고를 건드리지 않는다. 재판매 가능 여부를 코드가 알 수 없고,
// 자동으로 늘리면 파손품이 재고가 되어 다음 주문을 받는다. 재입고는 A-503 에서
// 사람이 한다.
//
// 함수로 남겨 두는 이유는 "여기서 재고를 늘려야 하지 않나" 라는 질문이 반품
// 처리를 구현할 때 반드시 나오기 때문이다. 답이 코드에 없으면 다음 사람이
// 늘리는 쪽을 고른다.
func PickupRestock() []StockDelta { return nil }
