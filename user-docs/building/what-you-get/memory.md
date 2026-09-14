---
sidebar_position: 5
title: Memory and knowledge
description:
  Choose how Mecatl retains project context, operator preferences, and agent
  identity.
---

# Memory and knowledge

Mecatl separates project knowledge, agent identity, and operator preferences so
you can configure each independently.

|System|Scope|Purpose|Agent-writable?|
|-|-|-|-|
|Project memory|One workspace|Retain project facts across sessions|Yes|
|Soul|All sessions that load the file|Set the agent's persona and working style|No|
|User model|All workspaces for one operator|Retain operator facts and preferences|Yes|

For configuration and workflows, see
[Skills, commands, and soul](/features/skills-commands-and-soul.md),
[Learning](/features/learning.md), and
[Dreaming and memory consolidation](/features/dreaming.md).

## Project memory

Project memory is enabled by default. Each workspace gets a separate store that
persists across sessions.

Mecatl keeps memory at three levels:

|Level|Content|Access|
|-|-|-|
|Index|Up to 200 keys and descriptions|Included at session start|
|Entry|The complete value for one key|`Recall`|
|Archive|Entries outside the index|`SearchMemory`|

Entries contain a key, value, and optional one-line description. If you omit the
description, Mecatl uses the value's first line in the index. Entries that leave
the index remain searchable.

### Memory tools

|Tool|Scope|Purpose|
|-|-|-|
|`Remember`|Project|Store an entry.|
|`Recall`|Project|Read an entry or list keys by prefix.|
|`SearchMemory`|Project|Search entries with BM25 keyword matching.|
|`RememberUser`|User|Store an operator fact.|
|`RecallUser`|User|Read an operator fact.|
|`SearchUserModel`|User|Search operator facts with BM25.|
|`InspectMemory` / `InspectUserMemory`|Both|Read the current version, provenance, timestamps, and history.|
|`ForgetMemory` / `ForgetUserMemory`|Both|Write a reversible tombstone after approval by default.|
|`UndoMemory` / `UndoUserMemory`|Both|Restore the previous state with a new revision.|

Versioned lifecycle tools appear only when the store supports complete history.
For `Remember`, omit `expected_version` to overwrite the current value or supply
the current token for a compare-and-swap update. A stale token returns a
conflict. `Forget` and `Undo` require the exact `expected_version` from a
same-scope `Recall`, `Inspect`, or mutation result. Inspect first when the
version might be stale. Treat the token as opaque.

Mecatl provides BM25 keyword search. It does not require or support an embedding
or vector database.

### Consolidation

Automatic consolidation schedules are off by default. They can remove exact
duplicates when the store confirms that the source versions have not changed.
Configure project and user schedules independently with
`--memory-consolidate-interval` and `--user-model-consolidate-interval`.

In `mecatui`, `/dream` lets you review a proposed consolidation when the server
advertises a compatible local store and planner. You approve or dismiss the
whole plan. Applying a plan can partially succeed because each operation is
independent. Plans expire and do not survive a restart.

For availability, review safeguards, and recovery behavior, see
[Dreaming and memory consolidation](/features/dreaming.md).

## Soul

The soul is an operator-authored Markdown fragment that sets the agent's
persona, tone, and working style. Mecatl reads it on every run and places it in
the turn-zero instructions, so compaction does not degrade it.

By default, Mecatl reads `~/.config/mecatl/soul.md` or
`$XDG_CONFIG_HOME/mecatl/soul.md`. The agent has no tool that can modify this
file.

|Flag|Effect|
|-|-|
|`--soul-file <PATH>`|Load a different file.|
|`--no-soul`|Disable the soul.|
|`--approve-soul`|Record the current content as the approved baseline.|
|`--soul-strict`|Reject content that differs from the approved baseline.|

A missing, empty, oversized, or injection-flagged file contributes no soul but
does not stop the run. Mecatl records a hash on first use and warns when the
content changes. Strict mode refuses changed content.

A trusted project can provide `<workspace>/.mecatl/soul.md`. A user-scoped soul
takes precedence when both exist.

## User model

The user model stores durable facts about the operator across workspaces. By
default, it lives at `~/.config/mecatl/usermodel`, and keys use the `user/`
namespace.

Current facts are added to the volatile system-prompt suffix for main and
delegated model requests. They do not enter conversation history or the
cache-stable prompt prefix. Current user instructions take precedence over
stored facts, and stored facts cannot change permissions, safety controls, or
the tool catalog.

Mecatl rejects high-confidence credentials and model-authored role overrides
before adding them to the operator profile. In `mecatui`, `/usermodel` provides
a read-only view of current values and available history. Updates still use the
memory tools and their permission rules.

Set `--no-user-model` to disable the user model.

### Learning from completed work

Completed-trajectory learning is independent of explicit memory tools and dream
consolidation. It is off by default:

- `review` stages evidence-backed proposals for approval.
- `auto` can promote eligible operator facts and activate eligible learned
  procedures under the configured assurance policy.
- `off` runs no automatic reflection.

Project settings can lower autonomy or increase assurance, but cannot loosen the
operator's policy. Sensitive, ambiguous, unsupported, or unverifiable proposals
remain staged or are rejected. See [Learning](/features/learning.md) for
triggers, budgets, evidence rules, and procedure activation.

## Defaults

|Capability|Default|
|-|-|
|Project memory and BM25 search|On|
|User model and live operator profile|On|
|Soul|On when the default file exists|
|Automatic consolidation|Off|
|Manual `/dream` review|Available when the server advertises support|
|Completed-trajectory learning|Off|
|Embedding or vector search|Unavailable|

No external memory service, embedding provider, or vector database is required.

## What's next

- [Tool catalog extension point](/building/extension-points/tool-catalog.md) to
  provide memory tools, skills, or custom tools.
