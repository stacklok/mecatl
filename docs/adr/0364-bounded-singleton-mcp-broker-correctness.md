# ADR 0364 — Bounded singleton MCP broker correctness

- Status: Accepted
- Date: 2026-09-07
- Scope: completion of the single-replica broker's transport, admission, readiness, and deployment contracts
- Supersedes: ADR 0362
- Superseded by: None

## Context

ADR 0362 selected process-bound remote attachments and prohibited application retries after
an uncertain Execute. Implementation review found that this was not sufficient to make the
single-replica service honest under lost responses: lifecycle outcomes were not retained for
the whole lease, Execute requests had no bounded receipt keyed by their call identity, and
some error identities depended on gRPC status text. The same review found unbounded logical
session admission, no production JWKS readiness probe, incomplete HTTP limits, and deployment
values that did not represent the client and network boundaries described by ADR 0363.

The broker remains deliberately non-HA. These corrections must not introduce a durable outer
broker, replica interchangeability, or an exactly-once claim for upstream side effects.

## Decision

Keep one process incarnation and one `Recreate` replica, but make the process-local contract
complete:

1. Retain immutable terminal Close and Abort receipts until an absolute, non-renewable handle
   lease expires. Concurrent duplicates wait for the first operation and receive its exact
   outcome. After reclamation, return a structured `state_unavailable` reason.
2. Give Execute a bounded process-local receipt keyed by incarnation, handle, and call ID. The
   first request pins a digest of tool name, item identity, and arguments; a duplicate with the
   same digest waits for or receives the first response without dispatching again, while a
   mismatched reuse fails closed. Receipt expiry returns `state_unavailable`. This gives
   at-most-once dispatch within one broker process and lease, not exactly-once upstream effects.
3. Mark failures proven to occur before dispatch with server-authenticated response metadata.
   Every other missing Execute response is ambiguous. Validate the returned call ID against the
   request before admitting a result.
4. Replace status-message inference with a closed, versioned structured gRPC error-reason
   vocabulary. Use method-specific protobuf request and response messages and preserve unknown
   protocol values as failures rather than coercing them.
5. Let composition create a fresh remote client only for confirmed pre-prompt state loss. The
   replacement owns and closes its connection; live protected-call continuations never rebind.
6. Bound logical session IDs, total retained sessions, and unattached retention with positive,
   operator-configurable limits. Capacity refusal and expiry are structured and never evict an
   active or attached session.
7. Production readiness includes a bounded, side-effect-free verifier health/freshness check,
   profile validation, ToolHive construction/discovery, and protected-route validation. The
   health check neither performs login/tool execution nor silently extends key acceptance.
8. Apply one canonical protected-profile URL validator to every configuration path. Protected
   upstream, issuer, authorization, token, and callback endpoints require canonical HTTPS.
   Loopback HTTP relaxation exists only in test-compiled helpers.
9. Bound public HTTP headers, reads, writes, idle connections, and route bodies without making
   the configured Execute deadline unreachable. Test the combined HTTP/2 gRPC and callback
   listener rather than relying on source inspection.
10. Make deployment values match the topology. Because gRPC and browser callbacks share one
    public port, expose one honest `publicFrom` peer union and do not claim vanilla NetworkPolicy
    can separate routes at layer 7. Require an all-or-none mecak8s remote-broker address, CA, DNS
    name, and audience-bound projected token configuration; digest-only production images; pinned
    build-tool versions; and step-environment handling for registry credentials.

### Resource ledger amendment to ADR 0027

