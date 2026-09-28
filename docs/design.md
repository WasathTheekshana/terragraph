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

The scanner takes any path: one repo, a folder inside one, or a folder of many repos at any
depth. It finds every Terraform root under it, and lists the tags of every git repo those roots'
modules come from (`git ls-remote`, no clone), so one scan covers projects and module repos
alike. A module repo's own pipeline can also run `terragraph scan --mode module-repo` so the
server learns about a release as soon as it's tagged.

## 4. Core entities

- **Organization**: top-level tenant (single-org is fine for v1, but model it now so
  multi-tenant isn't a rewrite).
- **Repo**: a git repository (or a folder outside git), holding one or more projects.
- **Project**: one Terraform root: a repo plus the root's path inside it, so a repo with
  `envs/dev` and `envs/prod` is two projects.
- **ModuleRepo**: a custom module's source repo (the ~10, growing to ~100s). When it's also
  scanned as a repo, its own module calls are what the module depends on.
- **ModuleVersion**: a git tag/ref on a ModuleRepo, resolved to semver where possible.
- **Scan**: one report submitted by the CLI: `{project/module_repo, commit, branch, timestamp, scanner_type, facts}`.
- **ModuleUsage (edge)**: derived from the latest scan of a Project: `(project, module_repo, pinned_ref, resolved_version, source_type)`.
- **Run**: one scanner invocation: the items it planned (projects and module repos) and how
  each went, so progress is visible while it works.

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
    "path": "envs/prod",
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

- `parent` (module_call): for a call made inside a module, the dotted path of calls leading to
  it, e.g. `addons` or `eks.kms`; absent for calls in the root itself.
- `subject.path`: the root's directory inside the repo, slash-separated; `.` or absent for the
  repo root. Folders outside git have a `file://host/path` `repo_url`.
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

- `repos`: one per repository, keyed by normalized repo URL. A repo whose key matches a module's
  key is that module's source: shown under Modules, with its calls as the module's dependencies.
- `projects`: one per Terraform root, keyed by normalized repo URL and path.
- `modules`: one per versioned unit, keyed by normalized source: a git repo (subdirectory and
  ref stripped) or a registry address. Created by whichever scan mentions it first, so project
  and module repo scans meet on the same row however each writes the URL.
- `module_versions`: a module repo's tags, with semver parts when the tag is an exact version.
- `scans`: every report as submitted (append-only history and audit).
- `module_usages`: each project's current module calls, replaced as a whole when a scan is
  applied, with the exact version pinned. Calls made inside modules are included, keyed by
  `parent`, the dotted path of calls leading to them (Terraform's own addressing in
  `modules.json`), so a project's full tree can be shown.
- `runs`, `run_items`: each scanner run and its planned items with their status (pending, done,
  failed), error, and the scan each produced.
- `users`, `sessions`, `login_attempts`: people who signed in (keyed by issuer and subject),
  their sessions (stored as hashes, with admin status and CSRF token), and in-progress sign-ins
  (single use, expire after 10 minutes).
- `api_tokens`: tokens as SHA-256 hashes with their permissions, repo patterns, creator, last
  use, expiry, and revocation.

A project scan is applied (becomes current state) only if its branch is tracked (`main`,
`master` by default; folders outside git always are) and it isn't older than the scan it
replaces. Everything else is kept in history only.

"Latest" is the highest stable tag; "outdated" and "majors behind" compare it with the pinned
version at query time, so nothing derived is stored.

## 7. API (v1)

- `POST /api/v1/scans`: ingest a scan report (body = schema in §5).
- `GET /api/v1/projects`, `GET /api/v1/projects/{id}`: projects with counts of outdated and
  major-behind module calls.
- `GET /api/v1/projects/{id}/usages`: a project's current module calls with pinned version,
  latest version, majors behind, and outdated flag.
- `GET /api/v1/modules`, `GET /api/v1/modules/{id}`: modules with latest version and consumer
  counts.
- `GET /api/v1/modules/{id}/consumers`: every project calling a module (the blast radius).
- `POST /api/v1/runs`, `POST /api/v1/runs/{id}/items/{item}/scan|fail`,
  `POST /api/v1/runs/{id}/finish`, `GET /api/v1/runs[/{id}]`: runs, used by the scanner.
- `GET /healthz`, `GET /readyz`: liveness and readiness.

The web UI pages (`/`, `/projects/{id}`, `/modules`, `/modules/{id}`, `/runs`, `/runs/{id}`)
show the same data. The graph endpoint for the v2 graph view isn't built yet.

### Authentication

Everything except the health checks needs credentials, and the server refuses to start unless
sign-in is configured or explicitly turned off for local use.

- **People** sign in through the organization's OpenID Connect provider (authorization code
  with PKCE). Sessions live in Postgres behind an HttpOnly cookie; forms carry a CSRF token and
  are checked against the request's Origin. Admins come from a configured email list or an IdP
  group.
