-- name: UserFields :many
SELECT key, label, field_type, is_required, show_in_list, options, sort_order
FROM user_fields ORDER BY sort_order, key;

-- name: SaveUserField :exec
INSERT INTO user_fields (key, label, field_type, is_required, show_in_list, options, sort_order)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (key) DO UPDATE SET
    label = EXCLUDED.label, field_type = EXCLUDED.field_type,
    is_required = EXCLUDED.is_required, show_in_list = EXCLUDED.show_in_list,
    options = EXCLUDED.options, sort_order = EXCLUDED.sort_order, updated_at = now();

-- name: DeleteUserField :execrows
DELETE FROM user_fields WHERE key = $1;
