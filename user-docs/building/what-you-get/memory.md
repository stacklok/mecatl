---
sidebar_position: 5
title: Memory & knowledge
description: Understand Mecatl's memory, soul, and knowledge systems and their extension ports.
---

# Memory & knowledge

This is the builder-facing map of Mecatl's memory-adjacent systems and their
ports. For user-facing configuration and workflows, see [Skills, commands, and
soul](/features/skills-commands-and-soul.md), [Learning](/features/learning.md),
and [Dreaming and memory consolidation](/features/dreaming.md).

Mecatl ships three distinct memory-adjacent systems out of the box. Each solves a different problem and they don't overlap:

|System|Problem it solves|
|-|-|
|**Tiered session memory**|Lets the agent persist and retrieve facts across sessions within a project|
|**Soul**|Gives the agent a stable, operator-controlled persona that survives compaction|
|**User model**|Accumulates durable facts about the operator across every project|

These are independent. The soul is read-only to the agent; memory and the user model are agent-writable. The soul shapes who the agent is; memory and the user model shape what it knows.

---

## Tiered session memory

Per-project memory is on by default. The agent can store entries that survive across sessions within a workspace. The store is scoped to a project directory — each workspace gets its own store, not shared with other projects.

### Tier hierarchy

Memory is organized into three tiers:

|Tier|What it holds|How the model accesses it|
|-|-|-|
|**Tier 0 — index**|A one-line-per-entry digest of every stored key and its description|Always in context at session start, injected before the first user prompt|
|**Tier 1 — entry**|The full value stored under a key|On demand, via the Recall tool|
|**Tier 2 — cold archive**|Raw historical entries beyond the index cap|Via the SearchMemory tool|

The tier-0 index is capped at 200 entries (~8 KB). If the store exceeds the cap, the oldest entries roll off the visible index but remain searchable. The footer in the index tells the agent how to retrieve them.

Each entry has a key, a value, and an optional one-line description. The description is what appears in the tier-0 index; the value is loaded on demand. If no description is provided when an entry is stored, the first line of the value is used as a fallback.

### Dream consolidation

Consolidation has two separate maintenance surfaces. Neither is completed-trajectory reflection or
learning.

**Automatic schedules** are optional and **off by default**. Their planner can classify exact
duplicates, but unattended application is narrower: the local file-backed store must atomically
compare bound survivor/source versions and tombstone only sources whose active value and
description exactly match the survivor. The survivor is never rewritten. Configure the independent
project-memory and user-model schedules with `--memory-consolidate-interval` and
`--user-model-consolidate-interval`; these flags do not change `learning.mode`.

**Manual review** is available in mecatui through `/dream` when the server advertises a supported
target. The flow is:

1. choose project memory or the cross-project user model;
2. acknowledge that generation sends the selected bounded values/descriptions to the configured
   model and spends tokens;
3. inspect every exact-duplicate and synthesized-replacement operation, with every untrusted line quoted
   and prefixed and the complete bounded reason visible;
4. apply or dismiss the whole plan; and
5. read the receipt's planned, applied, conflicted, skipped, and failed source counts.

Exact duplicates keep the displayed survivor unchanged. Hidden controls or Unicode format characters in
model-authored replacement/reason text reject the plan before retention, so an accepted review and apply
use the same replacement bytes. For an approved synthesis, the local store
atomically rewrites the displayed survivor to the displayed replacement and tombstones all displayed
sources for that operation. Operations are independent, so a whole-plan apply may be partial; there
are no per-source toggles, grouped transaction, or grouped undo.

Plans are short-lived and process-local. A same decision still applying and a genuinely indeterminate
transport error preserve the exact plan ID and decision for explicit same-decision receipt retrieval;
the latter may hide an already-applied request. An opposite applying decision is non-retryable and
offers no fresh generation until terminal; a known terminal opposite decision permits explicit fresh
generation. Restart, expiry, or another replica makes the old plan non-retryable and offers a fresh
plan instead. No state offers the opposite decision.
Manual review is unavailable while ownership enforcement is enabled, when no planner is configured, or
when the selected store lacks reviewed atomic consolidation (including current remote/base-only
stores). It shows no provider/model identity and collects no recall-usage telemetry.

### Memory and lifecycle tools

When memory is enabled, the agent has access to these tools in every session:

|Tool|What it does|
|-|-|
|**Remember**|Stores a key-value entry with an optional one-line description|
|**Recall**|Retrieves the full value for a key (or lists entries matching a prefix)|
|**SearchMemory**|BM25 keyword search across all stored entries, including those trimmed from the index|
|**RememberUser**|Stores a fact about the operator in the cross-project user model (see below)|
|**RecallUser**|Retrieves a user-model entry by key|
|**SearchUserModel**|BM25 search across user-model entries|
|**InspectMemory / InspectUserMemory**|Reads an exact value, version, provenance, timestamps, and bounded history|
|**ForgetMemory / ForgetUserMemory**|Writes a reversible tombstone after permission approval (asks by default)|
|**UndoMemory / UndoUserMemory**|Appends a compensating revision restoring the previous state|

