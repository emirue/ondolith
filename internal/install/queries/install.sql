-- name: CreateFirstAdmin :execrows
WITH created AS (
    INSERT INTO users (email, password_hash, display_name)
    VALUES ($1, $2, $3)
    ON CONFLICT (email) DO NOTHING
    RETURNING id
)
INSERT INTO user_roles (user_id, role_id)
SELECT created.id, r.id FROM created, roles r WHERE r.key = 'admin';
