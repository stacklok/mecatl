# ADR 0227 — Dream consolidation safety boundary

- Status: Accepted
- Date: 2026-08-17
- Scope: `internal/adapter/dream`, background memory/user-model maintenance, and operator-facing consolidation behavior.
- Supersedes: the model-authored tightening portions of [ADR 0009](./0009-tiered-memory.md), [ADR 0107](./0107-operator-profile-memory-lifecycle.md)'s base-six-operation consolidation decision, and the base-store legacy-application portion of [ADR 0109](./0109-staged-learning-proposals.md).
- Superseded by: none

## Context

The original consolidation description allowed a model to merge, tighten, or drop memory through a
single maintenance call. That was too broad for unattended mutation: model-authored replacement text,
standalone deletion, stale plans, and base-store mutation make the safety boundary hard to inspect and
recover from. Consolidation is still useful for exact duplicate cleanup, but it must not substitute for
an operator review workflow.

## Decision

Split consolidation into `GeneratePlan` and `ApplyPlan`. `Consolidate` remains the compatible
one-call orchestration and runs both phases serially.

Planning is read-only. The planner accepts exactly one bare whole JSON object and may propose only
existing-key survivor/superseded relationships. It cannot provide replacement text or a standalone
deletion. Candidate selection is deterministic within one process: keys are sorted and a rotating
cursor is bounded by entry count and aggregate encoded bytes. Entries are sent whole rather than
truncated. For a stable, finite set during one continuously running consolidator, every fitting entry
is eventually selected; insertion, removal, oversized values, and restart can change that result. The
cursor resets on restart.

Where available, plans bind the exact lifecycle versions inspected during planning. Automatic
application requires the internal atomic duplicate-retirement capability implemented by the local
file-backed store. For each source, that operation compares both the survivor and source versions and
tombstones the source in one backend transaction. It retires only source entries whose complete active
value and description are byte-identical to the chosen survivor. The retirement appends lifecycle
history and never rewrites the survivor. Base stores and convergence-only remote stores without this
atomic operation, plus non-identical proposals, are skipped pending future inspectable manual review.

A source retirement is atomic, but a plan is not a batch transaction. Application continues independent
operations and reports planned, applied, conflicted, skipped, and failed counts honestly. A
consolidator serializes its own plan/apply/consolidate calls only; it is not a multi-replica claim or
coordination mechanism. Periodic reports contain counts only. Consolidation intervals remain off by
default, and ownership enforcement continues to disable dreaming.

## Consequences

Unattended consolidation can remove only exact duplicates through a local store that atomically
checks both bound versions and retires the source, preserving
an auditable source history and avoiding model-authored replacement content. Operators cannot yet
inspect or manually apply a proposed non-identical consolidation, and no `/dream` command or
recall-usage telemetry ships in this increment. A deployment with multiple replicas must not infer
cross-replica fairness or mutual exclusion from the process-local cursor and gate.

## See also

- [Memory architecture](../architecture/memory.md)
- [Implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md)
- [Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md)
- [Cloud-native resource inventory](./0027-cloud-native.md)
- [ADR 0009 — Tiered memory](./0009-tiered-memory.md)
- [ADR 0109 — Evidence-backed reflection and durable staged learning](./0109-staged-learning-proposals.md)
