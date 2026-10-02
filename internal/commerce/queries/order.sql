-- 주문 (order.go, FR-604/FR-612/FR-619). 소유권 술어는 WHERE 에 있다 (SC-3).

-- name: LockCartForOrder :many
SELECT ci.variant_id, ci.quantity, p.id AS product_id, p.name, v.option_values,
       p.base_price, v.price_delta, p.is_visible AS product_visible, v.is_visible AS variant_visible, v.stock
FROM cart_items ci
JOIN carts c            ON c.id = ci.cart_id
JOIN product_variants v ON v.id = ci.variant_id
JOIN products p         ON p.id = v.product_id
WHERE (sqlc.narg('user_id')::uuid IS NOT NULL AND c.user_id = sqlc.narg('user_id'))
   OR (sqlc.narg('guest_key')::text IS NOT NULL AND c.guest_key = sqlc.narg('guest_key'))
ORDER BY ci.created_at, ci.id
FOR UPDATE OF ci;

-- **시행본을 먼저 고르고, 그다음에 필수인지 본다.** 필수만 걸러 놓고 최신을 고르면
-- 필수 → 선택으로 개정된 종류에서 옛 필수본이 계속 요구되는데, 주문서(TermsInForce)
-- 는 그 버전을 보여 주지 않는다 — 모든 주문이 ErrTermsRequired 로 끝난다.
-- name: RequiredTermIDs :many
SELECT t.id FROM terms t
WHERE t.is_required AND t.id IN (
    SELECT DISTINCT ON (f.kind) f.id FROM terms f
    WHERE f.effective_at <= sqlc.arg('now')
    ORDER BY f.kind, f.effective_at DESC, f.created_at DESC, f.id)
ORDER BY t.kind;

-- name: InsertOrder :one
INSERT INTO orders (order_no, user_id, status, total_amount, discount_amount,
                    receiver_name, receiver_phone, postcode, address1, address2,
                    delivery_memo, orderer_email, orderer_phone)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING id;

-- name: InsertOrderItem :exec
INSERT INTO order_items (order_id, product_id, variant_id, product_name,
                         option_label, unit_price, quantity, discount_amount)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8);

-- name: InsertOrderAgreement :exec
INSERT INTO order_agreements (order_id, terms_id) VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: ClearCart :exec
DELETE FROM cart_items ci
WHERE ci.cart_id IN (SELECT id FROM carts
                     WHERE (sqlc.narg('user_id')::uuid IS NOT NULL AND user_id = sqlc.narg('user_id'))
                        OR (sqlc.narg('guest_key')::text IS NOT NULL AND guest_key = sqlc.narg('guest_key')));

-- name: TermsInForce :many
SELECT DISTINCT ON (kind) id, kind, version, body, is_required
FROM terms WHERE effective_at <= sqlc.arg('now')
ORDER BY kind, effective_at DESC, created_at DESC, id;

-- name: OrderByNo :one
SELECT id, order_no, status, total_amount, discount_amount,
       receiver_name, receiver_phone,
       postcode, address1, address2, orderer_email, orderer_phone, created_at
FROM orders
WHERE order_no = sqlc.arg('order_no')
  AND ( (sqlc.narg('user_id')::uuid IS NOT NULL AND user_id = sqlc.narg('user_id'))
     OR (sqlc.narg('orderer_phone')::text IS NOT NULL AND orderer_phone = sqlc.narg('orderer_phone')) );

-- name: OrderItems :many
SELECT id, product_name, option_label, unit_price, quantity, line_amount::int AS line_amount,
       discount_amount, settled_quantity
FROM order_items WHERE order_id = $1 ORDER BY created_at, id;

-- name: MyOrders :many
SELECT order_no, status, total_amount, created_at
FROM orders WHERE user_id = sqlc.arg('user_id')
ORDER BY created_at DESC, id LIMIT sqlc.arg('limit')::int OFFSET sqlc.arg('offset')::int;

-- name: OrderByNoUnscoped :one
SELECT id, order_no, status, total_amount, discount_amount,
       receiver_name, receiver_phone,
       postcode, address1, address2, orderer_email, orderer_phone, created_at
FROM orders WHERE order_no = $1;

-- name: GuestOrder :one
SELECT id, order_no, status, total_amount, discount_amount,
       receiver_name, receiver_phone,
       postcode, address1, address2, orderer_email, orderer_phone, created_at
FROM orders
WHERE order_no = sqlc.arg('order_no')
  AND user_id IS NULL
  AND ( (sqlc.narg('phone')::text IS NOT NULL AND orderer_phone = sqlc.narg('phone'))
     OR (sqlc.narg('email')::text IS NOT NULL AND orderer_email = sqlc.narg('email')) );

-- name: Shipments :many
SELECT kind, carrier, tracking_no, shipped_at FROM shipments
WHERE order_id = $1 ORDER BY shipped_at DESC, id;

-- name: AdminOrders :many
SELECT order_no, status, total_amount, orderer_email, created_at
FROM orders
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  -- 기간 조회 (D19 0.6): 주문일 기준, `since ≤ 시각 < until`. NULL 이면 그쪽 끝이 열려 있다.
  AND (sqlc.narg('since')::timestamptz IS NULL OR created_at >= sqlc.narg('since'))
  AND (sqlc.narg('until')::timestamptz IS NULL OR created_at < sqlc.narg('until'))
ORDER BY created_at DESC, id LIMIT sqlc.arg('limit')::int OFFSET sqlc.arg('offset')::int;

-- name: LockOrderByNo :one
SELECT id, status FROM orders WHERE order_no = $1 FOR UPDATE;

-- name: SetOrderStatus :exec
UPDATE orders SET status = $2, updated_at = now() WHERE id = $1;

-- name: MarkDelivered :exec
UPDATE orders SET delivered_at = now() WHERE id = $1 AND delivered_at IS NULL;

-- name: MarkConfirmed :exec
UPDATE orders SET confirmed_at = now() WHERE id = $1 AND confirmed_at IS NULL;

-- name: OrderIDByNo :one
SELECT id FROM orders WHERE order_no = $1;

-- name: InsertFirstShipment :exec
INSERT INTO shipments (order_id, kind, carrier, tracking_no, shipped_at)
VALUES ($1, '최초발송', $2, $3, $4);

-- name: ExpirablePendingOrders :many
SELECT o.id FROM orders o
WHERE o.status = sqlc.arg('status') AND o.created_at < sqlc.arg('before')
  AND NOT EXISTS (SELECT 1 FROM payments p
                  WHERE p.order_id = o.id AND p.kind = '주문결제' AND p.status = '대기')
ORDER BY o.created_at LIMIT sqlc.arg('limit')::int;

-- name: LockOrderStatus :one
SELECT status FROM orders WHERE id = $1 FOR UPDATE;

-- name: HasPendingOrderPayment :one
SELECT EXISTS (
    SELECT 1 FROM payments WHERE order_id = $1 AND kind = '주문결제' AND status = '대기'
)::boolean AS pending;

-- name: MoveOrderStatus :execrows
UPDATE orders SET status = sqlc.arg('to_status'), updated_at = now()
WHERE id = sqlc.arg('id') AND status = sqlc.arg('from_status');

-- name: OpenOrders :one
SELECT count(*) FROM orders WHERE status = ANY(sqlc.arg('open')::text[]);
