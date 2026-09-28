-- +goose Up
-- People who have signed in through the identity provider.
CREATE TABLE users (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- issuer + "|" + the provider's subject, which never changes for a person.
    subject       text NOT NULL UNIQUE,
    email         text NOT NULL,
    name          text NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_login_at timestamptz NOT NULL DEFAULT now()
);

-- Only a SHA-256 of the session cookie is stored, so a database leak can't
-- be replayed as logins.
CREATE TABLE sessions (
    id_hash    bytea PRIMARY KEY,
    user_id    bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    is_admin   boolean NOT NULL,
    csrf_token text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);

CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

-- Sign-ins in progress: the OIDC state, PKCE verifier, and nonce.
CREATE TABLE login_attempts (
    state      text PRIMARY KEY,
    verifier   text NOT NULL,
    nonce      text NOT NULL,
    return_to  text NOT NULL,
    expires_at timestamptz NOT NULL
);

CREATE TABLE api_tokens (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name          text NOT NULL,
    token_hash    bytea NOT NULL UNIQUE,
    -- The first characters, to tell tokens apart without revealing them.
    prefix        text NOT NULL,
    can_read      boolean NOT NULL,
    can_ingest    boolean NOT NULL,
    -- Glob patterns on repo keys that ingest is limited to; empty means any.
    repo_patterns text[] NOT NULL DEFAULT '{}',
    created_by    bigint REFERENCES users (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_used_at  timestamptz,
    expires_at    timestamptz,
    revoked_at    timestamptz,
    CHECK (can_read OR can_ingest)
);

-- +goose Down
DROP TABLE api_tokens;
DROP TABLE login_attempts;
DROP TABLE sessions;
DROP TABLE users;
