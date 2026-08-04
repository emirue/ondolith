-- +goose Up

-- UNIQUE (slug) is A-302's concurrency control, not just a data rule: checking
-- for a duplicate and then inserting lets two simultaneous requests both pass.
--
-- No author_id / published_at / deleted_at: no screen consumes them, and "who
-- changed this" is answered by operation_logs, which D15 §7 made mandatory.
CREATE TABLE pages (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    slug       text        NOT NULL UNIQUE,
    title      text        NOT NULL,
    body       text        NOT NULL,
    status     text        NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published')),
    template   text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- The primary key is `key`, deliberately breaking D30 §3's uuid rule: no
-- foreign key points here and every access is WHERE key = $1. A uuid would
-- give the row two identities and split the PK from the upsert's ON CONFLICT
-- target.
--
-- No `type` or `is_secret` column. Which keys are credentials is known by the
-- code — there is no generic settings editor, each of A-201/205/206/208/512
-- writes only its own keys. A column would make D15 §7's "never log credential
-- values" depend on data that a UI could flip.
CREATE TABLE settings (
    key        text        PRIMARY KEY,
    value      text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- One column holds the link target; there is no link_type discriminator.
-- Internal vs external is recovered in one line by testing for a leading "/",
-- and storing it would create a second source that can disagree.
--
-- The CHECK is a fail-closed backstop, not a copy of the handler's validation.
-- Besides javascript: and data: it rejects protocol-relative URLs (//evil.com),
-- which are the most common leak in code that only checks "starts with /".
CREATE TABLE menus (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    title      text        NOT NULL,
    url        text        NOT NULL CHECK (url ~ '^(/([^/\\].*)?|https?://[^\s]+)$'),
    parent_id  uuid        REFERENCES menus(id) ON DELETE CASCADE,
    sort_order integer     NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX menus_parent_sort_idx ON menus (parent_id, sort_order);

-- +goose Down
DROP INDEX menus_parent_sort_idx;
DROP TABLE menus;
DROP TABLE settings;
DROP TABLE pages;
