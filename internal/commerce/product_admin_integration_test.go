package commerce

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// **재고는 조정값으로만 움직인다** (D13, D19 A-503).
//
// 동시 두 건이 각자 +5 하면 합계가 +10 이어야 한다. 절대값 덮어쓰기라면
// 하나가 다른 하나를 지운다.
func TestConcurrentStockEditsAccumulate(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	productID, variantID := seedProduct(t, pool, "tee-stock", 12000, 0, 10)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// version -1 은 낙관적 잠금을 쓰지 않는다는 뜻이다. 두 건이
			// 동시에 오는 것이 정상인 경로를 검사하려면 버전 검사를 꺼야 한다.
			errs[i] = s.EditVariants(ctx, productID, []VariantEdit{
				{ID: variantID, StockDelta: 5, PriceDelta: 0, Version: -1}})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("%d번 조정: %v", i, err)
		}
	}
	assertStock(t, pool, variantID, 20)
}

// 조정 결과가 음수면 DB CHECK 가 막는다. 백오더는 없다 (D50).
func TestStockCannotGoNegative(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	productID, variantID := seedProduct(t, pool, "tee-neg", 12000, 0, 3)

	if err := s.EditVariants(ctx, productID, []VariantEdit{
		{ID: variantID, StockDelta: -4, Version: 3}}); !errors.Is(err, ErrOutOfStock) {
		t.Fatalf("= %v, want ErrOutOfStock", err)
	}
	assertStock(t, pool, variantID, 3)

	// 정확히 0 까지는 된다 — 위 단언이 "음수 조정은 전부 막힌다" 가 아니다.
	if err := s.EditVariants(ctx, productID, []VariantEdit{
		{ID: variantID, StockDelta: -3, Version: 3}}); err != nil {
		t.Fatalf("0 까지 내리는 조정이 막혔다: %v", err)
	}
	assertStock(t, pool, variantID, 0)
}

// **낙관적 잠금.** 화면이 읽은 재고와 지금이 다르면 409 다 — 조정값이라도
// 화면이 보여준 결과와 달라지기 때문이다.
func TestStockVersionMismatchIsRefused(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	productID, variantID := seedProduct(t, pool, "tee-ver", 12000, 0, 10)

	// 화면이 10 을 읽은 사이 누가 +2 했다.
	if err := s.EditVariants(ctx, productID, []VariantEdit{
		{ID: variantID, StockDelta: 2, Version: 10}}); err != nil {
		t.Fatal(err)
	}
	if err := s.EditVariants(ctx, productID, []VariantEdit{
		{ID: variantID, StockDelta: 5, Version: 10}}); !errors.Is(err, ErrStockVersion) {
		t.Fatalf("= %v, want ErrStockVersion", err)
	}
	assertStock(t, pool, variantID, 12)
}

