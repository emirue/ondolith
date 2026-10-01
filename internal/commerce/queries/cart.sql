-- 장바구니 (cart.go). 소유권 술어는 WHERE 에 있다 (SC-3) — 세 문장에 같은 조각이 그대로 적혀 있다.

-- name: CartIDForUser :one
INSERT INTO carts (user_id) VALUES ($1)
ON CONFLICT (user_id) WHERE user_id IS NOT NULL
DO UPDATE SET updated_at = now() RETURNING id;

-- name: CartIDForGuest :one
INSERT INTO carts (guest_key) VALUES ($1)
ON CONFLICT (guest_key) WHERE guest_key IS NOT NULL
DO UPDATE SET updated_at = now() RETURNING id;

-- name: CartItemQuantity :one
SELECT quantity FROM cart_items WHERE cart_id = $1 AND variant_id = $2;

-- name: UpsertCartItem :exec
INSERT INTO cart_items (cart_id, variant_id, quantity) VALUES ($1, $2, $3)
ON CONFLICT (cart_id, variant_id)
DO UPDATE SET quantity = $3, updated_at = now();

-- name: CartItems :many
SELECT ci.id, ci.variant_id, p.id AS product_id, p.slug, p.name, v.option_values,
       (p.base_price + v.price_delta)::int AS unit_price, ci.quantity, v.stock,
       (p.is_visible AND v.is_visible AND v.stock >= ci.quantity)::boolean AS sellable
FROM cart_items ci
JOIN carts c            ON c.id = ci.cart_id
JOIN product_variants v ON v.id = ci.variant_id
JOIN products p         ON p.id = v.product_id
WHERE (sqlc.narg('user_id')::uuid IS NOT NULL AND c.user_id = sqlc.narg('user_id'))
   OR (sqlc.narg('guest_key')::text IS NOT NULL AND c.guest_key = sqlc.narg('guest_key'))
ORDER BY ci.created_at, ci.id;

-- name: DeleteCartItem :execrows
DELETE FROM cart_items ci WHERE ci.id = sqlc.arg('id') AND
    ci.cart_id IN (SELECT id FROM carts
                   WHERE (sqlc.narg('user_id')::uuid IS NOT NULL AND user_id = sqlc.narg('user_id'))
                      OR (sqlc.narg('guest_key')::text IS NOT NULL AND guest_key = sqlc.narg('guest_key')));

-- name: CartItemVariant :one
SELECT ci.variant_id FROM cart_items ci WHERE ci.id = sqlc.arg('id') AND
    ci.cart_id IN (SELECT id FROM carts
                   WHERE (sqlc.narg('user_id')::uuid IS NOT NULL AND user_id = sqlc.narg('user_id'))
                      OR (sqlc.narg('guest_key')::text IS NOT NULL AND guest_key = sqlc.narg('guest_key')));

-- name: SetCartItemQuantity :execrows
UPDATE cart_items ci SET quantity = sqlc.arg('quantity'), updated_at = now()
WHERE ci.id = sqlc.arg('id') AND
    ci.cart_id IN (SELECT id FROM carts
                   WHERE (sqlc.narg('user_id')::uuid IS NOT NULL AND user_id = sqlc.narg('user_id'))
                      OR (sqlc.narg('guest_key')::text IS NOT NULL AND guest_key = sqlc.narg('guest_key')));

-- name: GuestCartID :one
SELECT id FROM carts WHERE guest_key = $1;

-- name: MergePlan :many
SELECT gi.variant_id, gi.quantity AS guest_quantity, COALESCE(mi.quantity, 0)::int AS member_quantity, v.stock
FROM cart_items gi
JOIN product_variants v ON v.id = gi.variant_id
LEFT JOIN cart_items mi ON mi.cart_id = sqlc.arg('member_cart') AND mi.variant_id = gi.variant_id
WHERE gi.cart_id = sqlc.arg('guest_cart');

-- name: DeleteCart :exec
DELETE FROM carts WHERE id = $1;
