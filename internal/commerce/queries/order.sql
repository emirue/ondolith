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
       postcode, address1, address2, delivery_memo, orderer_email, orderer_phone, created_at
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
       postcode, address1, address2, delivery_memo, orderer_email, orderer_phone, created_at
FROM orders WHERE order_no = $1;

-- name: GuestOrder :one
SELECT id, order_no, status, total_amount, discount_amount,
       receiver_name, receiver_phone,
       postcode, address1, address2, delivery_memo, orderer_email, orderer_phone, created_at
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

-- FOR NO KEY UPDATE: 주문 상태를 쓰는 흐름끼리는 그대로 줄을 서고, 그 주문을 가리키는
-- 행의 INSERT(외래키 검사, FOR KEY SHARE)는 지나간다. FOR UPDATE 면 품목을 잡고 환불 행을
-- 넣는 부분 환불과, 주문을 잡고 그 품목을 기다리는 반품 정산이 교착한다 (LockVariantStock 과 같은 이유).
-- name: LockOrderByNo :one
SELECT id, status, delivered_at FROM orders WHERE order_no = $1 FOR NO KEY UPDATE;

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

-- name: DueDepositOrders :many
-- 입금 기한(발급 응답 원문의 virtualAccount.dueDate)이 before 보다 앞인 입금대기 주문과 그 살아
-- 있는 결제. 기한은 오프셋이 있으면 그대로, 없으면 한국 시각으로 읽고, 모양이 아니면 고르지
-- 않는다(잴 수 없는 것은 닫지 않는다). **확인 시각(updated_at) 순이다** — 확인하고 닫지 못한
-- 것은 TouchPayment 가 뒤로 보내, 앞줄 몇 건이 나머지를 영영 막지 않는다.
SELECT o.id AS order_id, o.order_no, p.id AS payment_id, p.payment_key
FROM orders o
JOIN payments p ON p.order_id = o.id AND p.kind = '주문결제' AND p.status = '대기'
CROSS JOIN LATERAL (SELECT p.raw_response #>> '{virtualAccount,dueDate}' AS d) v
WHERE o.status = sqlc.arg('status')
  AND CASE
        WHEN v.d ~ '^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:?\d{2})$' THEN v.d::timestamptz
        WHEN v.d ~ '^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?$' THEN v.d::timestamp AT TIME ZONE 'Asia/Seoul'
      END < sqlc.arg('before')::timestamptz
ORDER BY p.updated_at LIMIT sqlc.arg('limit')::int;

-- name: TouchPayment :exec
UPDATE payments SET updated_at = now() WHERE id = $1;

-- name: StrandedDepositOrders :many
-- 입금대기인데 살아 있는 결제(대기·승인)가 없는 주문 — 이 개정 전의 A-508 이 결제만 '실패' 로
-- 내린 것. 어느 만료도 집지 않아 재고를 잡고 있다.
SELECT o.id FROM orders o
WHERE o.status = sqlc.arg('status')
  AND NOT EXISTS (SELECT 1 FROM payments p
                  WHERE p.order_id = o.id AND p.kind = '주문결제' AND p.status IN ('대기', '승인'))
ORDER BY o.created_at LIMIT sqlc.arg('limit')::int;

-- name: HasLiveOrderPayment :one
SELECT EXISTS (SELECT 1 FROM payments
               WHERE order_id = $1 AND kind = '주문결제' AND status IN ('대기', '승인'));

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

-- name: LastOrderAddress :one
-- 회원 주문서(P-405)의 시작 값 — 지난 주문의 배송지·연락처. 배송 메모는 그 주문의 것이라 뺀다.
SELECT receiver_name, receiver_phone, postcode, address1, address2, orderer_phone
FROM orders WHERE user_id = $1
ORDER BY created_at DESC, id LIMIT 1;
