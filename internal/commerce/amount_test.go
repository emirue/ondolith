package commerce

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestLineAmount(t *testing.T) {
	cases := []struct {
		why     string
		line    Line
		want    int
		wantErr error
	}{
		{"기본가 + 차액 × 수량", Line{12000, 1000, 2}, 26000, nil},
		{"차액은 음수일 수 있다 — 낮은 등급 옵션이 더 싼 것은 정상이다",
			Line{12000, -2000, 1}, 10000, nil},
		{"차액이 기본가를 넘어 단가가 음수가 되면 조합이 쿠폰이 된다",
			Line{12000, -13000, 1}, 0, ErrPriceNegative},
		{"기본가가 음수", Line{-1, 0, 1}, 0, ErrPriceNegative},
		{"0원 상품은 있을 수 있다 (사은품)", Line{0, 0, 3}, 0, nil},
		{"수량 0", Line{1000, 0, 0}, 0, ErrQuantityRange},
		{"수량 음수", Line{1000, 0, -1}, 0, ErrQuantityRange},
		{"수량 상한", Line{1000, 0, 999}, 999000, nil},
		{"수량 상한 초과 — 폼 검증은 브라우저가 건너뛸 수 있다",
			Line{1000, 0, 1000}, 0, ErrQuantityRange},
		{"품목 하나가 상한을 넘는다", Line{maxAmount, 0, 2}, 0, ErrAmountTooBig},
	}
	for _, c := range cases {
		got, err := c.line.Amount()
		if !errors.Is(err, c.wantErr) {
			t.Errorf("%+v: err %v, want %v — %s", c.line, err, c.wantErr, c.why)
			continue
		}
		if err == nil && got != c.want {
			t.Errorf("%+v = %d, want %d — %s", c.line, got, c.want, c.why)
		}
	}
}

// 경계는 "미만이면 부과" 다. `>` 로 쓰면 5만원 기준에서 5만원짜리 주문이
// 배송비를 무는데, 화면은 "5만원 이상 무료배송" 이라고 적어 둔다.
func TestShippingFeeBoundary(t *testing.T) {
	ship := Shipping{FlatFee: 3000, FreeThreshold: 50000}
	cases := []struct {
		goods, want int
	}{
		{0, 3000},
		{49999, 3000},
		{50000, 0}, // 기준액과 같으면 무료
		{50001, 0},
	}
	for _, c := range cases {
		got, err := ship.Fee(c.goods)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("상품합계 %d → 배송비 %d, want %d", c.goods, got, c.want)
		}
	}

	// 기준액 0 은 "무료배송 없음" 이다. 0 을 "0원 이상 전부 무료" 로 읽으면
	// 설정을 비워 둔 사이트가 배송비를 한 푼도 못 받는다.
	always := Shipping{FlatFee: 3000, FreeThreshold: 0}
	for _, goods := range []int{0, 1, 1_000_000} {
		if got, _ := always.Fee(goods); got != 3000 {
			t.Errorf("기준액 0, 상품합계 %d → %d, want 3000", goods, got)
		}
	}
	if _, err := (Shipping{FlatFee: -1}).Fee(0); !errors.Is(err, ErrPriceNegative) {
		t.Errorf("음수 배송비 = %v", err)
	}
}

func TestTotal(t *testing.T) {
	ship := Shipping{FlatFee: 3000, FreeThreshold: 50000}

	goods, fee, total, err := Total([]Line{{12000, 1000, 2}, {5000, 0, 1}}, ship)
	if err != nil {
		t.Fatal(err)
	}
	if goods != 31000 || fee != 3000 || total != 34000 {
		t.Errorf("= (%d, %d, %d), want (31000, 3000, 34000)", goods, fee, total)
	}
	// 배송비는 상품합계로 판정한다. 총액으로 판정하면 배송비가 자기 자신을
	// 무료로 만드는 순환이 된다.
	goods, fee, total, err = Total([]Line{{47000, 0, 1}, {3000, 0, 1}}, ship)
	if err != nil {
		t.Fatal(err)
	}
	if goods != 50000 || fee != 0 || total != 50000 {
		t.Errorf("= (%d, %d, %d), want (50000, 0, 50000)", goods, fee, total)
	}

	if _, _, _, err := Total(nil, ship); err == nil {
		t.Error("빈 주문이 통과했다")
	}
	// 품목 하나가 잘못되면 주문 전체가 실패한다 — 조용히 빼면 총액이 화면과
	// 달라지고, 그 차이는 FR-607 검증에서야 드러난다.
	if _, _, _, err := Total([]Line{{12000, 0, 1}, {1000, 0, 0}}, ship); !errors.Is(err, ErrQuantityRange) {
		t.Errorf("잘못된 품목이 섞인 주문 = %v", err)
	}
	if _, _, _, err := Total([]Line{{maxAmount, 0, 1}, {maxAmount, 0, 1}}, ship); !errors.Is(err, ErrAmountTooBig) {
		t.Errorf("합계 상한 = %v", err)
	}

	// 배송비는 **상품합계**로 판정한다. 총액으로 판정하면 배송비가 자기 자신을
	// 무료로 만든다 — 48000원 주문이 51000원이 되어 5만원 기준을 넘어 버린다.
	goods, fee, total, err = Total([]Line{{48000, 0, 1}}, ship)
	if err != nil {
		t.Fatal(err)
	}
	if goods != 48000 || fee != 3000 || total != 51000 {
		t.Errorf("= (%d, %d, %d), want (48000, 3000, 51000) — 배송비가 자기를 무료로 만들었다",
			goods, fee, total)
	}
}

