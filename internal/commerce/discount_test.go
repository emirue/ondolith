package commerce

import (
	"errors"
	"testing"
)

// 배분 합계는 **언제나** 할인액과 같다. 이것 하나가 이 함수의 존재 이유다 —
// 내림만 하면 합이 모자라고, 그 차이가 어디로 갔는지 아무도 모른다.
func TestApportionSumsExactly(t *testing.T) {
	cases := []struct {
		why      string
		lines    []int
		discount int
		want     []int
	}{
		{"나누어떨어짐", []int{1000, 1000}, 200, []int{100, 100}},
		{"나머지 1원은 큰 쪽으로", []int{2000, 1000}, 100, []int{67, 33}},
		// 앞 품목이 아니라 **나머지가 큰** 품목이 가져간다. 순서만 보면
		// 34/66 이 되고, 그것도 합은 맞지만 배분이 비례에서 멀어진다.
		{"나머지가 큰 쪽이 뒤에 있음", []int{1000, 2000}, 100, []int{33, 67}},
		{"동점이면 앞 품목", []int{1000, 1000}, 1, []int{1, 0}},
		{"할인 0", []int{1000, 500}, 0, []int{0, 0}},
		{"전액 할인", []int{1000, 500}, 1500, []int{1000, 500}},
		{"0원 품목이 섞임", []int{0, 1000}, 100, []int{0, 100}},
		{"품목 하나", []int{999}, 1, []int{1}},
	}
	for _, c := range cases {
		got, err := Apportion(c.lines, c.discount)
		if err != nil {
			t.Errorf("%s: %v", c.why, err)
			continue
		}
		sum := 0
		for i, v := range got {
			sum += v
			if v > c.lines[i] {
				t.Errorf("%s: 품목 %d 배분 %d > 금액 %d", c.why, i, v, c.lines[i])
			}
		}
		if sum != c.discount {
			t.Errorf("%s: 합계 %d, want %d (%v)", c.why, sum, c.discount, got)
		}
		if c.want != nil {
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Errorf("%s: %v, want %v", c.why, got, c.want)
					break
				}
			}
		}
	}
}

// 무작위 조합에서도 합계가 맞는다. 표 테스트는 내가 생각한 경우만 본다.
func TestApportionSumIsExactForManyShapes(t *testing.T) {
	// 결정적 의사난수 — 실패를 재현할 수 있어야 한다.
	seed := 12345
	next := func(n int) int {
		seed = (seed*1103515245 + 12345) & 0x7fffffff
		return seed % n
	}
	for round := 0; round < 2000; round++ {
		n := 1 + next(6)
		lines := make([]int, n)
		total := 0
		for i := range lines {
			lines[i] = next(100000)
			total += lines[i]
		}
		discount := 0
		if total > 0 {
			discount = next(total + 1)
		}
		got, err := Apportion(lines, discount)
		if err != nil {
			t.Fatalf("라운드 %d: lines=%v discount=%d: %v", round, lines, discount, err)
		}
		sum := 0
		for i, v := range got {
			sum += v
			if v < 0 || v > lines[i] {
				t.Fatalf("라운드 %d: 품목 %d 배분 %d, 금액 %d", round, i, v, lines[i])
			}
		}
		if sum != discount {
			t.Fatalf("라운드 %d: 합계 %d, want %d (lines=%v got=%v)", round, sum, discount, lines, got)
		}
	}
}

func TestApportionRefusesImpossibleInput(t *testing.T) {
	if _, err := Apportion([]int{100}, 101); !errors.Is(err, ErrDiscountTooLarge) {
		t.Errorf("합계 초과 = %v", err)
	}
	if _, err := Apportion([]int{100}, -1); !errors.Is(err, ErrPriceNegative) {
		t.Errorf("음수 할인 = %v", err)
	}
	if _, err := Apportion([]int{-1}, 0); !errors.Is(err, ErrPriceNegative) {
		t.Errorf("음수 품목 = %v", err)
	}
}

// 같은 입력은 같은 배분이다. 달라지면 "스냅샷" 이라는 말이 무의미해진다.
func TestApportionIsDeterministic(t *testing.T) {
	lines := []int{1000, 1000, 1000}
	first, err := Apportion(lines, 100)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		got, err := Apportion(lines, 100)
		if err != nil {
			t.Fatal(err)
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("배분이 흔들린다: %v vs %v", first, got)
			}
		}
	}
}

