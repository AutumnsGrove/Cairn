# Repository settings

The settings that are not in the code, because GitHub keeps them, and the one thing S00 cannot do for me. The
rule they implement is in [`CI.md`](CI.md) 4.6 and the reasoning is in [`BUILD.md`](BUILD.md) 5.

## Now: S00 to S12, direct pushes to `main`

A single-operator repository pushing straight to `main` cannot also require a pull request, so the guard rails
move onto the push. Apply these once:

- **Branch ruleset on `main`**
  - Target: the default branch.
  - **Block force pushes** and **block deletions**: on.
  - **Require linear history**: on.
  - **Require status checks to pass before merging**: on, and add the required checks below. Because direct
    pushes are allowed, the checks are reported on the push itself; a red run is visible within the run.
  - **Require a pull request**: **off**, for now.
- **Required status checks:** `go`, `nocgo`, `coverage`, `web`, `e2e`, `guard`, `deps`, `codeql`. `deps` only
  runs on pull requests, so it is required only once pull requests are required (S13); until then it is listed
  but not enforced.
- **Secret scanning** and **push protection**: on. The repo is public and holds no keys, and push protection
  is what stops the first one being added by accident.
- **Dependabot alerts** and **security updates**: on.
- **Actions**: allow only the actions in `.github/workflows/` (GitHub-owned, plus `golangci`, `gitleaks`,
  `pnpm/action-setup`). Default workflow permissions: read. Turn on **require actions to be pinned by SHA** if
  the setting is offered: every action here is pinned, and this makes the check enforcement rather than habit.
- **Environments**: create `evals` and `live-checks`, each with **Autumn** as a required reviewer. They hold
  the only secrets that will ever exist, and they are never triggered by a pull request
  ([`CI.md`](CI.md) 3).

## From S13: pull requests

At deploy and update, flip one switch and one addition:

- **Require a pull request before merging**: on, with **1** approving review and "dismiss stale approvals".
- Add `deps` to the required checks, and keep every other check unchanged.

Nothing else about the workflow changes, so the change is small and reversible.

## Never

- No self-hosted runner. A public repository plus a self-hosted runner lets any fork run code on that machine.
- No `pull_request_target`, and no workflow that checks out or runs fork code with secrets.
- No repository-level secrets. The two environments above are the only place a key may live, and neither is
  reachable from a pull request.
- No Tailscale Funnel, no `0.0.0.0` bind, and nothing that puts the journal on a public interface.
