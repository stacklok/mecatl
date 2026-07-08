---
sidebar_position: 5
title: Memory & knowledge
---

# Memory & knowledge

mecatl ships three distinct memory-adjacent systems out of the box. Each solves a different problem and they don't overlap:

| System | Problem it solves |
|--------|------------------|
| **Tiered session memory** | Lets the agent persist and retrieve facts across sessions within a project |
| **Soul** | Gives the agent a stable, operator-controlled persona that survives compaction |
| **User model** | Accumulates durable facts about the operator across every project |

These are independent. The soul is read-only to the agent; memory and the user model are agent-writable. The soul shapes who the agent is; memory and the user model shape what it knows.

---

## Tiered session memory

Per-project memory is on by default. The agent can store entries that survive across sessions within a workspace. The store is scoped to a project directory — each workspace gets its own store, not shared with other projects.

### Tier hierarchy

Memory is organized into three tiers:

| Tier | What it holds | How the model accesses it |
|------|--------------|--------------------------|
| **Tier 0 — index** | A one-line-per-entry digest of every stored key and its description | Always in context at session start, injected before the first user prompt |
| **Tier 1 — entry** | The full value stored under a key | On demand, via the Recall tool |
| **Tier 2 — cold archive** | Raw historical entries beyond the index cap | Via the SearchMemory tool |

The tier-0 index is capped at 200 entries (~8 KB). If the store exceeds the cap, the oldest entries roll off the visible index but remain searchable. The footer in the index tells the agent how to retrieve them.

Each entry has a key, a value, and an optional one-line description. The description is what appears in the tier-0 index; the value is loaded on demand. If no description is provided when an entry is stored, the first line of the value is used as a fallback.

### Dream consolidation

Consolidation is an optional background pass that distills stored memory — merging near-duplicates, dropping stale entries, and tightening descriptions. It runs as a background LLM call on a configurable interval via `--memory-consolidate-interval`. It is **off by default** on both `mecated` and the embedded TUI server to avoid silent token spend.

Memory works correctly without consolidation. Consolidation is an optimization for stores that have accumulated many entries over many sessions.

### The six memory tools

When memory is enabled, the agent has access to these tools in every session:

| Tool | What it does |
|------|-------------|
| **Remember** | Stores a key-value entry with an optional one-line description |
| **Recall** | Retrieves the full value for a key (or lists entries matching a prefix) |
| **SearchMemory** | BM25 keyword search across all stored entries, including those trimmed from the index |
| **RememberUser** | Stores a fact about the operator in the cross-project user model (see below) |
| **RecallUser** | Retrieves a user-model entry by key |
| **SearchUserModel** | BM25 search across user-model entries |

The first three operate on the per-project store. The last three operate on the user model. There is no agent-facing "forget" tool — the underlying `MemoryStore` interface does have a `Forget(key)` method, but it's used internally by consolidation (to drop stale entries), not exposed for the model to call directly.

### Semantic recall

mecatl ships **BM25 lexical search** (`SearchMemory`) as the recall backstop. Full semantic/embedding recall (vector search) is explicitly deferred — there is no embedding backend required and no vectors are stored. BM25 is the only search mode available today.

---

## Soul (system-prompt customization)

The soul is an operator-authored persona fragment injected into every session's context. It controls who the agent is — its style, tone, and posture — and is read-only to the agent by construction.

### What it does

The soul is loaded from `~/.config/mecatl/soul.md` (or `$XDG_CONFIG_HOME/mecatl/soul.md`) and injected as a fenced turn-0 message before the memory index and the user model. It survives compaction: because it is re-read from disk on every run, it does not degrade as sessions grow and context is trimmed.

**The agent has no tool to modify the soul.** There is no write path. This is deliberate — a writable identity anchor is a persistent prompt-injection risk: a single poisoned write would rewrite the agent's persona across all future sessions. Edit the soul file with a text editor.

### Configuration

