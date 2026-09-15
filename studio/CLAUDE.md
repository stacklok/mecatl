# CLAUDE.md — Mecatl Studio

The web client for the mecatl harness: a Next.js app (App Router) serving the
Atrium workspace — Chats · Scheduled · Skills · Memory · Settings — against a
`mecated` daemon. See ADR 0345 (module + posture) and ADR 0346 (server-backed
chats).

## Commands

Run through the root Taskfile, not bare npm:

```sh
task build          # repo root first — studio's managed mode spawns ../bin/mecated
task studio:dev     # start Studio + its mecated supervisor (background; logs in studio/dev.log)
task studio:stop    # stop web server + controller + the mecated it supervises
task studio:test    # vitest run + the hermetic server-tier suite (builds first)
task studio:lint    # biome
task studio:typecheck
```

`npm run dev` = managed mode via `scripts/dev-local.mjs` (controller on :8788,
web on :3000). `npm run dev:web` = bare `next dev` (external mode or against an
already-running controller). Setting `MECATL_BASE_URL` selects external mode.
The hermetic suite (`npm run test:server`) builds Next first — a stale build is
the usual reason it fails mysteriously.

## Module shape

- `@stacklok-oss/mecatl-sdk` (source `../sdk/typescript`, a `file:` dependency
  — `task studio:install` and CI build it before `npm ci`) — the ONLY reader
  of the daemon wire. Studio consumes the daemon exclusively through the SDK's
  HTTP transport pointed at the same-origin `/api/mecatl` proxy; the SDK owns
  generated proto bindings, SSE run/watch decoding, and unknown-event forward
  compatibility. `src/lib/harness/` adapts SDK values to Studio's view models
  (`sdk.ts` builds the client). Controller calls are NOT in the SDK.
- `src/lib/server-proxy.ts` + `src/app/api/mecatl{,-control}/[...path]` — the
  server tier: origin trust, header allowlists, bearer injection (auth ONLY —
  daemon bodies and queries are forwarded verbatim), external-mode 409 policy.
- `scripts/local-controller.mjs` — managed-mode sidecar (supervises `mecated`,
  owns model-router/MCP-gateway config + OAuth); policy helpers in
  `src/lib/controller-security.mjs`.
- `src/features/agent/` — runtime-status provider + the daemon-backed hooks;
  `src/app/workspace/**` — the five surfaces.

## Rules that have teeth

Each rule is backed by a test; break the rule and its test names you.

1. **Daemon-only: an unreachable daemon renders offline, never demo data.**
   There are no fixtures to fall back to — do not add any. The one sanctioned
   exception is the explicit, default-off, clearly-labeled Labs mock content
   (Settings → Labs → "Show mock features", `src/features/agent/mock-tour.ts`)
   — an opt-in demo the user turns on, never a fallback for an unreachable
   daemon.
   (`tests/rendered-html.test.mjs`: unreachable daemon → friendly 503.)
2. **Session placement is server-owned; Studio never sends a workspace.**
   The daemon binds every session to its deployment's environment (ADR 0291)
   and decodes `POST /v1/sessions` with unknown fields disallowed, so the
   proxy forwards create bodies verbatim — no `workspace` injection into
   session/team/schedule creation, no `?workspace=` on `/v1/commands` (it is
   keyed by `session_id`). `MECATL_WORKSPACE` (external) and the controller's
   `/status` `workspace` are display-only labels.
   (hermetic: session creation is forwarded verbatim.)
3. **Credentials never cross the browser/controller boundary.** No key-paste
   UI anywhere; `mecated` reads `~/.config/mecatl/auth.yaml`. The proxy's
   header allowlist excludes `authorization` from the browser.
   (hermetic: bearer injected server-side.)
4. **Controller mutations are server-only.** They require the server-set
   `x-mecatl-studio-request` header, a loopback Host, and an allowlisted
   Origin. (hermetic: CSRF/DNS-rebinding truth table.)
