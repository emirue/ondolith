package commerce

import (
	"context"
	"errors"
	"strconv"
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

// **옵션이 없는 상품도 팔 수 있다.** 그룹 없이 저장하면 옵션 없는 조합 하나를
// 만든다 — 텀블러 같은 단일 상품에 가짜 옵션을 만들지 않아도 된다. 이미 조합이
// 있는 상품을 빈 옵션으로 덮지는 않는다: 실수 한 번에 옵션 그룹이 사라진다.
func TestSetOptionsWithoutGroupsMakesOneVariant(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	single := mkProduct(t, s, "tumbler", 15000)
	for range 2 { // 두 번 저장해도 하나다
		if err := s.SetOptions(ctx, single, nil); err != nil {
			t.Fatal(err)
		}
	}
	vs, err := s.Variants(ctx, single, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || len(vs[0].OptionValues) != 0 {
		t.Fatalf("조합 %v — 옵션 없는 조합 하나여야 한다", vs)
	}

	optioned := mkProduct(t, s, "mat", 10000)
	if err := s.SetOptions(ctx, optioned, []Option{{Name: "색상", Values: []string{"빨강"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOptions(ctx, optioned, nil); !errors.Is(err, ErrOptionsRequired) {
		t.Errorf("조합이 있는 상품을 빈 옵션으로 저장 = %v, want ErrOptionsRequired", err)
	}
	if opts, _ := s.Options(ctx, optioned); len(opts) != 1 {
		t.Errorf("거부됐는데 옵션 그룹이 %d개다", len(opts))
	}
}

// **옵션에서 뺀 값의 조합은 판매를 멈춘다.** 행은 남긴다(주문·재고가 가리킨다).
// 전에는 「대」를 빼도 그 조합이 그대로 매장에 나와, 정의와 화면이 어긋났다.
func TestRemovedOptionValueStopsItsVariants(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	pid := mkProduct(t, s, "cover", 10000)
	if err := s.SetOptions(ctx, pid, []Option{{Name: "크기", Values: []string{"소", "대"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOptions(ctx, pid, []Option{{Name: "크기", Values: []string{"소"}}}); err != nil {
		t.Fatal(err)
	}
	vs, err := s.Variants(ctx, pid, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 {
		t.Fatalf("조합 %d개 — 뺀 값의 조합도 행은 남아야 한다", len(vs))
	}
	for _, v := range vs {
		if want := v.OptionValues["크기"] == "소"; v.Visible != want {
			t.Errorf("「%s」 판매 %v, want %v", v.OptionValues["크기"], v.Visible, want)
		}
	}
}

// 조합마다 판매를 멈추고 다시 연다 (A-503). 단종된 색 하나를 내릴 방법이 재고 0
// 밖에 없었다.
func TestEditVariantsStopsAndResumesSelling(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	productID, variantID := seedProduct(t, pool, "tee-stop", 12000, 0, 5)
	visible := func() bool {
		var v bool
		if err := pool.QueryRow(ctx, `SELECT is_visible FROM product_variants WHERE id = $1`, variantID).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if err := s.EditVariants(ctx, productID, []VariantEdit{{ID: variantID, Hidden: true, Version: -1}}); err != nil {
		t.Fatal(err)
	}
	if visible() {
		t.Error("판매 중지가 반영되지 않았다")
	}
	if err := s.EditVariants(ctx, productID, []VariantEdit{{ID: variantID, Version: -1}}); err != nil {
		t.Fatal(err)
	}
	if !visible() {
		t.Error("판매 재개가 반영되지 않았다")
	}
}

// **조합이 상한(500)을 넘는 옵션은 저장하지 않는다.** 전에는 곱을 500에서 잘라
// 만들었고, 그 잘린 목록 밖의 조합을 판매 중지로 돌리면서 팔던 조합과 재고가 조용히
// 내려갔다(중간 그룹에서 잘리면 키가 모자란 조합까지 생겼다).
func TestSetOptionsRefusesTooManyCombinations(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	pid := mkProduct(t, s, "many", 10000)
	values := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = strconv.Itoa(i)
		}
		return out
	}
	if err := s.SetOptions(ctx, pid, []Option{{Name: "가", Values: values(25)}, {Name: "나", Values: values(20)}}); err != nil {
		t.Fatal(err) // 정확히 500 은 된다
	}
	err := s.SetOptions(ctx, pid, []Option{{Name: "가", Values: values(25)}, {Name: "나", Values: values(21)}})
	if !errors.Is(err, ErrTooManyVariants) {
		t.Fatalf("525 조합 = %v, want ErrTooManyVariants", err)
	}
	vs, err := s.Variants(ctx, pid, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vs {
		if !v.Visible {
			t.Fatalf("거절된 저장이 조합 %v 를 내렸다", v.OptionValues)
		}
	}
	if len(vs) != 500 {
		t.Errorf("조합 %d개, want 500", len(vs))
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

// 목록은 「조합이 있는가」를 함께 낸다 — 조합 없는 상품(판매 준비 중)과 재고 없는 상품
// (품절)을 목록이 가를 수 있어야 상품 화면과 같은 말을 쓴다.
func TestListTellsUnreadyFromSoldOut(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	mkProduct(t, s, "ready", 10000)
	sold := mkProduct(t, s, "sold", 10000)
	if err := s.SetOptions(ctx, sold, nil); err != nil { // 재고 0 인 조합 하나
		t.Fatal(err)
	}
	list, err := s.ListProducts(ctx, ProductQuery{Page: 1})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][2]bool{}
	for _, p := range list {
		got[p.Slug] = [2]bool{p.HasVariants, p.InStock}
	}
	if got["ready"] != [2]bool{false, false} || got["sold"] != [2]bool{true, false} {
		t.Errorf("(조합 있음, 재고 있음) = %v, want ready=(false,false) sold=(true,false)", got)
	}
}
