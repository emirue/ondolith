-- +goose Up

-- Phase 1 additions to the Phase 0 table.
--
-- is_admin is NOT dropped here. D30's two-release rule requires release N to
-- add the replacement and write both, and release N+1 to drop the old column
-- (00006, W2-01). Dropping it now would leave no downgrade path (NFR-308).
--
-- sessions_valid_from is NOT NULL with a default on purpose: a nullable cutoff
-- makes "no cutoff" a NULL, and every comparison against it becomes fail-open.
ALTER TABLE users
    ADD COLUMN is_active           boolean     NOT NULL DEFAULT true,
    ADD COLUMN sessions_valid_from timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN email_verified_at   timestamptz;

-- key has no dot: the dot is the <resource>.<action> separator for permission
-- keys (D15 2.1), and allowing it here makes the two kinds indistinguishable
-- on A-403/A-404, which show both.
CREATE TABLE roles (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    key           text        NOT NULL UNIQUE CHECK (key ~ '^[a-z][a-z0-9_]*$'),
    name          text        NOT NULL,
    description   text        NOT NULL DEFAULT '',
    is_builtin    boolean     NOT NULL DEFAULT false,
    is_superuser  boolean     NOT NULL DEFAULT false,
    -- anonymous and member are granted implicitly, so no user_roles row may
    -- ever point at them. This column is the only thing the database can use
    -- to enforce that.
    is_assignable boolean     NOT NULL DEFAULT true,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- Exactly one superuser role, enforced by the database rather than by a
-- handler that someone will later bypass.
CREATE UNIQUE INDEX roles_one_superuser_idx ON roles ((is_superuser)) WHERE is_superuser;

CREATE TABLE permissions (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    key         text        NOT NULL UNIQUE CHECK (key ~ '^[a-z][a-z0-9]*\.[a-z][a-z0-9_]*$'),
    description text        NOT NULL,
    -- Every scoped permission is Phase 2, but the column ships now: the grant
    -- handler and the seed must not have a different shape per phase (D15 2.4).
    is_scoped   boolean     NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- board_id is absent until Phase 2: D30 §3 requires FKs to name REFERENCES,
-- and `boards` does not exist yet. Phase 2 adds the column and replaces
-- role_permissions_uniq with a NULLS NOT DISTINCT triple.
CREATE TABLE role_permissions (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    role_id       uuid        NOT NULL REFERENCES roles(id)       ON DELETE CASCADE,
    permission_id uuid        NOT NULL REFERENCES permissions(id) ON DELETE RESTRICT,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT role_permissions_uniq UNIQUE (role_id, permission_id)
);

CREATE TABLE user_roles (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id    uuid        NOT NULL REFERENCES roles(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT user_roles_uniq UNIQUE (user_id, role_id)
);

-- Not for speed: A-403 refuses to delete a role that still has holders, and
-- this index is what that count reads.
CREATE INDEX user_roles_role_id_idx ON user_roles (role_id);

-- +goose Down
DROP INDEX user_roles_role_id_idx;
DROP TABLE user_roles;
DROP TABLE role_permissions;
DROP TABLE permissions;
-- roles_one_superuser_idx belongs to roles and goes with it.
DROP TABLE roles;
ALTER TABLE users
    DROP COLUMN email_verified_at,
    DROP COLUMN sessions_valid_from,
    DROP COLUMN is_active;