// **부분 환불을 다 합치면 할인후 금액과 정확히 같다.** 이것이 돈 버그가 사는
// 자리다 — 한 개씩 나눠 환불했더니 마지막에 1원이 남거나 모자라는 것.
func TestPartialRefundsSumToTheDiscountedLine(t *testing.T) {
	cases := []struct{ line, discount, qty int }{
		{26000, 0, 2},
		{26000, 1000, 2},
		{26000, 1, 3},
		{25000, 3333, 3},
		{1, 0, 1},
		{100000, 99999, 7},
		{13, 7, 5},
	}
	for _, c := range cases {
		net := c.line - c.discount
		sum := 0
		for settled := 0; settled < c.qty; settled++ {
			amt, err := RefundableAmount(c.line, c.discount, c.qty, settled, 1)
			if err != nil {
				t.Fatalf("%+v settled=%d: %v", c, settled, err)
			}
			if amt < 0 {
				t.Errorf("%+v settled=%d: 음수 환불 %d", c, settled, amt)
			}
			sum += amt
		}
		if sum != net {
			t.Errorf("%+v: 한 개씩 합계 %d, want %d", c, sum, net)
		}
		// 한꺼번에 환불해도 같다.
		whole, err := RefundableAmount(c.line, c.discount, c.qty, 0, c.qty)
		if err != nil {
			t.Fatal(err)
		}
		if whole != net {
			t.Errorf("%+v: 전량 환불 %d, want %d", c, whole, net)
		}
	}
}

// 부분 환불을 임의 순서·묶음으로 나눠도 합계가 같다.
func TestPartialRefundsSumRegardlessOfChunking(t *testing.T) {
	const line, discount, qty = 100000, 33333, 6
	net := line - discount

	for _, chunks := range [][]int{
		{1, 1, 1, 1, 1, 1}, {2, 2, 2}, {3, 3}, {1, 5}, {5, 1}, {4, 2}, {6},
	} {
		settled, sum := 0, 0
		for _, q := range chunks {
			amt, err := RefundableAmount(line, discount, qty, settled, q)
			if err != nil {
				t.Fatalf("%v: %v", chunks, err)
			}
			sum += amt
			settled += q
		}
		if sum != net {
			t.Errorf("%v: 합계 %d, want %d", chunks, sum, net)
		}
	}
}

func TestRefundableAmountRefusesOverRefund(t *testing.T) {
	if _, err := RefundableAmount(26000, 0, 2, 2, 1); !errors.Is(err, ErrRefundQuantity) {
		t.Errorf("전량 소진 뒤 = %v, want ErrRefundQuantity", err)
	}
	if _, err := RefundableAmount(26000, 0, 2, 0, 3); !errors.Is(err, ErrRefundQuantity) {
		t.Errorf("주문 수량 초과 = %v", err)
	}
	if _, err := RefundableAmount(26000, 0, 2, 0, 0); !errors.Is(err, ErrQuantityRange) {
		t.Errorf("0개 환불 = %v", err)
	}
	if _, err := RefundableAmount(1000, 2000, 1, 0, 1); !errors.Is(err, ErrPriceNegative) {
		t.Errorf("할인이 금액 초과 = %v", err)
	}
}

// 금액 상한 근처에서도 곱이 넘치지 않는다.
//
// Apportion 은 `품목금액 × 할인액` 을 계산하는데, 둘 다 상한(100억)이면 10^20
// 이라 int64 를 넘는다 — 나눗셈을 먼저 하지 않으면 배분이 음수가 되거나
// 합계가 어긋난다.
func TestApportionDoesNotOverflowAtTheAmountCeiling(t *testing.T) {
	lines := []int{maxAmount, maxAmount, 1}
	discount := maxAmount
	got, err := Apportion(lines, discount)
	if err != nil {
		t.Fatal(err)
	}
	sum := 0
	for i, v := range got {
		if v < 0 {
			t.Fatalf("음수 배분: %v", got)
		}
		if v > lines[i] {
			t.Fatalf("품목 %d 배분 %d > 금액 %d", i, v, lines[i])
		}
		sum += v
	}
	if sum != discount {
		t.Errorf("합계 %d, want %d (%v)", sum, discount, got)
	}
}
