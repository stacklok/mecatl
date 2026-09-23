# ADR 0228 — Manual dream review

- Status: Accepted
- Date: 2026-08-17
- Scope: manual project-memory and user-model consolidation review, server APIs, and mecatui `/dream`.
- Supersedes: only [ADR 0227](./0227-dream-consolidation-safety-boundary.md)'s statement that inspectable manual review is future work.
- Superseded by: none

## Context

ADR 0227 made unattended consolidation safe by limiting automatic mutation to exact-duplicate
retirement, but deliberately deferred inspection and approval of useful non-identical proposals.
Operators need a separate maintenance workflow that shows exactly what a model proposed before any
model-authored replacement reaches durable memory. This is not completed-trajectory reflection or
learning: it operates on the current contents of a selected memory store and must not change the
learning mode or the separately authorized schedules.

A review can outlive the generation RPC, so retaining its version-bound mutation material creates
process state. Persisting that sensitive, short-lived material or accepting edited operations from a
client would enlarge the v1 trust and recovery boundary. The local memory adapter can instead apply
one reviewed synthesis atomically while preserving independent-operation progress.

## Decision

Add an explicit manual flow: choose `project_memory` or `user_model`, acknowledge that generation
sends the selected bounded memory values and descriptions to the configured model and spends tokens,
review the returned operations, then apply or dismiss the whole retained plan. Regeneration is an
explicit new provider call and spends tokens again.

Use one strict planner grammar with exactly two required operation families:
`exact_duplicates` and `synthesized_replacements`. Exact-duplicate operations identify an existing
survivor and existing sources and contain no replacement content. Synthesized-replacement operations
also contain the complete replacement value and description. Reject unknown or missing members,
trailing content, repeated/cross-role keys, unknown keys, unchanged synthesis, and standalone deletion.
Automatic schedules continue to apply only byte-identical exact duplicates and remain off by default;
they never apply synthesized replacements.

On human-approved apply, use the authoritative retained plan rather than client-supplied operation
material. For each synthesis operation, atomically compare the bound inspected versions for the
displayed survivor and sources, rewrite that displayed survivor to the displayed replacement, and tombstone all displayed
sources. Exact duplicates retain the ADR 0227 operation. Operations are independent: conflicts or
failures in one do not roll back successful operations in another. Apply and dismiss are whole-plan
decisions; v1 has no per-source toggles and claims neither grouped atomicity nor grouped undo.

Retain plans in a bounded Build-owned registry under opaque random process-local IDs. Bound total
records and pending records per target, expire them by TTL with lazy cleanup, and perform no client
operation or cleanup goroutine. Decisions are idempotent for the same decision; an opposite or
concurrent decision conflicts. Terminal records retain only the decision and receipt: dispose of the
plan and review content immediately.

Do not persist or replicate pending plans. Restart, expiry, or routing a decision to another replica
loses the plan and requires explicit regeneration and another provider spend. This v1 protocol is not
HA- or sticky-route-portable. Disable manual dreaming while ownership enforcement is enabled; v1 does
not define caller partitioning for deployment-owned project-memory and process-global user-model
targets. Expose per-target availability through server capabilities. Do not add recall-usage
telemetry, provider/model display, durable plans, or client-authored operations.

## Consequences

Operators can inspect exact and synthesized maintenance proposals before mutation while unattended
maintenance keeps its narrower exact-duplicate-only boundary. Each accepted synthesis is atomic at
its own operation boundary and preserves lifecycle history for retired sources, but a receipt may
honestly be partial across independent operations. Dismiss performs no mutation.

The workflow is deliberately single-process. A lost or wrong-replica plan cannot be recovered, and
regeneration can produce a different plan while spending tokens again. Ownership-enforced and remote
stores without both reviewed atomic capabilities do not expose the manual action in v1. Recall
frequency does not influence planning because no recall counters are collected.

## See also

- [Memory architecture](../architecture/memory.md)
- [mecatui](../tui.md)
- [gRPC API](../usage/grpc-api.md)
- [HTTP API](../usage/http-sse-api.md)
- [Implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md)
- [Production readiness](../design/PRODUCTION-READINESS.md)
- [Cloud-native resource inventory](./0027-cloud-native.md)
- [ADR 0227 — Dream consolidation safety boundary](./0227-dream-consolidation-safety-boundary.md)
