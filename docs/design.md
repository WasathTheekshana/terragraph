# TerraGraph: Design Doc v0.1

## 1. Problem

At enterprise scale (100s of projects, 100s of custom Terraform modules, module-of-module
nesting, GitOps pipelines provisioning everything), nobody has a single place to answer:

- Which projects use module X, and at which version?
- Which projects are pinned to an old / pre-major version of a module?
- If we cut a new major version of module X, what's the blast radius?
- How has a given project's dependency graph drifted over time?

This doc defines a self-hosted platform (working name **TerraGraph**) modeled on the
SonarQube/Veracode pattern: a lightweight scanner runs inside each project's existing GitOps
pipeline, posts a report to a central service, and a web UI/API answers the questions above.
The ingest schema is deliberately generic so later scanners (cost, drift, security, resource
inventory) can plug into the same platform without a redesign.

## 2. Non-goals (v1)

- Not a Terraform runner/executor (not competing with Atlantis/Spacelift/Terrateam), it only
  *observes* what pipelines already do.
- Not a private module registry, modules keep living in their existing git repos.
- Not doing live drift detection against real infra in v1, static analysis of source +
  lockfile only.

## 3. Architecture

```
 ┌─────────────────┐      ┌─────────────────┐
 │  Project repo A  │      │  Project repo B  │   ... x100s
 │  (CI/GitOps job) │      │  (CI/GitOps job) │
 │   runs:          │      │   runs:          │
 │  terragraph scan │      │  terragraph scan │
 └────────┬─────────┘      └────────┬─────────┘
          │  POST /api/v1/scans (JSON report)
          ▼                          ▼
 ┌─────────────────────────────────────────────┐
 │              TerraGraph server               │
 │  - ingest API (validates + stores reports)    │
 │  - Postgres (projects, modules, edges,        │
 │    versions, scan history)                    │
 │  - query API (for the web UI)                 │
 └───────────────────┬───────────────────────────┘
                      │
                      ▼
            ┌───────────────────┐
            │     Web UI         │
            │ table + graph view │
            └───────────────────┘
```

Module repos (the 10, later 100s, of custom modules) are scanned the same way, a
`terragraph scan --mode module-repo` job on their own pipeline (or a scheduled job hitting
their git remotes) reports available tags/semver versions, so the server always knows
"latest" per module without projects needing to know it.

## 4. Core entities

