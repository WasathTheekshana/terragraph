# terragraph

Tracks which Terraform projects use which modules, at which versions, and how far behind the
latest release they are. A scanner, run on your machine or in pipelines, finds every Terraform
root under a folder and reports to a self-hosted server with a web UI.

| folder | |
|---|---|
| [scanner/](scanner/README.md) | CLI that scans a repo or a folder of many repos and submits what it finds |
| [server/](server/README.md) | web UI, API, and Postgres store that ingests reports and answers usage queries |
| [docs/](docs/design.md) | platform design |

## Quick start

Needs Docker, Go, and GNU make (on Windows: `choco install make` or `winget install ezwinports.make`).
Run everything from the repo root.

```sh
cp .env.example .env              # PowerShell: Copy-Item .env.example .env
make up                           # start Postgres and the server in the background
make scan DIR=path/to/your/repos  # a repo, a folder inside one, or a folder of many repos
```

Open `http://localhost:8080`: the Scans page shows the scan's progress live, and Projects and
Modules show what it found. The scan also lists the released versions of every git repo your
modules come from, using your normal git access, so private module repos work if you can clone
them.

`make` on its own lists every target. The common ones:

| target | |
|---|---|
| `make up` / `make down` | start / stop the local stack |
| `make reset` | stop the stack and delete its data |
| `make logs` | follow the server logs |
| `make scan DIR=path` | scan everything under a folder; optional `BRANCH=`, `EXCLUDE=a,b`, `CONCURRENCY=` |
| `make scan-sample` | scan the bundled sample project |
| `make scan-module MODULE_REPO=url` | list one module repo's versions |
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
