-- 스캔·입고·실사·피킹 (scan.go, FR-620~623). 재고는 delta 로만 움직인다.

-- name: ScanVariant :one
SELECT v.id, v.product_id, p.name AS product_name, v.option_values, v.sku, v.stock
FROM product_variants v JOIN products p ON p.id = v.product_id
WHERE v.id = $1;

-- **조합을 특정하는 질의는 이것 하나다** (FR-627, D13 「식별 값」). A-516·A-517 이
-- 같은 규칙을 봐야 하므로 화면마다 따로 쓰지 않는다.
--
-- 정확 일치(QR = id · SKU · 바코드)가 먼저다. 하나라도 있으면 그것만 돌려주고,
-- 없을 때만 상품명 부분 일치로 찾는다 — 스캔한 값이 우연히 어느 상품명의 일부여도
-- 그 조합이 다른 상품들 사이에 묻히지 않는다. `exact` 는 어느 쪽으로 찾았는지다.
-- 한 조합의 SKU 가 다른 조합의 바코드와 같을 수 있어 정확 일치도 여러 행일 수 있다.
--
-- id 는 uuid 인자로 따로 받는다. `v.id::text = q` 로 쓰면 기본키 인덱스를 못 탄다.
-- 이름 일치는 strpos 다 — LIKE 는 `%`·`_` 를 이스케이프해야 한다.
-- ponytail: 이름 일치는 products 순차 탐색이다. 상품이 수만 개가 되어 느려지면
-- pg_trgm 인덱스를 건다.
-- name: FindVariants :many
WITH exact AS (
    SELECT v.id FROM product_variants v
    WHERE v.id = sqlc.narg('id')::uuid
       OR v.sku = sqlc.arg('q')::text OR v.barcode = sqlc.arg('q')::text
)
SELECT v.id, v.product_id, p.name AS product_name, v.option_values,
       COALESCE(v.sku, '')::text AS sku, COALESCE(v.barcode, '')::text AS barcode, v.stock,
       EXISTS (SELECT 1 FROM exact)::boolean AS exact
FROM product_variants v JOIN products p ON p.id = v.product_id
WHERE (sqlc.narg('product_id')::uuid IS NULL OR v.product_id = sqlc.narg('product_id'))
  AND (sqlc.arg('q')::text = ''
       OR v.id IN (SELECT id FROM exact)
       OR (NOT EXISTS (SELECT 1 FROM exact)
           AND strpos(lower(p.name), lower(sqlc.arg('q')::text)) > 0))
ORDER BY p.name, p.id, v.price_delta, v.id
LIMIT sqlc.arg('limit')::int OFFSET sqlc.arg('offset')::int;

-- name: ReceiveStock :one
UPDATE product_variants SET stock = stock + sqlc.arg('qty'), updated_at = now()
WHERE id = sqlc.arg('id') RETURNING stock;

-- name: VariantStock :one
SELECT stock FROM product_variants WHERE id = $1;

-- name: StocktakeAdjust :execrows
UPDATE product_variants SET stock = stock + sqlc.arg('delta'), updated_at = now()
WHERE id = sqlc.arg('id') AND stock = sqlc.arg('ledger');

-- name: PickList :many
SELECT oi.variant_id, oi.product_name, oi.option_label, oi.quantity
FROM order_items oi JOIN orders o ON o.id = oi.order_id
WHERE o.order_no = $1 ORDER BY oi.product_name;
