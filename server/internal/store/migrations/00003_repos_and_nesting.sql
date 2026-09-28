-- +goose Up
-- A repository holds one or more projects (Terraform roots).
CREATE TABLE repos (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    repo_key   text NOT NULL UNIQUE,
    repo_url   text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO repos (repo_key, repo_url)
SELECT DISTINCT ON (repo_key) repo_key, repo_url
FROM projects
ORDER BY repo_key, last_scan_at DESC NULLS LAST;

ALTER TABLE projects ADD COLUMN repo_id bigint REFERENCES repos (id) ON DELETE CASCADE;
UPDATE projects p SET repo_id = r.id FROM repos r WHERE r.repo_key = p.repo_key;
ALTER TABLE projects ALTER COLUMN repo_id SET NOT NULL;
CREATE INDEX projects_repo_id_idx ON projects (repo_id);

-- A module call inside another module is keyed by the dotted path of the
-- calls leading to it ("addons" for a call inside module "addons").
ALTER TABLE module_usages ADD COLUMN parent text NOT NULL DEFAULT '';
ALTER TABLE module_usages DROP CONSTRAINT module_usages_pkey;
ALTER TABLE module_usages ADD PRIMARY KEY (project_id, parent, call_name);

-- +goose Down
DELETE FROM module_usages WHERE parent <> '';
ALTER TABLE module_usages DROP CONSTRAINT module_usages_pkey;
ALTER TABLE module_usages ADD PRIMARY KEY (project_id, call_name);
ALTER TABLE module_usages DROP COLUMN parent;
ALTER TABLE projects DROP COLUMN repo_id;
DROP TABLE repos;
