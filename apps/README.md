# Mecatl Studio (`apps/`)

> **Early access. Expect breaking changes between releases.** Mecatl Studio is
> under active development. Its user interface, its `/api/v1` surface, its
> configuration, and its browser storage can change in any release, without a
> deprecation period and without a migration path. Pin an exact image tag, read
> the release notes before upgrading, and do not build anything durable on the
> shape of this API yet. The published image carries
> `org.stacklok.mecatl.studio.stability=early-access` for the same reason.

Studio ships the bootstrap (health, runtime status, browser login, the shell) plus its
product features, starting with chat. Each feature lands with its own acceptance plan
under `docs/acceptance/studio-*.md`, which lists exactly what it ships. The published image is
`ghcr.io/stacklok/mecatl/studio`.

Mecatl Studio is a browser UI for a mecatl deployment, split in three packages that form
one self-contained pnpm workspace (pnpm 12.4.2, Node 24, see `package.json`):

| Package                               | What it is                                                                                                                                                            |
| ------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `server/` (`@mecatl-studio/server`)   | A Hono **backend for frontend (BFF)**. Holds the user's mecatl credential, talks to the daemon through the published `@stacklok-oss/mecatl-sdk`, serves the SPA and `/api` from one origin. |
| `web/` (`@mecatl-studio/web`)         | A Vite + React SPA. Calls only the BFF's `/api/v1`; imports neither the SDK nor daemon protocol types.                                                                  |
| `contracts/` (`@mecatl-studio/contracts`) | Zod schemas, the generated `openapi.json`, and the generated Hey API / TanStack Query client. Generated files are committed and drift-gated.                        |

