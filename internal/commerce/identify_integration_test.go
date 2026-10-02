package commerce

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// codedVariant 는 이름·SKU·바코드를 정한 상품과 조합 하나를 넣는다. 빈 값은 NULL.
func codedVariant(t *testing.T, pool *pgxpool.Pool, slug, name, sku, barcode string) (productID, variantID string) {
	t.Helper()
	ctx := context.Background()
	if err := pool.QueryRow(ctx,
		`INSERT INTO products (slug,name,base_price,is_visible) VALUES ($1,$2,1000,true) RETURNING id`,
		slug, name).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO product_variants (product_id,option_values,price_delta,stock,sku,barcode)
		 VALUES ($1,'{"크기":"L"}',0,5,NULLIF($2,''),NULLIF($3,'')) RETURNING id`,
		productID, sku, barcode).Scan(&variantID); err != nil {
		t.Fatal(err)
	}
	return productID, variantID
}

func matchedIDs(m *VariantMatches) []string {
	var out []string
	for _, r := range m.Rows {
		out = append(out, r.ID)
	}
	slices.Sort(out)
	return out
}

// **QR(id)·SKU·바코드가 각각 그 조합 하나를 찾는다** (FR-627).
func TestFindVariantsMatchesIDSkuAndBarcodeExactly(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	_, target := codedVariant(t, pool, "tee", "티셔츠", "SKU-TEE", "8801234567890")
	codedVariant(t, pool, "cap", "모자", "SKU-CAP", "8809999999999")

	// 대문자 uuid 도 같은 조합이다 — 스캐너 설정에 따라 대문자로 온다.
	for _, q := range []string{target, strings.ToUpper(target), "SKU-TEE", "8801234567890", " SKU-TEE "} {
		got, err := s.FindVariants(ctx, VariantQuery{Q: q})
		if err != nil {
			t.Fatal(err)
		}
		if !got.Exact || len(got.Rows) != 1 || got.Rows[0].ID != target {
			t.Errorf("%q → exact=%v %v, want 정확 일치로 %s 하나", q, got.Exact, matchedIDs(got), target)
		}
	}
	got, err := s.FindVariants(ctx, VariantQuery{Q: "SKU-TEE"})
	if err != nil {
		t.Fatal(err)
	}
	if r := got.Rows[0]; r.ProductName != "티셔츠" || r.SKU != "SKU-TEE" || r.Barcode != "8801234567890" || r.Stock != 5 {
		t.Errorf("행 %+v — 화면이 그릴 값이 빠졌다", r)
	}
	// SKU 의 일부는 정확 일치가 아니다.
	if got, _ := s.FindVariants(ctx, VariantQuery{Q: "SKU-TE"}); got.Exact || len(got.Rows) != 0 {
		t.Errorf("SKU 의 일부가 맞았다: exact=%v %v", got.Exact, matchedIDs(got))
	}
}

// **한 조합의 SKU 가 다른 조합의 바코드와 같으면 둘 다 돌려준다** (D13 「식별 값」).
//
// 둘은 서로 다른 외부 체계가 정하는 코드라 DB 가 막지 않는다. 하나를 골라
// 돌려주면 입고·피킹이 틀린 조합에 반영된다.
func TestFindVariantsReturnsBothWhenSkuEqualsAnotherBarcode(t *testing.T) {
	s, pool := testStore(t)
	_, bySKU := codedVariant(t, pool, "tee", "티셔츠", "12345", "")
	_, byBarcode := codedVariant(t, pool, "cap", "모자", "", "12345")

	got, err := s.FindVariants(context.Background(), VariantQuery{Q: "12345"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{bySKU, byBarcode}
	slices.Sort(want)
	if !got.Exact || !slices.Equal(matchedIDs(got), want) {
		t.Fatalf("exact=%v %v, want 두 조합 %v", got.Exact, matchedIDs(got), want)
	}
	if _, err := got.One(); !errors.Is(err, ErrPickAmbiguous) {
		t.Errorf("One() = %v, want ErrPickAmbiguous", err)
	}
}

// **정확 일치가 있으면 그 값을 이름에 포함한 다른 상품은 나오지 않는다.**
//
// 스캔한 값이 우연히 어느 상품명의 일부여도 그 조합이 다른 상품들 사이에
// 묻히면 안 된다. 정확 일치가 없을 때만 이름으로 찾는다.
func TestFindVariantsPrefersExactOverNameMatch(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	_, coded := codedVariant(t, pool, "tee", "티셔츠", "RED", "")
	_, named := codedVariant(t, pool, "red-cap", "RED 모자", "", "")

	got, err := s.FindVariants(ctx, VariantQuery{Q: "RED"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Exact || !slices.Equal(matchedIDs(got), []string{coded}) {
		t.Errorf("exact=%v %v, want 정확 일치 %s 만", got.Exact, matchedIDs(got), coded)
	}

	// 정확 일치가 없으면 이름 부분 일치다. 대소문자를 가리지 않는다.
	got, err = s.FindVariants(ctx, VariantQuery{Q: "red 모"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Exact || !slices.Equal(matchedIDs(got), []string{named}) {
		t.Errorf("exact=%v %v, want 이름 일치 %s 만", got.Exact, matchedIDs(got), named)
	}
	// 이름으로 찾은 것은 피킹이 고르지 않는다.
	if _, err := got.One(); !errors.Is(err, ErrPickNotInOrder) {
		t.Errorf("One() = %v, want ErrPickNotInOrder", err)
	}

	// LIKE 문자가 와일드카드로 쓰이지 않는다.
	if got, _ := s.FindVariants(ctx, VariantQuery{Q: "%"}); len(got.Rows) != 0 {
		t.Errorf("%% 가 %d 행에 맞았다", len(got.Rows))
	}
}

// 검색어가 없으면 전체 목록이고, 상품으로 좁힐 수 있다. 맞는 것이 없거나
// 상품 id 가 uuid 가 아니어도 오류가 아니라 빈 결과다.
func TestFindVariantsListsAndNarrowsByProduct(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	teeProduct, tee := codedVariant(t, pool, "tee", "티셔츠", "", "")
	codedVariant(t, pool, "cap", "모자", "", "")

	all, err := s.FindVariants(ctx, VariantQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if all.Exact || len(all.Rows) != 2 {
		t.Errorf("전체 목록: exact=%v %d행, want 2행", all.Exact, len(all.Rows))
	}
	one, err := s.FindVariants(ctx, VariantQuery{ProductID: teeProduct})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(matchedIDs(one), []string{tee}) {
		t.Errorf("상품으로 좁힘: %v, want %s", matchedIDs(one), tee)
	}
	for _, q := range []VariantQuery{{Q: "없는 것"}, {ProductID: "not-a-uuid"},
		{Q: "00000000-0000-4000-8000-000000000000"}} {
		got, err := s.FindVariants(ctx, q)
		if err != nil {
			t.Errorf("%+v = %v, want 빈 결과", q, err)
			continue
		}
		if len(got.Rows) != 0 || got.Exact {
			t.Errorf("%+v → %d행 exact=%v, want 빈 결과", q, len(got.Rows), got.Exact)
		}
	}
}

// **두 조합이 같은 바코드를 가질 수 없다** (FR-627, DB 유일 제약).
// 비어 있는 것은 몇 개든 된다 — 부분 인덱스다.
func TestBarcodeIsUniqueAcrossVariants(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	p1, v1 := codedVariant(t, pool, "tee", "티셔츠", "", "")
	p2, v2 := codedVariant(t, pool, "cap", "모자", "", "")

	if err := s.EditVariants(ctx, p1, []VariantEdit{{ID: v1, Barcode: "8801234567890", Version: -1}}); err != nil {
		t.Fatal(err)
	}
	err := s.EditVariants(ctx, p2, []VariantEdit{{ID: v2, Barcode: "8801234567890", Version: -1}})
	if !errors.Is(err, ErrBarcodeTaken) {
		t.Fatalf("같은 바코드 = %v, want ErrBarcodeTaken", err)
	}
	// SKU 중복은 여전히 SKU 로 보고된다 — 둘을 한 오류로 접지 않는다.
	if err := s.EditVariants(ctx, p1, []VariantEdit{{ID: v1, SKU: "S1", Barcode: "8801234567890", Version: -1}}); err != nil {
		t.Fatal(err)
	}
	err = s.EditVariants(ctx, p2, []VariantEdit{{ID: v2, SKU: "S1", Version: -1}})
	if !errors.Is(err, ErrSkuTaken) {
		t.Errorf("같은 SKU = %v, want ErrSkuTaken", err)
	}
	err = s.EditVariants(ctx, p2, []VariantEdit{{ID: v2, Barcode: strings.Repeat("9", 65), Version: -1}})
	if !errors.Is(err, ErrBarcodeLength) {
		t.Errorf("65자 바코드 = %v, want ErrBarcodeLength", err)
	}

	// 저장한 값이 A-503 이 읽는 목록에 나온다.
	vs, err := s.Variants(ctx, p1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || vs[0].Barcode != "8801234567890" {
		t.Errorf("Variants = %+v, want 바코드 8801234567890", vs)
	}
	// 빈 값은 NULL 이다 — 두 조합이 함께 비어 있을 수 있다.
	if err := s.EditVariants(ctx, p1, []VariantEdit{{ID: v1, Version: -1}}); err != nil {
		t.Fatalf("바코드 비우기: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM product_variants WHERE barcode IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("바코드가 NULL 인 조합 %d개, want 2 — 빈 문자열로 저장됐다", n)
	}
}
