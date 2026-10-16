---
matlatl: orphan-intentional
paths:
  - "apps/server/**"
  - "apps/contracts/**"
---
# Studio BFF and contracts

A BFF route is a `createRoute` from `@hono/zod-openapi` in `apps/server/src/routes/<surface>.ts`, with an `operationId` and an `/api/v1/` path, registered in `app.ts`. For an SDK-backed surface, the SDK calls live in `apps/server/src/mecatl/<surface>.ts`. Route tests build `createApp()` with the fakes in `apps/server/src/testing/fakes.ts` and send mutations with its `csrfHeaders()`. Body and response schemas are Zod schemas in `apps/contracts/src/schemas/`, exported from `apps/contracts/src/index.ts`; path and query parameter schemas stay in the route file.

Errors are RFC 9457 problem details. Return `problem()` from `apps/server/src/http/problem.ts`, declare each error status with `problemDetailsSchema` as `application/problem+json`, and pass upstream error text through `sanitizeUpstreamDetail`, which redacts bearer tokens and upstream addresses.

A schema or route change also regenerates the contracts artifacts, as [the Studio agent contract](../../apps/AGENTS.md#generated-files) describes.

Keep these security invariants (`http/security.ts`, `auth/service.ts`, `app.ts`, and `bootstrap.ts` under `apps/server/src/`):

- Session cookies are sealed and HttpOnly, with the `Secure` flag derived from `STUDIO_PUBLIC_URL` (the request scheme when it is unset).
- Requests must name the `STUDIO_PUBLIC_URL` host, or a loopback host when it is unset; only `/api/health` is exempt, for probes.
- State-changing `/api/v1` requests must be same-origin (`Origin` or `Sec-Fetch-Site`) and carry the double-submit token: the `X-Studio-CSRF` header equals the `studio_csrf` cookie.
- `/api/v1/auth/*` (except the session check), `/oauth/callback`, and `GET /api/v1/status` are rate-limited per client address, trusting `X-Forwarded-For` only through `STUDIO_TRUSTED_PROXY_HOPS`.
- Concurrent requests holding one refresh token share a single refresh.
- With interactive login, `/api/v1` answers `401` without a session, except the status and auth routes.
- With `STUDIO_IMAGE=1`, spawn and mock modes are refused, and a static-token or no-auth target needs `STUDIO_ALLOW_UNAUTHENTICATED=1`.

The [Studio guide](../../user-docs/operating/studio.md) describes the security model for operators.