The design and its rationale are in
[ADR 0351](../docs/adr/0351-mecatl-studio-in-repo-web-ui.md); the acceptance contract is
[docs/acceptance/studio-bootstrap.md](../docs/acceptance/studio-bootstrap.md); the
architecture guide has a [Mecatl Studio](../docs/architecture.md#mecatl-studio) section.
Two rules from the ADR shape everything here:

- **Published SDK only.** `apps/` depends on `@stacklok-oss/mecatl-sdk` by a semver range
  from npm and never references `sdk/typescript` by path or `workspace:` link (Biome's
  `noRestrictedImports` rejects it). A UI change that needs an unreleased SDK change waits
  for the SDK release.
- **The browser never talks to mecatl.** Only the BFF holds a credential; the browser
  holds four cookies (see the [security model](https://mecatl.dev/docs/building/deployment/studio)).

## Run locally

Everything is driven from the repository root through `task studio:*` (the Taskfile
include of `apps/Taskfile.yml`), or from `apps/` with `pnpm` directly.

### Spawn mode against this checkout's `mecated`

```sh
# from the repository root
task build         # produces bin/mecated (needed once, and after Go changes)
task studio:dev    # MECATED_BIN=bin/mecated, BFF on :3100, Vite on 127.0.0.1:18473
```

Open <http://127.0.0.1:18473>. Vite proxies `/api` and `/oauth/callback` to the BFF on
`:3100`, so the browser sees one origin. `task studio:dev` fails fast with a message if
`bin/mecated` is missing.

Equivalent without Task, with a `mecated` already on your `PATH` (or named by
`MECATED_BIN`):

```sh
cd apps
cp .env.example .env     # optional; the dev server reads ../.env when present
pnpm install --frozen-lockfile
pnpm dev
```

Set a provider key (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, or `OPENROUTER_API_KEY`) in
that shell first; the spawned daemon inherits it. `MECATL_DEV_MOCK=1` spawns
`mecated --mock` instead, for UI work without a model.

### The gates

```sh
task studio:install          # pnpm install --frozen-lockfile (fingerprinted; no-op when fresh)
task studio:lint             # Biome lint + format check, all three packages
task studio:typecheck        # tsc --noEmit for contracts, server, web
task studio:test             # Vitest (offline; never spawns mecated)
task studio:test:integration # Vitest over the REAL SDK against a spawned `mecated --mock`; runs `task build` first (needs Go)
task studio:generated-check  # regenerate contracts artifacts, fail if the committed copies differ
task studio:check            # lint + typecheck + test + generated-check — what CI runs
task studio:format           # apply Biome formatting / import organization
task studio:generate         # regenerate contracts/openapi.json + contracts/src/generated
task studio:build            # build server/dist and web/dist
task studio:image            # docker build the Studio image as mecatl-studio:dev
```

## Runtime modes

The BFF connects to a mecatl runtime in exactly one of three modes, chosen from the
environment at startup (a conflicting combination exits non-zero before listening, naming
the offending variable):

| Mode       | Selected by                                    | What happens                                                                                                        |
| ---------- | ---------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| `external` | `MECATL_BASE_URL`                              | Connects to an existing mecatl gRPC listener. The only mode allowed inside the image. Interactive login is discovered from the deployment's RFC 9728 resource document. |
| `spawn`    | neither `MECATL_BASE_URL` nor `MECATL_DEV_MOCK` | Spawns a local `mecated` from `MECATED_BIN` or `PATH`. Development only; refused when `STUDIO_IMAGE=1`.           |
| `mock`     | `MECATL_DEV_MOCK=1`                            | Spawns `mecated --mock` (offline, no model). Development only; refused when `STUDIO_IMAGE=1` or with `MECATL_BASE_URL`. |

Authentication follows from the mode and the target: **interactive** (Authorization Code
+ PKCE against the single issuer the resource document names), **static**
(`MECATL_AUTH_TOKEN`, one service identity for every browser), or **none** (the target has
no issuer, e.g. a local `mecated` with no auth). Static and none WARN at startup and are
reported as `mode: "static"` / `"none"`; inside the image they additionally require
`STUDIO_ALLOW_UNAUTHENTICATED=1`.

## Configuration

Deploying Studio, its full environment reference, the image, browser login, and the
security model are documented on the public
[Mecatl Studio web UI](https://mecatl.dev/docs/building/deployment/studio) page; this README covers
only local development. All configuration is environment variables read once at startup:
`MECATL_*` describe the target, `STUDIO_*` are Studio's own (ADR 0351, decision 5).
`.env.example` lists them with comments; `pnpm dev` reads `../.env` when it exists, and
`docker compose` reads `apps/.env`.

Variables that matter only for development:

| Variable          | Default                                          | Meaning                                                                                     |
| ----------------- | ------------------------------------------------ | ------------------------------------------------------------------------------------------- |
| `MECATL_DEV_MOCK` | unset                                            | `1` only. Spawns `mecated --mock`. Rejected together with `MECATL_BASE_URL` and inside the image. |
| `MECATED_BIN`     | `PATH` lookup                                    | Path of the `mecated` binary for spawn mode (`task studio:dev` sets it to `bin/mecated`).     |
| `STUDIO_HOST`     | `127.0.0.1` outside the image                    | Listen address. The development server binds loopback only unless you set this.             |
| `STUDIO_WEB_DIST` | `../web/dist` relative to the server package     | Directory holding the built SPA the BFF serves. The Dockerfile sets it for the image.        |

Any startup error exits non-zero before listening, with a stderr line naming the variable.

## Docker Compose

`docker-compose.yml` runs a locally built `mecated` and a locally built Studio image on an
internal network, publishing only Studio's port on loopback:

```sh
cd apps
cp .env.example .env        # set ANTHROPIC_API_KEY, OPENAI_API_KEY, or OPENROUTER_API_KEY
docker compose up --build
```

Open http://127.0.0.1:3100 (Studio answers only on the host in `STUDIO_PUBLIC_URL`). The
daemon has no OIDC issuer here, so login is disabled and every browser is one principal;
do not publish the port beyond loopback. `docker/mecated.Dockerfile` is a development
image, not the official `ko`-built `mecated`.