| Flag | Effect |
|------|--------|
| `--soul-file PATH` | Use a soul file at an explicit path instead of the default XDG location |
| `--no-soul` | Disable the soul entirely for this run |
| `--approve-soul` | Accept a changed soul file, writing a new hash baseline |
| `--soul-strict` | Refuse to load a soul whose content has drifted from the approved baseline |

The soul file is free-form Markdown. A missing, empty, oversized (> 20 KiB), or injection-flagged soul file degrades to no fragment — it never aborts a run.

**Project-sourced soul.** A project can provide a soul at `<workspace>/.mecatl/soul.md`. This file is untrusted by default and contributes nothing unless `--trust-project` is set. If both a user-scoped soul and a project soul are present, the user-scoped soul always wins and the project soul is ignored.

**Drift detection.** mecatl keeps a hash baseline of the soul body in a sidecar file (`<soul-path>.sha256`). On first load the baseline is written (trust-on-first-use). On subsequent loads, a hash mismatch logs a warning and still loads the soul. Use `--approve-soul` after an intentional edit to silence the warning, or `--soul-strict` to refuse a drifted soul.

---

## User model

The user model is a cross-project, agent-writable store of durable facts about the operator — who they are, how they prefer to work, their communication style. Unlike per-project memory, it is shared across every workspace on the machine.

### How it differs from project memory

| | Per-project memory | User model |
|---|---|---|
| **Scope** | One workspace | All workspaces on this machine |
| **What it stores** | Project-specific context and notes | Facts about the operator |
| **Writable by agent?** | Yes | Yes |
| **Default location** | Computed from workspace path | `~/.config/mecatl/usermodel` |
| **Survives project change?** | No | Yes |

### How it surfaces

- **Turn-0 block:** At session start, a `<user-model>` block is injected after the soul and after the memory index. It summarizes the stored facts. The agent treats this as data, not instructions — behavioural rules belong in the soul, not the user model.
- **Tools:** `RememberUser`, `RecallUser`, `SearchUserModel` are available by default whenever the user model is enabled.

Keys in the user model are automatically namespaced under `user/`.

### Background reviewer (off by default)

With `--user-model-review`, mecatl enables a background reviewer that runs after a session completes. It spawns a fresh single-shot child that reads the session transcript and calls `RememberUser` to extract operator facts. It **never reopens or re-runs the user's session** — it is an independent read pass over a finished transcript.

The reviewer is off by default to avoid per-session LLM spend. Enable it explicitly when you want the user model to self-populate without the agent manually calling `RememberUser`.

A separate `--user-model-consolidate-interval` drives a dream consolidator scoped to the `user/` namespace.

**Disable the user model entirely** with `--no-user-model`.

---

## What's on by default

| Feature | Default state | How to change |
|---------|--------------|---------------|
| Per-project memory tools | **On** (mecatui: per-project dir under `~/.local/share/mecatui/memory/`) | `--no-memory` to disable; `--memory-dir` to relocate |
| Tier-0 memory index (at session start) | **On** when memory is enabled | Automatic; not configurable separately |
| BM25 SearchMemory | **On** when memory is enabled | Automatic |
| Dream consolidation | **Off** | `--memory-consolidate-interval` |
| Soul | **On** if `~/.config/mecatl/soul.md` exists | `--no-soul` to disable; `--soul-file` to relocate |
| User model tools + turn-0 block | **On** | `--no-user-model` to disable; `--user-model-dir` to relocate |
| Background user-model reviewer | **Off** | `--user-model-review` to enable |
| User-model consolidation | **Off** | `--user-model-consolidate-interval` |
| Semantic/embedding recall | **Not available** | No embedding backend required or supported |

No external service, embedding backend, or vector database is required for any of the default-on features.

---

## What's next

To add tools, swap the memory store, or plug in a custom skill source, see [Extension points — tool catalog](/extension-points/tool-catalog.md).
