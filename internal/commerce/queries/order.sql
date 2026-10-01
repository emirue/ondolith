-- name: OpenOrders :one
SELECT count(*) FROM orders WHERE status <> ALL(sqlc.arg('terminal')::text[]);
