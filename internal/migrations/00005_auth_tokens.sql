-- +goose Up

-- token_hash is SHA-256 of a 32-byte crypto/rand value, NOT bcrypt.
--
-- Reaching for bcrypt here is the natural mistake — it is what "store a hash"
-- means everywhere else in this schema. But bcrypt salts every row, so the
-- hash cannot be looked up: P-105 would have to scan every token and run a
-- bcrypt compare against each. A 256-bit random value is not dictionary
-- attackable, so an unsalted SHA-256 is correct, and it is what makes
-- UNIQUE (token_hash) resolve the lookup in one index probe.
--
-- The two tables are not merged behind a `kind` column: the expiry policies
-- differ (30 minutes vs 24 hours), and merging pushes that constant into row
-- data, producing a branch on every read path.
CREATE TABLE password_reset_tokens (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash bytea       NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX password_reset_tokens_user_id_idx ON password_reset_tokens (user_id);

CREATE TABLE email_verification_tokens (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash bytea       NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX email_verification_tokens_user_id_idx ON email_verification_tokens (user_id);

-- Two uniques, each load-bearing for a different reason.
--
-- (provider, provider_uid) stops one social account from attaching to two of
-- ours. (user_id, provider) is what P-111's delete predicate assumes:
-- DELETE ... WHERE user_id = $session AND provider = $1 removes exactly one
-- row only because of this constraint. Without it, two accounts from the same
-- provider could be linked and a single unlink would drop both.
CREATE TABLE social_accounts (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider     text        NOT NULL,
    provider_uid text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT social_accounts_provider_uid_uniq UNIQUE (provider, provider_uid),
    CONSTRAINT social_accounts_user_provider_uniq UNIQUE (user_id, provider)
);

-- +goose Down
DROP TABLE social_accounts;
DROP INDEX email_verification_tokens_user_id_idx;
DROP TABLE email_verification_tokens;
DROP INDEX password_reset_tokens_user_id_idx;
DROP TABLE password_reset_tokens;
