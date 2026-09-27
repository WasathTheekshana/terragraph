-- +goose Up
CREATE TABLE projects (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    repo_key        text NOT NULL UNIQUE,
    repo_url        text NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    -- Set by the scan whose module calls are the project's current state.
    last_scan_at    timestamptz,
    last_commit_sha text NOT NULL DEFAULT '',
    last_branch     text NOT NULL DEFAULT ''
);

CREATE TABLE modules (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_key          text NOT NULL UNIQUE,
    kind                text NOT NULL CHECK (kind IN ('git', 'registry', 'other')),
    source              text NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    versions_scanned_at timestamptz
);

CREATE TABLE module_versions (
    module_id  bigint NOT NULL REFERENCES modules (id) ON DELETE CASCADE,
    tag        text NOT NULL,
    commit_sha text NOT NULL,
    -- NULL when the tag isn't an exact semver version.
    major      integer,
    minor      integer,
    patch      integer,
    prerelease text NOT NULL DEFAULT '',
    PRIMARY KEY (module_id, tag),
    CHECK (num_nulls(major, minor, patch) IN (0, 3))
);

CREATE TABLE scans (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    scanner_type text NOT NULL,
    project_id   bigint REFERENCES projects (id) ON DELETE CASCADE,
    module_id    bigint REFERENCES modules (id) ON DELETE CASCADE,
    commit_sha   text NOT NULL DEFAULT '',
    branch       text NOT NULL DEFAULT '',
    generated_at timestamptz NOT NULL,
    received_at  timestamptz NOT NULL DEFAULT now(),
    applied      boolean NOT NULL,
    report       jsonb NOT NULL,
    CHECK (num_nonnulls(project_id, module_id) = 1)
);

CREATE INDEX scans_project_id_idx ON scans (project_id, generated_at DESC);
CREATE INDEX scans_module_id_idx ON scans (module_id, generated_at DESC);

CREATE TABLE module_usages (
    project_id        bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    call_name         text NOT NULL,
    -- NULL for local modules.
    module_id         bigint REFERENCES modules (id) ON DELETE SET NULL,
    source            text NOT NULL,
    ref_declared      text NOT NULL DEFAULT '',
    ref_resolved      text NOT NULL DEFAULT '',
    version_resolved  text NOT NULL DEFAULT '',
    resolution_source text NOT NULL,
    file              text NOT NULL DEFAULT '',
    line              integer NOT NULL DEFAULT 0,
    -- The exact version pinned, when one is known.
    major             integer,
    minor             integer,
    patch             integer,
    prerelease        text NOT NULL DEFAULT '',
    scan_id           bigint NOT NULL REFERENCES scans (id) ON DELETE CASCADE,
    PRIMARY KEY (project_id, call_name),
    CHECK (num_nulls(major, minor, patch) IN (0, 3))
);

CREATE INDEX module_usages_module_id_idx ON module_usages (module_id);

-- Highest stable release per module.
CREATE VIEW module_latest_versions AS
SELECT DISTINCT ON (module_id) module_id, tag, major, minor, patch
FROM module_versions
WHERE major IS NOT NULL AND prerelease = ''
ORDER BY module_id, major DESC, minor DESC, patch DESC;

-- +goose Down
DROP VIEW module_latest_versions;
DROP TABLE module_usages;
DROP TABLE scans;
DROP TABLE module_versions;
DROP TABLE modules;
DROP TABLE projects;
