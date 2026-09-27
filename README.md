# terragraph

Tracks which Terraform projects use which modules, at which versions, and how far behind the
latest release they are. A scanner runs in each project's and module repo's pipeline and
reports to a self-hosted server.

| folder | |
|---|---|
| [scanner/](scanner/README.md) | CLI that scans a Terraform project or module repo and submits a report |
| [server/](server/README.md) | web UI, API, and Postgres store that ingests reports and answers usage queries |
| [docs/](docs/design.md) | platform design |

## Quick start

Needs Docker, Go, and GNU make (on Windows: `choco install make` or `winget install ezwinports.make`).
Run everything from the repo root.

```sh
cp .env.example .env     # PowerShell: Copy-Item .env.example .env
make up                  # start Postgres and the server in the background
make scan-module         # tell the server about terraform-aws-vpc's released versions
make scan-sample         # scan the bundled sample project
```

Then open `http://localhost:8080` to see the result in the web UI.

`make` on its own lists every target. The common ones:

| target | |
|---|---|
| `make up` / `make down` | start / stop the local stack |
| `make reset` | stop the stack and delete its data |
| `make logs` | follow the server logs |
| `make scan PROJECT=path/to/terraform` | scan one of your own projects (`BRANCH=main` by default) |
| `make scan-module MODULE_REPO=url` | list one of your module repos' versions |
| `make projects` / `make modules` | query the API from the terminal |
| `make generate` | regenerate the web UI's templ code and CSS after editing it |
| `make test` / `make test-db` | tests; `test-db` also runs the database tests against `make up`'s Postgres |

`.env` holds the local settings: the ingest token the server accepts and the scanner sends (keep
`TERRAGRAPH_INGEST_TOKEN` and `TERRAGRAPH_TOKEN` equal), the server URL, and the test database.
Without a `.env`, the defaults in `.env.example` apply. `.env` is gitignored.

The stack runs as the `terragraph` Compose project: containers `terragraph-postgres-1` and
`terragraph-server-1`, data in the `terragraph_pgdata` volume, server on `http://localhost:8080`.

Each folder has its own Go module and its own GitHub Actions workflow, which only runs when that
folder changes.
