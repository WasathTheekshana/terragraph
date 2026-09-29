# Contributing to terragraph

Thanks for wanting to help. This page covers how to get a change merged.

By taking part you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md). Contributions are
licensed under the [Apache License 2.0](LICENSE), the same as the project.

## Before you start

- **Bugs and small fixes:** open a pull request directly. If the bug isn't reported yet, an issue
  with steps to reproduce it helps, but isn't required for a small fix.
- **New features or larger changes:** open an issue first and describe the problem you want to
  solve. That avoids you spending a weekend on something that doesn't fit, and lets us agree on
  the shape before the code exists.
- **Questions:** use [Discussions](https://github.com/WasathTheekshana/terragraph/discussions), not issues.
- **Security problems:** don't open an issue. Follow [SECURITY.md](SECURITY.md).

The [design document](docs/design.md) explains what the project is for, what it deliberately
doesn't do, and the reasoning behind the main decisions. Read it before proposing something big.

## Setting up

You need Go, Docker, and GNU make. The scanner module targets Go 1.23 and the server module Go
1.26; use the newer one and both build. On Windows, install make with `choco install make` or
`winget install ezwinports.make`.

```sh
git clone https://github.com/WasathTheekshana/terragraph.git
cd terragraph
cp .env.example .env    # PowerShell: Copy-Item .env.example .env
make up                 # Postgres and the server, http://localhost:8080
make scan-sample        # put some data in it
```

The repo is a monorepo of two independent Go modules, `scanner/` and `server/`. Run `make` with no
arguments to list every target.

| you changed | how to check it |
|---|---|
| anything | `make vet` and `make test` |
| the database layer | `make test-db` (needs `make up` running; each test uses its own schema) |
| the web UI (`*.templ`, `view.go`, `styles/`) | `make generate`, then commit the regenerated files |
| the sign-in flow | `make up-sso` and try it in a browser as both Dex users |

The generated files (`*_templ.go` and `static/app.css`) are committed so that building the server
needs no extra tools. CI regenerates them and fails if they differ, so don't edit them by hand.

## Making a change

1. Fork the repo and create a branch from `main`.
2. Keep the change focused. One pull request should do one thing; unrelated cleanups go in their
   own.
3. Add or update tests. A bug fix should come with a test that fails without it.
4. Update the docs that describe what you changed: the READMEs, the configuration table, and the
   design document if you changed a decision recorded there.
5. Add a line under **Unreleased** in [CHANGELOG.md](CHANGELOG.md) for anything a user or
   operator would notice.
6. Run `gofmt -l .` in the module you changed (it must print nothing), then `make vet` and
   `make test`. CI runs the same checks with the race detector, so a green local run is a good sign.
7. Open the pull request and fill in the template.

### Commit messages

Use a short prefix and an imperative summary, as the existing history does:

```
feat: add API tokens with repo scopes
fix: keep the branch when a folder has no git remote
docs: explain GitHub OIDC setup
```

Common prefixes are `feat`, `fix`, `docs`, `test`, `refactor`, and `chore`. Explain the why in the
body when it isn't obvious from the diff.

## Code style

- Format with `gofmt`. Follow the conventions of the code around your change.
- Prefer the standard library. A new dependency needs a reason in the pull request.
- Comments explain why, not what. If a comment restates the next line, delete it.
- Errors are wrapped with context (`fmt.Errorf("doing x: %w", err)`) and handled once.
- The server's HTML is server-rendered with templ and has no JavaScript. Keep it that way unless
  we've agreed otherwise in an issue.
- Anything that stores a secret stores a hash, and anything that compares secrets does it in
  constant time.

## Review

A maintainer will review your pull request when they can. This is a small project, so it may take
a few days. If you haven't heard anything after a week, a polite comment on the PR is fine.

Feedback is about the code, not you. If you disagree with a request, say so and explain; we may
have missed something.

## Reporting bugs

Use the bug report form. The most useful reports include the scanner and server versions, the
exact command you ran, what you expected, what happened instead, and the relevant output. Redact
repo names, tokens, and internal URLs you don't want public.
