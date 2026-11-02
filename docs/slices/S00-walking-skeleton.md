# S00. Walking skeleton

## Purpose

A deployable no-op that proves the whole toolchain end to end: the repo builds, the binary serves a health
check and an embedded page, the `world` harness exists, and strict CI is green. Everything after this builds on
a skeleton that is already tested, linted, scanned and visible in CI, so quality is never retrofitted.

## Depends on

Nothing. Everything depends on this.

## Where we start

An empty Go module. The repo has docs and mockups only, and `LICENSE` (MIT).

## Settled

- Pure Go, `modernc.org/sqlite` later; SvelteKit with Svelte 5 and `adapter-static`, pnpm, embedded with
  `go:embed` ([`../STANDARDS.md`](../STANDARDS.md) 2, 5).
- The repo layout and the dependency rule ([`../BUILD.md`](../BUILD.md) 4).
- CI jobs and public-repo security rules ([`../CI.md`](../CI.md)).
- Host and Origin checks ([`../STANDARDS.md`](../STANDARDS.md) 6).

## Scope

1. **S00.1 Repo and tooling.** `go.mod` (module path to confirm), `Makefile` (`check`, `test`, `fuzz-smoke`,
   `web`, `gen`), `.golangci.yml`, `.editorconfig`, `STACK.md` seeded, `SECURITY.md`, `CODEOWNERS`, a
   `CLAUDE.md` for agents, an **architecture test** that fails on any import that breaks the dependency rule.
2. **S00.2 Server skeleton.** `cmd/cairn serve` on **127.0.0.1:8893** (reserved for Cairn; loopback only), `GET /healthz` returning exactly `{"status":"ok","version":"r…"}`,
   structured logging, graceful shutdown, security headers, the **Host and Origin middleware** with its table
   of hostile cases, the error envelope for unknown paths and wrong methods.
3. **S00.3 Web shell.** A SvelteKit app that renders a placeholder page in the real fonts and tokens, built in a
   scratch directory and embedded. Only a placeholder file is ever committed under the embed directory, and a
   check enforces it.
4. **S00.4 The world, core.** `internal/world` with the injected `Clock` and ID source, the fault-injection
   primitives (slow, error, malformed, huge, drop, sticky), a request recorder, and one trivial fake HTTP
   service to prove the pattern. A tiny `cmd/cairn-dev` that boots the service against it.
5. **S00.5 CI.** `ci.yml` with every job in [`../CI.md`](../CI.md) that can run now (`go`, `nocgo`, `web`, `guard`,
   `deps`, `codeql`, `coverage`), `nightly.yml` with a fuzz smoke, Dependabot, actions pinned by SHA, least
   privilege permissions. The operator applies the repository settings (branch ruleset, secret scanning,
   push protection); this slice lists them in a checklist.
6. **S00.6 End-to-end smoke.** Playwright boots the real binary and the world, loads the page on a phone
   viewport, runs axe, and records the first screenshot baseline.

## Tests and exit

| Route | Cases |
|---|---|
| Happy | `/healthz` is exactly two fields; the page loads and passes axe; `make check` is green; the arm64 build compiles |
| Bad | A `Host` off the allow-list is refused on every route; a hostile `Origin` on a POST and on a socket upgrade is refused; an unknown path and a wrong method return the JSON envelope; a request with an enormous header is refused |
| Sad | The port is in use (a clear error, non-zero exit); SIGTERM drains and exits within the deadline; a cancelled context stops the server; the embed directory containing anything but the placeholder fails `guard` |

Fuzz: the Host and Origin parser has a target and a seed corpus.

## Gates

None. The operator's action is applying the repository settings listed in S00.5.

## Decisions (answered 2026-10-31, before starting)

- **Module path** is `github.com/AutumnsGrove/cairn`, capitalised like Warbler, Ivy and Coffer. Meridian's
  lowercase path is not the house style; it is left as it is.
- **Go is pinned in `go.mod` and CI reads it from there** (`go-version-file`), so the local toolchain and the
  runner cannot drift apart. The pin is now an exact patch (`go 1.27.3`), not the minor, because adding a
  dependency makes `go get` rewrite the directive to the toolchain in use. That is worth knowing: the bump also
  moved `Dockerfile` and `STACK.md`, and it needs a golangci-lint and CodeQL release that understands the new
  version, which is why the linter is pinned at v2.15.0.
- **No branch-per-slice yet.** Commits go straight to `main` until S13 (deploy and update), when the
  PR-required ruleset is switched on. This supersedes the earlier assumption in [`../BUILD.md`](../BUILD.md) 5
  and the PR-required ruleset in [`../CI.md`](../CI.md) 4.6; both are amended to match. Required checks still
  run on **every push to `main`**, force-push and branch deletion stay blocked, so nothing lands unreviewed by
  CI, only unreviewed by a second human. Because the repo is public and `main` is directly pushable, `guard`,
  `gitleaks` and push protection carry the weight a review would otherwise carry.

## Done when

CI is green on `main` with every required check, `docker build` succeeds locally, the binary serves the page
and `/healthz`, and a deliberately broken dependency rule or a stray file in the embed directory turns CI red.

## Built (2026-10-31)

CI is green on all seven jobs; `docker build` makes a 14.9 MB distroless image that serves the page and
`/healthz` and drains on SIGTERM; the architecture test was seen red (a `domain -> store` import) and a stray
file under the embed directory fails `guard`. What the build learned, and what is left for Autumn, is in
[`../BUILD-LOG.md`](../BUILD-LOG.md) and [`../REPO-SETTINGS.md`](../REPO-SETTINGS.md).
