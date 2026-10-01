-- 약관 (terms.go, FR-619). UPDATE 가 없다 — 개정은 새 행이다.

-- name: ListTerms :many
SELECT t.id, t.kind, t.version, t.body, t.effective_at, t.is_required, t.created_at,
       EXISTS (SELECT 1 FROM order_agreements a WHERE a.terms_id = t.id)::boolean AS in_use
FROM terms t ORDER BY t.kind, t.effective_at DESC;

-- name: AddTerms :one
INSERT INTO terms (kind, version, body, effective_at, is_required)
VALUES (sqlc.arg('kind'), sqlc.arg('version'), sqlc.arg('body'), GREATEST(sqlc.arg('effective_at')::timestamptz, now()), sqlc.arg('is_required')) RETURNING id;

-- name: RequiredTerms :many
SELECT DISTINCT ON (kind) id, kind, version, body, effective_at, is_required, created_at
FROM terms
WHERE is_required AND effective_at <= sqlc.arg('now')
ORDER BY kind, effective_at DESC;

-- name: TermsByID :one
SELECT id, kind, version, body, effective_at, is_required, created_at
FROM terms WHERE id = $1;
