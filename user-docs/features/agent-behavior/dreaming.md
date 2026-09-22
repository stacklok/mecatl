---
slug: /features/dreaming
sidebar_position: 240
title: Dreaming and memory consolidation
description:
  Consolidate project memory and the user model with bounded, reviewed
  operations.
---

# Dreaming and memory consolidation

Consolidation removes duplicate memory entries and can propose synthesized
replacements for review. It is separate from completed-run learning and does not
change `learning.mode`.

## Availability

Two maintenance options are available:

- **Automatic consolidation** is optional, off by default, and intended for
  exact duplicate cleanup on supported local stores.
- **Manual review** is available in `mecatui` through `/dream` when the server
  advertises a planner and reviewed atomic operations for the selected store.

Both can target either per-project memory or the cross-project user model. The
target is selected explicitly; a project-memory operation cannot silently apply
to the user model.

## Automatic consolidation

Enable the two scopes independently:

```sh
mecated serve \
  --memory-consolidate-interval 24h \
  --user-model-consolidate-interval 24h \
  --memory-dir "$HOME/.local/state/mecatl/memory" \
  --user-model-dir "$HOME/.local/state/mecatl/usermodel"
```

Automatic application is narrow. The local file-backed store may only tombstone
a source when its active value and description are byte-identical to the
displayed survivor, and it must compare the expected versions atomically. The
survivor is never rewritten. Synthesized replacements are not applied by this
unattended path.

These intervals do not change `learning.mode`. A project setting of
`learning.mode: off` cannot suppress an explicit operator consolidation
schedule. Zero disables the corresponding schedule.

## Manual `/dream` review

In `mecatui`, `/dream` opens the reviewed maintenance flow when the server
advertises it. See [Commands and memory](/mecatui/commands-and-memory.md) for
the TUI workflow. This page covers consolidation behavior, authorization, and
storage limitations.

The review shows the proposed survivor, source entries, replacement text,
reasons, and evidence.

The operator applies or dismisses the entire plan. The receipt reports planned,
applied, conflicted, skipped, and failed source counts. There are no per-source
toggles, grouped transactions, or grouped undo operations.

Regeneration is explicit and makes another provider call. Applying an approved
synthesis atomically rewrites the displayed survivor to the displayed
replacement and tombstones the displayed sources. Exact duplicates preserve the
survivor unchanged. Hidden controls and Unicode format characters in
model-authored replacement or reason text reject the plan before it can be
retained.

The plan is process-local and short-lived. Generate a new plan after it expires
or the process or replica changes. If application is still running or its result
is uncertain, use the same decision and plan ID to retrieve the receipt. Do not
submit the opposite decision.

## Safety and authorization

Manual dreaming is a maintenance authorization, not an ordinary memory write. It
is unavailable when:

- ownership enforcement is enabled;
- no planner is configured;
- the selected store lacks reviewed atomic consolidation.

Remote memory drivers provide the mandatory lifecycle/CAS contract but do not add
the separately required reviewed atomic consolidation. The target store must remain
the same supported store through planning and
application. Version checks turn concurrent edits into conflicts rather than
silently overwriting newer facts. Storage-wide maintenance also requires the
normal management authorization and a working cross-process lease where the
backend is shareable.

The model never receives raw secrets, tool arguments, or hidden control data as
part of the review. Manual review does not expose provider and model identity or
recall-usage telemetry.

## Project memory and user model

|Target|Scope|Typical content|
|-|-|-|
|Project memory|One workspace/project|Repository conventions, project decisions, local facts|
|User model|Cross-project `user/` namespace|Operator preferences and durable personal facts|

Project operations require a convergence-capable project store for the exact
trusted configured workspace. Candidates from another or alternate workspace may
remain staged and inspectable but cannot approve, undo, or change the configured
project's memory. User-model operations use the configured user-model store.

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
- Manual planning requires a model call.

For the underlying memory tools, tiers, lifecycle versions, and learning
boundary, see [Memory and user model](/features/agent-behavior/memory.md). For the
broader `/dream` UI and receipt behavior, see
[mecatui memory commands](/mecatui/commands-and-memory.md#review-and-maintain-memory).

## Next steps

- [Learning](/features/agent-behavior/learning.md)
- [Memory and user model](/features/agent-behavior/memory.md)
- [Session continuity](/features/sessions/session-continuity.md)
