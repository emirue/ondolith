-- +goose Up

-- One set of tables for every board (DEC-3.9). A board is a row, its custom
-- field schema is rows in board_fields, and a post's custom values are one
-- JSONB column — never a CREATE TABLE per board, which would put schema
-- changes on the request path and make every migration board-dependent.

CREATE TABLE boards (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    slug              text        NOT NULL UNIQUE
                                  CHECK (slug ~ '^[a-z0-9][a-z0-9-]*$' AND length(slug) <= 64),
    name              text        NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    -- skin names a template file, so it is bounded like slug and may not
    -- escape a directory: the loader checks too, but a value that cannot be
    -- stored cannot be reached at all.
    skin              text        NOT NULL DEFAULT ''
                                  CHECK (skin = '' OR (skin ~ '^[a-z0-9][a-z0-9-]*$' AND length(skin) <= 64)),
    allow_attachments boolean     NOT NULL DEFAULT false,
    allow_comments    boolean     NOT NULL DEFAULT true,
    allow_secret      boolean     NOT NULL DEFAULT false,
    per_page          integer     NOT NULL DEFAULT 20 CHECK (per_page BETWEEN 1 AND 100),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

-- No permission column here. Scope is expressed by role_permissions.board_id
-- (D15 2.4) — a second place to say who may read a board is a second place to
-- forget, and the two would disagree silently.

-- role_permissions.board_id waited for this table: D30 §3 requires an FK to
-- name REFERENCES, and Phase 1 had nothing to point at.
ALTER TABLE role_permissions
    ADD COLUMN board_id uuid REFERENCES boards(id) ON DELETE CASCADE;

-- Uniqueness now has to include the scope, or the same permission cannot be
-- granted on two different boards.
--
-- NULLS NOT DISTINCT is the point: a global grant stores NULL in board_id, and
-- under the default rule two NULLs never collide — the same global grant could
-- be inserted a hundred times and the constraint would allow every one.
ALTER TABLE role_permissions DROP CONSTRAINT role_permissions_uniq;
ALTER TABLE role_permissions
    ADD CONSTRAINT role_permissions_uniq
    UNIQUE NULLS NOT DISTINCT (role_id, permission_id, board_id);

CREATE INDEX role_permissions_board_id_idx ON role_permissions (board_id)
    WHERE board_id IS NOT NULL;

CREATE TABLE board_fields (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    board_id     uuid        NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
    key          text        NOT NULL
                             CHECK (key ~ '^[a-z][a-z0-9_]*$' AND length(key) <= 32),
    label        text        NOT NULL CHECK (length(label) BETWEEN 1 AND 100),
    -- text, not a PG enum: ALTER TYPE ... ADD VALUE cannot be undone, which
    -- collides with NFR-303's requirement that every migration has a Down.
    -- A CHECK drops and re-adds cleanly.
    field_type   text        NOT NULL CHECK (field_type IN
                             ('text','textarea','number','select','checkbox','multiselect','date','url')),
    is_required  boolean     NOT NULL DEFAULT false,
    show_in_list boolean     NOT NULL DEFAULT false,
    options      jsonb       NOT NULL DEFAULT '[]',
    sort_order   integer     NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT board_fields_key_uniq UNIQUE (board_id, key),
    CONSTRAINT board_fields_options_shape
        CHECK (jsonb_typeof(options) = 'array' AND octet_length(options::text) <= 4096),
    -- A select with no choices is an unusable field; choices on a checkbox are
    -- values nothing reads. Both are configuration mistakes that only show up
    -- to a visitor, so the database refuses them.
    CONSTRAINT board_fields_options_when
        CHECK ((field_type IN ('select','multiselect')) = (options <> '[]'::jsonb))
);

-- UNIQUE (board_id, key) leads with board_id, so it already satisfies the
-- FK-index rule; no separate index on board_id.

-- Reserved keys (id, title, body, author_id, board_id) are NOT blocked here.
-- The list would grow with every column added, and the handler refuses them
-- with a 422 that says which key and why (A-306).

CREATE TABLE posts (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    board_id      uuid        NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
    -- NULL is the SET NULL landing spot: the post stays, the author goes.
    author_id     uuid        REFERENCES users(id) ON DELETE SET NULL,
    title         text        NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    body          text        NOT NULL CHECK (length(body) <= 50000),
    custom_fields jsonb       NOT NULL DEFAULT '{}',
    status        text        NOT NULL DEFAULT 'published'
                              CHECK (status IN ('published','hidden')),
    is_pinned     boolean     NOT NULL DEFAULT false,
    is_secret     boolean     NOT NULL DEFAULT false,
    view_count    integer     NOT NULL DEFAULT 0 CHECK (view_count >= 0),
    -- Two-argument to_tsvector: the one-argument form is not IMMUTABLE and a
    -- generated column requires immutability. 'simple' rather than 'english'
    -- because stock PostgreSQL has no Korean dictionary and english mangles it
    -- — prefix queries ('게시판:*') cover the particle problem instead.
    search_vector tsvector    GENERATED ALWAYS AS
                              (to_tsvector('simple'::regconfig, title || ' ' || body)) STORED,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT posts_custom_fields_shape
        CHECK (jsonb_typeof(custom_fields) = 'object'
               AND octet_length(custom_fields::text) <= 16384)
);

-- The trailing id is the keyset tiebreaker FR-508 needs. Measured on 20,000
-- rows: LIMIT 20 OFFSET 19000 fell to a Seq Scan plus a 19,020-row sort even
-- with this index, while (is_pinned, created_at, id) < (...) was an Index Only
-- Scan touching 20 rows. All three directions must match for the row
-- comparison to reach the index.
CREATE INDEX posts_board_list_idx ON posts (board_id, is_pinned DESC, created_at DESC, id DESC);
CREATE INDEX posts_author_id_idx  ON posts (author_id);

-- No deleted_at. Two ways to be invisible (status='hidden' and deleted_at)
-- means every query has to remember both, and the first one that forgets
-- resurrects a deleted post.

-- No GIN on custom_fields. FR-509 asks for sorting by a custom field, and GIN
-- supports containment only — it provides no ordering. Per-field expression
-- indexes would be per-board DDL, which DEC-3.9 rules out.

CREATE TABLE comments (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    post_id    uuid        NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    -- NO ACTION, not RESTRICT: RESTRICT cannot be deferred, so deleting a post
    -- whose comments are removed in the same statement would be refused on
    -- ordering alone. NO ACTION lets that pass while still refusing to delete
    -- a comment that has replies — which is what produces the tombstone rule.
    parent_id  uuid        REFERENCES comments(id) ON DELETE NO ACTION,
    author_id  uuid        REFERENCES users(id) ON DELETE SET NULL,
    body       text        NOT NULL CHECK (length(body) <= 2000),
    -- Tombstone marker. The body is emptied in the database rather than hidden
    -- in code: themes are third-party and written by people who will forget the
    -- `if`.
    deleted_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT comments_no_self_parent CHECK (parent_id IS NULL OR parent_id <> id),
    CONSTRAINT comments_tombstone_is_empty CHECK (deleted_at IS NULL OR body = '')
);

CREATE INDEX comments_post_id_idx   ON comments (post_id, created_at);
CREATE INDEX comments_parent_id_idx ON comments (parent_id) WHERE parent_id IS NOT NULL;
CREATE INDEX comments_author_id_idx ON comments (author_id);

CREATE TABLE attachments (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    post_id       uuid        NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    -- Relative YYYY/MM/<uuid> with no extension. Absolute paths die the moment
    -- an operator moves the upload directory; dropping the extension carries
    -- D60 §3's filename regeneration all the way — if the directory is ever
    -- served by accident, no executable name exists on disk to serve.
    -- The regex refuses `../` at the database, not only in the handler.
    stored_path   text        NOT NULL UNIQUE
                              CHECK (stored_path ~ '^[0-9]{4}/[0-9]{2}/[0-9a-f-]{36}$'
                                     AND length(stored_path) <= 128),
    original_name text        NOT NULL CHECK (length(original_name) BETWEEN 1 AND 255),
    mime_type     text        NOT NULL CHECK (length(mime_type) BETWEEN 1 AND 128),
    byte_size     bigint      NOT NULL CHECK (byte_size > 0),
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX attachments_post_id_idx ON attachments (post_id);

-- stored_path UNIQUE: two rows pointing at one file means deleting either one
-- silently removes the other's bytes. A-309 decided a failed disk delete is not
-- rolled back, so the opposite direction has to be impossible.

-- No updated_at on attachments (D30 §3 exception): they are created and
-- deleted, and no screen edits one.

-- +goose Down

DROP TABLE attachments;
DROP TABLE comments;
DROP TABLE posts;
DROP TABLE board_fields;

-- role_permissions goes back to two columns. Board-scoped grants are deleted
-- first: they cannot mean anything once boards is gone, and leaving them would
-- collapse into duplicate global grants that the restored constraint refuses.
DELETE FROM role_permissions WHERE board_id IS NOT NULL;
ALTER TABLE role_permissions DROP CONSTRAINT role_permissions_uniq;
ALTER TABLE role_permissions DROP COLUMN board_id;
ALTER TABLE role_permissions
    ADD CONSTRAINT role_permissions_uniq UNIQUE (role_id, permission_id);

DROP TABLE boards;
