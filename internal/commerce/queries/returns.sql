-- 반품·교환 (returns.go, FR-617/FR-618). 수거를 거치지 않은 환불은 상태머신이 막는다.

-- name: ReturnFeeSettings :many
SELECT key, value FROM settings WHERE key = ANY(sqlc.arg('keys')::text[]);

-- name: LockOrderItemForReturn :one
SELECT oi.product_id, oi.quantity, oi.settled_quantity, v.price_delta
FROM order_items oi JOIN product_variants v ON v.id = oi.variant_id
WHERE oi.id = $1 AND oi.order_id = $2 FOR UPDATE OF oi;

-- name: ExchangeTarget :one
SELECT v.product_id, v.price_delta FROM product_variants v
JOIN products p ON p.id = v.product_id
WHERE v.id = $1 AND v.is_visible AND p.is_visible;

-- name: InsertReturn :one
INSERT INTO returns (return_no, order_id, kind, status, reason,
                     new_variant_id, price_difference)
VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id;

-- name: InsertReturnItem :exec
INSERT INTO return_items (return_id, order_item_id, quantity) VALUES ($1,$2,$3);

-- name: LockReturnGross :many
SELECT ri.order_item_id, ri.quantity AS return_quantity,
       oi.line_amount::int AS line_amount, oi.discount_amount, oi.quantity, oi.settled_quantity
FROM return_items ri
JOIN order_items oi ON oi.id = ri.order_item_id
WHERE ri.return_id = $1 AND ri.is_open
ORDER BY ri.order_item_id
FOR UPDATE OF oi;

-- name: LockReturnForPickup :one
SELECT r.id, r.kind, r.status, r.order_id FROM returns r
JOIN orders o ON o.id = r.order_id
WHERE r.return_no = sqlc.arg('return_no') AND o.order_no = sqlc.arg('order_no') FOR UPDATE OF r;

-- name: RecordPickup :exec
UPDATE returns SET status = sqlc.arg('status'), fault = sqlc.arg('fault'),
       shipping_fee_policy = sqlc.arg('fee_policy'), shipping_fee_amount = sqlc.arg('fee_amount'), updated_at = now()
WHERE id = sqlc.arg('id') AND status = sqlc.arg('from_status');

-- name: LockReturnForSettle :one
SELECT r.id, r.kind, r.status, r.order_id,
       COALESCE(r.shipping_fee_policy, '')::text AS fee_policy, COALESCE(r.shipping_fee_amount, 0)::int AS fee_amount
FROM returns r JOIN orders o ON o.id = r.order_id
WHERE r.return_no = sqlc.arg('return_no') AND o.order_no = sqlc.arg('order_no') FOR UPDATE OF r;

-- name: ApprovedPaymentID :one
SELECT id FROM payments
WHERE order_id = $1 AND kind = '주문결제' AND status = '승인';

-- name: InsertReturnRefund :one
INSERT INTO refunds (order_id, payment_id, return_id, status, requester,
                     amount, reason, request_key)
VALUES ($1,$2,$3,'요청','관리자',$4,'반품 환불',$5) RETURNING id;

-- name: CloseReturnItems :exec
UPDATE return_items SET is_open = false, updated_at = now() WHERE return_id = $1;

-- name: SetReturnStatus :exec
UPDATE returns SET status = $2, updated_at = now() WHERE id = $1;

-- name: LockReturnForReject :one
SELECT r.id, r.kind, r.status, r.order_id, COALESCE(r.new_variant_id::text, '')::text AS new_variant_id
FROM returns r JOIN orders o ON o.id = r.order_id
WHERE r.return_no = sqlc.arg('return_no') AND o.order_no = sqlc.arg('order_no') FOR UPDATE OF r;

-- name: ReturnItemQuantity :one
SELECT COALESCE(sum(quantity), 0)::int AS quantity FROM return_items WHERE return_id = $1;

-- name: RejectReturn :exec
UPDATE returns SET status = '거부', reject_reason = $2, updated_at = now()
WHERE id = $1;

