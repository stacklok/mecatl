# ADR 0347 — Studio: the Atrium workspace as mecatl's daemon-only web client

- Status: Accepted
- Date: 2026-08-18
- Scope: `studio/` — what the web client is, what it may talk to, how it deploys,
  and what it deliberately does not do

> History: this decision subsumes the unmerged ADR drafts from PR #548 ("Studio
> module", numbered 0225 there before that id was taken on main) and carries its
> security posture forward under the new UI. PR #548 is superseded by the change
> that lands this ADR.

## Context

Mecatl needed a web client. Two candidates existed side by side: the original
Studio module on the unmerged `feat/studio-module` branch — a single-page chat
with a hardened server tier (origin-pinned proxies, a managed-`mecated`
supervisor, a typed wire seam, behavior tests) but a one-file UI — and the
Atrium workspace prototype in `stacklok/enterprise-ui-prototypes`, a designed
five-surface product (Chats · Scheduled · Skills · Memory · Settings) that
already spoke mecated's SSE protocol, but grew inside a ToolHive console fork:
its own OIDC stack, a mock server, generated clients for services mecatl does
not have, and per-surface demo fallbacks that rendered fabricated content
whenever the daemon was away.

Neither was shippable alone. The prototype had the product; the module had the
posture.

## Decision

**Studio is the Atrium workspace UI mounted on the original module's server
tier, in-repo at `studio/`, and it is daemon-only.**

- **A Node module, never a Go module.** Studio is a CLIENT of the daemon like
  `mecatui`: it is not in `go.work`, the layering DAG, depguard, or the
  api-compat gate. It consumes only the public HTTP/SSE API, through its own
  server-side route handlers — the browser never holds a daemon address or
  credential.
- **Two pure deployment modes.** Managed: `scripts/local-controller.mjs`
  supervises a `mecated` it spawns from `../bin/mecated` on a random loopback
  port with a generated bearer token. External: `MECATL_BASE_URL` selects a
  remote daemon; no controller runs and every local control surface answers 409
  as owned by the deployment. Nothing in between.
- **Daemon-only.** The prototype's mock server, demo fixtures, and per-hook
  fallbacks are excluded at import. An unreachable daemon is a rendered state —
  a shared runtime-status provider polls the daemon and controller, shows the
  offline banner, and gates every surface's loads. Probe failure must never
  produce fabricated content. (The managed controller may still run
  `mecated --mock`: that is a real daemon with a mock LLM provider, which is
  what offline development means here.)
- **One typed wire seam.** `src/lib/protocol/` is the only reader of raw daemon
  JSON: structural decoders that throw on malformed frames, surface unknown
  event kinds as visible notices, and encode the request/response asymmetry
  (protojson requests, stdlib-JSON responses). Generated TypeScript bindings
  from `contracts/proto` remain deferred; the seam plus its behavior tests is
  the stopgap, and a breaking wire change owes a Studio update in the same PR.
- **The security posture is inherited, not re-derived.** Host/Origin/CSRF
  pinning at the Next tier (`MECATL_STUDIO_PUBLIC_ORIGIN`); bearer injection
  server-side only; controller mutations require the server-set
  `x-mecatl-studio-request` header, loopback Host, and an allowlisted Origin;
  bounded bodies; MCP gateway egress is HTTPS-only (loopback HTTP behind an
  operator env opt-in) with the gateway URL user-entered but always validated;
  the session workspace is resolved server-side (controller status or
  `MECATL_WORKSPACE`) and injected into create bodies so the browser never
  learns or chooses paths.
- **Deliberate non-features.** No provider-credential entry anywhere: mecated
  reads keys from its auth file, and Studio's provider card only reports
  status. Memory is read-only (the daemon has no write API, by design — a
  hand-typed value would enter turn-0 context without injection scanning).
  Skill and agent-definition authoring, learned-skill review, team runs, the
  plan-approval flow, and live re-attach to running sessions (gRPC-only today)
  are named follow-ups, not silent gaps.
- **Toolchain.** npm with a committed lockfile (security pins carried from the
  prototype as npm `overrides`), Node 22 LTS via `.nvmrc`, Biome for
  lint+format, vitest for unit/decoder tests, and a hermetic `node --test`
  harness that boots the production build in external mode against a fake
  recording daemon to prove the server tier's behavior. CI runs all of it plus
  `npm audit --audit-level=high` and a license-header guard; installs run with
  scripts disabled.

## Consequences

One product instead of two halves: the designed workspace, on the hardened
tier, with the daemon as the single source of truth.

The costs, stated plainly:

- A fresh checkout without `bin/mecated` shows an offline screen, not a demo.
  The screen names the fix (`task build`, then `task studio:dev`); losing the
  zero-setup demo is the price of never rendering fabricated state.
- The deferred surfaces are real feature regressions against the prototype's
  dormant code (authoring flows existed there, unmounted) and stay out until
  they can land controller-mediated with the same posture.
- Vendoring a designed UI brings a large dependency tree (~40 runtime
  packages) into the repo's audit surface; dependabot and the audit gate own
  that from here.
- The same-PR rule now binds a much larger client: a daemon wire change costs
  a Studio change in the same PR, every time.

## Status / amendments

- **2026-09-15 — the wire seam is the TypeScript SDK.** The "One typed wire
  seam" decision above described `src/lib/protocol/` — hand-written structural
  decoders, with generated TypeScript bindings deferred. That stopgap is
  superseded: Studio now consumes the daemon exclusively through the
  TypeScript SDK (`@stacklok-oss/mecatl-sdk`, source `sdk/typescript`,
  installed into `studio/` as a `file:../sdk/typescript` dependency that CI
  and `task studio:install` build first). The SDK carries the generated
  `contracts/proto` bindings, the request/response asymmetry, the SSE run and
  durable-watch decoding, and the unknown-event-kind forward compatibility.
  The SDK's HTTP transport is pointed at Studio's same-origin `/api/mecatl`
  proxy, which keeps the inherited security posture unchanged: it pins
  Host/Origin, holds and injects the credential, and forwards every daemon
  request otherwise verbatim — the former server-side workspace injection is
  gone because session placement is server-owned (ADR 0291). Controller calls
  (`/api/mecatl-control/*`) remain Studio-owned and are not part of the SDK.
  The same-PR rule stands, re-targeted: a breaking wire change lands in the
  SDK first and Studio moves with it.

## See also

- [ADR 0348](./0348-studio-server-backed-chats.md) — the chat list is the
  daemon's session store
- `docs/architecture.md` — the Studio client section
- `user-docs/building/what-you-get/studio.md` — what operating it looks like
