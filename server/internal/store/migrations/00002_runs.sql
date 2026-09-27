-- +goose Up
-- A repo can hold several Terraform roots; each is its own project.
ALTER TABLE projects ADD COLUMN path text NOT NULL DEFAULT '.';
ALTER TABLE projects DROP CONSTRAINT projects_repo_key_key;
ALTER TABLE projects ADD CONSTRAINT projects_repo_key_path_key UNIQUE (repo_key, path);

-- A run is one scanner invocation: the items it planned and how each went.
CREATE TABLE runs (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    label           text NOT NULL,
    status          text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'finished', 'cancelled')),
    idempotency_key text UNIQUE,
    created_at      timestamptz NOT NULL DEFAULT now(),
    -- Bumped on every item update, so an abandoned run can be told apart.
    updated_at      timestamptz NOT NULL DEFAULT now(),
    finished_at     timestamptz
);

CREATE INDEX runs_created_at_idx ON runs (created_at DESC);

CREATE TABLE run_items (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_id     bigint NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    kind       text NOT NULL CHECK (kind IN ('project', 'module_repo')),
    repo_url   text NOT NULL,
    path       text NOT NULL DEFAULT '',
    status     text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'done', 'failed')),
    error      text NOT NULL DEFAULT '',
    scan_id    bigint REFERENCES scans (id) ON DELETE SET NULL,
    applied    boolean,
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (run_id, kind, repo_url, path)
);

-- +goose Down
DROP TABLE run_items;
DROP TABLE runs;
ALTER TABLE projects DROP CONSTRAINT projects_repo_key_path_key;
ALTER TABLE projects ADD CONSTRAINT projects_repo_key_key UNIQUE (repo_key);
ALTER TABLE projects DROP COLUMN path;