- **API tokens**, managed by admins in the UI, replace the shared static token: each can read,
  submit scans, or both, can be limited to repo patterns, expires, records its last use, and is
  stored only as a SHA-256 hash. The static `TERRAGRAPH_INGEST_TOKEN` remains as an optional
  bootstrap credential.
- **GitHub Actions** workflows submit with their OIDC ID token, so there's no secret to rotate.
  The server verifies it against GitHub's keys, checks the owner is allowed, accepts scans only
  of the workflow's own repo, and records the branch from the signed token rather than the report.

Scope limits apply to project scans. Module repo version listings are allowed for any
credential that can submit scans, since a project's scan lists the versions of every module repo
it uses.

### Reliability of scan runs

Every run endpoint is idempotent: creating a run honors an `Idempotency-Key`, and an item's
report is ingested and the item marked done in one transaction, so resubmitting a done item
returns its first result instead of recording it again. The scanner can therefore retry any
network or server failure (exponential backoff with jitter) without risk. A run the scanner
never closes shows as stalled after 10 minutes without progress.

There is no message broker: the data being scanned lives on the scanner's machine, and Postgres
commits each item before it's acknowledged. A broker would add a service to run without adding
durability, because if the scanner's machine goes away, nothing else could finish its scan. If
the server later clones and scans repos itself, that work needs a durable job queue; Postgres
(`FOR UPDATE SKIP LOCKED`) would be the first choice before a separate broker.

## 8. Scanner CLI (Go)

- Single static binary: `terragraph scan --path <anything> --api-url ... --token ...`, or
  `--github-oidc` instead of a token in GitHub Actions.
- Discovery: walks the path for directories with `.tf`/`.tf.json` files, skipping hidden
  directories, `node_modules`, `examples`, and `--exclude` patterns. Directories another root
  uses as a local module aren't roots. Each root's repo is found by looking for `.git` in it and
  its parents, so a folder inside a repo keeps the repo's identity.
- Runs: the scanner registers every planned item, then processes them in parallel
  (`--concurrency`), reporting each as done or failed; one failure doesn't stop the rest.
- HCL parsing via `hashicorp/hcl/v2` + `hashicorp/terraform-config-inspect` (the same library
  Terraform's own tooling uses to enumerate `module` blocks without a full `terraform init`).
- If `.terraform/modules/modules.json` is present (project already initialized in the
  pipeline), use it for `ref_resolved` and `version_resolved`; otherwise fall back to the
  parsed literal `ref_declared`, tagging `resolution_source` accordingly.
- Module versions: `git ls-remote --tags` (no clone needed) for every git module source found,
  or for one repo with `--mode module-repo`. Git runs non-interactively, so missing access fails
  with git's message instead of hanging on a prompt.
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

- CI identity beyond GitHub: GitLab and other CI providers also issue OIDC tokens and could be
  trusted the same way; until then they use scoped API tokens.
- Finer read access: every signed-in user can read everything. Per-team visibility would need
  ownership data the scanner doesn't collect yet.
- Registry modules: their versions come from the registry, not a git repo, so "latest" is
  unknown for them until a registry version scanner exists.
- Where module repos live relative to projects: assumed separate git remotes reachable via
  `git ls-remote`/SSH from wherever the server or CI runner sits; needs a real answer once
  target infra (GitHub/GitLab/on-prem) is known.
- Self-host packaging: docker-compose sketched above for v1; Helm chart if this ever needs to
  run in the same k8s cluster as the GitOps tooling it's observing.
