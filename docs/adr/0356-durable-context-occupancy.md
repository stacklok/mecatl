# ADR 0356 — Durable context occupancy in session snapshots

- Status: Proposed
- Date: 2026-09-23
- Scope: session aggregate state, snapshot persistence, `mecatl.v1.Session`, and resumed mecatui status metrics.
- Supersedes: none

## Context

Mecatui’s status line has two different usage views. The context meter needs the input-token count for the latest completed model turn; the input, output, and cache counters need lifetime main-session totals. The latter is already canonical durable `token_usage[main].total` under ADR 0307. The former currently exists only on the live `turn.end` event and in mecatui’s process-local state.

When an operator resumes or selects an existing chat, the client receives the authoritative transcript and a session snapshot. It can receive the resolved model and durable token ledger, but it cannot truthfully reconstruct latest-turn context occupancy. Lifetime input cannot stand in for the meter numerator: it grows across turns and survives compaction, while the meter describes the current model request. Event-log replay is not an authoritative transcript or snapshot source and may be incomplete; making the client replay it would also create a different result for otherwise equivalent stores.

Persisting an arbitrary client-local value would make the server snapshot less authoritative and create divergent results across mecatui instances. The datum belongs to the session aggregate because it is established by a completed main-agent model turn and must survive snapshot load, store migration, and restart. This extends the accounting record in ADR 0307 without changing its lifetime-ledger or budget decisions.

## Decision

Persist one optional `latest_context_occupancy` value in every canonical session snapshot. It is a display-state value with `input_tokens` and `estimated`: the most recent non-zero context-meter numerator established by a completed main-agent turn, retaining the existing display-only fallback estimate marker. It is not provider accounting, a run budget baseline, or a replacement for canonical `token_usage[main].total`.

After a main-agent turn successfully records its assistant message, the session aggregate applies the same display rule as the live context meter: a non-zero `turn.end` input count replaces `latest_context_occupancy`, including its `estimated` marker; a zero count retains an existing value and does not create one. The value is saved and restored only with an existing coherent authoritative snapshot boundary; this decision adds no per-turn save or event-log write. No event-log scan, transcript calculation, cumulative token total, model-window estimate, or client cache may synthesize it. Auxiliary model work, including title generation, failed/cancelled streams, and successful turns with no non-zero context numerator do not replace it.

Expose the value as an additive, presence-aware `ContextOccupancy latest_context_occupancy = 22` field on `mecatl.v1.Session`; `ContextOccupancy` contains `int64 input_tokens = 1` and `bool estimated = 2`. Continue exposing `token_usage` and `resolved_model` through the existing session snapshot/GetSession path; do not add a status-specific RPC or event. The server’s normal ownership checks continue to protect all three projections.

Missing presence is meaningful: legacy snapshots, older servers, and sessions with no completed main turn that established a non-zero numerator have unknown context occupancy. Consumers must leave it unknown in that case, even if the durable cumulative ledger is populated. The server does not rewrite legacy snapshots merely to add this field. A subsequent qualifying turn establishes it.

## Consequences

A newly attached mecatui instance can render the resolved context window, the same latest known context occupancy (including its estimated hint), and cumulative main-session counters before another prompt is sent. Every in-tree store, remote driver, snapshot mapper, protobuf binding, and restore path must preserve optional presence and exact values. The client must separately project lifetime main usage and context-occupancy display state, preserving their distinct meanings, and must apply a snapshot’s metrics only while adopting that snapshot for a session generation so a late same-session refetch cannot overwrite newer live events.

Snapshots grow by one optional `ContextOccupancy` message and public protobuf API gains an additive field. Consumers that do not understand it remain compatible; newer consumers must visibly handle the honest unknown state. Existing snapshot persistence frequency is unchanged; the field is serialized at the next normal coherent save and does not provide a historical per-turn accounting query.

The datum can be stale only in the same sense as any persisted session snapshot after an interrupted turn: it is the latest **saved** display-state measurement, not an in-progress estimate. Pricing, historical per-turn analytics, raw provider-only latest-turn reporting, and compaction policy remain outside this decision.

## See also

- [ADR 0307 — Canonical durable token accounting and run-scoped budgets](./0307-canonical-durable-token-accounting.md)
- [ADR 0217 — Session discovery uses durable kind metadata and an authoritative transcript](./0217-session-discovery-continuation.md)
- [Context management and compaction](../architecture/context-and-compaction.md)
- [Resumable session status metrics acceptance plan](../acceptance/resumable-session-status-metrics.md)
- [Issue #1822](https://github.com/stacklok/mecatl/issues/1822)
