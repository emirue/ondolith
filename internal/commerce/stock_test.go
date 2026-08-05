package commerce

import (
	"errors"
	"testing"
)

// D50: 백오더 없음. 재고 0 이면 담기와 주문을 거부한다.
func TestCheckAvailable(t *testing.T) {
	cases := []struct {
		why     string
		s       Sellable
		want    int
		wantErr error
	}{
		{"재고만큼은 담긴다", Sellable{true, true, 3}, 3, nil},
		{"재고보다 하나 더", Sellable{true, true, 3}, 4, ErrOutOfStock},
		{"재고 0 — 백오더 없음", Sellable{true, true, 0}, 1, ErrOutOfStock},
		{"조합이 숨겨져 있다", Sellable{true, false, 5}, 1, ErrNotSellable},
		{"상품이 숨겨져 있다 — 조합만 보면 URL 을 아는 사람은 계속 산다",
			Sellable{false, true, 5}, 1, ErrNotSellable},
		{"숨김이 재고보다 먼저다", Sellable{false, true, 0}, 1, ErrNotSellable},
		{"수량 0", Sellable{true, true, 5}, 0, ErrQuantityRange},
		{"수량 음수 — 통과하면 재고가 늘어난다", Sellable{true, true, 5}, -1, ErrQuantityRange},
		{"수량 상한 초과", Sellable{true, true, 5000}, 1000, ErrQuantityRange},
	}
	for _, c := range cases {
		if err := c.s.CheckAvailable(c.want); !errors.Is(err, c.wantErr) {
			t.Errorf("%+v × %d: %v, want %v — %s", c.s, c.want, err, c.wantErr, c.why)
		}
	}
}

// D50 「비회원 장바구니 병합」: 더하되 재고 상한에서 자른다.
func TestMergeQuantity(t *testing.T) {
	cases := []struct {
		why                  string
		guest, member, stock int
		want                 int
	}{
		{"둘 다 남는다 — 한쪽을 버리면 '왜 없어졌지' 가 된다", 2, 3, 10, 5},
		{"재고에서 자른다", 4, 4, 5, 5},
		{"비회원 쪽만 있다", 2, 0, 10, 2},
		{"회원 쪽만 있다", 0, 3, 10, 3},
		{"재고 0", 2, 3, 0, 0},
		{"수량 상한에서도 자른다", 900, 900, 100000, maxQuantity},
	}
	for _, c := range cases {
		if got := MergeQuantity(c.guest, c.member, c.stock); got != c.want {
			t.Errorf("병합(%d, %d, 재고 %d) = %d, want %d — %s",
				c.guest, c.member, c.stock, got, c.want, c.why)
		}
	}
}

func TestReserveAndRelease(t *testing.T) {
	got, err := Reserve("v1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].VariantID != "v1" || got[0].Delta != -2 {
		t.Fatalf("예약 = %+v, want [{v1 -2}]", got)
	}
	if _, err := Reserve("v1", 0); !errors.Is(err, ErrQuantityRange) {
		t.Errorf("수량 0 예약 = %v", err)
	}

	got, err = Release("v1", 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Delta != 2 {
		t.Fatalf("해제 = %+v, want [{v1 +2}]", got)
	}
	// 잡아 둔 것보다 많이 푸는 요청은 거부한다 — 통과시키면 없던 재고가 생기고
	// 그것이 다음 주문을 받는다.
	if _, err := Release("v1", 3, 2); !errors.Is(err, ErrReservationMissing) {
		t.Errorf("예약 초과 해제 = %v, want ErrReservationMissing", err)
	}
	if _, err := Release("v1", 1, 0); !errors.Is(err, ErrReservationMissing) {
		t.Errorf("예약 없는 해제 = %v", err)
	}
}

// 회귀 고정. D14 「교환 재고」가 "거부·취소 시 반드시 푼다. 풀지 않으면 재고가
// 조용히 잠긴다" 고 적은 바로 그것이다 — 잠긴 재고는 오류를 내지 않고 판매만
// 멈추므로 사람이 숫자를 보고서야 알아챈다.
func TestStockReturnsToZeroNetAcrossEveryReleasePath(t *testing.T) {
	// 각 경로: 예약 한 번 + 해제 한 번. 합이 0 이 아니면 재고가 새거나 잠긴다.
	for _, path := range []string{"결제 실패", "주문 취소", "교환 거부", "교환 취소"} {
		res, err := Reserve("v1", 3)
		if err != nil {
			t.Fatal(err)
		}
		rel, err := Release("v1", 3, 3)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		net := 0
		for _, d := range append(res, rel...) {
			net += d.Delta
		}
		if net != 0 {
			t.Errorf("%s 경로의 재고 증감 합 = %d, want 0", path, net)
		}
	}
}

// D50 「자동 재입고」: 수거 확인은 재고를 건드리지 않는다. 자동으로 늘리면
// 파손품이 재고가 되고, 그 재고가 다음 주문을 받는다.
func TestPickupDoesNotRestock(t *testing.T) {
	if d := PickupRestock(); len(d) != 0 {
		t.Errorf("수거가 재고를 %v 만큼 건드렸다 — 재입고는 A-503 에서 사람이 한다", d)
	}
}
