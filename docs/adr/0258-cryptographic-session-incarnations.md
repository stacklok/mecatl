# ADR 0258 — Cryptographic session and lineage incarnations

- Status: Accepted
- Date: 2026-08-31
- Scope: session identity, durable lineage, and debugger handle correlation
- Supersedes: ADR 0257's timestamp-derived incarnation identity and ID-only lineage edges
- Superseded by: —

## Context

ADR 0257 called an ID/time/owner digest an incarnation. Recreating a session with the same ID, timestamp, and owner reproduced that digest, so an old debugger binding, lineage edge, or delegation event could attach to a different lifetime of the same storage key. Child relationships also named only a parent ID, allowing descendants from an old root lifetime to appear under a recreated root.

## Decision

Every `session.New` mints an immutable opaque `session.IncarnationID` from 128 bits of `crypto/rand`, encoded as unpadded lowercase base32 with an `inc_` prefix. It contains no ID, timestamp, owner, sequence, or other metadata. Snapshot and event-source creation metadata restore the exact value. A snapshot predating the field restores to a deterministic SHA-256 `legacy_` token over its old ID/time/owner facts; the reserved prefix is disjoint from every newly minted value. Invalid persisted syntax fails closed.

`session.IncarnationFingerprint` is an internal domain-separated digest of the opaque incarnation, session ID, and non-reversible owner scope. It no longer consumes creation time. Debug target fingerprints and opaque evidence handles use that binding, while normal client and model projections never receive the incarnation.

`session.SessionRelationship` carries the related lifetime as well as its ID: parent incarnation for subagents, parallel branches, and tool-driven team members; origin incarnation for scheduled sessions; target incarnation for debug sessions. New related-session constructors require these values whenever the related ID is present. Legacy restored relationships may lack them, but cannot participate in incarnation-bound traversal.

Durable lineage records remain keyed by `(session ID, incarnation)`. `port.SessionLineageQuery` requires `(root ID, root incarnation)`: stores return all root-key tombstones for audit, but return descendants only when the relationship's ID and incarnation both match. Memstore, JSONL, Redis, and the gRPC driver preserve this rule across restart and recreation; Redis updates snapshot metadata and lineage in one script, while JSONL retains its lock/reconciliation protocol.

Typed delegation events persist the child/member incarnation where the producer has constructed the child. Event-log fallback attaches a child only when the event carries a valid incarnation and the loaded child's ID, incarnation, owner, kind, and complete relationship all match. Legacy or pre-start events without an incarnation can describe lifecycle activity but cannot mint an inspectable child handle.

## Consequences

Two sessions with identical ID, timestamp, owner, labels, and relationship are different lifetimes. Recreating a root does not inherit old descendants; its old root tombstone remains queryable. Recreating a child cannot satisfy an old scope/history handle or delegation event. Legacy snapshots remain loadable under a deterministic, visibly legacy identity, while legacy ID-only edges degrade without traversal rather than guessing.

The related-session constructor and lineage-query signature changes are breaking engine API changes (pre-v1 minor). Incarnations are internal correlation capabilities, not user-facing identifiers.

## See also

- [ADR 0257](./0257-session-debugger-hardening.md)
- [Architecture overview](../architecture.md)
- [Engine compatibility contract](../../engine/COMPATIBILITY.md)