// 다른 상품의 조합 ID 는 404 다. product_id 술어가 막는다.
func TestEditVariantsIsScopedToTheProduct(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	mine, _ := seedProduct(t, pool, "tee-mine", 12000, 0, 5)
	_, otherVariant := seedProduct(t, pool, "tee-other", 12000, 0, 5)

	if err := s.EditVariants(ctx, mine, []VariantEdit{
		{ID: otherVariant, StockDelta: 100, Version: -1}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("= %v, want ErrNotFound", err)
	}
	assertStock(t, pool, otherVariant, 5)
}

// 두 조합이 같은 SKU 를 가지면 재고가 두 벌이 되어 그 자체가 모순이다 (D30).
func TestDuplicateSkuIsRefused(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	productID, v1 := seedProduct(t, pool, "tee-sku", 12000, 0, 5)
	v2, err := s.AddVariant(ctx, productID, map[string]string{"크기": "M"}, 0, "")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.EditVariants(ctx, productID, []VariantEdit{
		{ID: v1, SKU: "SKU-1", Version: -1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.EditVariants(ctx, productID, []VariantEdit{
		{ID: v2, SKU: "SKU-1", Version: -1}}); !errors.Is(err, ErrSkuTaken) {
		t.Fatalf("= %v, want ErrSkuTaken", err)
	}
}

// **주문된 상품은 지울 수 없다** (409). FK 가 막는다 — 애플리케이션이 먼저
// 세어 보지 않는다.
func TestOrderedProductCannotBeDeleted(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, _, _ := paidOrder(t, s, pool, "tee-del", 1)
	var productID string
	if err := pool.QueryRow(ctx, `
		SELECT oi.product_id FROM order_items oi JOIN orders o ON o.id = oi.order_id
		WHERE o.order_no = $1`, orderNo).Scan(&productID); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteProduct(ctx, productID); !errors.Is(err, ErrProductInUse) {
		t.Fatalf("= %v, want ErrProductInUse", err)
	}
	// 주문된 적 없는 상품은 지워진다 — 위 단언이 "삭제가 늘 막힌다" 가 아니다.
	fresh, _ := seedProduct(t, pool, "tee-fresh", 12000, 0, 5)
	if err := s.DeleteProduct(ctx, fresh); err != nil {
		t.Errorf("주문 없는 상품 삭제가 막혔다: %v", err)
	}
}

// **읽고-더하고-쓰는 경로가 코드에 없다** (D81 W3-38 과 같은 기준).
//
// 단일 문장이 아니면 FOR UPDATE 를 빼는 순간 판매분이 사라지는데, 그때
// 테스트는 여전히 통과한다 — 그래서 소스를 직접 본다.
func TestStockIsUpdatedByASingleStatement(t *testing.T) {
	src, err := os.ReadFile("product_admin.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "SET stock = stock + $3") {
		t.Error("재고 조정이 `stock = stock + $n` 단일 문장이 아니다")
	}
	// Go 쪽에서 읽은 값을 다시 쓰는 형태가 없어야 한다.
	for _, bad := range []string{"stock + e.StockDelta", "stock+e.StockDelta", "SET stock = $"} {
		if strings.Contains(string(src), bad) {
			t.Errorf("읽고-더하고-쓰는 경로가 있다: %q", bad)
		}
	}
	// 절대값 설정 SQL 이 커머스 어디에도 없어야 한다.
	out, err := exec.Command("grep", "-rn", "SET stock = $", ".").CombinedOutput()
	if err == nil && len(out) > 0 {
		t.Errorf("재고 절대값 설정이 있다:\n%s", out)
	}
}

// **낙관적 잠금 검사가 동시 두 건에서도 성립한다.**
//
// 버전을 읽고 나서 조정하므로, 그 사이를 잠그지 않으면 두 건이 같은 버전을
// 읽고 **둘 다 통과한다** — 그때 화면은 "다른 사람이 먼저 바꿨습니다" 를 한
// 번도 보여주지 않고, 두 조정이 조용히 겹친다.
func TestConcurrentVersionedEditsSerializeSoOneLoses(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()

	// 한 번으로는 우연히 직렬화될 수 있다. 여러 회차를 돌려 "둘 다 통과" 가
	// 한 번도 없어야 한다.
	const rounds = 15
	for i := range rounds {
		productID, variantID := seedProduct(t, pool,
			"tee-ver"+string(rune('a'+i)), 12000, 0, 10)

		var wg sync.WaitGroup
		errs := make([]error, 2)
		for j := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// 둘 다 화면에서 10 을 읽었다고 주장한다.
				errs[j] = s.EditVariants(ctx, productID, []VariantEdit{
					{ID: variantID, StockDelta: 5, Version: 10}})
			}()
		}
		wg.Wait()

		passed := 0
		for _, err := range errs {
			switch {
			case err == nil:
				passed++
			case errors.Is(err, ErrStockVersion):
			default:
				t.Fatalf("%d회차: 예상 밖 오류 %v", i, err)
			}
		}
		if passed != 1 {
			t.Fatalf("%d회차: %d건이 통과했다, want 1 — 버전 검사가 직렬화되지 않았다", i, passed)
		}
		assertStock(t, pool, variantID, 15)
	}
}
