<p align="center">
  <img src="docs/assets/logo.svg" alt="terragraph logo" width="88" height="88">
</p>

<h1 align="center">terragraph</h1>

<p align="center">
  Find out which Terraform projects use which modules, at which versions, and how far behind they are.
</p>

<p align="center">
  <a href="https://github.com/WasathTheekshana/terragraph/actions/workflows/server-build.yml"><img src="https://github.com/WasathTheekshana/terragraph/actions/workflows/server-build.yml/badge.svg" alt="server-build"></a>
  <a href="https://github.com/WasathTheekshana/terragraph/actions/workflows/scanner-build.yml"><img src="https://github.com/WasathTheekshana/terragraph/actions/workflows/scanner-build.yml/badge.svg" alt="scanner-build"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-blue.svg" alt="License: Apache 2.0"></a>
</p>

Private Terraform modules get pinned once and then forgotten. When a module ships a breaking
release, nobody can say which of the hundred repos still use the old one. terragraph answers that.

A scanner finds every Terraform root under a folder, whether that is one repo or a directory of
many, and reads its `module` blocks. It reports to a server you host, which keeps the current state
and shows it in a web UI and a JSON API:

- which projects call a module, and at which exact version
- the latest release of each module and how many major versions behind a project is
- the blast radius of a module: every project that would be affected by a change to it
- calls made inside other modules, shown as a tree

The scanner runs on your machine or in CI. It needs no cloud account, and it reads private module
repos through your normal git access.

> **Status:** not released yet. The API, database schema, and scan report format can still change
> before 1.0. See the [changelog](CHANGELOG.md).

| folder | |
|---|---|
| [scanner/](scanner/README.md) | CLI that scans a repo or a folder of many repos and submits what it finds |
| [server/](server/README.md) | web UI, API, and Postgres store that ingests reports and answers usage queries |
| [docs/](docs/design.md) | design document |

## Quick start

Needs Docker, Go, and GNU make (on Windows: `choco install make` or `winget install ezwinports.make`).
Run everything from the repo root.

```sh
cp .env.example .env              # PowerShell: Copy-Item .env.example .env
make up                           # start Postgres and the server in the background
make scan DIR=path/to/your/repos  # a repo, a folder inside one, or a folder of many repos
```

Open `http://localhost:8080`. The Scans page shows progress while a scan runs, and Projects and
Modules show what it found. A scan also lists the released versions of every git repo your modules
come from, so private module repos work if you can clone them.

`make up` starts the server with sign-in turned off, which is only for local use. `make up-sso`
starts it with sign-in against a local Dex instead: `admin@example.com` / `password` is an admin,
`dev@example.com` / `password` is not. To run it for real, see
[server authentication](server/README.md#authentication).

`make` on its own lists every target. The common ones:

| target | |
|---|---|
| `make up` / `make down` | start / stop the local stack, with sign-in off |
| `make up-sso` | the stack with sign-in through a local Dex |
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
Without a `.env`, the defaults in `.env.example` apply. `.env` is gitignored. With `make up-sso`,
the ingest token still works for scans. Alternatively, sign in, create a token under Settings, and
put it in `TERRAGRAPH_TOKEN`.

The stack runs as the `terragraph` Compose project: containers `terragraph-postgres-1` and
`terragraph-server-1`, data in the `terragraph_pgdata` volume, server on `http://localhost:8080`.

## Project layout

Each folder is its own Go module with its own GitHub Actions workflow, which only runs when that
folder changes. They share no code; the scan report format described in the
[design document](docs/design.md) is the contract between them.

## Contributing

Bug reports, ideas, and pull requests are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) first;
it covers setup, tests, and what a good pull request looks like. Everyone taking part is expected to
follow the [Code of Conduct](CODE_OF_CONDUCT.md).

To report a security problem, do not open an issue. See [SECURITY.md](SECURITY.md).

## License

Apache License 2.0. See [LICENSE](LICENSE).
