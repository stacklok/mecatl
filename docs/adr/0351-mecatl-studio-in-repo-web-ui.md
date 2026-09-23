# ADR 0351 — Mecatl Studio: in-repo web UI behind a BFF over the published SDK

- Status: Proposed
- Date: 2026-09-21
- Scope: the new `apps/` workspace (`apps/web`, `apps/server`, `apps/contracts`), its
  container image and release job, and the boundary between a browser, the Studio backend
  for frontend (BFF), and a mecatl deployment.

## Context

Mecatl's web UI was first built as a design prototype outside this repository: a React
application, a thin Hono BFF, and a shared Zod/OpenAPI contracts package. The prototype's own
architecture document calls the code a design reference, names the parts that should
survive productization (the browser/BFF boundary, OIDC discovery and PKCE, the SDK
credential provider, local development mode, a Docker deployment shape), and names the parts
that should not move (Vercel entrypoint, preview OAuth relay, exploratory code).

This repository already ships one non-Go artifact the same way Studio needs to ship: the
Slack bot under `sdk/typescript/examples/slack-bot/` is built by `docker/build-push-action`,
signed, SBOM-attested, and published as `ghcr.io/stacklok/mecatl/slack-bot` from the same
`v*` tag as the Go images. It depends on the in-tree SDK via `file:../..`, which forces the
Docker build context to include the SDK and couples its lockfile and pnpm version to the SDK's.

Three shapes were considered for how Studio consumes the SDK: a root pnpm workspace spanning
`sdk/typescript` and `apps/` (`workspace:*`), the Slack bot's `file:` reference, or the
published npm package. Two layouts were considered for where the workspace root lives: the
repository root (a byte-faithful port of the prototype) or a self-contained `apps/`. Two image
shapes were considered: a single image serving the SPA and the API from one origin, or a
static-asset image plus a BFF image.

## Decision

1. **Studio consumes only the published `@stacklok-oss/mecatl-sdk` from npm.** `apps/`
   never references `sdk/typescript` by path or workspace link. A UI change that needs an
   unreleased SDK change waits for the SDK release.
2. **`apps/` is a self-contained pnpm workspace** with its own lockfile, pnpm pin, Biome
   configuration, and its own `Taskfile.yml`, included in the root Taskfile as `studio:*`. It
   contains `apps/web` (Vite + React), `apps/server` (Hono BFF), and `apps/contracts` (Zod
   schemas, the generated OpenAPI document, and the generated Hey API client, all committed
   and drift-gated). No Node toolchain files live at the Go repository root.
3. **The browser never talks to mecatl directly.** Browser code calls the BFF's `/api/v1`
   product API and imports neither the SDK nor daemon protocol types. The BFF holds the
   user's credential and forwards it per request through the SDK credential provider bound to
   the request's async context. Route files stay thin; product behavior lives in feature
   modules under `apps/web/src/features`, and the BFF's `/api/v1` routes describe product
   capabilities rather than mirroring daemon endpoints.
4. **Sessions are stateless encrypted HttpOnly cookies** sealed with `STUDIO_SESSION_SECRET`.
   The BFF keeps no server-side session store. The OIDC callback is the single URL
   `STUDIO_PUBLIC_URL/api/v1/auth/callback`; the Vercel preview relay is not ported. Cookie
   `Secure` flags and the callback origin derive from `STUDIO_PUBLIC_URL`, never from the
   request scheme, because production TLS terminates at an Ingress. The RFC 9728 document is
   fetched for the canonical resource named by `MECATL_RESOURCE_URL`, or derived from
   `MECATL_BASE_URL` when unset; a `404` disables interactive login and every other discovery
   failure is a startup error. No setting disables discovery.
5. **Configuration is split by ownership.** Settings that describe the mecatl target keep the
   `MECATL_*` prefix (`MECATL_BASE_URL`, `MECATL_AUTH_TOKEN`, `MECATL_DEV_MOCK`,
   `MECATED_BIN`); settings Studio owns use `STUDIO_*`.
6. **One image, one origin.** A multi-stage Dockerfile compiles `apps/web` and `apps/server`
   to `dist` and copies them onto a Chainguard node runtime pinned by digest, which runs
   `node dist/index.js`, serving the SPA and `/api` from the same origin. The image is
   published as `ghcr.io/stacklok/mecatl/studio` by a `publish-studio` release job with the
   same sign, SBOM, and provenance steps as the Slack bot image. The image refuses the local
   spawn and mock runtime modes, and refuses a static-token or no-auth runtime unless
   `STUDIO_ALLOW_UNAUTHENTICATED=1` is set, because in those modes every browser that reaches
   the Studio origin acts as one principal; its supported target is an external mecatl
   deployment, primarily `mecak8s`.
7. **Feature ports are Bounded follow-ups, one acceptance plan each.** The bootstrap is a
   skeleton (health, runtime, auth, shell); each feature adds `/api/v1` routes and contract
   schemas, which is a public BFF interface reviewed before code.

## Consequences

- Easier: Studio builds without a Go toolchain or the SDK's build, the Docker build context
  is `apps/`, CI for Studio is pure Node, and the Go root stays free of Node files.
- Easier: a stateless BFF scales horizontally behind mecak8s with no new infrastructure.
- Harder: the UI lags the daemon by one SDK release. A contract change lands in three steps
  (Go, SDK release, Studio) instead of one PR.
- Harder: cookie size bounds token size. The `AuthenticationService` seam is where a
  server-side store would be introduced if an issuer's tokens outgrow cookies; that is a new
  ADR, not an edit to this one.
- Committed to: the `STUDIO_*` / `MECATL_*` split, the single callback URL, the
  `ghcr.io/stacklok/mecatl/studio` path, and the one-origin image shape. The Slack bot's
  `MECATL_GRPC_ADDRESS` naming is left as is; reconciling it is out of scope here.
- Committed to: publishing the image from the first tag with the
  `org.stacklok.mecatl.studio.stability=early-access` label, so infrastructure can wire the
  deployment early without mistaking Studio for a stable surface; the label stays for as
  long as Studio may change without notice between versions.

## See also

- [Acceptance plan: Studio bootstrap](../acceptance/studio-bootstrap.md)
- [ADR 0093 — provider modules](./0093-provider-modules.md) (the opt-in submodule discipline
  the published-SDK-only rule mirrors)
- [ADR 0304 — TypeScript SDK public surface and release](./0304-typescript-sdk-public-surface-and-release.md)
- [ADR 0328 — TypeScript SDK on npmjs](./0328-typescript-sdk-npmjs-stacklok-oss.md)
- [Documentation lifecycle, ADR 0002](./0002-documentation-lifecycle.md)
