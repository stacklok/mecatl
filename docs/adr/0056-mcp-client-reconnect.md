# ADR 0056 — MCP client: reconnect on connection drop

- Status: Accepted
- Date: 2026-06-25
- Scope: `internal/adapter/mcp`
- Supersedes: none
- Superseded by: none

## Context

Issue #177: when a remote MCP server's session drops — the server restarts (losing
its in-memory session map), returns HTTP 404 "session not found", or closes the
transport — every *subsequent* MCP tool/resource/prompt call on that `*Server`
hard-failed. The agent had no way to recover short of a process restart, and a
single transient server bounce took a configured MCP server out for the rest of
the run.

The MCP Go SDK (`github.com/modelcontextprotocol/go-sdk/mcp`) exposes
`ErrConnectionClosed` and (as of the pinned version) `ErrSessionMissing`, but
its streamable client delegates session re-establishment to the **user**
(`streamable.go` TODO: "should we expose this error value ... to the user? … we
delegate that to the user"). The harness must therefore own the reconnect. The
SDK's terminal errors surface as the strings `"session not found"`,
`"client is closing"`, and `"connection closed"`; a string match is unavoidable
for the unexported sentinels, so `isConnectionDrop` is the single classifier
consulted by every call site and the tests.

## Decision

Reconnect lives on `*Server` (the type that owns the config, the diagnostics
sink, and the `*http.Client`), not on the tool wrappers. The construction of
transport + client + `Connect` is extracted into `Server.dial` so the initial
connect and a reconnect share one code path.

`remoteTool` holds the owning `*Server` (not a `*ClientSession`); `Execute`,
`readResource`, and `getPrompt` all funnel through `Server.withSession`, the one
retry shape:

1. fetch the live session;
2. run the call;
3. on success, return;
4. on a context cancellation or a genuine (non-drop) call fault, return the
   error unchanged;
5. on a connection-drop, reconnect **once** and re-run the call;
6. a second drop on the retry is surfaced as the drop error, which the caller
   maps to a clear terminal tool error (`"MCP server %q unavailable after
   reconnect"`), never the raw transport string.

The reconnect is serialized by a `sync.Mutex` with a **stale-session
double-check**: N concurrent failing calls pass the session they failed against
to `reconnect`; the first dials (under the lock), the rest acquire the lock,
observe `s.session != stale`, and receive the fresh session without dialling.
The dial is bounded by `cfg.Timeout` (default 30s), so the lock is held for
bounded time. Reconnect is best-effort-`Close()` on the dead session and a
session swap — the tool/resource/prompt lists are **not** re-fetched, so the
catalog registration taken at connect is preserved (a server re-advertising a
different tool set after restart keeps the old specs — accepted, same as the v1
snapshot contract).

A **circuit breaker is deliberately deferred.** The single-attempt bound already
prevents a permanently-down server from masking itself: each call pays at most
one reconnect timeout (bounded by `cfg.Timeout`) then receives a clear terminal
error. A breaker would only pay off against a flapping server that bounces
faster than the call rate — add one only if field data shows that latency
matters.

## Consequences

A restarted server is transparent to the agent: the next tool call reconnects
and succeeds. A permanently-down server fails each call with one bounded
reconnect timeout, then a clear terminal error the model can act on. Concurrency
is safe: concurrent drops coalesce into one dial. The cost is one mutex on the
hot path (uncontended in the common case — only a drop contends) and the
accepted staleness of the tool list across a reconnect. Diagnostics flow through
the injected `port.Diagnostics` (INFO `mcp server reconnecting` / `mcp server
reconnected`, WARN `mcp server reconnect failed`), never `slog` directly.

See `internal/adapter/mcp/reconnect.go` (classifier, `liveSession`,
`reconnect`, `withSession`, `clampErr`) and `internal/adapter/mcp/tool.go`
(`remoteTool` over `*Server`).

## See also

- [Extensibility — MCP, tools & progressive disclosure](../architecture/extensibility.md)
- [Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md)