| Resource | Owner and scope | Bound and cleanup | Restart / reattach decision |
|---|---|---|---|
| Attachment, lifecycle-receipt, and Execute-receipt registries | one `mcpbrokergrpc.Server`; one process incarnation | configured capacities and absolute leases; active operations delay reclamation; shutdown joins cleanup and closes attachments | **reset by design**; no receipt or handle crosses an incarnation |
| Logical-session and broker-control registries | one `mcpbroker.Runtime`; one process incarnation | bounded IDs, capacity, and unattached retention; active/attached/parked authority is never evicted; shutdown clears process state | **reset by design**; ToolHive inner storage does not recreate outer authority |
| Registry sweepers and timers | owning server/runtime | finite cadence and work per cycle; owner shutdown signals and joins every worker | **reset by design** |
| Workload verifier, JWKS cache, and readiness health operation | one `mcpbrokerserver.Server` | finite cache staleness, fetch/body/deadline bounds; readiness calls are side-effect-free; server close releases verifier resources | **derive on startup** from trusted configuration; no accepted-key extension by health polling |
| Replacement remote client and gRPC connection | per broker binding generation, owned by composition | old generation closes exactly once before publication of a fresh pre-prompt client; application shutdown closes current generation | **recreate only before a prompt** after confirmed loss; never rebind a protected continuation |
| Drain coordinator, active-operation registry, and propagation waiter | one `cmd/mecabroker` process | admission closes first; finite propagation wait is joined; drain cancels remaining operations and closes listeners/resources in order | **reset by design** |
| Drain coordinator (`mcpbrokerserver.New`), active operations, and propagation waiter | one `cmd/mecabroker` process; coordinator owns the public admission gate and operation context set | admission closes atomically; propagation delay and drain timeout are positive bounded durations; SIGTERM joins the waiter before final cancellation | **reset by design**; no drain state is reattached |
| Broker gRPC sweepers and lifecycle/Execute receipt timers (`mcpbrokergrpc.NewServerWithConfig`) | one broker incarnation | sweep interval and per-registry capacities are configured; active operations pin entries until terminal cleanup; `Shutdown` closes `stop`, joins `done`/operation waiters, then closes attachments | **reset by design** |
| Pending enrollment/control entries and ToolHive callback correlation (`mcpbroker.Runtime`) | one runtime process | bounded pending-control capacity and expiry; `Runtime.Close` stops and joins collection before ToolHive close | **reset by design**; callbacks from a prior incarnation are rejected |
| Readiness verifier operation and OIDC/JWKS health resources (`cmd/mecabroker` production assembly) | broker process, shared with readiness probe | bounded fetch/body/deadline and verifier staleness; readiness performs no login or tool execution; `Built.Close` closes verifier-owned transports after admission stops | **derive on startup**; health polling never extends key freshness |
| Replacement remote clients (`cmd/mecak8s.mcpBrokerFactory`) | one composition-owned client per broker generation | confirmed pre-prompt state loss closes the old gRPC connection before publishing a replacement; `Built.Close`/command shutdown closes the current connection | **recreate only before a prompt**; parked protected calls never rebind |
| ToolHive process and confidential broker-client material | one broker process | closed after admission and public listeners; secret remains only in process memory | **reset by design**; restart interrupts outer callback correlation |

ADR 0363 remains the topology decision; this amendment completes its resource inventory and the
ADR 0027 lifecycle accounting without rewriting that frozen historical ADR.

## Consequences

Lost-response retries are deterministic within one process and bounded lease, while failures
outside that boundary remain explicit. The receipt and session registries consume bounded
memory and add synchronization. Protobuf descriptor changes require regenerated bindings and
may require coordinated internal client/server rollout, although the broker protocol remains
repository-internal for this release.

Readiness becomes dependent on live identity infrastructure and can remove an otherwise-running
pod from service. Digest-only production installs and explicit network/client identity values
require more operator input, but avoid silently mutable or ineffective deployment settings.
None of these changes make replicas interchangeable or make upstream mutations exactly once.

## See also

- [ADR 0362 — Process-bound remote MCP broker attachments](./0362-process-bound-remote-mcp-broker.md)
- [ADR 0363 — Single-replica production topology](./0363-single-replica-mcp-broker-topology.md)
- [Initial production MCP broker acceptance record](../acceptance/initial-production-mcp-broker.md)
