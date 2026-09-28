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

This starts Postgres and the server on `http://localhost:8080` with sign-in turned off
(`TERRAGRAPH_AUTH_DISABLED=true`) and the token `dev-token` (override with
`TERRAGRAPH_INGEST_TOKEN`). To try sign-in locally, add the Dex override instead:

```sh
docker compose -f docker-compose.yml -f docker-compose.sso.yml up -d --build
```

or `make up-sso` from the repo root. Sign in as `admin@example.com` (an admin) or
`dev@example.com`, both with password `password`. Dex shares the server container's network, so
`http://localhost:5556/dex` is the same address for the browser and the server.

The Compose project is named `terragraph`, so the
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
| `/settings/tokens` | admins only: create and revoke API tokens |

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
| `TERRAGRAPH_PUBLIC_URL` | required with OIDC | the URL people open, e.g. `https://terragraph.example.com`; sign-in redirects back to `<url>/auth/callback` |
| `TERRAGRAPH_OIDC_ISSUER` | | your identity provider's issuer URL; turns sign-in on |
| `TERRAGRAPH_OIDC_CLIENT_ID` | required with OIDC | OIDC client ID |
| `TERRAGRAPH_OIDC_CLIENT_SECRET` | | OIDC client secret |
| `TERRAGRAPH_OIDC_ALLOWED_DOMAINS` | any | comma-separated email domains allowed to sign in, e.g. `example.com` |
| `TERRAGRAPH_ADMIN_EMAILS` | | comma-separated emails that are admins |
| `TERRAGRAPH_OIDC_ADMIN_GROUP` | | members of this IdP group are admins |
| `TERRAGRAPH_OIDC_GROUPS_CLAIM` | `groups` | ID token claim holding the user's groups |
| `TERRAGRAPH_SESSION_TTL` | `12h` | how long a sign-in lasts |
| `TERRAGRAPH_AUTH_DISABLED` | `false` | `true` turns sign-in off and lets anyone read and submit; local use only |
| `TERRAGRAPH_INGEST_TOKEN` | | optional static token that can read and submit scans; prefer API tokens |
| `TERRAGRAPH_GITHUB_OIDC_OWNERS` | | comma-separated GitHub orgs or users whose Actions workflows may submit scans without a token |
| `TERRAGRAPH_GITHUB_OIDC_AUDIENCE` | `terragraph` | audience those workflows must request |
| `TERRAGRAPH_ADDR` | `:8080` | listen address |
| `TERRAGRAPH_TRACKED_BRANCHES` | `main,master` | comma-separated branches whose scans become a project's current state |
| `TERRAGRAPH_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error` |

The server won't start unless `TERRAGRAPH_OIDC_ISSUER` is set or `TERRAGRAPH_AUTH_DISABLED=true`,
so a deployment can't be left open by accident.

Migrations run automatically at startup and are safe with several replicas.

## Authentication

Everything except `/healthz` and `/readyz` needs credentials.

**People** sign in with your identity provider (Okta, Entra ID, Google, Keycloak, Dex, or any
OpenID Connect provider) using the authorization code flow with PKCE. Register a web client with
the redirect URI `<TERRAGRAPH_PUBLIC_URL>/auth/callback` and the scopes `openid profile email`.
Signed-in users can read everything; admins (from `TERRAGRAPH_ADMIN_EMAILS` or
`TERRAGRAPH_OIDC_ADMIN_GROUP`) also manage API tokens. Sessions are stored in Postgres in an
HttpOnly cookie, and forms are protected against cross-site requests. Admin status is decided at
sign-in, so a change takes effect at the next sign-in.

**API tokens** are created by admins under Settings. Each token:

- can read, submit scans, or both;
- can be limited to repos matching patterns like `github.com/acme/*` (submitting scans only;
  module repo version checks are always allowed);
- expires after 30, 90, or 365 days, or never;
- is shown once, stored only as a SHA-256 hash, and starts with `tg_` so secret scanners can
  spot it;
- records when it was last used, and can be revoked at once.

Send it as `Authorization: Bearer tg_...`; the scanner reads it from `TERRAGRAPH_TOKEN`.

**GitHub Actions** workflows can submit scans with no stored secret. Set
`TERRAGRAPH_GITHUB_OIDC_OWNERS` to your org, and run the scanner with `--github-oidc`. The server
verifies the workflow's ID token and only accepts scans of the workflow's own repo, and the branch
recorded is the one in the signed token, not the one the report claims:

```yaml
jobs:
  terragraph:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      id-token: write
    steps:
      - uses: actions/checkout@v4
      - run: terragraph scan --path . --github-oidc
        env:
          TERRAGRAPH_API_URL: https://terragraph.example.com
```

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
| `POST` | `/api/v1/scans` | ingest a scan report |
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

Every endpoint except `/healthz` and `/readyz` needs `Authorization: Bearer <token>` (or a
signed-in browser session for `GET`s). `GET`s need read access and `POST`s need scan submission
access; missing or bad credentials get `401`, and credentials without the access, or scoped to
other repos, get `403`.

`POST /api/v1/scans` returns `201` with `{"scan_id": 12, "applied": true}`, `401` for a bad token,
`403` for a repo outside the token's scope, `415` for a non-JSON body, `413` above 10 MiB, `400` for malformed JSON, and `422` listing every
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
