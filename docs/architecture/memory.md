# Memory — cross-session recall & consolidation

> Part of the [mecatl architecture guide](../architecture.md).

**What this covers:** `tool.MemoryStore` (Remember/Recall/SearchMemory across sessions), the file-backed `memory` adapter, the opt-in `dream` consolidation service, the user model (RememberUser/RecallUser/SearchUserModel — cross-project operator FACTS), and the Stop-triggered user-model reviewer.

**Prerequisites:** [the agent loop](agent-loop.md) — the loop injects the memory index each run.

**Follow-on:** return to the [reading map](../READING.md) and choose another topic branch. **Related:** [context & compaction](context-and-compaction.md) covers per-run context management, which is independent of memory.

`tool.MemoryStore` (`RememberEntry`/`Recall`/`List`/`Forget`/`Index`/`Search`) is
the seam for conservative, **per-project** memory (every implementation must pass
the shared `engine/adapter/memconformance` conformance suite). The file-backed
`internal/adapter/memory` implementation persists entries scoped to a project
directory and exposes them to the model as the **Remember**, **Recall**, and
**SearchMemory** tools
(opt-in via `memory.Register`, `--memory-dir`). On top of it, `internal/adapter/dream` is an opt-in background
**consolidation** ("sleep") service: `dream.Consolidator` distills the stored
memory with an LLM call — merging duplicates and dropping stale entries — but is
deliberately conservative (it never invents keys and is fail-safe on error), run
once or on a ticker via `RunPeriodically` (`--memory-consolidate-interval`).

**User model (issue #14 Phase 2).** A SECOND `memory.Store` — user-scoped and
**cross-project** (`<xdg>/mecatl/usermodel`, distinct from the per-project store) —
holds durable FACTS about the operator. It is exposed (2a, default-on) as the
**RememberUser/RecallUser/SearchUserModel** tool family (the parameterized memory tool
structs, not duplicates) under an enforced `user/` key prefix, plus a turn-0
`<user-model>` block (`prompt.UserModelAssembler`, injected LAST — soul → memory index →
user model). RememberUser injection-scans the value AND the effective description at write time (`skills.ScanForInjection`) and rejects the `</user-model>` fence close-tag in either,
guarding the block against transcript-sourced poisoning. An OPT-IN (off by default,
`--user-model-review`) Stop-triggered background reviewer (`agent.UserModelReviewer`,
wired via a composition-layer Stop-hook decorator) re-reads a finished session's
transcript and extracts operator facts via a FRESH single-shot child — it **never
reopens the user's terminal session** (R10). A `--user-model-consolidate-interval`
drives a separate `dream.Consolidator{Prefix:"user/"}`. The user-model is a writable
instruction FRAGMENT of FACTS, NEVER a governance scope; behaviour comes from the soul +
system rules, not this block.

## Prerequisites

- [The agent loop that injects the memory index](agent-loop.md)

## Follow-on reading

- Return to the [reading map](../READING.md) and choose another topic branch.

## Related

- [Context & compaction](context-and-compaction.md)

---

[← Architecture guide](../architecture.md)
