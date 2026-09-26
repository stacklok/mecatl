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
- The chart optionally renders a singleton credential Redis StatefulSet backed by
  a retained PVC. Redis uses AOF `everysec`, so the store is durable but not HA or
  zero-loss; a failure can lose roughly one second of recent writes. ADR 0027 List
  1 row 84 records its owner, cleanup, and restart disposition.

## Resource ledger amendment to ADR 0027

ADR 0027 List 1 rows 79-83 inventory the protected Redis client, encrypted
storage decorator and KEK ring, custody core, continuity receipt registry, and
host recovery-attempt map. Row 84 inventories the optional chart-managed Redis
StatefulSet, Service, NetworkPolicy, and retained PVC. The Helm release owns
those Kubernetes resources; uninstall removes the workload and service while
retaining the PVC for operator-owned deletion. Broker `Recreate` rollouts do not
restart the credential Redis StatefulSet. The related List 2 references are
rows 48-50.

### Credential envelope key derivation

For each credential envelope, the broker draws a fresh 32-byte salt and uses
HKDF-SHA256 with the selected KEK and domain-separated format metadata to derive
a 32-byte AES key. The derived key encrypts one payload with AES-256-GCM, a fresh
12-byte nonce, and the existing logical-record AAD. The envelope stores the key
ID, salt, nonce, and ciphertext; the KEK is used for derivation, not directly as
an AES-GCM key. The salt makes accidental derived-key reuse negligible at
expected volumes, but not impossible. At 2^28 seals, the chance of any repeated
256-bit salt is approximately 2^-201; distinct salts can also theoretically
produce the same 256-bit HKDF output.

The key ID selects the KEK used to open an envelope. New envelopes use the
active key, while operators retain prior key IDs for records that have not been
resealed. Changing the active key does not rewrite existing records; a later
ordinary write reseals that record. This envelope format is unshipped and has no
legacy reader. A future KMS-backed derivation requires a new envelope version.

## See also

- [ADR 0364 — Bounded singleton MCP broker correctness](./0364-bounded-singleton-mcp-broker-correctness.md)

## References

- `internal/adapter/mcpbroker/toolhive_credential_custody.go`
- `internal/adapter/mcpbroker/toolhive_credential_envelope.go`
- `internal/adapter/mcpbroker/toolhive_protected_storage.go`
- `deploy/helm/mecak8s/templates/credential-redis.yaml`
- `internal/adapter/server/workspace_enrollment.go`
- `internal/mcpbroker/continuity.go`
