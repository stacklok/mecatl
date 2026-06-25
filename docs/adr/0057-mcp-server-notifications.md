# ADR 0057 — MCP client: consume server-initiated notifications

- Status: Accepted
- Date: 2026-06-25
- Scope: `internal/adapter/mcp`
- Supersedes: none
- Superseded by: none

## Context

The MCP Streamable HTTP transport has a **standalone SSE GET stream**: after
`initialize`, the client MAY open a persistent HTTP GET to the endpoint, over
which the server pushes server-initiated messages — most notably
`notifications/tools/list_changed`, `notifications/prompts/list_changed`, and
`notifications/resources/list_changed` (the MCP spec's "the list of available
tools/resources/prompts has changed, re-fetch" signal).

Since v1 the adapter set `DisableStandaloneSSE: true` on the
`StreamableClientTransport` (`internal/adapter/mcp/mcp.go`), with an honest
comment: it avoided holding a persistent connection open, which simplified
session/test teardown. The cost: a server that fires a list-changed
notification found no live SSE listener and the notification was dropped — the
harness's cached `Server.tools`/`resources`/`prompts` snapshots went stale
silently until a full session restart.

The Go SDK (`github.com/modelcontextprotocol/go-sdk/mcp`, pinned v1.6.1) fully
supports the client side: `ClientOptions` exposes `ToolListChangedHandler`,
`PromptListChangedHandler`, `ResourceListChangedHandler` (plus
`ResourceUpdatedHandler`/`LoggingMessageHandler`, out of scope here), and the
standalone SSE GET — opened when `DisableStandaloneSSE: false` — has its own
transparent reconnect loop (`handleSSE`, bounded by `maxRetries`/
`retriesWithoutProgress`). When that reconnect exhausts, it fails the
connection → the next POST surfaces `ErrSessionMissing` → which the #177
reconnect machinery (ADR 0056) now catches. So the two features compose
cleanly: the SDK heals transient SSE drops; #177 heals dead sessions.

## Decision

Enable the standalone SSE stream and subscribe to the three list-changed
notifications, with **no live catalog mutation** (that is Phase 2, deferred to
its own ADR).

1. **Enable the stream.** Remove `DisableStandaloneSSE: true` from the
   transport built in `Server.dial` (the SDK default is `false`). The SDK opens
   the standalone GET after `initialize` and drains it on `session.Close()`.
2. **Wire the handlers in `dial`.** `dial` is the single construction site
   reused by `Connect` (initial) and `reconnect` (after a drop, ADR 0056), so a
   reconnect re-attaches the handlers automatically. Each handler only sets a
   dirty flag + logs a `port.Diagnostics` WARN; it does **not** re-list eagerly
   (it runs on the SDK's SSE goroutine, where a network call would stall
   notification processing and could deadlock against `s.mu`, which `reconnect`
   holds across `dial`).
3. **Lazy re-list in the accessors.** `Tools()`/`Resources()`/`Prompts()` check
   the matching `*Dirty` flag under `s.mu`; if set, they re-list over a bounded
   `context.Background()` (bounded by `s.cfg.Timeout`) and swap in the fresh
   snapshot. The re-list runs **without** `s.mu` held across the network (only
   the check and the final swap hold the lock) — holding it across
   `liveSession`/`withSession` would deadlock against `reconnect`. On re-list
   failure the prior snapshot stays (fail-stale, never NPE) and the dirty flag
   remains set (the next read retries).
4. **No catalog mutation.** The registered `remoteTool` specs are untouched; a
   tool the server dropped surfaces a tool-call error on use, which the model
   can react to. `tool.Catalog` (`engine/tool/catalog.go`) is append-only —
   there is no `Replace`/`Unregister`/`Remove` — so live catalog refresh is a
   harder boundary that deserves its own ADR + PR (Phase 2, referenced here).

The `toolsDirty` path is effectively dormant in Phase 1: there is no live
`Tools()` consumer after the one-shot catalog registration at connect (live
re-registration is Phase 2). `resourcesDirty`/`promptsDirty` ARE observable in
Phase 1 — `Manager.ListResources`/`ListPrompts` (the `ListMcpResources`/
`ListMcpPrompts` wire surfaces) read `Resources()`/`Prompts()` live.

## Consequences

- **One persistent SSE goroutine per connected server.** The SDK spawns a
  `handleSSE` goroutine per session; it is drained by `session.Close()` →
  `conn.Close()` cancelling `connCtx`. It is inventoried in ADR 0027 List 1
  (row 28). The package gains a `goleak` gate (`leakmain_test.go`) with a
  targeted ignore list for the SDK + stdlib goroutines that unwind
  asynchronously after close (the SSE reader, the jsonrpc2 connection read
  loop, the stdlib HTTP `persistConn`/`connReader` loops) — the gate still
  catches a genuine harness-owned leak without flaking on the SDK's async
  cleanup.
- **Reconnect re-attaches handlers.** Because the handlers live on
  `ClientOptions` built in `dial`, a #177 reconnect rebuilds them on the fresh
  session. The dirty flags survive a reconnect (they are on `*Server`, not the
  session); a post-reconnect dirty flag triggers a re-list against the fresh
  session on the next read.
- **Test teardown ordering matters.** With the SSE stream on, `httptest.Server.
  Close()` blocks if the SDK's SSE reader is still attached. The test fixtures
  register `*Server.Close` (via `t.Cleanup`) BEFORE the httptest listener close
  (LIFO order), so the client session — and its SSE reader — is torn down
  before the listener. Callers must not `defer stop()` the listener (that would
  close it before `*Server.Close`); the helpers self-register.
- **Diagnostics** flow through `port.Diagnostics`, never `slog`.

## See also

- [ADR 0056](./0056-mcp-client-reconnect.md) — the reconnect-on-drop sibling.
- [ADR 0027](./0027-cloud-native.md) List 1 row 28 — the SSE goroutine
  inventory.
- `docs/architecture/extensibility.md` — the MCP client section.
- `docs/design/PRODUCTION-READINESS.md` — the MCP rows.
