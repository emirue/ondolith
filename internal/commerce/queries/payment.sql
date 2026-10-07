-- 결제 (payment.go, FR-607/FR-608). 선점은 부분 유니크가, 전이는 비교-교환이 막는다.

-- name: LockOrderForPayment :one
SELECT id, status, total_amount, created_at FROM orders
WHERE order_no = $1 FOR UPDATE;

-- name: ReservePayment :one
INSERT INTO payments (order_id, kind, status, pg, payment_key, approved_amount)
VALUES ($1, '주문결제', '대기', $2, $3, $4) RETURNING id;

-- name: FailPaymentByID :exec
UPDATE payments SET status = '실패', updated_at = now() WHERE id = $1;

-- name: DeclinePayment :exec
UPDATE payments SET status = '실패', raw_response = $2, updated_at = now()
WHERE id = $1;

-- name: RecordPaymentResult :exec
UPDATE payments SET status = sqlc.arg('status'),
       approved_at = CASE WHEN sqlc.arg('status') = '승인' THEN now() ELSE approved_at END,
       raw_response = sqlc.arg('raw_response'), secret = NULLIF(sqlc.arg('secret')::text, ''), updated_at = now()
WHERE id = sqlc.arg('id');

-- **결제대기 주문의 것만이다.** 가상계좌를 발급받은 주문(입금대기)의 '대기' 결제는
-- 입금을 기다리는 살아 있는 결제다 — 그것을 실패로 내리면 뒤에 오는 입금 웹훅이
-- 대조할 결제를 찾지 못한다.
-- name: FailPendingPayment :execrows
UPDATE payments SET status = '실패', updated_at = now()
WHERE order_id = (SELECT id FROM orders WHERE order_no = $1 AND status = '결제대기')
  AND kind = '주문결제' AND status = '대기';

-- A-508 「PG 결과로 반영」. 결제 행을 잠그고 다시 읽는다 — 조회하는 사이 웹훅이나
-- 다른 관리자가 같은 행을 닫았을 수 있다. 주문은 moveOrder 의 CAS 가 지킨다.
-- name: PaymentForResolve :one
SELECT p.status, p.kind, p.payment_key, o.id AS order_id, o.order_no,
       o.status AS order_status, o.total_amount
FROM payments p JOIN orders o ON o.id = p.order_id
WHERE p.id = $1 FOR UPDATE OF p;
