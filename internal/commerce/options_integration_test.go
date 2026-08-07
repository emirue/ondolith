package commerce

import (
	"context"
	"errors"
	"testing"
)

// **조합은 옵션 값의 곱으로 서버가 만든다** (D19 A-503).
//
// SetOptions 가 없을 때 새 상품은 조합이 0개인 채로 남았다. 조합이 없으면
// 장바구니에 담을 것이 없으므로 **아무것도 팔 수 없었다** — 스토어에는
// AddVariant 가 있었지만 어떤 화면도 그것을 부르지 않았다.
func TestSetOptionsCreatesEveryCombination(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	pid := mkProduct(t, s, "mat", 10000)

	if err := s.SetOptions(ctx, pid, []Option{
		{Name: "색상", Values: []string{"빨강", "파랑"}},
		{Name: "크기", Values: []string{"S", "M", "L"}},
	}); err != nil {
		t.Fatal(err)
	}

	vs, err := s.Variants(ctx, pid, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 6 {
		t.Fatalf("조합 %d개 — 2×3 = 6 이어야 한다", len(vs))
	}
	seen := map[string]bool{}
	for _, v := range vs {
		seen[v.OptionValues["색상"]+"/"+v.OptionValues["크기"]] = true
	}
	for _, want := range []string{"빨강/S", "빨강/M", "빨강/L", "파랑/S", "파랑/M", "파랑/L"} {
		if !seen[want] {
			t.Errorf("조합 %q 가 없다", want)
		}
	}
}

// **이미 있는 조합은 건드리지 않는다.** 재고와 SKU 가 거기 붙어 있으므로,
// 옵션을 다시 저장할 때마다 지웠다 만들면 팔린 이력과 재고가 사라진다.
func TestSetOptionsKeepsStockOfExistingCombinations(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	pid := mkProduct(t, s, "mat", 10000)

	if err := s.SetOptions(ctx, pid, []Option{
		{Name: "색상", Values: []string{"빨강"}},
	}); err != nil {
		t.Fatal(err)
	}
	vs, err := s.Variants(ctx, pid, false)
	if err != nil || len(vs) != 1 {
		t.Fatalf("준비 실패: %v (조합 %d)", err, len(vs))
	}
	if _, err := pool.Exec(ctx,
		`UPDATE product_variants SET stock = 7, sku = 'RED-1' WHERE id = $1`, vs[0].ID); err != nil {
		t.Fatal(err)
	}

	// 값을 하나 더해 다시 저장한다.
	if err := s.SetOptions(ctx, pid, []Option{
		{Name: "색상", Values: []string{"빨강", "파랑"}},
	}); err != nil {
		t.Fatal(err)
	}

	var stock int
	var sku *string
	if err := pool.QueryRow(ctx,
		`SELECT stock, sku FROM product_variants WHERE id = $1`, vs[0].ID).Scan(&stock, &sku); err != nil {
		t.Fatalf("기존 조합이 사라졌다: %v", err)
	}
	if stock != 7 {
		t.Errorf("재고 = %d, want 7 — 다시 저장하면서 지워졌다", stock)
	}
	if sku == nil || *sku != "RED-1" {
		t.Errorf("SKU = %v, want RED-1", sku)
	}
	after, err := s.Variants(ctx, pid, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 {
		t.Errorf("조합 %d개 — 새 값 하나가 더해져 2개여야 한다", len(after))
	}
}

// 빈 이름·빈 값·그룹 안의 중복은 저장하지 않는다 (D19 A-503 검증).
func TestSetOptionsRefusesEmptyAndDuplicateValues(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	pid := mkProduct(t, s, "mat", 10000)

	for _, tc := range []struct {
		name string
		opts []Option
	}{
		{"그룹 이름이 공백", []Option{{Name: "  ", Values: []string{"빨강"}}}},
		{"값이 전부 공백", []Option{{Name: "색상", Values: []string{" ", ""}}}},
		{"그룹 안 중복", []Option{{Name: "색상", Values: []string{"빨강", "빨강"}}}},
		{"그룹이 없음", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.SetOptions(ctx, pid, tc.opts); !errors.Is(err, ErrOptionDuplicate) {
				t.Errorf("거부되지 않았다: %v", err)
			}
			vs, err := s.Variants(ctx, pid, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(vs) != 0 {
				t.Errorf("거부됐는데 조합 %d개가 생겼다", len(vs))
			}
		})
	}
}

// mkProduct 는 조합을 붙일 상품 한 건을 만든다.
func mkProduct(t *testing.T, s *Store, slug string, price int) string {
	t.Helper()
	id, err := s.CreateProduct(context.Background(), Product{
		Slug: slug, Name: "온돌 매트", BasePrice: price, Visible: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
