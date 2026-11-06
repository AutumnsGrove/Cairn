# CI plan

Extensive, fast and strict, and safe on a **public repository** (Sepia's repo is public). Modelled on
Ivy's `CI.md` and Polaris's workflows. Status: DRAFT (2026-10-31). Testing strategy is in
[`TESTING.md`](TESTING.md).

## 1. On every pull request and push to main (`ci.yml`)

Parallel jobs, path-filtered, superseded runs cancelled, caches for the Go build, modules, the pnpm store
and Playwright browsers.

| Job | What it runs |
|---|---|
| `go` | `CGO_ENABLED=0 go build ./...`, `gofumpt -l` (must be empty), `go vet`, `staticcheck`, `golangci-lint`, `go test -race ./...` (fuzz seed corpora included), **`govulncheck ./...`**, and a `GOOS=linux GOARCH=arm64` compile check (the potato's architecture) |
| `nocgo` | Fails if any package or dependency has cgo files, since the SQLite driver is pure Go (`modernc.org/sqlite`) |
| `drift` | Regenerates the protocol types for the frontend, and any generated code, and fails on a diff |
| `web` | `pnpm install --frozen-lockfile`, `svelte-check`, ESLint, Prettier, Vitest, and a build in a scratch directory with size budgets |
| `e2e` | Playwright on WebKit and Chromium against the real binary and the world (never a live service), sharded, with visual baselines and axe; traces and screenshots on failure |
| `guard` | `gitleaks` on the diff, a check that nothing is committed under the embedded-frontend build directory except its placeholder, a check that no test path can reach a live provider, and a check that every fuzz target has a seed corpus |
| `deps` | `dependency-review-action`: new dependencies must have a compatible licence and no known vulnerabilities |
| `codeql` | CodeQL for Go and TypeScript, also weekly |
| `bench` | Short benchmarks compared with the base commit via `benchstat`, advisory until the numbers are trusted |
| `coverage` | Coverage floors on the critical packages: spans, rating, merge, guards, protocol, decisions, update, database triggers |

Required status checks on main: `go`, `nocgo`, `coverage`, `drift`, `web`, `e2e`, `guard`, `deps`,
`codeql`. `bench` stays advisory until its numbers are trusted.

## 2. On merge to main (`docker-publish.yml`)

Builds the multi-arch image (amd64 and arm64) from the multi-stage `Dockerfile`, with the frontend and Go
stages pinned to `--platform=$BUILDPLATFORM` so only the final stage is per-architecture, and pushes it to
GHCR by digest. It runs only on `push` to main with `contents: read` and `packages: write` and nothing
else, uses the build cache, and queues instead of cancelling (an interrupted push can leave a partial
manifest). It generates an SBOM and scans the image with Trivy, failing on critical findings. The potato
pulls the image and compiles nothing. This is what the update flow ([`UPDATE.md`](UPDATE.md)) consumes.

## 3. Nightly, manual and local

- **Nightly (`nightly.yml`):** time-boxed fuzzing of every target (five to ten minutes each, corpus cached,
  new crashers committed as regression seeds), `govulncheck` and `pnpm audit` against fresh advisories, the
  Firefox end-to-end suite, longer benchmarks.
- **Manual (`workflow_dispatch` only), in protected GitHub Environments with required reviewers:** `evals`
  (the real chat model and decisions backend on the fixture corpus, API key as an environment secret) and
  `live-checks`. Never required, never triggered by a PR.
- **Local only:** `make check` (format, vet, lint, unit and integration tests, a short fuzz smoke) and a
  potato benchmark over SSH. **The potato is never a CI runner.**
- **Dependabot:** Go modules, pnpm and GitHub Actions weekly, grouped, each update going through the normal
  required checks.

## 4. Public-repo security rules

1. **Triggers:** `pull_request`, never `pull_request_target`; no workflow checks out or runs fork code with
   secrets. Fork PRs get no secrets and run only the world-backed jobs.
2. **Least privilege:** default `permissions: contents: read`; jobs add only what they need.
3. **Pin third-party actions by full commit SHA,** updated by Dependabot, from a short allow-list.
4. **No self-hosted runners,** because a public repo plus a self-hosted runner lets any PR run code on that
   machine. GitHub-hosted runners only.
5. **Secrets:** none in the repo or at repository level. Eval credentials live in protected Environments.
6. **Repository settings:** secret scanning and push protection on, a `CODEOWNERS` file, and a `SECURITY.md`
   with a private reporting route. The branch ruleset on `main` has two phases, because a single-user repo
   pushing straight to `main` cannot also require a pull request:
   - **S00 to S12 (now):** direct pushes allowed; **force-push and deletion blocked**; required checks run on
     `push` as well as `pull_request`, so `main` is never ahead of a green run for long; linear history.
   - **From S13 (deploy and update):** pull requests required, the required checks below plus one approving
     review, no direct pushes. This is the phase [`../docs/BUILD.md`](../docs/BUILD.md) 5 describes as the
     default.
   The switch is a repository setting, not a code change, and it is listed on the S13 checklist.
7. **Logs are public.** Tests use **synthetic reviews and invented titles only**. A real review of yours must
   never appear in a test, a fixture or a CI log.
8. **Untrusted workflow inputs** (PR titles, branch names, commit messages) are never interpolated into shell
   commands; pass them as environment variables.

## 5. Definition of done for CI

- A failing required check on `main` is visible in the run for that push, and a force-push or branch deletion
  is impossible in either phase.
- After S13, a pull request cannot merge with a failing required check.
- A brand-new fuzz target without a seed corpus fails `guard`.
- `govulncheck` fails the build on a reachable vulnerability, and the nightly run catches advisories
  published after a merge.
- The image builds for both architectures and passes the Trivy scan before it is pushed.
- Nothing in CI can reach a live provider, the potato, or a real review.
