# Security policy

terragraph stores information about your infrastructure and handles sign-in and API tokens, so
security reports are taken seriously.

## Supported versions

There are no releases yet. Until the first one, only the latest commit on `main` is supported. Once
releases exist, this section will list which ones get security fixes.

## Reporting a vulnerability

Please don't open a public issue, discussion, or pull request for a security problem.

Report it privately through GitHub:
[Report a vulnerability](https://github.com/WasathTheekshana/terragraph/security/advisories/new).

Include what you can of:

- what the problem is and which component it affects (scanner, server, sign-in, API tokens, web UI)
- steps or a proof of concept to reproduce it
- the version or commit you tested
- what an attacker could do with it

You can expect an acknowledgement within a few days. After that we'll work with you on a fix and
agree a date to disclose it. You'll be credited in the advisory unless you'd rather not be.

## Scope

In scope: authentication and session handling, API token handling, cross-site request forgery and
scripting in the web UI, injection, and anything that lets one credential read or write beyond what
it was granted (for example a token scoped to one repo submitting scans for another).

Out of scope:

- running the server with `TERRAGRAPH_AUTH_DISABLED=true`, which turns sign-in off by design and is
  for local use only
- the throwaway Dex configuration in `server/dev`, which uses fixed passwords on purpose
- findings that need an already-compromised server, database, or administrator account
- reports from automated scanners with no demonstrated impact

## Hardening a deployment

- Serve the UI over HTTPS and set `TERRAGRAPH_PUBLIC_URL` to the `https://` address so session
  cookies are marked secure.
- Give each pipeline its own API token limited to the repos it needs, or use GitHub Actions OIDC so
  there is no secret to leak. Avoid the shared `TERRAGRAPH_INGEST_TOKEN` outside local use.
- Restrict `TERRAGRAPH_OIDC_ALLOWED_DOMAINS` and keep the admin list short.

The [server README](server/README.md#authentication) describes each of these settings.
