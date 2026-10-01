-- 스캔·입고·실사·피킹 (scan.go, FR-620~623). 재고는 delta 로만 움직인다.

-- name: ScanVariant :one
SELECT v.id, v.product_id, p.name AS product_name, v.option_values, v.sku, v.stock
FROM product_variants v JOIN products p ON p.id = v.product_id
WHERE v.id = $1;

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
