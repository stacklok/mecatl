# Mecatl Studio (`apps/`)

> **Early access. Expect breaking changes between releases.** Mecatl Studio is
> under active development. Its user interface, its `/api/v1` surface, its
> configuration, and its browser storage can change in any release, without a
> deprecation period and without a migration path. Pin an exact image tag, read
> the release notes before upgrading, and do not build anything durable on the
> shape of this API yet. The published image carries
> `org.stacklok.mecatl.studio.stability=early-access` for the same reason.

Studio ships the bootstrap (health, runtime status, browser login, the shell) plus its
product features, starting with chat. The published image is
`ghcr.io/stacklok/mecatl/studio`.

Mecatl Studio is a browser UI for a mecatl deployment, split in three packages that form
one self-contained pnpm workspace (pnpm 12.4.2, Node 26, see `package.json`):

| Package                               | What it is                                                                                                                                                            |
| ------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `server/` (`@mecatl-studio/server`)   | A Hono **backend for frontend (BFF)**. Holds the user's mecatl credential, talks to the daemon through the checked-out `@stacklok-oss/mecatl-sdk`, serves the SPA and `/api` from one origin. |
| `web/` (`@mecatl-studio/web`)         | A Vite + React SPA. Calls only the BFF's `/api/v1`; imports neither the SDK nor daemon protocol types.                                                                  |
| `contracts/` (`@mecatl-studio/contracts`) | Zod schemas, the generated `openapi.json`, and the generated Hey API / TanStack Query client. Generated files are committed and drift-gated.                        |

The architecture guide has a [Mecatl Studio](../docs/architecture/api-surface.md#mecatl-studio)
section.

## Boundary

Studio is a consumer of Mecatl's public surface, not part of the Go build:

- **Checked-out SDK.** `server/` uses the repository's `sdk/typescript` through
  a frozen `file:` dependency until an SDK release includes the execution-template
  contract. Run `task studio:install` to compile the local SDK before installing
  Studio. The image builds both from the repository root; the browser still never
  imports the SDK.
- **The browser never talks to Mecatl.** `web/` imports neither the SDK nor its
  generated protocol types (Biome rejects `@stacklok-oss/mecatl-sdk`, `/node`, and
  `/gen` there). Only the BFF holds a credential; the browser holds four cookies (see
  the [security model](../user-docs/operating/studio.md)).
- **One local gate.** `task studio:check` (lint, typecheck, offline tests,
  generated-artifact drift check) must pass for any change under `apps/`. CI runs those
  steps plus a dependency audit, integration tests against a spawned `mecated --mock`,
  the browser journeys, and an image build.

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
task studio:install # from repository root: build the SDK, then install Studio
cd apps
cp .env.example .env     # optional; the dev server reads ../.env when present
pnpm dev
```

Set a provider key (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, or `OPENROUTER_API_KEY`) in
that shell first; the spawned daemon inherits it. `MECATL_DEV_MOCK=1` spawns
`mecated --mock` instead, for UI work without a model.

### The gates

```sh
task studio:install          # build checked-out SDK, then pnpm install --frozen-lockfile
task studio:lint             # Biome lint + format check, all three packages
task studio:typecheck        # tsc --noEmit for contracts, server, web
task studio:test             # Vitest (offline; never spawns mecated)
task studio:test:integration # Vitest over the REAL SDK against a spawned `mecated --mock`; runs `task build` first (needs Go)
task studio:generated-check  # regenerate contracts artifacts, fail if the committed copies differ
task studio:check            # lint + typecheck + test + generated-check — the local gate
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
[Mecatl Studio web UI](../user-docs/operating/studio.md) page; this README covers
only local development. All configuration is environment variables read once at startup:
`MECATL_*` describe the target, `STUDIO_*` are Studio's own.
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
