# terragraph scanner

CLI that finds Terraform module usage and reports it to a TerraGraph server. Point it at one
repo, a folder inside a repo, or a folder holding many repos at any depth; it scans everything
under it. Run it on your machine or in a CI/GitOps pipeline. See
[docs/design.md](../docs/design.md) for the full platform design.

Commands below run from this `scanner/` folder. From the repo root, `make scan DIR=...` does the
same with the settings from `.env`.

## Build

```sh
go build -o terragraph ./cmd/terragraph
```

## Usage

```sh
export TERRAGRAPH_API_URL=http://localhost:8080
export TERRAGRAPH_TOKEN=dev-token        # the server's ingest token

./terragraph scan --path ~/next-projects
```

The scan shows each item as it finishes and links to its page in the web UI (Scans), which
shows the same progress live:

```
Found 15 Terraform root(s) in 10 repo(s), and 7 module repo(s) to check.
Scan #2: 22 items. Follow it at http://localhost:8080/runs/2
[ 1/22] project git@github.com:acme/platform-network.git (envs/dev)  2 module calls
...
[21/22] modules https://github.com/terraform-aws-modules/terraform-aws-vpc.git  244 tags
[22/22] modules ssh://git@github.com/acme-private/tf-module-network.git  FAILED: git ls-remote: ...
```

One item failing doesn't stop the others. The exit code is non-zero if any item failed, so a CI
step notices.

### What gets scanned

- **Terraform roots:** every directory with `.tf` or `.tf.json` files, except directories another
  root uses as a local module (`source = "./modules/x"`). Each root is its own project,
  identified by its git repo's `origin` URL and its path inside the repo, so `envs/dev` and
  `envs/prod` of one repo are separate projects.
- **Skipped:** hidden directories (`.git`, `.terraform`, ...), `node_modules`, and `examples`
  (module repos' usage examples). Add more with `--exclude name` or `--exclude path/glob`
  (repeatable).
- **Module versions:** every git repo a module is sourced from (`git::...`, `git@host:...`,
  `github.com/...`) is listed with `git ls-remote` so the server knows its latest release. This
  uses your normal git access (SSH key or credential manager) and never prompts; a repo you
  can't reach fails with git's reason. Turn it off with `--skip-module-versions`.
- **Folders outside git** are identified by their location on this machine
  (`file://host/path`).

### Flags

| flag | |
|---|---|
| `--path` | what to scan (default `.`) |
| `--exclude` | directory name or relative path glob to skip; repeatable |
| `--skip-module-versions` | don't list module repos' versions |
| `--concurrency` | items scanned at once (default 4) |
| `--branch` | branch to report for every repo, instead of each checkout's own |
| `--repo-url`, `--commit` | override what git reports; only when `--path` holds one repo |
| `--mode module-repo --repo-url URL` | only list one module repo's versions, e.g. in its own pipeline |
| `--dry-run`, `--out FILE` | print or save the reports as a JSON array instead of submitting |
| `--api-url`, `--token` | server and token; default to `TERRAGRAPH_API_URL` and `TERRAGRAPH_TOKEN` |

### Branches

The server only treats scans from its tracked branches (`main` and `master` by default) as a
project's current state; others are kept in history. The scanner reports each checkout's current
branch. CI checkouts are often a detached HEAD with no branch name, so pass it from the CI's own
variable, for example `--branch "$GITHUB_REF_NAME"` on GitHub Actions.

### Exact versions

If a root has been `terraform init`'d (`.terraform/modules/modules.json` exists), the scanner
reports the exact commit and, for registry modules, the exact version Terraform selected
(`resolution_source: "modules-json"`), instead of only the ref written in the `module` block
(`resolution_source: "source-parse"`).

### Reliability

A scan is a run on the server: the scanner registers every item first, then reports each one.
Requests that fail because of the network or a server error are retried with exponential backoff,
and every request is safe to repeat, so a retry never records anything twice. Ctrl+C stops
cleanly and marks the run cancelled; a scanner that dies without closing its run shows as
stalled in the UI after 10 minutes.

## Testing locally

```sh
go test ./...

./terragraph scan --path testdata --dry-run --skip-module-versions
```

Fixtures:

- `testdata/sample-project`: covers every source style, including a fake private repo and a local module. It can be scanned but not `terraform get`'d.
- `testdata/public-modules`: public modules only, so `terraform get` works and refs resolve to exact commits.

## Pipelines

- `scanner-build`: runs on pushes that change `scanner/`. Checks gofmt, vet, and tests, then builds binaries for linux, darwin, and windows.
- `scanner-e2e`: manual (Actions tab, "Run workflow"). Builds the CLI, checks folder discovery, scans both fixtures (the public one after `terraform get`), lists module versions, and checks the reports. Reports are uploaded as the `scan-reports` artifact.