The first scope operates on the project store and the `*UserMemory` scope operates on the user model. Lifecycle tools appear only when the configured store positively advertises complete versioned-history support; old remote drivers expose only Remember/Recall/Search. Remember remains non-modal and overwrites normally when `expected_version` is omitted. Supplying a non-empty version requests CAS and a stale version conflicts; Forget and Undo require an explicit current version. Remember, Recall, Search, Inspect, and Undo are allowed at the overridable built-in floor; Forget asks by default. Forget and Undo require copying the opaque expected_version verbatim from an exact Recall result, Inspect result, or mutation receipt in the same scope. If none is available or it may be stale, use InspectMemory (project) or InspectUserMemory (user/user-model) first. Never guess or interpret it. Learning mode `off` does not remove these explicit tools.

### Semantic recall

Mecatl ships **BM25 lexical search** (`SearchMemory`) as the recall backstop. Full semantic/embedding recall (vector search) is explicitly deferred — there is no embedding backend required and no vectors are stored. BM25 is the only search mode available today.

---

## Soul (system-prompt customization)

The soul is an operator-authored persona fragment injected into every session's context. It controls who the agent is — its style, tone, and posture — and is read-only to the agent by construction.

### What it does

The soul is loaded from `~/.config/mecatl/soul.md` (or `$XDG_CONFIG_HOME/mecatl/soul.md`) and injected as a fenced turn-0 message before the project memory index. It survives compaction: because it is re-read from disk on every run, it does not degrade as sessions grow and context is trimmed.

**The agent has no tool to modify the soul.** There is no write path. This is deliberate — a writable identity anchor is a persistent prompt-injection risk: a single poisoned write would rewrite the agent's persona across all future sessions. Edit the soul file with a text editor.

### Configuration

|Flag|Effect|
|-|-|
|`--soul-file PATH`|Use a soul file at an explicit path instead of the default XDG location|
|`--no-soul`|Disable the soul entirely for this run|
|`--approve-soul`|Accept a changed soul file, writing a new hash baseline|
|`--soul-strict`|Refuse to load a soul whose content has drifted from the approved baseline|

The soul file is free-form Markdown. A missing, empty, oversized (> 20 KiB), or injection-flagged soul file degrades to no fragment — it never aborts a run.

**Project-sourced soul.** A project can provide a soul at `<workspace>/.mecatl/soul.md`. This file is untrusted by default and contributes nothing unless `--trust-project` is set. If both a user-scoped soul and a project soul are present, the user-scoped soul always wins and the project soul is ignored.

**Drift detection.** Mecatl keeps a hash baseline of the soul body in a sidecar file (`<soul-path>.sha256`). On first load the baseline is written (trust-on-first-use). On subsequent loads, a hash mismatch logs a warning and still loads the soul. Use `--approve-soul` after an intentional edit to silence the warning, or `--soul-strict` to refuse a drifted soul.

---

## User model

The user model is a cross-project, agent-writable store of durable facts about the operator — who they are, how they prefer to work, their communication style. Unlike per-project memory, it is shared across every workspace on the machine.

### How it differs from project memory

||Per-project memory|User model|
|-|-|-|
|**Scope**|One workspace|All workspaces on this machine|
|**What it stores**|Project-specific context and notes|Facts about the operator|
|**Writable by agent?**|Yes|Yes|
|**Default location**|Computed from workspace path|`~/.config/mecatl/usermodel`|
|**Survives project change?**|No|Yes|

### How it surfaces

- **Live operator profile:** Current valid `user/` facts are loaded on every main, subagent, team-member, and lead-synthesis provider request into the bounded volatile system-prompt suffix. Internal checker/reviewer/judge requests omit it. Facts never enter conversation history or the cache-stable prefix. Current explicit user text wins stale facts; facts are data and cannot change policy, safety, or tools. Overflow names `SearchUserModel`/`RecallUser` only when both exist in that request's real tool catalog; otherwise it says omitted facts are unavailable in that context.
- **Narrow persistence defenses:** high-confidence credentials are rejected in values and descriptions (including Bearer/assignment/quoted/PEM wrappers), and model-authored role/directive overrides are rejected. The final profile boundary repeats both checks for migrated/imported/remote data while preserving ordinary Unicode preferences and security discussion.
- **Tools:** `RememberUser`, `RecallUser`, `SearchUserModel`, and lifecycle-aware Inspect/Forget/Undo tools are available whenever the configured store supports them.
- **Read-only TUI detail:** `/usermodel` keeps its bounded, terminal-height-scrolled key/description list; selecting one entry fetches its full value, status, version, source session, timestamp, and newest 16 revisions in a scrollable detail view. Secret-shaped values/descriptions are withheld. Base-only or old remote stores show history unavailable. Changes still go through model-facing tools and permissions. Once a remote driver advertises lifecycle support, a missing or failed lifecycle RPC is an error and never falls back to an unconditional legacy operation.

Keys in the user model are automatically namespaced under `user/`.

### Staged reflection (off by default)

Set the operator file `$XDG_CONFIG_HOME/mecatl/settings.yaml` to:

