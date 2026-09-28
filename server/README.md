# terragraph server

Receives scan reports from the [terragraph scanner](../scanner/README.md) and answers which
projects use which modules, at which versions, and how far behind they are, through a web UI and
a JSON API. See [docs/design.md](../docs/design.md) for the full platform design.

Commands below run from this `server/` folder.

## Run locally

```sh
docker compose up -d
```

From the repo root, `make up` does the same with the settings from `.env`; see the
[root README](../README.md) for the other make targets.

This starts Postgres and the server on `http://localhost:8080` with the ingest token `dev-token`
(override with `TERRAGRAPH_INGEST_TOKEN`). The Compose project is named `terragraph`, so the
containers are `terragraph-postgres-1` and `terragraph-server-1`, and data lives in the
`terragraph_pgdata` volume. `docker compose down` stops the stack; `docker compose down -v` also
deletes the data.

Then point the scanner at it:

```sh
export TERRAGRAPH_API_URL=http://localhost:8080
export TERRAGRAPH_TOKEN=dev-token

terragraph scan --path path/to/a/repo/or/folder/of/repos
```

Then open `http://localhost:8080` for the web UI.

## Web UI

Server-rendered pages, no JavaScript:

| page | |
|---|---|
| `/` | repositories, with counts of outdated and major-behind module calls; searchable and sortable. A repo with one Terraform project links straight to it |
| `/repos/{id}` | a repo holding several Terraform projects, and each project's counts |
| `/projects/{id}` | a project's module calls as a tree: calls made inside other modules are indented under them and marked nested |
| `/modules` | modules with their latest release and how many projects use them |
| `/modules/{id}` | which versions of a module are in use, every project using it, and, if the module's own repo was scanned, the modules it uses |
| `/runs` | each scanner run, with its status and progress |
| `/runs/{id}` | one run's items as they're scanned, failures first; refreshes every 2 seconds while the run is active |

A run with no progress for 10 minutes shows as stalled: its scanner was stopped before closing it.

