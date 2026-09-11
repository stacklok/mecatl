# ADR 0326 — MCP client: per-server opt-out of the standalone SSE stream

- Status: Accepted
- Date: 2026-08-05
- Scope: `internal/adapter/mcp`, `internal/cliconfig` (`--mcp-server` flag)
- Supersedes: none
- Superseded by: none
- Amends: [ADR 0057](./0057-mcp-server-notifications.md) (the stream is now
  per-server optional instead of unconditional)

## Context

ADR 0057 enabled the MCP standalone SSE GET stream unconditionally so servers
can push `notifications/{tools,prompts,resources}/list_changed`. The pinned
go-sdk (v1.6.1; v1.7.0 is byte-equivalent here) runs the stream on a reconnect
loop (`handleSSE`) whose terminal failure is **connection-wide**: after the
retry budget exhausts without progress it calls `c.fail()`, poisoning the POST
channel along with the GET. The initial GET is 4xx-tolerant, but a
**GET-hostile gateway** — one that answers the standalone GET with 200 and
then closes it immediately (or keeps rejecting it after establishment) while
POST request/response traffic stays healthy — is indistinguishable from a
flaky stream, so the loop retries, exhausts, and kills the session.

The blast radius is contained by ADR 0056: the next tool call rides
`withSession` → `reconnect` and succeeds transparently (verified empirically:
6 GETs over the retry window → one reconnecting/reconnected diagnostic pair →
the call succeeds). But the residual cost is real:

- reconnect = fresh `initialize` = a **new `Mcp-Session-Id`**, so all
  server-side session state (subscriptions, elicitation state) is lost;
- reconnect deliberately does not re-fetch tool/resource/prompt lists, so
  stale specs persist;
- the reconnected session re-opens the GET, so against a still-hostile
  gateway the **kill → reconnect → kill cycle repeats**, each cycle adding up
  to the dial timeout to one unlucky tool call.

The root cause is upstream: the SDK should degrade standalone-GET failure to
"no notifications" instead of failing the whole connection. Until that lands,
the operator of a GET-hostile gateway needs a local escape hatch.

## Decision

Add a **per-server opt-out** of the standalone SSE stream.

1. **`mcp.ServerConfig.DisableNotifications bool`** (default false, preserving
   ADR 0057). `Server.dial` maps it to the transport's
   `DisableStandaloneSSE` field — the exact knob the SDK documents for "the
   server doesn't properly handle GET requests for SSE streams". A server with
   the opt-out never opens the standalone GET; its session is a pure
   POST request/response channel, immune to the SSE-failure-poisons-POST
   failure mode.
2. **Operator surface: `MCP_<NAME>_DISABLE_NOTIFICATIONS=true`** on the shared
   `--mcp-server` flag (`internal/cliconfig`). It rides the existing
   name-derived env convention (`MCP_<NAME>_TOKEN`), so all three mains
   (mecated, mecatequi, mecak8s) inherit it with no new flag. An unparseable
   value is a fail-loud parse error (a typo must not silently keep the churny
   stream on); unset/empty keeps the ADR 0057 default.
3. The ToolHive-discovery, ACP client-provided, and agent-def inline
   construction sites gain no new knob: ToolHive proxy workloads terminate the
   GET correctly (the happy case the stream exists for), and the ACP/inline
   surfaces have no per-server env channel. The field is available on the
   struct for any future wiring.

The opt-out is **per-server by design**: one hostile gateway must not cost the
other servers their list-changed notifications.

## Consequences

- **A disabled server's list-changed notifications no longer arrive** — its
  cached tool/resource/prompt snapshot is static until a reconnect (the
  pre-ADR-0057 contract). This is the accepted trade: notifications on that
  server were already undeliverable in practice; what changes is the churn
  stops.
- **A disabled server pays no SSE goroutine** (ADR 0027 List 1 row 28 is
  now "only when enabled") and no reconnect latency spikes from GET hostility.
- **The default is unchanged**: servers without the env var keep the ADR 0057
  stream and its notification-driven lazy refresh.
- The characterization test
  (`TestGetHostileGatewayReconnectsTransparently`) locks in the ADR 0056
  mitigation so an upstream SDK change that alters the failure shape fails
  loudly here; the fix test
  (`TestDisableNotificationsOptsOutOfStandaloneGET`) asserts zero GETs, a
  first-try call success, and no reconnect diagnostic under the opt-out.
- A future upstream fix (degrading GET failure to "no notifications") would
  make this knob a no-op-cost default; it stays as the explicit opt-out for
  operators who simply do not want the persistent connection.

## See also

- [ADR 0056](./0056-mcp-client-reconnect.md) — the reconnect-on-drop layer
  that makes the GET-hostile failure recoverable instead of fatal.
- [ADR 0057](./0057-mcp-server-notifications.md) — the notification stream
  this ADR makes per-server optional.
- [ADR 0082](./0082-factory-mcp-wiring.md) — the shared `--mcp-server` flag +
  `MCP_<NAME>_*` env convention the opt-out rides.
- `user-docs/building/what-you-get/mcp-client.md` — the operator-facing documentation.
