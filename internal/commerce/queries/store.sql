-- 상품·조합·카테고리·검색 (store.go). 정렬 키는 CASE 로 한 질의에 담는다 (D22 6절).

-- **쪽을 먼저 고르고, 조합 요약은 그 행에만 붙인다** (ListProducts·SearchProducts).
-- 한 층으로 쓰면 정렬이 인덱스를 못 타는 순간 걸러진 상품 **전부**의 조합을 집계하고
-- 나서 정렬한다. 바깥 ORDER BY 는 안쪽과 같다 — 조인이 순서를 지켜 준다는 보장은 없다.
-- name: ListProducts :many
SELECT p.id, p.slug, p.name, p.description, p.base_price, p.is_visible,
       COALESCE(v.min_delta, 0)::int AS min_delta, COALESCE(v.in_stock, false)::boolean AS in_stock
FROM (
    SELECT p.id, p.slug, p.name, p.description, p.base_price, p.is_visible, p.created_at
    FROM products p
    WHERE (sqlc.arg('visible_only')::boolean IS NOT TRUE OR p.is_visible)
      AND (sqlc.narg('category_id')::uuid IS NULL OR EXISTS (
            SELECT 1 FROM product_categories pc
            WHERE pc.product_id = p.id AND pc.category_id = sqlc.narg('category_id')))
    ORDER BY
        CASE WHEN sqlc.arg('sort')::text = 'price'      THEN p.base_price END,
        CASE WHEN sqlc.arg('sort')::text = 'price_desc' THEN p.base_price END DESC,
        CASE WHEN sqlc.arg('sort')::text = 'name'       THEN p.name END,
        CASE WHEN sqlc.arg('sort')::text IN ('', 'new') THEN p.created_at END DESC,
        p.id
    LIMIT sqlc.arg('limit')::int OFFSET sqlc.arg('offset')::int
) p
LEFT JOIN LATERAL (
    SELECT min(price_delta) AS min_delta, bool_or(stock > 0) AS in_stock
    FROM product_variants
    WHERE product_id = p.id AND is_visible
) v ON true
ORDER BY
    CASE WHEN sqlc.arg('sort')::text = 'price'      THEN p.base_price END,
    CASE WHEN sqlc.arg('sort')::text = 'price_desc' THEN p.base_price END DESC,
    CASE WHEN sqlc.arg('sort')::text = 'name'       THEN p.name END,
    CASE WHEN sqlc.arg('sort')::text IN ('', 'new') THEN p.created_at END DESC,
    p.id;

-- name: ProductBySlug :one
SELECT id, slug, name, description, base_price, is_visible
FROM products WHERE slug = $1 AND (sqlc.arg('visible_only')::boolean IS NOT TRUE OR is_visible);

-- name: Variants :many
SELECT id, product_id, option_values, COALESCE(sku, '')::text AS sku, price_delta, stock, is_visible
FROM product_variants
WHERE product_id = $1 AND (sqlc.arg('sellable_only')::boolean IS NOT TRUE OR (is_visible AND stock > 0))
ORDER BY price_delta, id;

-- name: VariantForPurchase :one
SELECT v.id, v.product_id, v.option_values, COALESCE(v.sku, '')::text AS sku, v.price_delta,
       v.stock, v.is_visible, p.is_visible AS product_visible, p.base_price
FROM product_variants v JOIN products p ON p.id = v.product_id
WHERE v.id = $1;

-- FOR NO KEY UPDATE: 뒤따르는 UPDATE 는 키 컬럼을 바꾸지 않는다. FOR UPDATE 는
-- 외래키 검사(FOR KEY SHARE)까지 막아, 재고를 잠근 동안 그 조합을 장바구니에 담는
-- INSERT 가 줄을 선다.
-- name: LockVariantStock :one
SELECT stock FROM product_variants WHERE id = $1 FOR NO KEY UPDATE;

-- name: AddVariantStock :exec
UPDATE product_variants SET stock = stock + sqlc.arg('delta'), updated_at = now() WHERE id = sqlc.arg('id');

-- name: CreateProduct :one
INSERT INTO products (slug, name, description, base_price, is_visible)
VALUES ($1, $2, $3, $4, $5) RETURNING id;

-- name: CategoryParents :many
SELECT id, COALESCE(parent_id::text, '')::text AS parent_id FROM categories;

-- name: AdvisoryXactLock :exec
SELECT pg_advisory_xact_lock(sqlc.arg('key'));

-- name: SetCategoryParent :exec
UPDATE categories SET parent_id = sqlc.narg('parent_id'), updated_at = now() WHERE id = sqlc.arg('id');

-- name: Categories :many
SELECT id, COALESCE(parent_id::text, '')::text AS parent_id, name, slug, sort_order
FROM categories ORDER BY sort_order, name, id;

-- name: CategoryBySlug :one
SELECT id, COALESCE(parent_id::text, '')::text AS parent_id, name, slug, sort_order
FROM categories WHERE slug = $1;

-- name: CreateCategory :one
INSERT INTO categories (parent_id, name, slug, sort_order)
VALUES (NULLIF(sqlc.arg('parent_id')::text, '')::uuid, sqlc.arg('name'), sqlc.arg('slug'), sqlc.arg('sort_order')) RETURNING id;

-- name: DeleteCategory :execrows
DELETE FROM categories WHERE id = $1;

-- name: SearchProducts :many
SELECT p.id, p.slug, p.name, p.description, p.base_price, p.is_visible,
       COALESCE(v.min_delta, 0)::int AS min_delta, COALESCE(v.in_stock, false)::boolean AS in_stock
FROM (
    SELECT p.id, p.slug, p.name, p.description, p.base_price, p.is_visible,
           ts_rank(p.search_tsv, to_tsquery('simple', sqlc.arg('query'))) AS rank
    FROM products p
    WHERE p.is_visible AND p.search_tsv @@ to_tsquery('simple', sqlc.arg('query'))
    ORDER BY rank DESC, p.id
    LIMIT sqlc.arg('limit')::int OFFSET sqlc.arg('offset')::int
) p
LEFT JOIN LATERAL (
    SELECT min(price_delta) AS min_delta, bool_or(stock > 0) AS in_stock
    FROM product_variants WHERE product_id = p.id AND is_visible
) v ON true
ORDER BY p.rank DESC, p.id;
