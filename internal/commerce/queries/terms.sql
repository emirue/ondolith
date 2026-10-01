-- 약관 (terms.go, FR-619). UPDATE 가 없다 — 개정은 새 행이다.

-- name: ListTerms :many
SELECT t.id, t.kind, t.version, t.body, t.effective_at, t.is_required, t.created_at,
       EXISTS (SELECT 1 FROM order_agreements a WHERE a.terms_id = t.id)::boolean AS in_use
FROM terms t ORDER BY t.kind, t.effective_at DESC, t.created_at DESC, t.id;

-- name: AddTerms :one
INSERT INTO terms (kind, version, body, effective_at, is_required)
VALUES (sqlc.arg('kind'), sqlc.arg('version'), sqlc.arg('body'), GREATEST(sqlc.arg('effective_at')::timestamptz, now()), sqlc.arg('is_required')) RETURNING id;

-- RequiredTermIDs (order.sql) 와 같은 판정이다: 시행본을 고른 뒤 필수만 남긴다.
-- name: RequiredTerms :many
SELECT id, kind, version, body, effective_at, is_required, created_at
FROM terms t
WHERE t.is_required AND t.id IN (
    SELECT DISTINCT ON (f.kind) f.id FROM terms f
    WHERE f.effective_at <= sqlc.arg('now')
    ORDER BY f.kind, f.effective_at DESC, f.created_at DESC, f.id)
ORDER BY t.kind;

-- name: TermsByID :one
SELECT id, kind, version, body, effective_at, is_required, created_at
FROM terms WHERE id = $1;
