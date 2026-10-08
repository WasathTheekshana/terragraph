# Changelog

All notable changes to this project are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project will follow
[Semantic Versioning](https://semver.org/) from the first release.

## [Unreleased]

First public development version. Nothing has been released yet, so the API, database schema, and
scan report format may still change.

### Added

- **Scanner**
  - Finds every Terraform root under a path: one repo, a folder inside a repo, or a folder of many
    repos at any depth. Roots used as local modules, hidden directories, `node_modules`, and
    `examples` are skipped, and `--exclude` covers the rest.
  - Reads `module` blocks, including calls made inside local modules.
  - Reports the exact version Terraform selected when a root has been `terraform init`'d
    (`.terraform/modules/modules.json`), and otherwise the ref written in the source.
  - Lists the released versions of every git module repo with `git ls-remote --tags`, without
    cloning, or for a single repo with `--mode module-repo`.
  - Runs as a server-side run: items are registered first, then reported in parallel, with retries
    and backoff on network and server errors. Every request is safe to repeat.
  - `--dry-run` and `--out` to inspect reports without a server.
  - `--version` to print the build's version.
  - `--github-oidc` to sign in with a GitHub Actions ID token instead of a stored secret.
- **Server**
  - Ingest API, run API, and a read API for projects, modules, repos, and runs.
  - Postgres store with embedded migrations that are safe with several replicas.
  - Current-state rules: only tracked branches replace a project's state, and an older scan never
    replaces a newer one.
  - Web UI, server-rendered: repos, projects with their module call tree, modules with their
    consumers, and scan runs with live progress.
  - Modules are matched by repo however the source URL is written (`https`, `git@`, `ssh`, with or
    without `.git`, `//subdir`, or `?ref=`).
- **Dependency graph**
  - `GET /api/v1/graph` returns every project, repo, and module as nodes, with the calls between
    them as edges that carry the versions in use, a status (current, outdated, major behind,
    unknown), and where the dependency was seen (a project's own call, a call nested inside a
    module, or a dependency declared in the module's own repo).
  - A repo with many projects is one group, and a module repo with many modules is split by
    `//subdir`, so each module in a monorepo is its own node.
  - Local modules are folded into their caller, calls nested in shared modules are traced
    through, dependency cycles are flagged, and version drift is marked per module.
  - An interactive graph page at `/graph`: callers on the left and what they call on the right,
    with pan, zoom, search, and a detail panel per node. Projects, repos, and modules link to their
    own focused graph. The script is served by the server itself, with no CDN or library.
  - Focus on one project, repo, or module, up or down, to a given depth. `level=repo` collapses
    projects into their repos, and `max_nodes` falls back to repos and then trims the least
    connected nodes.
- **Resource inventory**
  - The scanner records what each root creates and looks up: resources and data sources with
    their names, locations, and tags, the variables that act as feature switches, outputs, and
    where state is kept. Values are worked out from variables, locals, tfvars, module inputs,
    `count`, `for_each`, and common functions; what can't be known from the source is kept as the
    expression and marked unresolved. Secrets and unlisted attributes are never read.
  - A resource dependency graph at `/resources/graph`: a data block is linked to the resource
    another project creates, and a `terraform_remote_state` block to the project that keeps that
    state. Lookups that nothing scanned creates point to "outside the scanned projects". View it
    per project, per repository, or per resource, filter by resource type or tag, and select a
    line to see each lookup with its file and line.
  - An inventory at `/resources`, filterable by type, tag, project, and whether the resource is
    switched on, with counts for each type and tag.
  - An environment comparison at `/repos/{id}/environments`: the projects of one repo side by
    side, showing which resources exist, which are switched off by a feature flag, which flags and
    settings differ, and which tags each environment uses.
  - `GET /api/v1/resources`, `/resources/facets`, `/resources/graph`, `/projects/{id}/inventory`,
    and `/repos/{id}/environments`.
- **Authentication**
  - Sign-in through any OpenID Connect provider with the authorization code flow and PKCE, with
    sessions stored in Postgres.
  - Admins from an email list or an identity provider group, and an optional email domain
    allow-list.
  - API tokens managed under Settings: read and/or submit permission, optional repo patterns,
    expiry, last-used tracking, and revocation. Tokens are stored as SHA-256 hashes and shown once.
  - GitHub Actions OIDC: a workflow can only submit scans for its own repo, and the branch comes from
    the signed token.
  - The server refuses to start unless sign-in is configured or explicitly turned off with
    `TERRAGRAPH_AUTH_DISABLED=true`.
- **Local development**
  - `make up` for a local stack with sign-in off, and `make up-sso` for one with a local Dex.

[Unreleased]: https://github.com/WasathTheekshana/terragraph/commits/main