// D81 W3-08: 부동소수점 타입이 계산 경로에 없다.
//
// 주석으로 적어 두는 것과 검사하는 것은 다르다 — 나중에 "할인율 0.1" 하나가
// float 를 들여오고, 그때 반올림이 어디서 일어나는지는 아무도 모른다.
func TestNoFloatingPointInTheMoneyPath(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok {
					return true
				}
				switch id.Name {
				case "float32", "float64", "complex64", "complex128":
					found = append(found, name+":"+fset.Position(id.Pos()).String()+" "+id.Name)
				}
				return true
			})
			// 리터럴도 본다: `x * 0.9` 는 타입 이름 없이 float 를 들여온다.
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if ok && lit.Kind == token.FLOAT {
					found = append(found, name+":"+fset.Position(lit.Pos()).String()+" "+lit.Value)
				}
				return true
			})
		}
	}
	if len(found) > 0 {
		t.Errorf("계산 경로에 부동소수가 있다: %v", found)
	}
	// 검사가 헛돌지 않는지: 이 패키지의 파일을 실제로 읽었는가.
	if len(pkgs) == 0 {
		t.Fatal("패키지를 하나도 읽지 못했다 — 검사가 헛돌았다")
	}
}

// 주문 뒤 화면(주문 상세·영수증)은 배송비를 저장된 값에서 거꾸로 푼다 — orders
// 에 배송비 열이 없다. 주문을 만들 때의 식(총액 = 상품합계 + 배송비 − 할인)과
// 어긋나면 화면의 배송비가 결제한 배송비와 달라진다.
func TestOrderDetailRecoversShippingFee(t *testing.T) {
	ship := Shipping{FlatFee: 3000, FreeThreshold: 50000}
	for _, tc := range []struct {
		lines    []Line
		discount int
	}{
		{[]Line{{12000, 1000, 2}, {5000, 0, 1}}, 0},    // 유료 배송
		{[]Line{{12000, 1000, 2}, {5000, 0, 1}}, 4000}, // 유료 배송 + 할인
		{[]Line{{47000, 0, 1}, {3000, 0, 1}}, 0},       // 무료 배송
	} {
		goods, fee, total, err := Total(tc.lines, ship)
		if err != nil {
			t.Fatal(err)
		}
		o := OrderDetail{Total: total - tc.discount, Discount: tc.discount}
		for _, l := range tc.lines {
			amount, _ := l.Amount()
			o.Items = append(o.Items, OrderItem{LineAmount: amount})
		}
		if o.Goods() != goods || o.ShippingFee() != fee {
			t.Errorf("할인 %d: 상품합계 %d·배송비 %d, want %d·%d",
				tc.discount, o.Goods(), o.ShippingFee(), goods, fee)
		}
	}
}

// 상품 화면의 옵션은 관리자가 넣은 순서대로다 — OptionGroups 가 사전순으로 정렬해
// 「5mm, 10mm」가 「10mm, 5mm」로, S·M·L 이 L·M·S 로 나왔다. 정의에 없는 값(옛 조합)은
// 뒤에 남는다.
func TestOptionGroupsFollowTheDefinedOrder(t *testing.T) {
	vs := []Variant{
		{OptionValues: map[string]string{"두께": "10mm", "크기": "L"}},
		{OptionValues: map[string]string{"두께": "5mm", "크기": "S"}},
		{OptionValues: map[string]string{"두께": "5mm", "크기": "M"}},
		{OptionValues: map[string]string{"두께": "20mm", "크기": "M"}}, // 정의에서 빠진 값
	}
	defined := []Option{{Name: "크기", Values: []string{"S", "M", "L"}}, {Name: "두께", Values: []string{"5mm", "10mm"}}}
	got := InDefinedOrder(OptionGroups(vs), defined)
	if len(got) != 2 || got[0].Name != "크기" || got[1].Name != "두께" {
		t.Fatalf("그룹 순서 = %+v, want 크기, 두께", got)
	}
	if strings.Join(got[0].Values, ",") != "S,M,L" || strings.Join(got[1].Values, ",") != "5mm,10mm,20mm" {
		t.Errorf("값 순서 = %v / %v, want S,M,L / 5mm,10mm,20mm", got[0].Values, got[1].Values)
	}
}
