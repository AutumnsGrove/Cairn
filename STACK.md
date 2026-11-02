# Stack

Every dependency Cairn takes, with its version, licence, the reason it is here and what it costs. A
dependency without an entry here is a build failure ([`docs/BUILD.md`](docs/BUILD.md) T9). Standard
library first: if a sentence here cannot explain why the standard library was not enough, the dependency
does not belong.

## Runtime (Go)

Nothing yet. S00 has no Go dependency: the server is `net/http` with the pattern mux, logging is
`log/slog`, the frontend is embedded with `embed`, and the architecture test shells out to `go list`
rather than importing `golang.org/x/tools`.

Added, with the slice that added it:

| Module | Slice | Licence | Why | Cost |
|---|---|---|---|---|
| `modernc.org/sqlite` | S04 | BSD-3-Clause | The pure-Go SQLite driver. `CGO_ENABLED=0` holds and the potato needs no C toolchain. The same choice as Warbler | About 5 MB of binary, no cgo, no C library to patch |
| `github.com/sqlc-dev/sqlc` | S04 | MIT | Generates the typed queries from plain `.sql`, so a column rename cannot leave a query behind. Run by `make gen`, pinned, never linked into the binary | A build tool and a generated package; the `drift` job keeps them honest |
| `github.com/santhosh-tekuri/jsonschema/v6` | S02 | Apache-2.0 | Validates the session protocol in Go, so the server tests, the Vitest tests and the Playwright tests all check the same document. A hand-written validator would be a second, weaker contract | Small, pure Go, no cgo. Pulls `golang.org/x/text` for format assertions |
| `github.com/AutumnsGrove/nova` (`decide`) | S05 | MIT | Nova's `decide` contract is the house's: `Request`, `Source`, `Response`, `ValidateResponse` and the `Roles` decorator that maps `cairn_preread` to a model, a timeout and a fallback. Writing a second interface would duplicate a contract nova's own consumers (Ivy, Polaris) already share. Pinned to tag `v0.2.0`; Cairn imports only `decide`, `decide/openrouter` (S08) and, in `agent`, nova proper | Pulls `nova/provider` and `nova/message` only, both pure Go. The tag moves when we move it, and `BUILD.md` 4 keeps nova's app shell (`mode`, `config`, `session`, `tui`, `bundled`) out of Cairn |
| `gopkg.in/yaml.v3` | S05 | MIT and Apache-2.0 (the `internal/` parts) | The question banks and the decisions registry are YAML that the operator edits by hand. `KnownFields(true)` makes an unknown key a load error rather than a silently ignored typo, and it rejects duplicate keys, which is exactly the strictness `BUILD.md` 4a asks for. The same library nova already ships | Small, pure Go, no cgo |

## Planned, with the slice that adds it

| Module | Slice | Licence | Why | Cost |
|---|---|---|---|---|
| `github.com/AutumnsGrove/nova` (`agent`, `tool`) | S08 | MIT | The agent loop, tools and providers. A new app should not re-implement tool calling. Already pinned at `v0.2.0` from S05 | The embedding is the largest piece of Cairn's behaviour; `importcheck` in nova's CI fails if Cairn reaches into its app shell |

## Runtime (frontend)

| Package | Slice | Licence | Why | Cost |
|---|---|---|---|---|
| `svelte`, `@sveltejs/kit`, `@sveltejs/adapter-static`, `vite`, `typescript` | S00 | MIT | The app shell, statically built and embedded. Svelte 5 runes | Build-time only; the output is a handful of small files |
| `@lucide/svelte` | S10 | ISC | Icons as components, so only the ones used are bundled and nothing loads from a CDN | Tree-shaken to the icons imported |
| `vitest`, `@testing-library/svelte`, `@playwright/test`, `@axe-core/playwright` | S00 | MIT / MPL-2.0 (axe-core) | Unit, end-to-end and accessibility checks | Dev dependencies, never shipped |
| `ajv`, `ajv-formats` | S02 | MIT | The TypeScript side of the contract validator, so a fixture that validates in Go validates in a browser test too | Dev dependencies, never shipped |
| `json-schema-to-typescript`, `openapi-typescript` | S02 | MIT | Generate the frontend's types from `api/`, so a component can never hand-copy a shape | Dev dependencies; run by `make gen`, and CI fails on a diff |

## Build and CI tools (never shipped)

`gofumpt` (BSD-3), `staticcheck` (MIT), `golangci-lint` (GPL-3, a binary run in CI only), `govulncheck`
(BSD-3), `gitleaks` (MIT), `pnpm` (MIT), `playwright` browsers, `oapi-codegen` and `openapi-typescript`
(MIT, added at S02), `sqlc` (MIT, added at S04).

GPL-3 tooling is acceptable here because none of it is linked into the binary or distributed with it; it
runs as a separate process in CI and on the operator's machine only. If that ever changes, the tool goes.

## Fonts and other assets

| Asset | Licence | Why it ships in the repo |
|---|---|---|
| Lexend (woff2) | OFL-1.1 | Interface and review text. Self-hosted, licence kept beside the file |
| Fraunces (woff2) | OFL-1.1 | Work titles and screen headings. Self-hosted, licence kept beside the file |

## Deliberately not used

- **A web framework** (Echo, Chi, Gin): the standard library's pattern mux covers routing.
- **A logging library**: `log/slog` is in the standard library.
- **An ORM**: plain `database/sql` with `sqlc`-generated queries.
- **Tailwind or CSS-in-JS**: plain CSS with the design tokens from the mockups.
- **A jsdom-only test setup for the socket**: the socket state machine is tested against a fake socket and,
  in Playwright, a real one.
- **A rate-limiter or YAML-validation library** (`golang.org/x/time/rate`, `go-playground/validator`): the
  provider pacing is a mutex and a `next` instant, and the two YAML loaders need `KnownFields` plus their own
  vocabulary checks, which are the parts a general library would not know. Both are a few dozen lines with tests
  that assert the exact error a person sees.

## Added during S00, and why

| Package | Where | Licence | Why | Cost |
|---|---|---|---|---|
| `cookie` | `web` (dev) | MIT | A phantom dependency of SvelteKit's post-build route analysis: the analysed chunk lives under `.svelte-kit/output` and resolves `cookie` from the app's own `node_modules`, which pnpm's strict layout does not populate for a transitive package. Declaring it makes the build independent of a stray copy higher up the tree | One small package, already in the tree |
| `@axe-core/playwright` | `web` (dev) | MPL-2.0 | The accessibility scan runs in the end-to-end suite, so "usable" is a test rather than an opinion | Dev only, never shipped |
| `typescript-eslint`, `eslint-plugin-svelte`, `prettier-plugin-svelte`, `globals` | `web` (dev) | MIT | Lint and format the Svelte and TypeScript sources with zero warnings | Dev only |

## Container bases

| Image | Pinned by | Why |
|---|---|---|
| `node:26-alpine` | tag (S13 pins a digest) | The frontend build stage. Node 25 and later dropped `corepack`, so pnpm is installed at an exact version instead |
| `golang:1.27-trixie` | tag (S13 pins a digest) | Builds the Go stage for the target architecture with `CGO_ENABLED=0` |
| `gcr.io/distroless/static-debian12:nonroot` | tag (S13 pins a digest) | The run stage: no shell, no package manager, non-root, and nothing to exploit |
