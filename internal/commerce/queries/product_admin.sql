-- 상품 관리 (product_admin.go, A-502/A-503). 재고는 조정값으로만 움직인다.

-- name: ProductByID :one
SELECT id, slug, name, description, base_price, is_visible
FROM products WHERE id = $1;

-- name: UpdateProduct :execrows
UPDATE products SET slug = $2, name = $3, description = $4,
       base_price = $5, is_visible = $6, updated_at = now()
WHERE id = $1;

-- name: DeleteProduct :execrows
DELETE FROM products WHERE id = $1;

-- name: LockVariantOfProduct :one
SELECT stock FROM product_variants WHERE id = $1 AND product_id = $2 FOR NO KEY UPDATE;

-- name: EditVariant :execrows
UPDATE product_variants
SET stock = stock + $3, sku = NULLIF($4::text, ''), price_delta = $5,
    barcode = NULLIF(sqlc.arg('barcode')::text, ''), updated_at = now()
WHERE id = $1 AND product_id = $2;

-- name: AddVariant :one
INSERT INTO product_variants (product_id, option_values, price_delta, stock, sku)
VALUES ($1, $2, $3, 0, NULLIF(sqlc.arg('sku')::text, '')) RETURNING id;

-- name: Options :many
SELECT name, values FROM product_options
WHERE product_id = $1 ORDER BY sort_order, name;

-- name: ProductExists :one
SELECT true::boolean AS found FROM products WHERE id = $1;

-- name: DeleteProductOptions :exec
DELETE FROM product_options WHERE product_id = $1;

-- name: InsertProductOption :exec
INSERT INTO product_options (product_id, name, values, sort_order)
VALUES ($1, $2, $3, $4);

-- name: InsertVariantIfMissing :exec
INSERT INTO product_variants (product_id, option_values, price_delta, stock)
VALUES ($1, $2, 0, 0)
ON CONFLICT (product_id, option_values) DO NOTHING;

-- 상품의 카테고리 (A-502, FR-615). **집합을 통째로 갈아 끼운다** — 지우고 넣는다.
-- product_categories 는 갱신하지 않는 연결 표다 (D30 3절 예외).
-- name: ProductCategoryIDs :many
SELECT category_id FROM product_categories WHERE product_id = $1 ORDER BY category_id;

-- name: ClearProductCategories :exec
DELETE FROM product_categories WHERE product_id = $1;

-- text[] 로 받아 안에서 uuid 로 바꾼다 — 드라이버가 문자열 배열을 uuid[] 로 싣는
-- 방식에 기대지 않는다. ON CONFLICT 는 같은 ID 가 두 번 실려 온 폼을 받아 준다.
-- name: AddProductCategories :exec
INSERT INTO product_categories (product_id, category_id)
SELECT sqlc.arg('product_id')::uuid, c::uuid
FROM unnest(sqlc.arg('category_ids')::text[]) AS c
ON CONFLICT DO NOTHING;