The pages live in `internal/web`: [templ](https://templ.guide) templates (`*.templ`) and
[Tailwind](https://tailwindcss.com) classes. The generated Go code (`*_templ.go`) and stylesheet
(`static/app.css`) are committed, so building the server needs no extra tools. After editing a
template or `view.go`, run `make generate` from the repo root to regenerate both; it downloads
Tailwind's standalone CLI into `.bin/` on first use, so no Node.js is needed. CI fails if the
committed output is stale.

## Configuration

| variable | default | |
|---|---|---|
| `TERRAGRAPH_DATABASE_URL` | required | Postgres connection URL |
| `TERRAGRAPH_INGEST_TOKEN` | required | bearer token the scanner must send; use a long random value |
| `TERRAGRAPH_ADDR` | `:8080` | listen address |
| `TERRAGRAPH_TRACKED_BRANCHES` | `main,master` | comma-separated branches whose scans become a project's current state |
| `TERRAGRAPH_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error` |

Migrations run automatically at startup and are safe with several replicas.

## Current state

A repo that projects use as a module (its URL matches a module source) is a shared module's
source: it's listed under Modules rather than Projects, and its module calls show as what that
module uses.

Calls made inside modules are stored with the path of calls leading to them (`parent`), so a
project's page shows its whole tree: local modules always, and remote modules' own calls when the
project was `terraform init`'d before scanning.

A project is one Terraform root: a repo plus the root's path inside it, so `envs/dev` and
`envs/prod` of one repo are separate projects.

Every scan is stored in history, but a project scan only replaces the project's current module
calls when:

- its branch is in `TERRAGRAPH_TRACKED_BRANCHES`, so feature branch pipelines don't overwrite
  what `main` uses (folders outside git, reported as `file://` URLs, have no branch and always
  count), and
- it's at least as new (`generated_at`) as the scan it would replace, so a delayed pipeline
  can't roll the state back.

The same newer-wins rule applies to module repo scans. The response's `applied` field says which
happened.

Module sources are matched by repo, whatever form they're written in: `git::https://...`,
`git@host:org/repo.git`, `ssh://...`, with or without `.git`, `//subdir`, or `?ref=`.

A module call is compared on the exact version it pins: the version Terraform selected
(`version_resolved`, when the project was `terraform init`'d before scanning), otherwise the
declared `?ref=` if it's an exact semver tag. "Latest" is the highest stable semver tag from the
module repo's most recent scan.

## API

All responses are JSON. Errors look like `{"error": "...", "details": ["..."]}`.

| method | path | |
|---|---|---|
| `POST` | `/api/v1/scans` | ingest a scan report; needs `Authorization: Bearer <token>` |
| `GET` | `/api/v1/projects` | projects with counts of outdated module calls |
| `GET` | `/api/v1/projects/{id}` | one project, with the same counts |
| `GET` | `/api/v1/projects/{id}/usages` | a project's module calls with pinned and latest versions |
| `GET` | `/api/v1/modules` | modules with their latest version and consumer counts |
| `GET` | `/api/v1/modules/{id}` | one module, with the same fields |
| `GET` | `/api/v1/modules/{id}/consumers` | every project calling a module (its blast radius) |
| `GET` | `/api/v1/modules/{id}/dependencies` | the module calls in the module's own repo, if it was scanned |
| `GET` | `/api/v1/repos`, `/api/v1/repos/{id}` | repos with their projects' combined counts; one repo with its projects |
| `POST` | `/api/v1/runs` | start a run: `{"label": "...", "items": [{"kind": "project", "repo_url": "...", "path": "envs/prod"}, {"kind": "module_repo", "repo_url": "..."}]}` |
| `POST` | `/api/v1/runs/{id}/items/{item}/scan` | submit an item's scan report and mark it done |
| `POST` | `/api/v1/runs/{id}/items/{item}/fail` | mark an item failed: `{"error": "..."}` |
| `POST` | `/api/v1/runs/{id}/finish` | close a run: `{"status": "finished"}` or `"cancelled"` |
| `GET` | `/api/v1/runs`, `/api/v1/runs/{id}` | runs with progress counts; one run with its items |
| `GET` | `/healthz` | liveness; doesn't touch the database |
| `GET` | `/readyz` | readiness; checks the database |

All `POST` endpoints need `Authorization: Bearer <token>`.

`POST /api/v1/scans` returns `201` with `{"scan_id": 12, "applied": true}`, `401` for a bad token,
`415` for a non-JSON body, `413` above 10 MiB, `400` for malformed JSON, and `422` listing every
validation problem.

The run endpoints are what the scanner uses, and every one is safe to retry:

- `POST /api/v1/runs` honors an `Idempotency-Key` header: repeating it returns the run it
  created.
- Submitting an item that's already done returns `200` with the original result and
  `"duplicate": true`, without recording it again. The report is ingested and the item marked
  done in one transaction.
- A report that doesn't match its item (other repo, path, or kind) gets `422`; changing a
  finished or cancelled run gets `409`.

A usage looks like:

```json
{
  "project_id": 2,
  "project_repo_url": "git@github.com:example-org/payments.git",
  "project_path": ".",
  "parent": "",
  "call_name": "vpc",
  "module_id": 1,
  "module_key": "github.com/terraform-aws-modules/terraform-aws-vpc",
  "module_kind": "git",
  "source": "git::https://github.com/terraform-aws-modules/terraform-aws-vpc.git?ref=v5.1.0",
  "ref_declared": "v5.1.0",
  "ref_resolved": "v5.1.0",
  "version_resolved": "",
  "resolution_source": "source-parse",
  "file": "main.tf",
  "line": 1,
  "pinned_version": "5.1.0",
  "latest_tag": "v6.7.3",
  "latest_version": "6.7.3",
  "majors_behind": 1,
  "outdated": true
}
```

`pinned_version`, `latest_version`, and `majors_behind` are `null` when unknown, for example a
registry module whose versions haven't been scanned, or a ref that isn't an exact version.

## Development

```sh
go test ./...
```

The store tests need Postgres and are skipped without it. With the compose stack running:

```sh
TERRAGRAPH_TEST_DATABASE_URL="postgres://terragraph:terragraph@localhost:5432/terragraph?sslmode=disable" go test ./...
```

Each test runs in its own schema, so they don't interfere with each other or with local data.

## Pipelines

- `server-build`: runs on pushes that change `server/`. Checks gofmt, that the generated UI files
  are current, and vet; runs all tests with the race detector against a Postgres service
  container; then builds the Docker image.
