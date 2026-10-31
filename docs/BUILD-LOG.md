# Build log

What was built, when, by which slice, and what was learned. Append-only: add a new entry at the bottom, never
edit an old one. One entry per slice stage or notable event.

Format:

```
## YYYY-MM-DD, S0N.M, short title
What changed. What was learned. What the next agent should know. Any live check left for the operator.
```

## 2026-10-31, planning complete
The plan, the specs (`ENTRY_CARD`, `PROTOCOL`, `UPDATE`), the question banks, the decisions draft, the testing and
CI standards, 40 mockup frames across seven pages, and the build plan (`BUILD`, `STANDARDS`, slices S00 to S20) are
written. Nothing is built. The next step is S00, the walking skeleton.

## 2026-10-31, S00.1-S00.4, the walking skeleton

**Built.** `go.mod` (module `github.com/AutumnsGrove/cairn`, `go 1.26`), the Makefile (`check`, `test`,
`e2e`, `gen`, `web`, `web-check`, `guard`, `nocgo`, `fuzz-smoke`), `.golangci.yml`, `.editorconfig`,
`STACK.md`, `SECURITY.md`, `CODEOWNERS`, the architecture test, the server skeleton, the embedded frontend,
the world harness and `cmd/cairn-dev`.

**What is true now.** `GET /healthz` answers exactly `{"status":"ok","version":"dev"}`; the Host check refuses
anything off the allow-list on every route; a state-changing request or a socket upgrade without our own
`Origin` is refused; unknown `/api` paths and wrong methods answer the JSON envelope; a hostile header over
16 KiB is refused; the frontend is embedded and served with brotli or gzip chosen from what the client
accepts; SIGTERM drains and exits 0. The architecture test fails the build on any import that breaks
`BUILD.md` 4, proved by adding `domain -> store` and watching it go red.

**Learned, for the next agent.**

- **SvelteKit 3 moved configuration out of `svelte.config.js`** into the `sveltekit()` plugin in
  `vite.config.ts`, and route options now travel as `export const config`. The old file is a hard build error
  (`config_file_unsupported`). `tsconfig.json` must extend `"$app/tsconfig"`.
- **A stray `~/node_modules/cookie` on this machine shadowed pnpm's copy** during the post-build route
  analysis, which resolves from `.svelte-kit/output`, not from Kit's own directory. `cookie` is therefore an
  explicit dev dependency: it is a genuine phantom dependency of the analysed output, not a workaround.
- **`svelte-check` was green while the page rendered unstyled**, because deleting `+layout.ts` took the
  `app.css` import with it. Rendered and looked, as the mockup rule says.
- **The guard script caught two of its own rules** on first run: a test calling `http.Get` and one using
  `http.DefaultClient`. Both were rewritten against a context, and the rules stay strict.
- The `drift` CI job waits for S02, because there is no `api/` contract to regenerate yet.

**Live checks left for Autumn:** the first green CI run on `main` after this push, and the repository settings
in [`REPO-SETTINGS.md`](REPO-SETTINGS.md).
