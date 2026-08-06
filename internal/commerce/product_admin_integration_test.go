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

// **동시 입고 2건이 합산된다** (FR-621). 하나가 다른 하나를 덮어쓰면
// 입고분이 조용히 사라진다.
func TestConcurrentReceivesAccumulate(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	_, variantID := seedProduct(t, pool, "tee-recv", 12000, 0, 10)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.ReceiveStock(ctx, variantID, 7)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("%d번 입고: %v", i, err)
		}
	}
	assertStock(t, pool, variantID, 24)
}

// **실사의 조정값은 서버가 계산한다** (FR-622). 폼이 조정값을 보내도 쓰이지
// 않는다 — 함수가 받지 않기 때문이다.
func TestStocktakeComputesTheDeltaItself(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	_, variantID := seedProduct(t, pool, "tee-count", 12000, 0, 10)

	got, err := s.Stocktake(ctx, variantID, 7, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got.Delta != -3 || got.Ledger != 10 || got.Counted != 7 {
		t.Errorf("%+v, want 장부 10 · 실측 7 · 조정 -3", got)
	}
	assertStock(t, pool, variantID, 7)

	// 0 이 유효하다 — "세어보니 없었다" 가 실사의 정상 결과다.
	if _, err := s.Stocktake(ctx, variantID, 0, 7); err != nil {
		t.Fatalf("실측 0 이 거부됐다: %v", err)
	}
	assertStock(t, pool, variantID, 0)
}

// **세는 도중 주문이 들어오면 조정이 판매분을 지우지 않는다** (FR-622).
//
// 절대값 대입이면 그 주문은 사라진다. `WHERE stock = $장부` 가 0행을 내고
// 409 가 되어, 사람이 다시 센다.
func TestStocktakeRefusesWhenTheLedgerMovedUnderIt(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	_, variantID := seedProduct(t, pool, "tee-race", 12000, 0, 10)

	// 창고에서 10 을 세는 동안 2개가 팔렸다.
	if _, err := pool.Exec(ctx,
		`UPDATE product_variants SET stock = stock - 2 WHERE id = $1`, variantID); err != nil {
		t.Fatal(err)
	}

	_, err := s.Stocktake(ctx, variantID, 10, 10)
	if !errors.Is(err, ErrStockLedger) {
		t.Fatalf("= %v, want ErrStockLedger", err)
	}
	// 판매분이 그대로 남았다. 조정이 들어갔다면 10 이 됐을 것이다.
	assertStock(t, pool, variantID, 8)

	// 다시 세면 통과한다 — 위 단언이 "실사가 늘 막힌다" 가 아니다.
	if _, err := s.Stocktake(ctx, variantID, 9, 8); err != nil {
		t.Fatalf("다시 센 실사가 막혔다: %v", err)
	}
	assertStock(t, pool, variantID, 9)
}

// 차이가 없을 때도 장부가 바뀌었으면 409 다 — "차이 없음" 이 거짓이 되면
// 실사를 한 의미가 없다.
func TestStocktakeWithNoDifferenceStillChecksTheLedger(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	_, variantID := seedProduct(t, pool, "tee-same", 12000, 0, 10)

	got, err := s.Stocktake(ctx, variantID, 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got.Delta != 0 {
		t.Errorf("조정 %d, want 0", got.Delta)
	}

	if _, err := pool.Exec(ctx,
		`UPDATE product_variants SET stock = 4 WHERE id = $1`, variantID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stocktake(ctx, variantID, 10, 10); !errors.Is(err, ErrStockLedger) {
		t.Errorf("= %v, want ErrStockLedger", err)
	}
}

// **피킹 대조는 재고도 주문 상태도 건드리지 않는다** (FR-623).
//
// 건드리면 재고는 P-406 에서 이미 차감됐으므로 이중 차감이 되고, 상태는
// A-506 이 옮기는 것이라 유령 전이가 생긴다.
func TestPickListTouchesNothing(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, _, _ := paidOrder(t, s, pool, "tee-pick", 2)

	before, err := s.OrderByNoUnscoped(ctx, orderNo)
	if err != nil {
		t.Fatal(err)
	}
	var stockBefore int
	if err := pool.QueryRow(ctx, `
		SELECT v.stock FROM product_variants v
		JOIN order_items oi ON oi.variant_id = v.id
		JOIN orders o ON o.id = oi.order_id WHERE o.order_no = $1`,
		orderNo).Scan(&stockBefore); err != nil {
		t.Fatal(err)
	}

	lines, err := s.PickList(ctx, orderNo)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0].Ordered != 2 {
		t.Fatalf("피킹 목록 %+v", lines)
	}
	// 전 품목 대조를 마쳐도 아무것도 변하지 않는다.
	scanned := map[string]int{}
	for range lines[0].Ordered {
		if err := CheckPick(lines, scanned, lines[0].VariantID); err != nil {
			t.Fatal(err)
		}
		scanned[lines[0].VariantID]++
	}
	if !PickComplete(lines, scanned) {
		t.Error("전 품목을 대조했는데 완료가 아니다")
	}

	after, err := s.OrderByNoUnscoped(ctx, orderNo)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != before.Status {
		t.Errorf("주문 상태가 %s → %s 로 바뀌었다", before.Status, after.Status)
	}
	var stockAfter int
	if err := pool.QueryRow(ctx, `
		SELECT v.stock FROM product_variants v
		JOIN order_items oi ON oi.variant_id = v.id
		JOIN orders o ON o.id = oi.order_id WHERE o.order_no = $1`,
		orderNo).Scan(&stockAfter); err != nil {
		t.Fatal(err)
	}
	if stockAfter != stockBefore {
		t.Errorf("재고가 %d → %d 로 바뀌었다 — 이중 차감이다", stockBefore, stockAfter)
	}
}

// 없는 조합·형식 오류를 구분한다.
func TestScanVariantSeparatesFormatFromNotFound(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	_, variantID := seedProduct(t, pool, "tee-scan", 12000, 0, 5)

	if _, err := s.ScanVariant(ctx, "SKU-1234"); !errors.Is(err, ErrScanFormat) {
		t.Errorf("형식 오류 = %v, want ErrScanFormat", err)
	}
	if _, err := s.ScanVariant(ctx,
		"00000000-0000-4000-8000-000000000000"); !errors.Is(err, ErrNotFound) {
		t.Errorf("없는 조합 = %v, want ErrNotFound", err)
	}
	got, err := s.ScanVariant(ctx, variantID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stock != 5 {
		t.Errorf("재고 %d, want 5", got.Stock)
	}
	// SKU 가 NULL 인 조합도 스캔된다 — 라벨은 SKU 가 아니라 id 를 담는다.
	if got.SKU != "" {
		t.Errorf("SKU %q, want 빈 문자열", got.SKU)
	}
}
