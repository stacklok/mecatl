# ADR 0365: Broker replacement uses fresh outer authority over retained custody

- Status: Proposed (implementation in progress)
- Date: 2026-09-23
- **Supersedes:** none; complements ADR 0364

## Context

A singleton MCP broker can be replaced while a Mecatl session remains durable. The
broker's process-local outer state (OAuth client, grant, callback/PKCE state,
logical generation, attachment handles, and execution receipts) cannot safely be
reconstructed. The encrypted ToolHive custody record, however, can retain the
exact upstream credential set for the lifetime established by ToolHive.

## Decision

Broker replacement recovery is a confirmed, pre-prompt operation only. The host
must first receive structured broker-instance-loss, hold the ordinary run-entry
and mutation controls, and compare the current durable session, workload,
configuration guard, and custody. The replacement creates fresh process-local
outer authority and a complete provisional catalogue. The host adopts only the
fresh opaque binding and tool names, saves the durable candidate, then commits
and publishes the attachment. Pending browser flows, parked calls, and uncertain
Execute calls are interrupted rather than resumed.

Custody remains subordinate evidence: it never authorizes recovery without the
current durable session and presenting verified workload. It is encrypted at the
ToolHive storage boundary and expires at the earliest finite native ToolHive
session expiry; refresh and recovery do not extend that expiry. The replacement
may issue only an adapter-private, access-only bearer for the exact retained
ToolHive session identity, bounded to two minutes and without a refresh token or
external recovery grant. A copied bearer may remain valid until that fixed expiry.

Outer broker state therefore resets on replacement; encrypted inner custody
permits fresh authority without claiming broker HA, takeover, exactly-once
execution, distributed revocation, or callback failover. Invalidation removes
host authority before best-effort custody tombstoning and exact broker cleanup.
Forks, clear successors, and carryover sessions never copy custody.

## Consequences

- Save-before-publish and exact-binding reattach are required around crashes and
  ambiguous Commit results.
- Stage/Recover retry receipts are bounded and process-local; replacement loses
  them.
- Protected continuation paths fail closed and cannot enter recovery.
- Operators must treat the singleton broker and its protected Redis as one
  operational durability boundary; this ADR does not add managed-Redis resources.

## See also

- [ADR 0364 — Bounded singleton MCP broker correctness](./0364-bounded-singleton-mcp-broker-correctness.md)

## References

- `internal/adapter/mcpbroker/toolhive_credential_custody.go`
- `internal/adapter/server/workspace_enrollment.go`
- `internal/mcpbroker/continuity.go`
