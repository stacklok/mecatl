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
  holds four cookies (see [Security notes](#security-notes)).

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

All configuration is environment variables read once at startup. Variables that describe
the **mecatl target** keep the `MECATL_*` prefix; variables **Studio owns** use `STUDIO_*`
(ADR 0351, decision 5). `.env.example` lists them with comments; `pnpm dev` reads `../.env`
when it exists, and `docker compose` reads `apps/.env`.

| Variable                       | Default                                       | Meaning                                                                                                                                                                    |
| ------------------------------ | --------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `MECATL_BASE_URL`              | unset (spawn mode)                            | `http`/`https` authority of the mecatl gRPC listener. Selects **external** mode.                                                                                            |
| `MECATL_RESOURCE_URL`          | derived from `MECATL_BASE_URL`                | Canonical RFC 9728 protected-resource URL used for OIDC discovery, when it differs from the gRPC authority (the compose file needs this; a mecak8s Ingress usually does not). |
| `MECATL_AUTH_TOKEN`            | unset                                         | Static bearer used for every request. Requires `MECATL_BASE_URL`; disables browser login (auth mode `static`).                                                              |
| `MECATL_DEV_MOCK`              | unset                                         | `1` only. Spawns `mecated --mock`. Rejected together with `MECATL_BASE_URL` and inside the image.                                                                            |
| `MECATED_BIN`                  | `PATH` lookup                                 | Path of the `mecated` binary for spawn mode (`task studio:dev` sets it to `bin/mecated`).                                                                                    |
| `STUDIO_PUBLIC_URL`            | unset                                         | The browser-facing origin, e.g. `https://studio.example.com` (scheme + host, no path). **Required** when interactive login is active or `STUDIO_IMAGE=1`. Cookie `Secure` flags and the OIDC callback `STUDIO_PUBLIC_URL/api/v1/auth/callback` derive from it, never from the request. |
| `STUDIO_SESSION_SECRET`        | ephemeral (generated, with a WARN)            | Raw UTF-8 bytes, at least 32, that seal the session cookies. **Required** when interactive login is active; every replica must share the same value. `openssl rand -base64 48` is a fine generator. |
| `STUDIO_PORT`                  | `3100`                                        | TCP port the BFF listens on.                                                                                                                                               |
| `STUDIO_HOST`                  | `127.0.0.1` (`0.0.0.0` in the image)          | Listen address. Outside the image the BFF is a development server and binds loopback only; set this to expose it deliberately.                                           |
| `STUDIO_IMAGE`                 | unset (`1` in the image, set by the Dockerfile) | Marks the container runtime: refuses spawn and mock modes and requires `STUDIO_ALLOW_UNAUTHENTICATED=1` for static/none auth.                                              |
| `STUDIO_ALLOW_UNAUTHENTICATED` | unset                                         | `1` acknowledges that a static-token or no-auth runtime makes every browser reaching the origin one principal. Required for those runtimes inside the image.                 |
| `STUDIO_TRUSTED_PROXY_HOPS`    | `0`                                           | Number of trusted reverse-proxy hops in front of Studio. `0` uses the socket peer as the client address; `n > 0` uses the corresponding right-most `X-Forwarded-For` entry.  |
| `STUDIO_RATE_LIMIT_MAX`        | `20`                                          | Requests allowed per client address per window on `/api/v1/auth/*`.                                                                                                        |
| `STUDIO_RATE_LIMIT_WINDOW_MS`  | `60000`                                       | The rate-limit window in milliseconds.                                                                                                                                     |
| `STUDIO_ACTIVITY_REPLAY_MAX` | `2000` | Durable replay events one activity request forwards before it reports truncation. Live events are never bounded. |
| `STUDIO_ACTIVITY_MAX_STREAMS` | `4` | Concurrent activity streams one session admits per replica; beyond it the route answers `429`. |
| `STUDIO_LOG_LEVEL`             | `info`                                        | `debug`, `info`, `warn`, or `error`.                                                                                                                                       |
| `STUDIO_WEB_DIST`              | `../web/dist` relative to the server package (`/app/web/dist` in the image) | Directory holding the built SPA the BFF serves. Normally you never set it; the Dockerfile does.                                                        |

Any startup error (a malformed or conflicting variable, a discovery failure other than a
`404`) exits non-zero **before** listening, with a stderr line naming the variable.

## Docker Compose quick start

`docker-compose.yml` runs a real `mecated` (built from `docker/mecated.Dockerfile`, context
= repository root) and Studio (built from `Dockerfile`) on an internal network, with
**only** Studio's port published, on loopback:

```sh
cd apps
cp .env.example .env        # set ANTHROPIC_API_KEY, OPENAI_API_KEY, or OPENROUTER_API_KEY
docker compose up --build
```

Open <http://127.0.0.1:3100>. The daemon has no OIDC issuer here, so Studio starts with
interactive login **disabled**, logs a startup WARN, and reports auth mode `none`;
`STUDIO_ALLOW_UNAUTHENTICATED=1` in the compose file is what lets the image accept that.
Do not publish that port beyond loopback.

Notes:

- `MECATL_BASE_URL=http://mecated:50051` (gRPC) and `MECATL_RESOURCE_URL=http://mecated:8080`
  (HTTP, for the RFC 9728 document) are wired for you; the daemon runs both listeners.
- `mecated`'s workspace persists in the `mecatl-workspace` volume across restarts;
  `docker compose down --volumes` discards it.
- Both images are local builds. `docker/mecated.Dockerfile` is a dev/demo image, not the
  official `ko`-built mecated; `Dockerfile` is the same file the release job publishes as
  `ghcr.io/stacklok/mecatl/studio`, here built from your checkout.

## The image

`Dockerfile` is multi-stage: a `node:24-slim` builder runs the frozen install, `pnpm build`,
and `pnpm deploy --prod` to prune the server to production dependencies; the runtime is
`cgr.dev/chainguard/node` **pinned by digest** (the header comment records which `latest`
it was resolved from and how to bump it), containing only `dist/`, the pruned
`node_modules`, the server `package.json`, and the built SPA. It runs as the base image's
non-root user (65532), sets `STUDIO_IMAGE=1`, exposes `3100`, and starts
`node dist/index.js`.

Its supported target is an **external** mecatl deployment, primarily `mecak8s` behind a
TLS-terminating Ingress. Minimum environment: `MECATL_BASE_URL`, `STUDIO_PUBLIC_URL`, and
(for interactive login) `STUDIO_SESSION_SECRET`.

## Security notes

- **The browser never holds a mecatl credential.** The BFF forwards the user's bearer per
  request through the SDK credential provider bound to the request's async context. Browser
  state is four cookies, all `Path=/`, `SameSite=Lax`: `studio_access` and `studio_refresh`
  (HttpOnly, sealed, 30-day max-age), `studio_login` (HttpOnly, the in-flight PKCE
  transaction, 10 minutes), and `studio_csrf` (readable by the page; the double-submit
  token). Sealed values are AES-256-GCM under a key derived from `STUDIO_SESSION_SECRET`;
  there is no server-side session store, so every replica needs the same secret.
- **`STUDIO_PUBLIC_URL` behind a TLS-terminating proxy.** Cookies are `Secure` **iff**
  `STUDIO_PUBLIC_URL` is `https`, and the OIDC callback origin comes from it too — never
  from the request scheme, because production TLS terminates at the Ingress and the BFF
  itself sees plain HTTP. Set it to the exact origin users type into the browser.
- **`STUDIO_TRUSTED_PROXY_HOPS`.** The auth-route rate limiter and the audit records key
  on the client address. With `0` (default) that is the socket peer — behind a proxy every
  user would share the proxy's address. Set it to the number of proxies you control so the
  right `X-Forwarded-For` entry is used; setting it higher than the real hop count lets a
  client spoof its address.
- **`STUDIO_ALLOW_UNAUTHENTICATED=1` means "every browser is one principal".** In static
  (`MECATL_AUTH_TOKEN`) and none modes there is no per-user identity: whoever reaches the
  Studio origin acts with the BFF's single credential. The image refuses to start in those
  modes without this flag so the choice is explicit. Use it only on a network you already
  restrict (the compose file publishes to loopback only).
- **CSRF and cross-site requests.** State-changing `/api/v1` requests need
  `Sec-Fetch-Site: same-origin` or an `Origin` matching `STUDIO_PUBLIC_URL`, plus an
  `X-Studio-CSRF` header equal to the `studio_csrf` cookie; every response carries a CSP
  whose `default-src` is `'self'` and no CORS allow-origin header is ever emitted.
- `/api/health` is the one route exempt from session gating and rate limiting.
