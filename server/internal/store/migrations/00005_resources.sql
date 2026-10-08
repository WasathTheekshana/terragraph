-- +goose Up
-- What a project creates and looks up, as of the scan that is its current state.
-- A scan replaces all of it, so a resource removed from the code disappears.
CREATE TABLE project_inventory (
    project_id bigint PRIMARY KEY REFERENCES projects (id) ON DELETE CASCADE,
    -- Where the project keeps its state, so a terraform_remote_state data source
    -- elsewhere can be matched to it.
    backend    text NOT NULL DEFAULT '',
    state_key  text NOT NULL DEFAULT '',
    variables  jsonb NOT NULL DEFAULT '[]',
    outputs    jsonb NOT NULL DEFAULT '[]',
    scan_id    bigint NOT NULL REFERENCES scans (id) ON DELETE CASCADE
);

CREATE INDEX project_inventory_state_key_idx ON project_inventory (state_key) WHERE state_key <> '';

CREATE TABLE resources (
    project_id      bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    -- The full address without an instance index, "module.net.azurerm_virtual_network.main".
    address         text NOT NULL,
    mode            text NOT NULL CHECK (mode IN ('managed', 'data')),
    resource_type   text NOT NULL,
    name            text NOT NULL,
    module_path     text NOT NULL DEFAULT '',
    -- Whether a count or for_each leaves any copy of it: true, false, or unknown
    -- when that depends on something only known when Terraform runs.
    enabled         text NOT NULL CHECK (enabled IN ('true', 'false', 'unknown')),
    instances       integer,
    condition       text NOT NULL DEFAULT '',
    file            text NOT NULL DEFAULT '',
    line            integer NOT NULL DEFAULT 0,
    attrs           jsonb NOT NULL DEFAULT '{}',
    unresolved      jsonb NOT NULL DEFAULT '{}',
    tags            jsonb NOT NULL DEFAULT '{}',
    tags_unresolved jsonb NOT NULL DEFAULT '{}',
    scan_id         bigint NOT NULL REFERENCES scans (id) ON DELETE CASCADE,
    PRIMARY KEY (project_id, address)
);

CREATE INDEX resources_type_idx ON resources (resource_type, mode);
CREATE INDEX resources_tags_idx ON resources USING gin (tags);

-- +goose Down
DROP TABLE resources;
DROP TABLE project_inventory;
