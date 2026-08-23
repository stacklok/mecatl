---
sidebar_position: 4
title: Dreaming and memory consolidation
description: Consolidate project memory and the user model with bounded, reviewed operations.
---

# Dreaming and memory consolidation

Consolidation is maintenance for facts that are already in memory. It finds
exact duplicates and, when explicitly reviewed, can propose synthesized
replacements. It is deliberately separate from completed-trajectory learning:
consolidation does not enable `learning.mode`, and learning does not silently
rewrite memory.

## Availability

Two maintenance surfaces are available:

- **Automatic consolidation** is optional, off by default, and intended for
  exact duplicate cleanup on supported local stores.
- **Manual review** is available in mecatui through `/dream` when the server
  advertises a planner and reviewed atomic operations for the selected store.

Both can target either per-project memory or the cross-project user model. The
target is selected explicitly; a project-memory operation cannot silently apply
to the user model.

## Automatic consolidation

Enable the two scopes independently:

```console
mecated serve \
  --memory-consolidate-interval 24h \
  --user-model-consolidate-interval 24h \
  --memory-dir "$HOME/.local/state/mecatl/memory" \
  --user-model-dir "$HOME/.local/state/mecatl/usermodel"
```

Automatic application is deliberately narrow. The local file-backed store may
only tombstone a source when its active value and description are byte-identical
to the displayed survivor, and it must compare the expected versions
atomically. The survivor is never rewritten. Synthesized replacements are not
applied by this unattended path.

These intervals do not change `learning.mode`. A project setting of
`learning.mode: off` cannot suppress an explicit operator consolidation
schedule. Zero disables the corresponding schedule.

## Manual `/dream` review

In mecatui, `/dream` opens the reviewed maintenance flow when the server advertises it. The exact command behavior and TUI interaction live in [Commands and memory](/mecatui/commands-and-memory.md); the consolidation semantics, authorization, and storage limitations are documented here.

The review evaluates the proposed survivor and source entries, exact duplicates,
synthesized replacements, bounded reasons and evidence, and the canonical values
needed for an informed decision.

The operator applies or dismisses the entire plan. The receipt reports planned,
applied, conflicted, skipped, and failed source counts. There are no per-source
toggles, grouped transactions, or grouped undo operations.

Regeneration is explicit and makes another provider call. Applying an approved
synthesis atomically rewrites the displayed survivor to the displayed
replacement and tombstones the displayed sources. Exact duplicates preserve the
survivor unchanged. Hidden controls and Unicode format characters in
model-authored replacement or reason text reject the plan before it can be
retained.

The plan is process-local and short-lived. Restart, expiry, or another replica
makes it unavailable and offers a fresh plan. A same-decision request that is
still applying, or an indeterminate transport error, preserves the plan ID for
explicit same-decision receipt retrieval; do not submit the opposite decision.
A known terminal conflict permits a new generation, but an opposite decision
while an apply is in progress does not.

## Safety and authorization

Manual dreaming is a maintenance authorization, not an ordinary memory write.
It is unavailable when:

- ownership enforcement is enabled;
- no planner is configured;
- the selected store lacks reviewed atomic consolidation; or
- a remote/base-only store does not positively advertise the required lifecycle
  capabilities.

The target store must remain the same supported store through planning and
application. Version checks turn concurrent edits into conflicts rather than
silently overwriting newer facts. Storage-wide maintenance also requires the
normal management authorization and a working cross-process lease where the
backend is shareable.

The model never receives raw secrets, tool arguments, or hidden control data as
part of the review rendering. Provider/model identity and recall-usage telemetry
are not exposed by the manual review surface.

## Project memory and user model

| Target | Scope | Typical content |
| --- | --- | --- |
| Project memory | One workspace/project | Repository conventions, project decisions, local facts |
| User model | Cross-project `user/` namespace | Operator preferences and durable personal facts |

Project operations require a convergence-capable project store for the exact
trusted configured workspace. Candidates from another or alternate workspace
may remain staged and inspectable but cannot approve, undo, or write launch-root
project memory. User-model operations use the configured user-model store.

## Limitations

- Automatic cleanup handles exact duplicates only; it does not synthesize or
  rewrite facts unattended.
- Manual plans are not durable and do not survive restart, expiry, or replica
  changes. Generate a fresh plan when the old one is no longer valid.
- Whole-plan application can be partial: independent operations may apply,
  conflict, skip, or fail separately.
- Manual dreaming is unavailable under ownership enforcement in the current
  release, even when a caller has ordinary access to its own memory.
- A project memory store and a user-model store are separate targets; intervals,
  plans, and conflicts do not cross between them.
- Consolidation does not provide semantic or embedding recall. Memory search
  remains BM25 lexical search, and consolidation does not create vectors.
- The feature requires bounded model calls for manual planning. It is not a
  no-network maintenance operation.

For the underlying memory tools, tiers, lifecycle versions, and learning
boundary, see [Memory & knowledge](/building/what-you-get/memory.md). For the broader
`/dream` UI and receipt behavior, see [mecatui memory commands](/mecatui/commands-and-memory.md#review-and-maintain-memory).

## Next steps

- [Learning](./learning.md)
- [Memory and knowledge](/building/what-you-get/memory.md)
- [Session continuity](./session-continuity.md)
- [Capability and deployment matrix](./capability-matrix.md)
