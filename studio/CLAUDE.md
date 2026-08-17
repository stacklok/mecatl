# studio/ — Mecatl Studio, the local web client

A local web client for the harness: chat with tool-call and approval cards, plus
panels for the provider, MCP gateway, semantic model routing, skills, memory, and
scheduled tasks. It talks to `mecated` over the SAME public HTTP/SSE API any
external client would use — see [ADR 0225](../docs/adr/0225-studio-module.md).

**This module is not a Go module.** It is not in `go.work`, the layering DAG, the
depguard allowlists, or the api-compat gate, and `task test` does not run it.

## Commands

Use the root Taskfile's `studio:` namespace — not `npm run` from inside here:

```sh
task build            # FIRST for managed mode; external mode does not need the local binary
task studio:dev       # start Studio + its mecated supervisor (background) at http://localhost:3000
task studio:stop      # stop the web server, the controller, and the supervised mecated
task studio:test      # build + the test suite (what CI runs)
task studio:lint      # ESLint
task studio:typecheck # tsc --noEmit
```

## Shape

- `app/` — the client. `page.tsx` owns ALL state (the SSE stream, session id,
  task transcripts, every panel's form state); `globals.css` holds the design
  tokens plus the conversation/panel CSS.
- `components/` — presentation only, no fetching. `shell/` is the two nav levels
  (`icon-rail.tsx` + `chat-panel.tsx`) and the navbar
  (+ `nav-items.ts`, the destination list), `chat/composer.tsx` the composer,
  `settings/` the Settings page, `user-menu/` the profile menu, `ui/` the
  shadcn primitives copied from the enterprise console.
- `app/api/` + `lib/server-proxy.ts` — server-side same-origin proxies. External
  mode injects `MECATL_AUTH_TOKEN`; managed mode delegates to the controller.
- `scripts/local-controller.mjs` — the supervisor on `127.0.0.1:8788`. Spawns and
  restarts `../bin/mecated` on a free port, owns MCP gateway OAuth, and reports
  the resolved workspace on `/status`.
- `scripts/dev-local.mjs` — starts the controller and Next server in managed
  mode; with `MECATL_BASE_URL`, starts Next only.
- `lib/protocol.ts` — the typed runtime decoder for daemon wire JSON.
- `tests/rendered-html.test.mjs` — server rendering, authenticated external
  proxy, control-policy, egress-policy, and wire-decoder behavior tests.

## Rules that have teeth

- **Destinations are VIEWS, not routes.** `page.tsx` holds the live SSE stream, so
  navigating by URL would mean lifting that into a layout and remounting the
  stream on every move. The rail switches a `view` value, and the conversation is
  `display:none`d rather than unmounted so a run keeps streaming while the
  operator reads Settings. Adding a real route means solving that first.
- **Colour goes through a token, never a literal.** `globals.css` defines the
  console's palette for `:root` and `.dark`; a hex or `zinc-*` class in a
  component is a dark-mode bug waiting to happen. Tints use
  `color-mix(in oklab, var(--token) N%, transparent)` so they re-derive on a
  theme flip instead of staying light. The only literals left are the modal
  scrim and its shadow, which are meant to be dark in both themes.
- **Type sizes are rem, never px.** The font-scale control works by setting the
  ROOT font-size, so a px value silently opts that text out of scaling.
- **The workspace is resolved, never hardcoded.** The controller derives the repo
  root from its own location and reports it on `/status`; the client refuses to
  open a session until it has one. Do not reintroduce a literal path — the app
  then works on exactly one machine (it did, once).
- **Provider credentials never cross the browser/controller boundary.** Select a
  managed provider with `MECATL_STUDIO_PROVIDER`; mecated resolves its key from
  the normal auth file. External mode leaves provider configuration remote.
- **Daemon traffic is authenticated.** Managed mode generates a bearer token and
  chooses a free port; external mode injects `MECATL_AUTH_TOKEN` server-side.
- **Controller mutations are server-only.** Keep the loopback Host/Origin gate
  and `x-mecatl-studio-request` check. Browser input must not select arbitrary
  plain-HTTP MCP egress.
- **The memory panel is read-only.** Mecatl curates its own memory through
  injection-scanned tool calls; a value typed into the UI would land in turn-0
  context without passing that check.
- **A failed turn must render as failed.** A provider failure arrives as a
  well-formed `result` carrying `stop:"error"` and no text — the "Done." fallback
  must not swallow it.
- **Skills stay project-scoped.** `--skills-dir` only; never
  `--skills-conventional`, which would widen discovery to the user-global tree.

These boundaries have behavior tests. If you change one deliberately, update the
test at the observable seam; do not replace it with a source-text regex.

## Gotcha

`npm test` BUILDS before it asserts, so it is slower than it looks and it fails
on a compile error before any test output appears. `npm run lint` and
`npm run typecheck` are the fast feedback loop.

<!-- BEGIN:nextjs-agent-rules -->

# This is NOT the Next.js you know

This version has breaking changes — APIs, conventions, and file structure may all differ from your training data. Read the relevant guide in `node_modules/next/dist/docs/` (resolved from this file's directory; in monorepos the `next` package may not be visible from the repo root) before writing any code. Heed deprecation notices.

This block is written and re-added by `next dev` — verify at `node_modules/next/dist/server/lib/generate-agent-files.js`. Removing it from a diff only re-creates the uncommitted change; committing it with your work keeps the tree clean.

<!-- END:nextjs-agent-rules -->