5. **External mode owns nothing locally.** Every control write answers 409.
   (hermetic: control writes 409.)
6. **A failed turn renders as failed.** `result.stop === "error"` with no text
   must never become a quiet success — the SDK's terminal `result` event
   always reaches the UI.
7. **Unknown event kinds are surfaced, never dropped.** The SDK decodes a new
   daemon event kind as a typed unknown; Studio shows it as "not rendered
   yet".
8. **The memory panel is read-only.** A value typed into the UI would land in
   turn-0 context bypassing injection scanning; the daemon has no write API by
   design. Do not add an editor.
9. **Session rows obey the store.** Eligibility comes from row capabilities
   (omitted = denied); a row is removed only by a complete inventory walk or a
   404; renames adopt the daemon's clamped echo.
   (`src/lib/protocol/sessions.test.ts` + `use-agent-sessions`.)
10. **Schedule PUT replaces the whole spec.** Fields the form cannot edit ride
    the row's `carried` spec and are re-encoded, or they are silently deleted.
    (`src/lib/protocol/schedules.test.ts`: carried round-trip.)
11. **Requests are protojson; responses are stdlib JSON.** Never echo a decoded
    response back as a request body. (`schedules.test.ts`: the asymmetry test.)
12. **Skills are project-scoped only.** The controller pins `--skills-dir` and
    never passes `--skills-conventional`.

## Gotchas

- `npm test` runs vitest in watch mode; CI and `task studio:test` use
  `npx vitest run` + `npm run test:server`.
- The controller restarts `mecated` on every config write; in-flight runs and
  session ids die with it. Surfaces warn before writes that restart. Startup
  rides the daemon's ready file (`--ready-file` + an ephemeral `--http-addr`
  + a mkfifo lifetime pipe — Node's stdio "pipe" is a socketpair mecated
  rejects); `/status` reports the ready doc's `apiMajor`/`features`/
  `deployment`.
- `package-lock.json` cannot be regenerated from scratch while the
  `file:../sdk/typescript` link is present: both npm 10 and npm 11 crash in
  arborist (`Cannot read properties of null (reading 'edgesOut')`) walking the
  SDK's pnpm-managed `node_modules`. Keep the committed lock and install
  INCREMENTALLY (`npx -y npm@10.9.4 install`); bump a transitive family that
  peer-pins itself to one exact version (tiptap) through `overrides`, never by
  deleting the lock. `npm audit fix` hits the same crash — bump by hand.
- FireNow (`POST /v1/schedules/{name}/fire`) is synchronous — the request lasts
  the whole agent run.
- Live re-attach to a running session rides the durable watch
  (`GET /v1/sessions/{id}/watch`, ADR 0250; gate on the
  `watch_session_events` feature): SSE `{event, cursor, phase}` envelopes —
  replay from the cursor (empty = the beginning), one event-less
  `phase: "live"` boundary frame, then live follow. The SDK's
  `session.attach()`/`activity()` own the envelope decoding; `use-agent-chat`
  attaches when the inventory reads running/awaiting and Studio is not itself
  driving the run.
  Residual: `POST /prompt` still cancels its run on client disconnect, so a
  reload of the DRIVING tab still ends the run — the watch covers runs
  driven elsewhere (schedules, other tabs/clients) and parked approvals.

<!-- BEGIN:nextjs-agent-rules -->

# This is NOT the Next.js you know

This version has breaking changes — APIs, conventions, and file structure may all differ from your training data. Read the relevant guide in `node_modules/next/dist/docs/` (resolved from this file's directory; in monorepos the `next` package may not be visible from the repo root) before writing any code. Heed deprecation notices.

This block is written and re-added by `next dev` — verify at `node_modules/next/dist/server/lib/generate-agent-files.js`. Removing it from a diff only re-creates the uncommitted change; committing it with your work keeps the tree clean.

<!-- END:nextjs-agent-rules -->