-- name: Returns :many
SELECT id, return_no, kind, status, reason, reject_reason,
       COALESCE(fault, '')::text AS fault, COALESCE(shipping_fee_policy, '')::text AS fee_policy,
       COALESCE(shipping_fee_amount, 0)::int AS fee_amount, COALESCE(new_variant_id::text, '')::text AS new_variant_id,
       COALESCE(price_difference, 0)::int AS price_difference, created_at
FROM returns WHERE order_id = $1 ORDER BY created_at DESC, id;

-- name: ReturnItemsOf :many
SELECT ri.return_id, ri.order_item_id, oi.product_name, oi.option_label, ri.quantity, ri.is_open
FROM return_items ri JOIN order_items oi ON oi.id = ri.order_item_id
WHERE ri.return_id = ANY(sqlc.arg('return_ids')::uuid[]) ORDER BY oi.created_at, oi.id;

-- name: ReturnItems :many
SELECT ri.order_item_id, oi.product_name, oi.option_label, ri.quantity, ri.is_open
FROM return_items ri JOIN order_items oi ON oi.id = ri.order_item_id
WHERE ri.return_id = $1 ORDER BY oi.created_at, oi.id;

-- name: ReturnByNo :one
SELECT id, return_no, kind, status, reason, reject_reason,
       COALESCE(fault, '')::text AS fault, COALESCE(shipping_fee_policy, '')::text AS fee_policy,
       COALESCE(shipping_fee_amount, 0)::int AS fee_amount, COALESCE(new_variant_id::text, '')::text AS new_variant_id,
       COALESCE(price_difference, 0)::int AS price_difference, created_at
FROM returns WHERE return_no = $1;

-- name: VariantsForExchange :many
SELECT v.id, v.product_id, v.option_values, COALESCE(v.sku, '')::text AS sku,
       v.price_delta, v.stock, v.is_visible
FROM product_variants v
WHERE v.product_id = (SELECT oi.product_id FROM order_items oi WHERE oi.id = sqlc.arg('order_item_id'))
  AND v.id <> (SELECT oi.variant_id FROM order_items oi WHERE oi.id = sqlc.arg('order_item_id'))
  AND v.is_visible AND v.stock > 0
ORDER BY v.price_delta, v.id;

-- name: LockReturnForExchange :one
SELECT r.id, r.kind, r.status, r.order_id, COALESCE(r.price_difference, 0)::int AS price_difference
FROM returns r JOIN orders o ON o.id = r.order_id
WHERE r.return_no = sqlc.arg('return_no') AND o.order_no = sqlc.arg('order_no') FOR UPDATE OF r;

-- name: CloseReturnItemsNoTouch :exec
UPDATE return_items SET is_open = false WHERE return_id = $1;

-- name: MoveReturnStatus :execrows
UPDATE returns SET status = sqlc.arg('to_status'), updated_at = now()
WHERE id = sqlc.arg('id') AND status = sqlc.arg('from_status');

-- name: ExchangeDiffDue :one
SELECT r.id, r.return_no, r.order_id, r.status, COALESCE(r.price_difference, 0)::int AS price_difference
FROM returns r JOIN orders o ON o.id = r.order_id
WHERE r.return_no = sqlc.arg('return_no') AND o.order_no = sqlc.arg('order_no') AND r.kind = '교환'
  AND (sqlc.arg('user_id')::text = '' OR o.user_id::text = sqlc.arg('user_id'));

-- name: ReserveExchangePayment :one
INSERT INTO payments (order_id, return_id, kind, status, pg, payment_key, approved_amount)
VALUES ($1, $2, '교환차액', '대기', $3, $4, $5) RETURNING id;

-- name: ApproveExchangePayment :exec
UPDATE payments SET status = sqlc.arg('status'), approved_at = now(), raw_response = sqlc.arg('raw_response'),
       secret = NULLIF(sqlc.arg('secret')::text, ''), updated_at = now()
WHERE id = sqlc.arg('id');
