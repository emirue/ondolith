-- 환불·취소 (refund.go, FR-611/FR-625). 한도는 DB CHECK 가, 중복은 request_key 유니크가 막는다.

-- name: ApprovedPaymentForOrderNo :one
SELECT o.id AS order_id, p.id AS payment_id FROM orders o
JOIN payments p ON p.order_id = o.id AND p.kind = '주문결제' AND p.status = '승인'
WHERE o.order_no = $1;

-- name: LockOrderItemForRefund :one
SELECT line_amount::int AS line_amount, discount_amount, quantity, settled_quantity
FROM order_items WHERE id = $1 AND order_id = $2 FOR UPDATE;

-- name: OpenReturnItemCount :one
SELECT count(*) FROM return_items WHERE order_item_id = $1 AND is_open;

-- name: SettleOrderItem :exec
UPDATE order_items SET settled_quantity = settled_quantity + sqlc.arg('quantity'), updated_at = now()
WHERE id = sqlc.arg('id');

-- name: ReserveRefundAmount :exec
UPDATE payments SET refunded_amount = refunded_amount + sqlc.arg('amount'), updated_at = now()
WHERE id = sqlc.arg('id');

-- name: InsertRefund :one
INSERT INTO refunds (order_id, payment_id, status, requester, amount, reason, request_key)
VALUES ($1, $2, '요청', $3, $4, $5, $6) RETURNING id;

-- name: InsertRefundItem :exec
INSERT INTO refund_items (refund_id, order_item_id, quantity) VALUES ($1,$2,$3);

-- name: RejectRefund :one
UPDATE refunds SET status = '거부', reason = $2, updated_at = now()
WHERE id = $1 AND status = '요청' RETURNING order_id, payment_id, amount;

-- name: ReleaseRefundAmount :exec
UPDATE payments SET refunded_amount = refunded_amount - sqlc.arg('amount'), updated_at = now()
WHERE id = sqlc.arg('id');

-- name: UnsettleRefundItems :exec
UPDATE order_items oi SET settled_quantity = oi.settled_quantity - ri.quantity,
                          updated_at = now()
FROM refund_items ri
WHERE ri.refund_id = $1 AND oi.id = ri.order_item_id;

-- name: Refunds :many
SELECT id, status, requester, amount, reason, created_at FROM refunds
WHERE order_id = $1 ORDER BY created_at DESC, id;

-- name: RefundedTotal :one
SELECT p.approved_amount, p.refunded_amount FROM payments p
JOIN orders o ON o.id = p.order_id
WHERE o.order_no = $1 AND p.kind = '주문결제' AND p.status = '승인';

-- name: LockOrderForCancel :one
SELECT id, status, total_amount FROM orders WHERE order_no = $1 FOR UPDATE;

-- 잠금 순서의 앞 절반이다: **order_items(id 오름차순) → payments.** 환불·취소·반품
-- 경로가 전부 이 순서로 잡아야 서로를 기다리지 않는다.
-- name: LockOrderItems :many
SELECT id FROM order_items WHERE order_id = $1 ORDER BY id FOR UPDATE;

-- name: LockApprovedPayment :one
SELECT id, approved_amount, refunded_amount FROM payments
WHERE order_id = $1 AND kind = '주문결제' AND status = '승인' FOR UPDATE;

-- name: SettleAllOrderItems :exec
UPDATE order_items SET settled_quantity = quantity, updated_at = now()
WHERE order_id = $1;

-- name: InsertCancelRefund :one
INSERT INTO refunds (order_id, payment_id, status, requester, amount, reason, request_key)
VALUES ($1, $2, '요청', $3, $4, '주문 취소', $5) RETURNING id;

-- name: OrderItemStock :many
SELECT variant_id, quantity FROM order_items WHERE order_id = $1;

-- name: ApprovedPaymentAmounts :one
SELECT approved_amount, refunded_amount FROM payments
WHERE order_id = $1 AND kind = '주문결제' AND status = '승인';

-- name: LockRefundForExecute :one
SELECT r.status, r.reason, r.request_key, r.amount, p.payment_key
FROM refunds r JOIN payments p ON p.id = r.payment_id
WHERE r.id = $1 FOR UPDATE;

-- name: MarkRefundExecuting :exec
UPDATE refunds SET status = '승인', updated_at = now() WHERE id = $1 AND status = '요청';

-- name: RevertRefundToRequested :exec
UPDATE refunds SET status = '요청', updated_at = now() WHERE id = $1 AND status = '승인';

-- name: CompleteRefund :execrows
UPDATE refunds SET status = '완료', pg_response = sqlc.narg('pg_response'), updated_at = now()
WHERE id = sqlc.arg('id') AND status = '승인';

-- name: RefundForReturn :one
SELECT rf.id FROM refunds rf
JOIN returns rt ON rt.id = rf.return_id
JOIN orders o ON o.id = rf.order_id
WHERE o.order_no = $1 AND rt.return_no = $2
ORDER BY rf.created_at DESC, rf.id DESC LIMIT 1;