```yaml
learning:
  mode: auto
  sensitivity: balanced
  skills:
    activation: validated # validated | evaluated
  automatic:
    cooldown: 10m
    window: 1h
    max_reflections: 8
    max_tokens: 100000
    max_reflections_per_principal: 4
    max_tokens_per_principal: 50000
```

After an eligible main-session completion, Mecatl scores only evidence in the verified current
run. Balanced requires 4 points (conservative 6, eager 3); modifiers cannot admit by
themselves. A genuine current principal-authored prompt that affirmatively asks to remember or
learn/create a procedure is a hard trigger on the exact clean terminal set, but negation,
capability questions, tool/WebFetch/WebSearch/MCP, repository, historical, event-only, and
assistant-only text cannot manufacture one. Hard triggers bypass score and weighted cooldown,
not count/token budgets or coordinator capacity. Standard non-off composition reserves through
the durable automatic ledger, so cooperating processes share global/principal count and token
windows, cooldown, and digest deduplication. Reservation is tied to deterministic attempt create;
failed create is reconciled to one retained or reclaimed charge, while failed, timed-out,
and abstaining attempts retain theirs. Queue-full does not consume a reservation. An unwired
embedding retains ADR-0114's process-local limitation and must not advertise global bounds.

`review` durably stages valid evidence-backed proposals without changing memory. `auto` stages
first, then promotes operator facts only when the candidate cites the genuine user message carrying an explicit remember request. Tool/WebFetch/WebSearch/MCP, repository, event-only, and assistant-only evidence stays staged. Project facts use a separate narrow rule: candidates from any non-empty session workspace remain staged and inspectable in that project's partition, but auto-promotion, approval, and undo require the exact trusted configured workspace and an available convergence-capable project memory store. Operator facts continue to follow operator policy. Proposal detail re-checks source ownership and evidence digests and shows a bounded, redacted canonical preview before approval; unavailable, changed, or cross-owner evidence has no preview and cannot be promoted. Ambiguous, conflicting, sensitive, and unsupported
material remains staged or rejected. Procedures first persist a `deferred_unsupported` crash checkpoint, then the learned-skill pipeline evaluates them: review stages PASS/ABSTAIN and rejects FAIL; auto PASS activates, while the stock omitted/`validated` policy may also activate a structurally accepted, evidence-backed ABSTAIN. Set `activation: evaluated` for PASS-only assurance. Similar/colliding/unpublishable candidates stay staged, and direct SkillDraft output stays inactive. `off`
means no automatic observer, controller/coordinator worker, eager proposal repository, or reflection provider call. Authenticated explicit reflection remains synchronous: it lazily initializes persistence, bypasses automatic admission/budgets/cache, and uses the completed session's persisted provider/model; without genuine current-prompt promotion provenance it remains stage-only. A project may lower the operator
mode and sensitivity, and may tighten skill activation from validated to evaluated; it can never raise autonomy or lower assurance. Project automatic limits are ignored. The legacy `--user-model-review` flag is a deprecated `auto` alias
for one compatibility window. Proposal data defaults beside the user-model store under
`reflections/`.

The explicit memory and SkillDraft tools are independent and remain available while
completed-trajectory learning is off. Dream intervals also do not enable that learning.

A separate `--user-model-consolidate-interval > 0` independently authorizes the automatic
exact-duplicate-only schedule for the cross-project `user/` namespace. It remains off by default,
and a project `learning.mode: off` cannot suppress that explicit operator schedule. Manual `/dream`
review is immediate and separate from this flag: it can inspect exact and synthesized operations for
the user model, but only on a supported local lifecycle store and never under ownership enforcement.

**Disable the user model entirely** with `--no-user-model`.

---

## What's on by default

|Feature|Default state|How to change|
|-|-|-|
|Per-project memory tools|**On** (mecatui: per-project dir under `~/.local/share/mecatui/memory/`)|`--no-memory` to disable; `--memory-dir` to relocate|
|Tier-0 memory index (at session start)|**On** when memory is enabled|Automatic; not configurable separately|
|BM25 SearchMemory|**On** when memory is enabled|Automatic|
|Dream consolidation schedules|**Off**|`--memory-consolidate-interval` / `--user-model-consolidate-interval`|
|Manual dream review|**Available when advertised**|mecatui `/dream`; unavailable under ownership enforcement or without a supported planner/store|
|Soul|**On** if `~/.config/mecatl/soul.md` exists|`--no-soul` to disable; `--soul-file` to relocate|
|User model tools + live operator profile|**On**|`--no-user-model` to disable; `--user-model-dir` to relocate|
|Automatic evidence reflection|**Off**|`learning.mode: review` stages proposals; `auto` may conservatively promote eligible facts|
|User-model consolidation|**Off**|`--user-model-consolidate-interval`|
|Semantic/embedding recall|**Not available**|No embedding backend required or supported|

No external service, embedding backend, or vector database is required for any of the default-on features.

---

## What's next

To add tools, swap the memory store, or plug in a custom skill source, see [Extension points — tool catalog](/building/extension-points/tool-catalog.md).