- **Organization**: top-level tenant (single-org is fine for v1, but model it now so
  multi-tenant isn't a rewrite).
- **Project**: a Terraform root repo that consumes modules (the ~100s of "20 projects").
- **ModuleRepo**: a custom module's source repo (the ~10, growing to ~100s).
- **ModuleVersion**: a git tag/ref on a ModuleRepo, resolved to semver where possible.
- **Scan**: one report submitted by the CLI: `{project/module_repo, commit, branch, timestamp, scanner_type, facts}`.
- **ModuleUsage (edge)**: derived from the latest scan of a Project: `(project, module_repo, pinned_ref, resolved_version, source_type)`.

Scans are append-only (history/audit trail, like Sonar's analysis history). ModuleUsage is a
materialized "current state" view computed from the latest scan per project, refreshed on
ingest.

## 5. Scan report schema (v1)

Emitted by the CLI, POSTed to `/api/v1/scans`. Kept generic (`scanner_type` + `facts`) so
future scanner types don't require a schema migration on the ingest side.

```json
{
  "schema_version": 1,
  "scanner_type": "module-usage",
  "subject": {
    "kind": "project",
    "repo_url": "git@github.com:org/project-a.git",
    "commit_sha": "a1b2c3d",
    "branch": "main"
  },
  "generated_at": "2026-09-27T12:00:00Z",
  "facts": [
    {
      "type": "module_call",
      "call_name": "vpc",
      "source": "git::ssh://git@github.com/org/tf-module-vpc.git",
      "ref_declared": "v2.1.0",
      "ref_resolved": "v2.1.0",
      "resolution_source": "lockfile",
      "file": "main.tf",
      "line": 14
    }
  ]
}
```

For `scanner_type: module-repo` (run against a ModuleRepo, not a Project):

```json
{
  "schema_version": 1,
  "scanner_type": "module-repo",
  "subject": { "kind": "module_repo", "repo_url": "git@github.com:org/tf-module-vpc.git" },
  "generated_at": "2026-09-27T12:00:00Z",
  "facts": [
    { "type": "version_tag", "tag": "v2.1.0", "semver": "2.1.0", "commit_sha": "d4e5f6" },
    { "type": "version_tag", "tag": "v3.0.0", "semver": "3.0.0", "commit_sha": "f7a8b9" }
  ]
}
```

`resolution_source` matters: `lockfile` (from `.terraform.lock.hcl` / `.terraform/modules/modules.json`;
exact resolved commit, most trustworthy) vs `source-parse` (regex/HCL-parsed literal ref,
used when the project hasn't been `init`'d in CI, e.g. a plan-only pipeline). Surface this in
the UI so stale/unresolved data is distinguishable from verified data.

## 6. Database schema (Postgres, sketch)

```sql
CREATE TABLE projects (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  repo_url TEXT UNIQUE NOT NULL,
  display_name TEXT,
  created_at TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE module_repos (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  repo_url TEXT UNIQUE NOT NULL,
  display_name TEXT,
  created_at TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE module_versions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  module_repo_id UUID REFERENCES module_repos(id),
  tag TEXT NOT NULL,
  semver TEXT,               -- nullable if tag isn't valid semver
  commit_sha TEXT NOT NULL,
  seen_at TIMESTAMPTZ DEFAULT now(),
  UNIQUE (module_repo_id, tag)
);

CREATE TABLE scans (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  subject_kind TEXT NOT NULL,       -- 'project' | 'module_repo'
  subject_id UUID NOT NULL,         -- FK into projects or module_repos (app-level, no cross-table FK)
  scanner_type TEXT NOT NULL,
  commit_sha TEXT,
  branch TEXT,
  raw_report JSONB NOT NULL,        -- full report as submitted, for audit/replay
  submitted_at TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE module_usage (
  project_id UUID REFERENCES projects(id),
  module_repo_id UUID REFERENCES module_repos(id),
  call_name TEXT NOT NULL,
  source TEXT NOT NULL,
  ref_declared TEXT,
  ref_resolved TEXT,
  resolution_source TEXT,           -- 'lockfile' | 'source-parse'
  last_scan_id UUID REFERENCES scans(id),
  updated_at TIMESTAMPTZ DEFAULT now(),
  PRIMARY KEY (project_id, call_name)
);
```

`module_usage` is upserted on every ingested `module-usage` scan (delete rows for that
project not present in the new scan, upsert the rest); this is the "current state" table the
dashboard queries directly. `scans` stays append-only for history/audit and for later
time-series views ("drift over the last 6 months").

"Latest version" and "major versions behind" are computed at query time by joining
`module_usage.ref_resolved` (parsed as semver) against `MAX(semver)` in `module_versions` for
that `module_repo_id`; no need to denormalize/store it.

## 7. API contract (v1, minimal)

- `POST /api/v1/scans`: ingest a scan report (body = schema in §5). Auth via per-project or
  per-module-repo token, like a Sonar project token.
- `GET /api/v1/projects`: list projects.
- `GET /api/v1/projects/:id/usage`: current module_usage rows for a project, joined with
  latest-known version + major-behind flag.
- `GET /api/v1/modules/:id/consumers`: reverse lookup: every project currently pinning this
  module, and at what version (the "blast radius" query).
- `GET /api/v1/graph`: full edge list for the graph view, filterable by `?major_behind=true`
  etc.

## 8. Scanner CLI (Go)

- Single static binary: `terragraph scan --mode project|module-repo --api-url ... --token ...`.
- HCL parsing via `hashicorp/hcl/v2` + `hashicorp/terraform-config-inspect` (the same library
  Terraform's own tooling uses to enumerate `module` blocks without a full `terraform init`).
- If `.terraform.lock.hcl` or `.terraform/modules/modules.json` is present (project already
  initialized in the pipeline), prefer it for `ref_resolved`; falls back to the parsed
  literal `ref_declared` otherwise, tagging `resolution_source` accordingly.
- `--mode module-repo`: shells out to `git ls-remote --tags` (no clone needed) to enumerate
  tags, parses semver.
- Fails open by default (log + non-zero exit optionally suppressible) so adding the scan step
  to a pipeline can't break existing applies; this is a read-only observability tool, not a
  policy gate (that's a v2 feature, see §10).

## 9. Repositories

Each component is its own git repository:

- `scanner`: Go CLI, HCL parsing, git tag enumeration. Holds this design doc under `docs/`.
- `server`: Go ingest + query API, Postgres access.
- `web`: frontend with table view + graph view.
- `deploy`: docker-compose for self-hosting (server + postgres + web).

The CLI ships independently into every pipeline while the server deploys once, centrally, so
there's no reason to force them into lockstep versioning.

## 10. Roadmap

- **v1 (MVP)**: `module-usage` + `module-repo` scanners, ingest API, Postgres, table-view UI
  (project × module × pinned version × latest × major-behind), `consumers` reverse-lookup
  endpoint.
- **v2**: graph view (Cytoscape/React Flow) with filters; scan history / drift-over-time
  charts; webhook/Slack notification on "module X released a new major version, N projects
  affected."
- **v3**: pluggable scanner types on the same ingest schema: cost (Infracost-style),
  security findings (tfsec/checkov ingestion), full resource inventory/visualizer per
  project, policy gating (fail pipeline if pinning a deprecated major version).

## 11. Open questions

- Auth model for the CLI token: per-repo static token (simplest) vs OIDC from the CI
  provider (more setup, no secret to rotate). Leaning static token for v1.
- Where module repos live relative to projects: assumed separate git remotes reachable via
  `git ls-remote`/SSH from wherever the server or CI runner sits; needs a real answer once
  target infra (GitHub/GitLab/on-prem) is known.
- Self-host packaging: docker-compose sketched above for v1; Helm chart if this ever needs to
  run in the same k8s cluster as the GitOps tooling it's observing.
