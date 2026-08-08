package admin

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emirue/ondolith/internal/commerce"
)

// categoryFixture 는 `product.manage` 를 가진 운영자로 A-509 를 연다.
func categoryFixture(t *testing.T) (*Deps, *pgxpool.Pool, context.Context) {
	t.Helper()
	d, pool := fixture(t, &fakeCaller{
		perms: map[string]bool{"product.manage": true},
		id:    "u1", email: "op@example.com",
	})
	return d, pool, context.Background()
}

// **A-509 로 카테고리를 만들 수 있다** (FR-615).
//
// 이 화면에는 「이동」만 있었다. 만들 방법이 없으니 카테고리는 언제나 0개였고,
// 그래서 P-302 `/shop/c/{slug}` 는 어떤 주소로도 열 수 없는 화면이었다 —
// 만들 수 없는 것의 목록 화면이다.
func TestCategoryCreateMakesOneThePublicScreenCanOpen(t *testing.T) {
	d, _, ctx := categoryFixture(t)

	rec := postAdmin(t, d.CategoryCreate, "/admin/categories/new", nil, url.Values{
		"name": {"매트"}, "slug": {"mat"}, "sort_order": {"3"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("생성 = HTTP %d, want 303 (%s)", rec.Code, rec.Body.String())
	}

	// 공개 화면이 읽는 것과 **같은 함수**로 확인한다. INSERT 를 직접 세면
	// P-302 가 실제로 열리는지는 여전히 모른다.
	c, err := d.Commerce.CategoryBySlug(ctx, "mat")
	if err != nil {
		t.Fatalf("만든 카테고리를 공개 화면이 찾지 못한다: %v", err)
	}
	if c.Name != "매트" || c.SortOrder != 3 {
		t.Errorf("이름=%q 순서=%d, want 매트/3", c.Name, c.SortOrder)
	}
	if c.ParentID != "" {
		t.Errorf("상위=%q, want 최상위(빈 값)", c.ParentID)
	}
}

// **상위를 지정해 만들 수 있고, 없는 상위는 거부된다** (D19 A-509).
//
// 없는 상위를 500 으로 흘리면 운영자는 자기 입력이 아니라 서버를 의심한다.
func TestCategoryCreateHonoursTheParent(t *testing.T) {
	d, _, ctx := categoryFixture(t)

	postAdmin(t, d.CategoryCreate, "/admin/categories/new", nil, url.Values{
		"name": {"가구"}, "slug": {"furniture"}, "sort_order": {"0"},
	})
	parent, err := d.Commerce.CategoryBySlug(ctx, "furniture")
	if err != nil {
		t.Fatal(err)
	}

	rec := postAdmin(t, d.CategoryCreate, "/admin/categories/new", nil, url.Values{
		"name": {"매트"}, "slug": {"mat"}, "parent_id": {parent.ID}, "sort_order": {"0"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("상위 지정 생성 = HTTP %d, want 303", rec.Code)
	}
	child, err := d.Commerce.CategoryBySlug(ctx, "mat")
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentID != parent.ID {
		t.Errorf("상위=%q, want %q", child.ParentID, parent.ID)
	}

	// 없는 상위. 422 이지 500 이 아니다.
	rec = postAdmin(t, d.CategoryCreate, "/admin/categories/new", nil, url.Values{
		"name": {"고아"}, "slug": {"orphan"},
		"parent_id": {"00000000-0000-0000-0000-000000000000"}, "sort_order": {"0"},
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("없는 상위 = HTTP %d, want 422", rec.Code)
	}
	if _, err := d.Commerce.CategoryBySlug(ctx, "orphan"); err == nil {
		t.Error("거부됐는데 행이 생겼다")
	}
}

// **주소는 유일하고 형식이 있다** (D19 A-509 「거부 조건」).
//
// 주소가 공개 URL 이 되므로, 게시판·페이지와 **같은 검증 함수**를 쓴다. 여기서
// 따로 정규식을 쓰면 그 둘과 갈라지고, 갈라진 쪽만 예약어를 통과시킨다.
func TestCategoryCreateRefusesBadSlugs(t *testing.T) {
	d, _, ctx := categoryFixture(t)

	postAdmin(t, d.CategoryCreate, "/admin/categories/new", nil, url.Values{
		"name": {"매트"}, "slug": {"mat"}, "sort_order": {"0"},
	})

	for _, tc := range []struct {
		name string
		form url.Values
	}{
		{"주소 중복", url.Values{"name": {"다른 이름"}, "slug": {"mat"}}},
		{"대문자", url.Values{"name": {"이름"}, "slug": {"Mat"}}},
		{"경로 문자", url.Values{"name": {"이름"}, "slug": {"a/b"}}},
		{"예약어", url.Values{"name": {"이름"}, "slug": {"admin"}}},
		{"이름 없음", url.Values{"name": {"  "}, "slug": {"blank"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := postAdmin(t, d.CategoryCreate, "/admin/categories/new", nil, tc.form)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("HTTP %d, want 422", rec.Code)
			}
		})
	}

	// 헛돌기 방지: 위 다섯이 전부 422 인 것이 「이 핸들러가 늘 422」 여서는
	// 안 된다. 멀쩡한 입력은 통과해야 한다.
	rec := postAdmin(t, d.CategoryCreate, "/admin/categories/new", nil, url.Values{
		"name": {"방석"}, "slug": {"cushion"}, "sort_order": {"0"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("멀쩡한 입력 = HTTP %d, want 303 — 위 단언들이 헛돌았다", rec.Code)
	}
	if _, err := d.Commerce.CategoryBySlug(ctx, "cushion"); err != nil {
		t.Error("통과했는데 행이 없다")
	}
}

// **소속 상품이나 하위가 있는 카테고리는 지울 수 없다** (D19 A-509 → 409).
//
// 판정은 DB 의 `ON DELETE RESTRICT` 가 한다. 사전 조회로 세면 세는 것과 지우는
// 것 사이에 다른 요청이 상품을 붙일 수 있다.
func TestCategoryDeleteIsRefusedWhileSomethingPointsAtIt(t *testing.T) {
	d, pool, ctx := categoryFixture(t)

	postAdmin(t, d.CategoryCreate, "/admin/categories/new", nil, url.Values{
		"name": {"가구"}, "slug": {"furniture"}, "sort_order": {"0"},
	})
	parent, err := d.Commerce.CategoryBySlug(ctx, "furniture")
	if err != nil {
		t.Fatal(err)
	}
	postAdmin(t, d.CategoryCreate, "/admin/categories/new", nil, url.Values{
		"name": {"매트"}, "slug": {"mat"}, "parent_id": {parent.ID}, "sort_order": {"0"},
	})
	child, err := d.Commerce.CategoryBySlug(ctx, "mat")
	if err != nil {
		t.Fatal(err)
	}

	// 하위가 있다 → 409.
	rec := postAdmin(t, d.CategoryDelete, "/admin/categories/x/delete",
		map[string]string{"id": parent.ID}, nil)
	if rec.Code != http.StatusConflict {
		t.Errorf("하위가 있는 카테고리 삭제 = HTTP %d, want 409", rec.Code)
	}
	if _, err := d.Commerce.CategoryBySlug(ctx, "furniture"); err != nil {
		t.Error("거부됐는데 행이 사라졌다")
	}

	// 소속 상품이 있다 → 409.
	var productID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO products (slug,name,base_price,is_visible)
		 VALUES ('tee','티셔츠',12000,true) RETURNING id`).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO product_categories (product_id, category_id) VALUES ($1,$2)`,
		productID, child.ID); err != nil {
		t.Fatal(err)
	}
	rec = postAdmin(t, d.CategoryDelete, "/admin/categories/x/delete",
		map[string]string{"id": child.ID}, nil)
	if rec.Code != http.StatusConflict {
		t.Errorf("소속 상품이 있는 카테고리 삭제 = HTTP %d, want 409", rec.Code)
	}

	// 없는 카테고리 → 404. 500 이 아니다.
	rec = postAdmin(t, d.CategoryDelete, "/admin/categories/x/delete",
		map[string]string{"id": "00000000-0000-0000-0000-000000000000"}, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("없는 카테고리 삭제 = HTTP %d, want 404", rec.Code)
	}

	// 헛돌기 방지: 아무것도 가리키지 않는 카테고리는 지워진다. 이것이 없으면
	// 위 셋은 「삭제가 늘 실패한다」일 때도 통과한다.
	postAdmin(t, d.CategoryCreate, "/admin/categories/new", nil, url.Values{
		"name": {"빈 것"}, "slug": {"empty"}, "sort_order": {"0"},
	})
	free, err := d.Commerce.CategoryBySlug(ctx, "empty")
	if err != nil {
		t.Fatal(err)
	}
	rec = postAdmin(t, d.CategoryDelete, "/admin/categories/x/delete",
		map[string]string{"id": free.ID}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("빈 카테고리 삭제 = HTTP %d, want 303 — 위 단언들이 헛돌았다", rec.Code)
	}
	if _, err := d.Commerce.CategoryBySlug(ctx, "empty"); err == nil {
		t.Error("삭제됐다는데 행이 남아 있다")
	}
}

// **`product.manage` 없이는 아무것도 못 한다** (D15 2.2).
//
// A-509 의 세 동작이 같은 권한을 요구한다 — 하나라도 빠지면 그 하나가 우회
// 경로가 된다.
func TestCategoryWritesRequireProductManage(t *testing.T) {
	// **다른 권한을 가진 호출자**다. 권한이 하나도 없는 호출자로 시험하면
	// 「무언가는 요구한다」까지만 확인되고, 요구하는 것이 `product.manage` 인지
	// `admin.access` 인지는 구별되지 않는다 — 그 둘은 문턱이 다르다.
	d, _ := fixture(t, &fakeCaller{
		perms: map[string]bool{"order.view": true, "admin.access": true},
		id:    "u1", email: "x@example.com"})

	for name, h := range map[string]http.HandlerFunc{
		"목록": d.CategoryList,
		"생성": d.CategoryCreate,
		"삭제": d.CategoryDelete,
		"이동": d.CategoryReparent,
	} {
		t.Run(name, func(t *testing.T) {
			rec := postAdmin(t, h, "/admin/categories", map[string]string{"id": "x"},
				url.Values{"name": {"x"}, "slug": {"x"}, "id": {"x"}})
			if rec.Code != http.StatusForbidden {
				t.Errorf("권한 없이 %s = HTTP %d, want 403", name, rec.Code)
			}
		})
	}

	// 헛돌기 방지: 권한이 있으면 통과한다.
	ok, _ := fixture(t, &fakeCaller{perms: map[string]bool{"product.manage": true},
		id: "u1", email: "op@example.com"})
	if rec := postAdmin(t, ok.CategoryCreate, "/admin/categories/new", nil,
		url.Values{"name": {"매트"}, "slug": {"mat"}, "sort_order": {"0"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("권한이 있는데 = HTTP %d, want 303 — 위 단언들이 헛돌았다", rec.Code)
	}
}

// 저장소 계층의 오류 매핑. 핸들러를 거치지 않고 직접 본다 — 핸들러가 이 값을
// HTTP 코드로 옮기므로, 값이 틀리면 코드도 틀린다.
func TestCategoryStoreMapsDatabaseErrors(t *testing.T) {
	d, _, ctx := categoryFixture(t)

	if _, err := d.Commerce.CreateCategory(ctx,
		commerce.Category{Name: "매트", Slug: "mat"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Commerce.CreateCategory(ctx,
		commerce.Category{Name: "다른 것", Slug: "mat"}); err != commerce.ErrSlugTaken {
		t.Errorf("주소 중복 = %v, want ErrSlugTaken", err)
	}
	if _, err := d.Commerce.CreateCategory(ctx, commerce.Category{
		Name: "고아", Slug: "orphan",
		ParentID: "00000000-0000-0000-0000-000000000000",
	}); err != commerce.ErrCategoryMissing {
		t.Errorf("없는 상위 = %v, want ErrCategoryMissing", err)
	}
	if err := d.Commerce.DeleteCategory(ctx,
		"00000000-0000-0000-0000-000000000000"); err != commerce.ErrNotFound {
		t.Errorf("없는 행 삭제 = %v, want ErrNotFound", err)
	}
}
