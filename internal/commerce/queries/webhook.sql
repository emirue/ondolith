-- 웹훅·대사 (webhook.go, FR-610, A-508/A-603). 멱등은 유니크 인덱스가 판단한다.

-- name: RecordWebhook :one
INSERT INTO webhook_events (pg, event_id, order_id, status, payload)
VALUES ($1, $2, (SELECT id FROM orders WHERE order_no = $3), '수신', $4)
RETURNING id;

-- name: FinishWebhook :exec
UPDATE webhook_events SET status = sqlc.arg('status'), error = NULLIF(sqlc.arg('error')::text, ''), updated_at = now()
WHERE id = sqlc.arg('id');

-- name: OrderForWebhook :one
SELECT id, status, total_amount FROM orders WHERE order_no = $1;

-- name: LivePayment :one
SELECT id, payment_key, status, COALESCE(secret, '')::text AS secret FROM payments
WHERE order_id = $1 AND kind = '주문결제' AND status <> '실패';

-- name: ApprovePendingPayment :execrows
UPDATE payments SET status = '승인', approved_at = now(), raw_response = sqlc.arg('raw_response'),
       updated_at = now()
WHERE id = sqlc.arg('id') AND status = '대기';

-- name: WebhookHistory :many
SELECT w.id, w.pg, w.event_id, COALESCE(o.order_no, '')::text AS order_no, w.status,
       w.payload::text AS payload, COALESCE(w.error, '')::text AS error, w.created_at
FROM webhook_events w LEFT JOIN orders o ON o.id = w.order_id
-- 기간 조회 (D19 0.6): 수신 시각 기준.
WHERE (sqlc.narg('since')::timestamptz IS NULL OR w.created_at >= sqlc.narg('since'))
  AND (sqlc.narg('until')::timestamptz IS NULL OR w.created_at < sqlc.narg('until'))
ORDER BY (w.status = '수신') DESC, w.created_at DESC, w.id
LIMIT sqlc.arg('limit')::int OFFSET sqlc.arg('offset')::int;

-- name: PaymentsToReconcile :many
SELECT p.id, p.payment_key, o.order_no, p.pg, p.kind, p.status,
       p.approved_amount, p.refunded_amount, p.created_at
FROM payments p JOIN orders o ON o.id = p.order_id
WHERE p.created_at >= sqlc.arg('since') AND p.created_at < sqlc.arg('until')
ORDER BY (p.status = '대기') DESC, p.created_at DESC
LIMIT 500;
