# terragraph server

Receives scan reports from the [terragraph scanner](../scanner/README.md) and answers which
projects use which modules, at which versions, and how far behind they are. See
[docs/design.md](../docs/design.md) for the full platform design.

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

terragraph scan --mode module-repo --repo-url https://github.com/terraform-aws-modules/terraform-aws-vpc.git
terragraph scan --mode project --path path/to/terraform --branch main
```

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

Every scan is stored in history, but a project scan only replaces the project's current module
calls when:

- its branch is in `TERRAGRAPH_TRACKED_BRANCHES`, so feature branch pipelines don't overwrite
  what `main` uses, and
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
| `GET` | `/api/v1/projects/{id}/usages` | a project's module calls with pinned and latest versions |
| `GET` | `/api/v1/modules` | modules with their latest version and consumer counts |
| `GET` | `/api/v1/modules/{id}/consumers` | every project calling a module (its blast radius) |
| `GET` | `/healthz` | liveness; doesn't touch the database |
| `GET` | `/readyz` | readiness; checks the database |

`POST /api/v1/scans` returns `201` with `{"scan_id": 12, "applied": true}`, `401` for a bad token,
`415` for a non-JSON body, `413` above 10 MiB, `400` for malformed JSON, and `422` listing every
validation problem.

A usage looks like:

```json
{
  "project_id": 2,
  "project_repo_url": "git@github.com:example-org/payments.git",
  "call_name": "vpc",
  "module_id": 1,
  "module_key": "github.com/terraform-aws-modules/terraform-aws-vpc",
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

- `server-build`: runs on pushes that change `server/`. Checks gofmt and vet, runs all tests
  with the race detector against a Postgres service container, then builds the Docker image.
