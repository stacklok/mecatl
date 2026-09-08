# ADR 0316 — Process-bound remote MCP broker attachments

- Status: Proposed
- Date: 2026-09-06
- Scope: initial remote MCP broker transport, attachment handles, and failure semantics
- Supersedes: None
- Superseded by: ADR 0318

## Context

The initial production broker needs a remote adapter without importing the later
distributed-broker design. A transport outage does not prove whether a dispatched MCP
mutation ran, and a replacement broker process cannot recover process-local outer
authorization correlation. Treating replicas as interchangeable would let stale handles
act on a new authority or cause an Execute to be replayed after an uncertain response.

The adapter also retains attachment handles beyond one RPC. Unbounded handles and cleanup
workers would violate the resource-lifecycle discipline in ADR 0027.

## Decision

Bind every attachment and operation to one random broker-process incarnation. The first
successful Attach pins a client to that incarnation. A later Attach on that client and all
handle, authorization, enrollment, execution, and bound-delete operations carry the pin;
a different process rejects them before broker state access. A new pre-prompt enrollment
may use a new client after discarding its stale outer correlation. A live protected-call
authorization never rebinds.

Use positive finite configuration for connection establishment, ordinary RPCs, Execute,
handle idle retention, cleanup cadence, and cleanup calls. Caller cancellation releases
only that RPC. Explicit Close and idle reclamation remove and close an attachment without
deleting its logical broker session.

Never install an application retry for Execute. `Aborted` after possible dispatch becomes
one fixed model-visible ambiguous-outcome tool error. It is not success and does not replay,
reattach, or recreate authorization. A later model call is a new ordinary invocation.
Transport `Unavailable` remains distinct from a broker-confirmed state-loss sentinel.

### Resource ledger amendment to ADR 0027

| Resource | Owner and scope | Cleanup | Restart / reattach decision |
|---|---|---|---|
| Remote server attachment registry | one `mcpbrokergrpc.Server`; process incarnation; bounded by `MaxHandles` | explicit handle Close/Abort, idle sweep, then `Server.Shutdown`; each close has `CleanupTimeout` | **reset by design**; handles never cross process incarnation |
| Attachment idle sweeper goroutine | one `mcpbrokergrpc.Server` | `Server.Shutdown` signals and joins it within the caller deadline | **reset by design**; no state is reconstructed |
| Remote gRPC client connection | caller that invoked `mcpbrokergrpc.Dial` | caller closes the returned connection | reconnect may target only the same pinned incarnation; replacement requires a new client and only the pre-prompt seam may re-enroll |

No registry entry is a durable transaction or logical deletion record. The underlying
`mcpbroker.Service` remains authoritative for logical session state.

## Consequences

Network failure and restart are explicit interruption boundaries rather than continuity
claims. Cancellation cannot promise that an upstream effect stopped. Orphan cleanup is
bounded and does not delete broker state. This initial topology remains single-process and
does not provide callback failover, restart durability, exactly-once external effects, or
high availability.

## See also

- [ADR 0027 — Cloud-native deployment](./0027-cloud-native.md)
- [ADR 0311 — ToolHive-owned multi-upstream MCP broker OAuth](./0311-per-upstream-mcp-broker-oauth-grants.md)
- [Initial production MCP broker acceptance plan](../acceptance/initial-production-mcp-broker.md)
- [Architecture guide](../architecture.md)
