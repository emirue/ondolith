-- +goose Up

-- FR-507. The tsvector column ships with posts (00007); this is the index that
-- makes querying it a lookup instead of a scan.
--
-- It is a separate migration because an index on a populated table is the part
-- that takes time, and separating it lets an operator watch one statement
-- rather than wonder which half of a mixed migration is running.
CREATE INDEX posts_search_idx ON posts USING GIN (search_vector);

-- No index on custom_fields. FR-509 wants ordering by a custom field, and GIN
-- answers containment only — it cannot order. Per-field B-tree expressions
-- would be DDL per board, which DEC-3.9 rules out.

-- +goose Down

DROP INDEX posts_search_idx;
