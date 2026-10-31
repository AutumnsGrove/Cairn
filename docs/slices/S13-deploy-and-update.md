# S13. Deploy and update

## Purpose

Put Cairn on the potato the way every other app of yours lives there, and make it update itself. The image is
published to GHCR by digest, a host-side watcher pulls and recreates it, a failed health check rolls back, and
Settings shows every state of that flow. After this slice, every later change reaches the potato by pressing a
button.

## Depends on

S09 (a working service) and S12 (the Settings shell). S00 supplies the CI that builds the image.
**S14 depends on it.**

## Where we start

[`../UPDATE.md`](../UPDATE.md) specifies the flow, modelled on Warbler, Polaris and Ivy. Polaris's
`compose/watcher/update.sh`, Warbler's `update/view.ts` and Ivy's `updateFlow.ts` are the references. The
mockups are page 05.

## Settled

- The container never touches Docker; the watcher is a host systemd path unit (`cairn-update.path`) reacting to a
  signal file, with a heartbeat. It resolves the latest digest, fast-forwards the host checkout, pins the digest
  in `.env`, recreates the container, health-checks `/healthz`, and rolls back on failure.
- REST: `GET /api/version`, `POST /api/update/check`, `GET /api/update/available`, `POST /api/update/start`,
  `GET /api/update/status`, `POST /api/restart`, `GET /api/services`.
- The panel's eight states derive from a pure function; Update is refused while a reply is being written; the
  new frontend is picked up by the `hello` version check.
- Compose project at `~/Cairn`, published `127.0.0.1:8893` only, non-root, read-only root filesystem, port 8893
  (verified free), `tailscale serve`, `/healthz` with two fields, a Meridian tile, and one line in
  `service-healthcheck.sh`.

## Scope

1. **S13.1 Image and compose.** The multi-stage `Dockerfile` (the frontend and Go stages pinned to
   `$BUILDPLATFORM`), the hardened compose file, `.env` handling and the env passthrough list.
2. **S13.2 Publish.** `docker-publish.yml` from [`../CI.md`](../CI.md) 2: multi-arch, by digest, SBOM, Trivy.
3. **S13.3 The watcher.** The systemd units and update script, the signal files, the heartbeat, the rollback, and
   the "rollback also failed" result.
4. **S13.4 The update API.** The endpoints above, the in-flight refusal, and the restart.
5. **S13.5 Screens.** Settings, Updates (all states) and Services (reachability and what each outage costs), from
   page 05, driven by a pure view function with a fixture per state.
6. **S13.6 Operations.** The Meridian tile and vital ("reviews this month"), the health-check line, `tailscale
   serve`, the doctor command (`.env` mode 0600, listener not public, triggers present), and `docs/INSTALL.md`.

## Tests and exit

| Route | Cases |
|---|---|
| Happy | A newer digest is detected, installed and reported as done; the page reloads onto the new version |
| Bad | An update requested while a reply streams is refused with a reason; a request with a hostile `Origin` is refused; a watcher result older than the request (clock skew up to a minute) is ignored; a fabricated result file is not trusted without the heartbeat |
| Sad | The health check fails and the old version returns (the **demonstrated, deliberately failed update**); the rollback also fails and the panel says so honestly; the helper is not installed or has gone quiet; the potato reboots mid-update; GHCR is unreachable |

**View function:** a fixture per state. **Watcher:** script tests against a scripted fake Docker in CI. **Live:**
the real flow on the potato is the operator's check.

## Gates

**G5** before the first production deploy: an update and a deliberately failed update with rollback shown on a
scratch container, and the secrets plan.

## Open questions

- **Host networking or a published port?** (opened by S00). The service refuses any bind that is not loopback,
  which is the rule in `STANDARDS.md` 6 and is enforced in `config.Load`; the container proved it by refusing
  `CAIRN_ADDR=0.0.0.0:8893` and exiting 1. That leaves two coherent shapes:
  - **Host networking** (`network_mode: host`): the container's `127.0.0.1` is the potato's, so the loopback
    rule stays literally true, a compose mistake cannot publish the port, and the Cairn container can reach
    Polaris on `127.0.0.1:8899` directly, which closes `PLAN.md` open question 3. Cost: no port mapping, no
    per-container health isolation, and it only behaves on Linux, so `cairn-dev` stays the macOS story.
  - **Published loopback port** (`127.0.0.1:8893:8893`, the Warbler pattern): the process must bind all
    interfaces *inside* its own namespace, so the loopback-only rule in `config.Load` needs an explicit,
    narrowly named exception. The safety then rests on the compose line and the Host check.
  *Recommended: host networking*, because it keeps the rule true in the code rather than in a YAML file. This
  needs Autumn's decision before S13.1.
- Whether the watcher also fast-forwards the checkout as Polaris's does (proposed: yes, so config templates land
  with the image).

## Done when

The image deploys to the potato, an update from Settings succeeds, a deliberately failed one rolls back, the
Meridian tile shows, and the operator has run the live check.
