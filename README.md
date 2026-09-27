# terragraph

Tracks which Terraform projects use which modules, at which versions, and how far behind the
latest release they are. A scanner runs in each project's and module repo's pipeline and
reports to a self-hosted server.

| folder | |
|---|---|
| [scanner/](scanner/README.md) | CLI that scans a Terraform project or module repo and submits a report |
| [server/](server/README.md) | API and Postgres store that ingests reports and answers usage queries |
| [docs/](docs/design.md) | platform design |

## Quick start

From the repo root, start Postgres and the server locally:

```sh
cd server && docker compose up -d
```

This runs as the `terragraph` Compose project: containers `terragraph-postgres-1` and
`terragraph-server-1`, data in the `terragraph_pgdata` volume. The server listens on
`http://localhost:8080` with the ingest token `dev-token`.

In another terminal, also from the repo root, build the scanner and send it some scans:

```sh
cd scanner && go build -o terragraph ./cmd/terragraph
export TERRAGRAPH_API_URL=http://localhost:8080 TERRAGRAPH_TOKEN=dev-token
./terragraph scan --mode module-repo --repo-url https://github.com/terraform-aws-modules/terraform-aws-vpc.git
./terragraph scan --mode project --path testdata/sample-project --repo-url https://github.com/example/app.git --branch main

curl http://localhost:8080/api/v1/projects
```

To stop the stack, run `docker compose down` in `server/`; add `-v` to also delete the data.

Each folder has its own Go module and its own GitHub Actions workflow, which only runs when that
folder changes.
