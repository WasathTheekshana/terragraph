# terragraph scanner

CLI that runs inside a project's or module repo's CI/GitOps pipeline and
reports Terraform module usage / version facts to a central TerraGraph
server. See [docs/design.md](../docs/design.md) for the full platform design.

Commands below run from this `scanner/` folder.

## Build

```sh
go build -o terragraph ./cmd/terragraph
```

## Testing locally

Without a server, use `--dry-run` (print the report) or `--out` (save it to a file). To submit
scans to a local server, see [server/README.md](../server/README.md).

```sh
go test ./...

# scan the bundled sample project
./terragraph scan --mode project --path testdata/sample-project --dry-run

# scan one of your own Terraform projects and save the report
./terragraph scan --mode project --path /path/to/your/terraform-project --out report.json

# list the released versions of a module repo
./terragraph scan --mode module-repo --repo-url https://github.com/terraform-aws-modules/terraform-aws-vpc.git --dry-run
```

Fixtures:

- `testdata/sample-project`: covers every source style, including a fake private repo and a local module. It can be scanned but not `terraform get`'d.
- `testdata/public-modules`: public modules only, so `terraform get` works and refs resolve to exact commits.

## Pipelines

- `scanner-build`: runs on pushes that change `scanner/`. Checks gofmt, vet, and tests, then builds binaries for linux, darwin, and windows.
- `scanner-e2e`: manual (Actions tab, "Run workflow"). Builds the CLI, scans both fixtures (the public one after `terraform get`) and the terraform-aws-vpc module repo, and checks the reports. Reports are uploaded as the `scan-reports` artifact.

## Usage

Scan a project (a repo that *calls* modules) and print the report without
submitting it anywhere:

```sh
./terragraph scan --mode project --path . --dry-run
```

Scan a module repo (a repo that a project *depends on*) to enumerate its
released versions:

```sh
./terragraph scan --mode module-repo --repo-url git@github.com:org/tf-module-vpc.git --dry-run
```

Submit to a running TerraGraph server instead of printing:

```sh
export TERRAGRAPH_API_URL=https://terragraph.internal
export TERRAGRAPH_TOKEN=...   # the server's ingest token

./terragraph scan --mode project
```

In project mode, `--repo-url`, `--commit`, and `--branch` are auto-detected
from the local `.git` checkout if not passed explicitly. Most CI systems
already have the repo checked out, so this typically needs no extra flags
beyond the API URL and token (set once as pipeline secrets).

The server only treats scans from its tracked branches (`main` and `master`
by default) as a project's current state. CI checkouts are often a detached
HEAD with no branch name, so pass `--branch` from the CI's own variable, for
example `--branch "$GITHUB_REF_NAME"` on GitHub Actions.

If the project has been `terraform init`'d before the scan runs (i.e.
`.terraform/modules/modules.json` exists), the scanner cross-checks it and
reports the exact resolved commit and, for registry modules, the exact
version Terraform selected (`resolution_source: "modules-json"`), instead of
just the literal ref parsed from the `module` block
(`resolution_source: "source-parse"`).
