-- +goose Up

-- Emails are stored lower-cased by the application so this unique index is
-- effectively case-insensitive without needing the citext extension.
CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email         text        NOT NULL UNIQUE,
    password_hash text        NOT NULL,
    display_name  text        NOT NULL DEFAULT '',
    is_admin      boolean     NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- ponytail: is_admin is a single boolean, not roles. Phase 1 replaces it with
-- the RBAC tables; designing them now would be guessing at requirements.

-- Schema dictated by github.com/alexedwards/scs/pgxstore.
CREATE TABLE sessions (
    token  text        PRIMARY KEY,
    data   bytea       NOT NULL,
    expiry timestamptz NOT NULL
);

CREATE INDEX sessions_expiry_idx ON sessions (expiry);

-- +goose Down
DROP INDEX sessions_expiry_idx;
DROP TABLE sessions;
DROP TABLE users;
