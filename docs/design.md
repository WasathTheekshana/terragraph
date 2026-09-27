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
  `.terraform/modules/modules.json` only.

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
 │  - query API (JSON)                           │
 │  - web UI (server-rendered pages)             │
 └───────────────────┬───────────────────────────┘
                      │
                      ▼
                  browsers
```

The web UI is rendered by the server itself (Go, [templ](https://templ.guide), Tailwind, no
JavaScript), reading the same store as the API, so the whole platform is one binary and one
container.

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
      "source": "git::ssh://git@github.com/org/tf-module-vpc.git?ref=v2.1.0",
      "ref_declared": "v2.1.0",
      "ref_resolved": "b588428cf7026e378c6438fc9b8a6e7d960c040b",
      "resolution_source": "modules-json",
      "file": "main.tf",
      "line": 14
    },
    {
      "type": "module_call",
      "call_name": "s3_bucket",
      "source": "terraform-aws-modules/s3-bucket/aws",
      "ref_declared": "~> 4.0",
      "ref_resolved": "f90d8a385e4c70afd048e8997dcccf125b362236",
      "version_resolved": "4.11.0",
      "resolution_source": "modules-json",
      "file": "main.tf",
      "line": 20
    }
  ]
}
```

- `ref_declared`: the `?ref=` of a git source, or the `version` constraint of a registry source.
- `ref_resolved`: the exact commit when the module was installed as a git clone, otherwise the same as `ref_declared`.
- `version_resolved`: the exact version Terraform selected for a registry module. Only present after `terraform init`/`get`.

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

`resolution_source` matters: `modules-json` (cross-checked against
`.terraform/modules/modules.json`; exact commit and version, most trustworthy) vs `source-parse`
(literal ref parsed from the `module` block, used when the project hasn't been `init`'d in CI).
Surface this in the UI so unresolved data is distinguishable from verified data.

## 6. Data model

The server repo's migrations are authoritative; in outline:

- `projects`: one per project repo, keyed by normalized repo URL.
- `modules`: one per versioned unit, keyed by normalized source: a git repo (subdirectory and
  ref stripped) or a registry address. Created by whichever scan mentions it first, so project
  and module repo scans meet on the same row however each writes the URL.
- `module_versions`: a module repo's tags, with semver parts when the tag is an exact version.
- `scans`: every report as submitted (append-only history and audit).
- `module_usages`: each project's current module calls, replaced as a whole when a scan is
  applied, with the exact version pinned.

A project scan is applied (becomes current state) only if its branch is tracked (`main`,
`master` by default) and it isn't older than the scan it replaces. Everything else is kept in
history only.

"Latest" is the highest stable tag; "outdated" and "majors behind" compare it with the pinned
version at query time, so nothing derived is stored.

## 7. API (v1)

- `POST /api/v1/scans`: ingest a scan report (body = schema in §5), with a shared bearer token.
- `GET /api/v1/projects`, `GET /api/v1/projects/{id}`: projects with counts of outdated and
  major-behind module calls.
- `GET /api/v1/projects/{id}/usages`: a project's current module calls with pinned version,
  latest version, majors behind, and outdated flag.
- `GET /api/v1/modules`, `GET /api/v1/modules/{id}`: modules with latest version and consumer
  counts.
- `GET /api/v1/modules/{id}/consumers`: every project calling a module (the blast radius).
- `GET /healthz`, `GET /readyz`: liveness and readiness.

The web UI pages (`/`, `/projects/{id}`, `/modules`, `/modules/{id}`) show the same data. The
graph endpoint for the v2 graph view isn't built yet.

## 8. Scanner CLI (Go)

- Single static binary: `terragraph scan --mode project|module-repo --api-url ... --token ...`.
- HCL parsing via `hashicorp/hcl/v2` + `hashicorp/terraform-config-inspect` (the same library
  Terraform's own tooling uses to enumerate `module` blocks without a full `terraform init`).
- If `.terraform/modules/modules.json` is present (project already initialized in the
  pipeline), use it for `ref_resolved` and `version_resolved`; otherwise fall back to the
  parsed literal `ref_declared`, tagging `resolution_source` accordingly.
- `--mode module-repo`: shells out to `git ls-remote --tags` (no clone needed) to enumerate
  tags, parses semver.
- Exits non-zero on failure. It's a read-only observability tool, not a policy gate (that's a
  v3 feature, see §10), so pipelines that must never be blocked by it can mark the step
  `continue-on-error`.

## 9. Repository layout

One repository, one folder per component:

```
docs/        this design doc
scanner/     Go module: CLI, HCL parsing, git tag enumeration
server/      Go module: ingest + query API, web UI, Postgres access, docker-compose for local runs
deploy/      (planned) production packaging for self-hosting
.github/     one workflow per component, triggered only by changes to that component
```

`scanner` and `server` are separate Go modules
(`github.com/WasathTheekshana/terragraph/scanner` and `.../server`) with their own
dependencies. The CLI ships into every pipeline while the server deploys once, centrally, so
they're built, tested, and versioned independently. They share no Go code; the scan report
schema in §5 is the contract between them.

## 10. Roadmap

- **v1 (MVP)**: `module-usage` + `module-repo` scanners, ingest API, Postgres, table-view UI
  (project × module × pinned version × latest × major-behind), `consumers` reverse-lookup
  endpoint.
- **v2**: graph view with filters; scan history / drift-over-time
  charts; webhook/Slack notification on "module X released a new major version, N projects
  affected."
- **v3**: pluggable scanner types on the same ingest schema: cost (Infracost-style),
  security findings (tfsec/checkov ingestion), full resource inventory/visualizer per
  project, policy gating (fail pipeline if pinning a deprecated major version).

## 11. Open questions

- Auth model for the CLI token: v1 uses one shared static token. Per-repo tokens or OIDC from
  the CI provider (no secret to rotate) are the next step. Read endpoints are unauthenticated
  for now, which assumes the server is only reachable inside the organization.
- Registry modules: their versions come from the registry, not a git repo, so "latest" is
  unknown for them until a registry version scanner exists.
- Where module repos live relative to projects: assumed separate git remotes reachable via
  `git ls-remote`/SSH from wherever the server or CI runner sits; needs a real answer once
  target infra (GitHub/GitLab/on-prem) is known.
- Self-host packaging: docker-compose sketched above for v1; Helm chart if this ever needs to
  run in the same k8s cluster as the GitOps tooling it's observing.
