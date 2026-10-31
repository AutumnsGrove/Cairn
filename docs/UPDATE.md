# Updating Cairn

Cairn updates itself the way Polaris, Ivy and Warbler do. The page asks, a host-side watcher does the
work, and a failed update rolls back on its own. The state machine below is modelled on Warbler's
`update/view.ts`, which has the most careful version of it. The screens are in
[`mockups/05-settings-and-updates.html`](../mockups/05-settings-and-updates.html).

## How it works

1. **The container never controls Docker.** There is no socket mount. The panel calls Cairn's own REST API,
   and Cairn writes a signal file that a **host-side watcher** (a systemd path unit, `cairn-update.path`)
   picks up.
2. **The watcher updates.** It resolves the latest published image digest from GHCR (waiting out an
   in-progress CI build first), fast-forwards the host checkout so `docker-compose.yml` and the
   bind-mounted config land with the image, pins the new digest in `.env`, recreates the container, and
   checks `GET /healthz`.
3. **A failed health check rolls back** to the previous digest and records a result of `failed`.
4. **The watcher keeps a heartbeat**, so the app can tell "no updater installed" from "the updater has gone
   quiet".

## REST

| Endpoint | Purpose |
|---|---|
| `GET /api/version` | `{version, deployment, updater: {state: ready, not_installed, stale; heartbeat_at}}` |
| `POST /api/update/check` | Ask the watcher to look for a newer image |
| `GET /api/update/available` | `{available: {current, latest, latest_digest, checked_at, update_available}, check_pending}` |
| `POST /api/update/start` | Ask the watcher to install the latest. Refused while a reply is being written |
| `GET /api/update/status` | `{pending, result: {status: ok, failed, skipped; detail; target; finished_at}}` |
| `POST /api/restart` | Restart only: no pull, no rebuild |
| `GET /api/services` | Reachability of each dependency, for the Services screen |

All state-changing requests go through the same Origin check as everything else
([`PROTOCOL.md`](PROTOCOL.md) section 1).

## The panel's states

A pure function from what the page knows to one of these views, so every state is testable without a
browser:

| View | Shows |
|---|---|
| loading | Nothing yet |
| no updater | "Updates are not working on this server", the helper's state (not installed, or gone quiet), and the command to run on the potato |
| checking | A check was asked for and the watcher has not answered |
| up to date | The version, "Last checked ...", and Check for updates |
| available | The new version and Update now |
| updating | An indeterminate bar, "This takes a minute. Cairn will reload when it is done.", and a longer-than-expected line after 90 s |
| done | "Updated to ...", then the page reloads |
| failed | The reason in plain text. If the rollback worked: "the previous version is running again" and Try again. If the rollback also failed: "Cairn may not be running properly. Check the potato." with no button |

## Rules that are specific to Cairn

- **Update is refused while a reply is being written,** and the panel says "Finish the reply first". A turn in
  flight would be killed by the restart. Drafts, sessions and entries are in SQLite and are untouched.
- **Open sessions reconnect on their own.** The WebSocket drops, the client reconnects with its last
  sequence number ([`PROTOCOL.md`](PROTOCOL.md) section 6), and the update never loses a draft.
- **The new server may carry a new embedded frontend.** `hello` includes the server's version, and a client
  that sees a different one than it was loaded with reloads itself, so an old UI never talks to a new server.
- **"Rollback also failed" cannot always be shown,** because if no version is running there is no app to
  draw the message. The panel covers the case where the app is up and the watcher says so; otherwise the
  fix is on the potato (`journalctl -u cairn-update.service`).
- **Restart is separate from Update.** It does no pull and no rebuild, and is for when something is simply
  stuck.

## Fixture cases to write

- the page derives each view above from synthetic inputs, including a result older than the update that was
  just started (the phone's clock and the potato's differ by up to a minute);
- an update requested while a reply is streaming is refused;
- an update during which the socket drops and resumes loses no draft text;
- a client loaded with an older frontend reloads after `hello` reports a newer server version.
