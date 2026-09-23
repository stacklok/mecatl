# ADR 0009 — Tiered memory: tier-0 index and BM25 search

- Status: Accepted
- Date: 2026
- Scope: `engine/tool` (MemoryStore interface), `engine/prompt` (MemoryIndexAssembler), `internal/adapter/memory` (store, tools), `internal/app` (composition wiring).
- Superseded by (injection mechanism only): [ADR 0043](./0043-ephemeral-turn0-instruction-fragments.md) — the tier-0 index (like all turn-0 fragments) is no longer "recorded once at turn 0 as a persisted user message"; it is assembled once per run and PREPENDED to the request EPHEMERALLY, never persisted into the conversation. The "always in context, computed once per run, after the cache breakpoint, fenced as data" semantics are unchanged — only the persistence is dropped.

## Context

The memory store described itself as "tiered" but was a flat key-to-value map with no always-in-context routing table. The Recall tool description told the model memory "is not an index," leaving it blind to what it had stored. Without a tier-0 index the model could only retrieve entries by guessing exact keys. Tier-2 cold storage and semantic/embedding recall were candidates but assessed as premature at this scale with consolidation off by default.

## Decision

Add a tier-0 index: a derived, capped (200 entries/~8 KB), one-line-per-entry digest injected once per run as a turn-0 user-role message via the existing InstructionAssembler seam, so the model always sees what it has stored. Extend the MemoryStore interface with `Index` and `RememberEntry` (adding an optional description field to entries). The Remember tool echoes the resulting index line on write so the model sees its own write immediately. Tier-2 cold storage is rejected in favour of the single store plus a keyword-search tool; semantic/embedding recall remains deferred. BM25 lexical SearchMemory ships as the blind-spot backstop (issue #12).

## Consequences

The model can enumerate its memory at session start and search trimmed entries by keyword without guessing keys. The `memory.json` format gains an additive `description` field (zero-migration: old files load cleanly with an empty description, which falls back to the first line of value). Cross-process safety is provided by a flock sentinel so concurrent writers cannot lose updates. The one-`*Store`-per-dir invariant must be upheld by composition. Semantic/embedding recall stays deferred; the deferred path and its design are recorded in docs/adr/0010-semantic-memory-recall.md. Current behaviour: docs/architecture.md. Shipped/deferred state: [Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md).

---

## TL;DR / recommended increment

The store calls itself "tiered memory" (`internal/adapter/memory/store.go:1`) but
is a flat key→value map (`store.go:65-73`) the model can only read by guessing keys
— the Recall tool description even tells the model memory "is not an index"
(`tools.go:62-64`). That is the gap: **there is no always-in-context tier-0 index**,
so the model is blind to what it stored.

**Recommended increment (smallest thing that genuinely closes the gap):**

1. Add a **tier-0 index**: a derived, capped, one-line-per-entry digest of what is
   stored, **injected once per run as a user-role message** via the existing
   `prompt.InstructionAssembler` seam (the same seam AGENTS.md rides
   `loop.go:379-386`). It rides in the VOLATILE part of the conversation, NOT the
   cache-stable system prefix — cache is preserved.
2. Add one store method, `Index(ctx)`, returning the entry summaries (key +
   one-line description + updated-at). The model-facing **Recall tool stays the
   primary tier-1 loader** — exact key fetches the full value; the "not an index"
   line in its description is deleted and replaced with "your index is shown to you
   at session start; Recall a key from it to load the full entry."
3. The consolidator (`dream`) becomes **index-aware only in that it keeps the set
   of entries tight** — it already does this (`dream.go`). No new tier-2 machinery.

**Explicitly deferred (NOT in this increment):**

- **Tier-2** (raw transcripts / cold storage). No caller needs it; the flat store
  never had it; adding a cold tier now is the wrong abstraction. Section "Tier 2"
  below records the seam we'd grow into, but ships nothing. **(Re-assessed in
  Task 3 — `MEMORY-TIER2.md` — and still rejected: vectors persist inline on the
  single store, no separate cold tier.)**
- **Semantic / embedding recall.** Doc 07 §6 calls a vector store the thing you add
  "when memory exceeds what's enumerable." **(Task 3 — `MEMORY-TIER2.md` — resolved
  as a pure-Go BM25 lexical `SearchMemory` tool, SHIPPED (#12); semantic/embedding
  recall stays DEFERRED — no `port.Embedder`, no persisted vectors, no
  `SemanticRecall` in the tree. The phased semantic design is retained in that doc
  as the future path.)**
- **A separate "browse the index" tool.** The index is *already in context*; a tool
  to fetch what the model can already see is dead surface. Recall-by-key is the
  loader. (Open decision D3 below — flagged for you.)
- **Splitting tier-1 into topic files on disk.** The reference design (one file per
  fact) is evaluated and **rejected for now** in favour of keeping the single
  `memory.json` and *deriving* the index from it (decision D1). One JSON file is
  the cheapest store that delivers a real tier-0 index; per-fact files buy nothing
  until tier-2 or hand-editing matters.

The whole increment is: **one new derived view (the index), one new interface
method, one new assembler wired in `app`, and a description fix.** No on-disk format
change beyond an additive `description` field; the Task-1 `memory.json` is read
as-is (migration is a no-op read, see "Migration").

---

## BIG DECISIONS FOR YOU TO WEIGH IN ON

These shape the increment; I have made a recommendation on each but they are the
points worth your review before implementation.

- **D1 — Derived index vs. `MEMORY.md`-on-disk + per-fact files.**
  Recommendation: **derive the index in-memory from the single `memory.json`; do
  NOT split to per-fact files or write a `MEMORY.md`.** The proven reference shape
  (a `MEMORY.md` index file + one file per fact) earns its keep when facts are
  hand-edited by humans and version-controlled (it is exactly what *this* repo's
  agent-memory dir does). Our store is **machine-written, machine-read, not in
  git, opaque per Task 1** — so the file-per-fact split is pure overhead: more
  fsync surface, a real migration, index/file drift to reconcile in the
  consolidator. The tier-0 *concept* (a compact always-in-context index pointing at
  on-demand entries) is what matters; whether the index is a file or a derived view
  is invisible to the model. Deriving it keeps the store one atomic file and makes
  the index *impossible* to desync from the entries. **If** you foresee users
  hand-editing memory or committing it, flip to file-per-fact — say so and I will
  redesign around that.

- **D2 — Prompt-cache placement.** Recommendation: tier-0 index goes in a
  **user-role message recorded once at turn 0** (alongside AGENTS.md via the
  `InstructionAssembler` seam), NEVER in `prompt.Build`'s `StablePrefix`. It is
  volatile (changes whenever the model Remembers), so putting it near the cache
  prefix would wreck the cache invariant (gauntlet #6, `builder.go:14-18`). As a
  turn-0 user message it sits *after* the cache breakpoint and is computed once per
  run. Confirm you are OK with "index reflects state at run start, not mid-run"
  (it does not refresh after a Remember within the same run — see Risks R1).

- **D3 — New tool, or reuse Recall?** Recommendation: **reuse Recall, no new
  tool.** The index is already in context; Recall(key) is the tier-1 loader.
  Adding a `MemoryIndex`/`Browse` tool duplicates what the model can already read.
  The only argument for a new tool is if you want the model to refresh the index
  *mid-run* after writing — but that is better served by Remember returning the
  updated one-liner (see "Tool surface"). Flag if you want the explicit browse tool
  anyway.

- **D4 — Index size bound.** Recommendation: **cap tier-0 at 200 entries / ~100
  lines / ~8 KB**, oldest-trimmed with a "...(N older entries; Recall a prefix to
  see them)" footer. Doc 02 §3 says ~200 lines; our entries are one line each so
  100 entries ≈ 100 lines is already generous for a curated per-project store.
  Confirm the number; it is the one knob that trades context cost vs. completeness.

---

## 1. What "tiered" concretely means here

| Tier | What it is | Where it lives | Load strategy | Size bound |
|---|---|---|---|---|
| **0 — index** | One line per entry: `key — <one-line description>`. The routing table. | **Derived** from `memory.json` at run start; rendered into a user-role message. | **Always in context** (turn-0 user message). | **≤ 200 entries, ≤ ~8 KB.** Over the cap → oldest trimmed, footer points at Recall. |
| **1 — entry** | The full stored `value` for one key. | `memory.json` (`store.go:33`). | **On demand** — model calls `Recall(key)` after seeing the key in tier-0. | Per-entry value truncated to `toolkit.MaxOutputBytes` on read (`tools.go:264`), as today. |
| **2 — raw** | Raw session transcripts / cold archive. | — | — | **NOT SHIPPED** (deferred). |

Tier-0's bound is the load-bearing one: it must stay small enough to keep in every
prompt. The description (not the value) is what tier-0 carries, so a long value does
not bloat the index. **What bounds it:** entry count cap (D4) + per-line description
cap (one line, truncated to ~120 chars) + a total-byte ceiling with oldest-first
trimming. The cap is enforced when *rendering* the index, so it is independent of
how many entries the store holds.

The "description" is new (entries today are bare key→value). It is **optional and
derived-with-fallback**: if an entry has no explicit description, tier-0 shows the
first line of the value, truncated. The consolidator can later tighten descriptions
(it already rewrites values; descriptions are the same mechanism).

---

## 2. Tier-0 prompt injection

**Location:** `engine/prompt` — a new `MemoryIndexAssembler` that implements the
existing `prompt.InstructionAssembler` interface (`instructions.go:19-24`), composed
*after* `RootAssembler` so the conversation opens with: AGENTS.md/CLAUDE.md, then the
memory index, then the user prompt.

**Role:** **user message**, not system. CLAUDE.md and doc 08 §5 are explicit that
standing instructions and discovered context ride in a user message, never the
elevated-trust system role (`builder.go:147-152`). The memory index is exactly that
kind of content. `DiscoverInstructions` already returns `session.NewUserMessage`
(`builder.go:175`); the index assembler returns the same shape.

**When computed:** once, at **turn 0** of a run. `recordPrompt` only assembles
instructions when `sess.Counters.Turns == 0` (`loop.go:380`), and those messages are
prepended exactly once via `RecordUserPromptWithParts` (`session.go:252-262`). So the
index is computed once per session-run and then lives as ordinary history — it is NOT
recomputed every turn, and it does NOT touch `prompt.Build`.

**Cache-safety story (the critical part):**

- `prompt.Build`'s `StablePrefix` (`builder.go:82-95`) is byte-identical across turns
  — that is what the OpenAI adapter prompt-caches. The index is **not** in the
  StablePrefix; it is conversation content recorded after the cache breakpoint.
- Within a run the index message is fixed history (recorded once at turn 0), so every
  turn of that run re-sends identical bytes → the conversation prefix is itself
  cacheable turn-to-turn.
- Across runs the index may differ (the model Remembered something last run), but so
  does the whole conversation — there is no cross-run cache expectation. The
  StablePrefix stays byte-stable across runs regardless, so the gauntlet #6 invariant
  is untouched.
- **Net:** zero impact on the StablePrefix cache; the index is just another piece of
  turn-0 user content, exactly like AGENTS.md, which already varies per project
  without a cache problem.

**How the index reaches `prompt` without a domain→adapter edge (see §7).**

**Data fence.** The rendered entry list is content the model previously stored, so
`renderMemoryIndex` wraps it in explicit `<memory-index>...</memory-index>`
delimiters (matching the house style of the `<env>` block in `env.go`), and the
header instructs the model to treat the fenced contents as DATA, never as
instructions. This is a cheap prompt-injection fence around the one injected block
that carries model-authored, persisted text.

**Trust model.** The index assumes a SINGLE-USER, SINGLE-TRUST-ZONE memory dir:
everything in `memory.json` was written by this user's own sessions, so injecting it
(fenced) is safe. A SHARED or MULTI-TENANT memory dir would change that — another
party's entries could carry adversarial text — and would need per-entry
provenance/author labeling (and likely per-author trust gating) before the index is
safe to inject. That is out of scope here; the data fence is the cheap hardening for
the single-user case, not a substitute for provenance in a shared store.

**Sibling user-model store (issue #14 Phase 2).** A SECOND `memory.Store` instance —
user-scoped and CROSS-PROJECT (`<xdg>/mecatl/usermodel`), holding durable FACTS about
the operator and exposed as the RememberUser/RecallUser/SearchUserModel tools + a
turn-0 `<user-model>` block — reuses this exact store implementation (no new store type,
no schema change), just rooted at a different directory and scoped to a `user/` key
prefix. Its trust model is **identical** to the above: it assumes the SAME single-user,
single-trust-zone assumption — every entry was written by this operator's own sessions
(or the opt-in Stop-time reviewer over this operator's own transcripts), so the fenced
`<user-model>` block is safe to inject. A shared/multi-tenant user-model dir would need
the same per-author provenance work before it is safe; per-user keying is explicitly out
of scope. As defence-in-depth the RememberUser write path additionally injection-scans
each value (`skills.ScanForInjection`) so a transcript-sourced fact cannot smuggle
role-override text into the block.

---

## 3. Interface impact — `tool.MemoryStore`

The domain interface gains **one** method. Everything else (the index rendering, the
description fallback, the cap) is the adapter's job — the domain stays clean.

**Before** (`tool.go:190-202`):

```go
type MemoryStore interface {
    Remember(ctx context.Context, key, value string) error
    Recall(ctx context.Context, key string) (MemoryEntry, bool, error)
    List(ctx context.Context, prefix string) ([]MemoryEntry, error)
    Forget(ctx context.Context, key string) error
}
```

**After:**

```go
type MemoryStore interface {
    // RememberEntry stores an entry. Description is the optional one-line tier-0
    // hook; empty means "derive from value". Supersedes the old Remember, which
    // becomes a convenience wrapper (Description: "").
    RememberEntry(ctx context.Context, e MemoryEntry) error

    Recall(ctx context.Context, key string) (MemoryEntry, bool, error)
    List(ctx context.Context, prefix string) ([]MemoryEntry, error)
    Forget(ctx context.Context, key string) error

    // Index returns the tier-0 routing table: every entry as (key, description,
    // updated-at) with the VALUE OMITTED, sorted for deterministic output. The
    // store does NOT apply the tier-0 size cap — rendering/capping is the
    // consumer's concern — but it does fill Description (explicit, else derived
    // from the value's first line). It is the cheap, always-in-context view.
    Index(ctx context.Context) ([]MemoryEntry, error)
}
```

`MemoryEntry` (`tool.go:170-177`) gains one field:

```go
type MemoryEntry struct {
    Key         string
    Value       string    // empty in Index results (tier-0 omits values)
    Description string    // NEW: one-line tier-0 hook; derived if unset
    UpdatedAt   time.Time
}
```

Notes on keeping it clean:

- `Remember(ctx, key, value)` is kept as a defaulted wrapper so callers/tests that
  do not care about descriptions are unchanged (`store_test.go` keeps compiling).
  (Since superseded: the Phase-A port extraction dropped `Remember` from the
  INTERFACE — `RememberEntry` is the sole write — while the concrete
  `*memory.Store.Remember` convenience survives for direct store users, so
  `store_test.go` still compiles unchanged. The interface also gained `Search`
  later; see `engine/tool/tool.go` for the live contract and
  `engine/adapter/memconformance` for the conformance suite.)
- `Index` returns `MemoryEntry` with `Value:""` rather than a new `MemoryIndexEntry`
  type — one fewer type, and "value omitted in index results" is documented on the
  method. (If you prefer an explicit `MemoryIndexEntry{Key,Description,UpdatedAt}`
  for type-honesty, that is a fine alternative — minor.)
- The interface does NOT learn the word "tier" or "index file" or any format. It
  exposes *what the model needs* (a cheap summary view); the adapter owns *how*.

---

## 4. Tool surface

### Recall — description fixed (the false "not an index" line goes)

`recallDescription` (`tools.go:53-76`) loses the line
`Memory holds only what was deliberately saved with Remember; it is not an index of
the repository.` and gains:

```
Behavior:
- Your current memory INDEX (every saved key + a one-line summary) is shown to you
  automatically at the start of each session. Use Recall to load the FULL value of a
  key you see in that index.
- An exact key returns that entry's full value.
- A key with no exact match is treated as a PREFIX and lists matching entries.
- A miss returns a clear "not found" result, not an error.
```

The "not an index of the repository" intent (do not use memory to discover code —
use Read/Grep/Glob) is preserved as a separate, still-true line. What changes is the
false claim that the model has no index of *memory*: now it does.

### Remember — gains an optional `description`, and returns the index line

`rememberArgs` (`tools.go:101-104`) gains `description string` (optional). The schema
(`tools.go:111-118`) gains a `description` property. The model-facing doc gains:

```
- description (optional): a one-line summary shown in your memory index. If omitted,
  the first line of value is used. Keep it short and specific — this is what future
  sessions see at a glance.
```

`Execute` (`tools.go:126-141`) calls `RememberEntry` and on success returns
`Remembered "key" — <description>.` so the model sees the exact index line its write
just produced — this is the in-run feedback that makes a separate mid-run "browse"
tool unnecessary (D3).

### No new tool (D3). 

The index is in context; Recall is the loader; Remember echoes the index line. That
is the complete loop with zero new surface.

---

## 5. Consolidator (`dream`) changes

The `dream` consolidator already does the tier-pattern-4 job: list entries, ask a
model to merge near-duplicates and drop stale ones, apply conservatively
(`dream.go:174-251`). With a derived index (D1), **the index cannot go stale** — it
is recomputed from entries every run — so the consolidator does NOT need to "rewrite
an index file." That removes the doc-02-§3 "index lies about deeper tiers" failure
mode by construction.

Minimal, optional change: when the consolidator tightens an entry's value, let it
also tighten the **description** (same `Merge.Value` mechanism, add a sibling
`Description` field to `Merge` and a `descriptions` map in the plan). This keeps
tier-0 one-liners crisp over time. It is **not required** for the increment — a
derived description (first line of value) is a fine default and the consolidator can
gain description-tightening in a follow-up. Recommendation: **defer the
description-tightening to a follow-up**; ship the consolidator unchanged. It already
keeps the entry set tight, which is what bounds tier-0.

**Synchronous vs. dream:** nothing new runs synchronously. Index *rendering* is
synchronous (cheap, per-run, in the assembler). Entry merging/pruning stays in the
background dream pass exactly as today. No change to the read/run hot path.

---

## 6. On-disk layout + migration from Task 1's flat format

**Layout: unchanged single file** `memory.json` (`store.go:33`). The `record` type
(`store.go:70-73`) gains one additive field:

```go
type record struct {
    Value       string    `json:"value"`
    Description string    `json:"description,omitempty"` // NEW, additive
    UpdatedAt   time.Time `json:"updated_at"`
}
```

**Migration is a no-op read.** Task-1 files have no `description` key; `omitempty` +
Go's zero-value decode means old files load cleanly with `Description: ""`, and the
index renderer falls back to the value's first line. No version bump, no rewrite
pass, no data movement. The first time the model `Remember`s with a description (or
the consolidator tightens one), that entry's record gains the field on its next
atomic save (`store.go:168-197`); untouched entries keep their old shape forever and
still render fine. This is the cheapest possible migration: **forward-compatible
additive schema, zero migration code.**

A test asserts a literal Task-1 `memory.json` (no `description` fields) opens and
produces a sensible index (descriptions derived from values).

---

## 6a. Concurrency / locking (cross-process safety)

The store is **per-project** but multiple PROCESSES share one `memory.json`: several
`mecated`/`mecatui` instances, plus agent-team / subagent runs. An in-process
`sync.Mutex` + atomic temp+rename prevents *corruption* but NOT **lost updates** —
two processes can interleave read-modify-write and one clobbers the other. The store
closes that with a cross-process advisory lock (`github.com/gofrs/flock`, promoted
from indirect to direct):

- **Sentinel lock file.** The flock is held on a STABLE sentinel `<dir>/memory.lock`,
  never on `memory.json` itself — the data file is renamed-over on every save, which
  would break a flock associated with the old inode. The sentinel is created/owned by
  the store and never renamed, so its lock association is stable for the store's life.
- **Exclusive for writes, shared for reads.** `RememberEntry`/`Remember`/`Forget`
  take an EXCLUSIVE (`TryLockContext`) lock spanning the whole load→mutate→save;
  `Recall`/`List`/`Index` take a SHARED (`TryRLockContext`) lock spanning the read.
  Because the whole read-modify-write sits inside one exclusive critical section,
  two processes can no longer interleave and lose an update.
- **Lock order: `s.mu` THEN flock.** The in-process mutex is kept and acquired FIRST
  — a `flock.Flock` handle shared across goroutines on one fd is not goroutine-safe,
  so `s.mu` serialises in-process callers and the flock serialises across processes.
- **Bounded, ctx-aware acquisition.** The store now USES the previously-ignored
  `context.Context`: it derives a bounded child context (`lockTimeout`, default 5s)
  and acquires via `TryLockContext`/`TryRLockContext` with a small `retryDelay`
  (5ms). A stuck/crashed holder therefore fails LOUD with a clear error instead of
  deadlocking a run. The lock is released (`Unlock`) before return on every path,
  including errors, via `defer`.

`TestCrossProcessRememberNoLostUpdates` proves it: N goroutines each open a FRESH
`Store` over one dir (distinct flock fds == distinct "processes") and Remember a
distinct key concurrently; all N survive.

**Constraint / future work — one `*Store` per dir per process.** `gofrs/flock` uses
BSD `flock(2)`, which contends across file descriptors *even within a single
process*. So opening TWO `*Store` values over the same dir in the SAME process
would self-deadlock (the second writer blocks on the first's lock until the
`lockTimeout` fires). The composition root upholds this by sharing ONE `*Store` per
project (`internal/app`). If per-session memory is ever needed, share a single
`*Store` keyed by absolute dir (the way the session-engine map is keyed) rather than
calling `memory.New` per session — do not open a second `*Store` over an
already-owned dir. No process-wide registry is built today (YAGNI); the invariant is
documented on `memory.New` and the `Store` type instead.

## 7. Layering proof (no domain→adapter edge)

The constraint: `prompt` is a DOMAIN package and must not import the memory adapter
(`CLAUDE.md` layering rule). Here is how the index reaches prompt assembly cleanly.

**The index source is a consumer-defined port in `prompt`:**

```go
// in engine/prompt — a tiny consumer-defined interface; prompt does NOT import
// the memory adapter, only this seam it declares.
type MemoryIndexSource interface {
    // Index returns the tier-0 entries (key + description + updated-at, value
    // omitted) for the current project, or nil if memory is disabled.
    Index(ctx context.Context) ([]session.MemoryEntrySummary, error)
}
```

Two clean options for the summary type — pick one (minor):

- **(a)** `prompt` consumes `[]tool.MemoryEntry` directly. `prompt` already imports
  `tool` (`builder.go:11`) and `tool.MemoryStore` already lives there
  (`tool.go:190`), so this introduces **no new import**. `*memory.Store` already
  satisfies a `tool.MemoryStore`-shaped seam. **Recommended** — zero new types.
- **(b)** a dedicated `prompt.MemoryEntrySummary` value type, mapped at the app
  boundary. More ceremony; only worth it if you dislike `prompt` touching `tool`'s
  memory type (but it already does, transitively, via `Workspace`).

With (a) the seam is literally `interface { Index(context.Context)
([]tool.MemoryEntry, error) }`, satisfied by `*memory.Store`.

**The assembler:**

```go
// engine/prompt
type MemoryIndexAssembler struct {
    Src       MemoryIndexSource // nil → assembler is a no-op (memory disabled)
    MaxEntries int              // tier-0 cap (D4); 0 → default
    MaxBytes   int
}
func (a MemoryIndexAssembler) Assemble(ctx context.Context, ws tool.Workspace) ([]session.Message, error) {
    if a.Src == nil { return nil, nil }
    entries, err := a.Src.Index(ctx)
    // render capped tier-0 → one user message; never error on memory faults
    // (memory is best-effort context, not correctness — fail soft to nil).
}
```

**Composition (`internal/app`):** `app.Build` already constructs `*memory.Store`
(`build.go:732`). It composes a `prompt.MultiAssembler{ RootAssembler{},
MemoryIndexAssembler{Src: store} }` and assigns it to `agent.Deps.Instructions`
(`loop.go:62`). The adapter (`*memory.Store`) meets the `prompt`-defined seam **only
in the composition layer**, which is exactly where adapters are allowed to meet
ports (`CLAUDE.md`). `app` may import `memory` (it does already) and `agent`/`prompt`.

**Edges introduced:**

- `prompt` → `tool` (already exists; `MemoryIndexSource` uses `tool.MemoryEntry`). No
  new package edge.
- `app` → `memory`, `app` → `prompt`, `app` → `agent` (all already exist).
- **`prompt` → `memory`: NONE.** `prompt` declares the `MemoryIndexSource` interface;
  the adapter satisfies it structurally; they meet in `app`. Dependency points inward
  (adapter → domain interface), never outward.

A `MultiAssembler` (compose N `InstructionAssembler`s, concatenate their messages) is
a 10-line addition to `engine/prompt`. It is the only new prompt-package type
besides the assembler + seam.

---

## 8. File-by-file change list

**Domain — `engine/tool/tool.go`**
- `MemoryEntry`: add `Description string` (`tool.go:170`).
- `MemoryStore`: add `Index(ctx) ([]MemoryEntry, error)` and `RememberEntry(ctx,
  MemoryEntry) error`; keep `Remember` as a documented convenience wrapper
  (`tool.go:190`).

**Domain — `engine/prompt/`**
- New `MemoryIndexSource` interface + `MemoryIndexAssembler` (new file, e.g.
  `memoryindex.go`) — renders the capped tier-0 user message; no-ops when source
  is nil; fails soft.
- New `MultiAssembler` (concatenates child assemblers) in `instructions.go`.

**Adapter — `internal/adapter/memory/store.go`**
- `record`: add `Description string json:"description,omitempty"` (`store.go:70`).
- Implement `Index` (list all, omit values, fill/derive descriptions, sort).
- Implement `RememberEntry`; make `Remember` delegate to it.
- `_ tool.MemoryStore` assertion (`store.go:46`) now also covers the new methods.

**Adapter — `internal/adapter/memory/tools.go`**
- `recallDescription`: delete the false "not an index" line; add the
  "your index is shown at session start" lines (`tools.go:53-76`).
- `rememberArgs` + schema + description: add optional `description`
  (`tools.go:101-118`).
- `Remember`'s `Execute`: call `RememberEntry`; echo the resulting index line
  (`tools.go:126-141`).

**App — `internal/app/build.go`**
- Where the memory store is built (`build.go:731-739`): compose
  `prompt.MultiAssembler{prompt.RootAssembler{}, prompt.MemoryIndexAssembler{Src:
  store, MaxEntries: ...}}` and set it on `agent.Deps.Instructions`. When memory is
  disabled, the assembler is simply not added (or added with `Src: nil`).

**Consolidator — `internal/adapter/dream/`**
- **No change in this increment** (description-tightening deferred, §5).

**No change:** `cmd/mecatui/*`, `cmd/mecated/*` (Task-1 wiring is opaque and
unaffected — they pass a dir, the store owns format). The proto contract, the server
adapter, the ACP layer: untouched (memory is a tool + a turn-0 user message, both
already-modeled wire shapes).

---

## 9. Test plan (offline)

All offline (`mockllm` + `memfs` + `t.TempDir()`), per CLAUDE.md.

**`internal/adapter/memory/store_test.go`**
- `TestIndexOmitsValuesAndDerivesDescription` — Remember two entries (one with an
  explicit description, one without); `Index` returns both with `Value:""`, explicit
  description preserved, derived description = first line of value, sorted by key.
- `TestRememberEntryRoundTripsDescription` — `RememberEntry` with a description;
  `Recall` returns the full value, `Index` shows the description.
- `TestMigrationReadsTask1FlatFile` — write a literal Task-1 `memory.json` (records
  with only `value`/`updated_at`, no `description`); open a fresh `Store`; `Index`
  derives descriptions and `Recall` returns values — proving the additive schema
  needs zero migration code.
- Existing tests (`store_test.go:11-163`) keep passing (Remember wrapper unchanged).

**`engine/prompt/` (new `memoryindex_test.go`)**
- `TestMemoryIndexAssemblerRendersUserMessage` — a fake `MemoryIndexSource` with 3
  entries → exactly one `session.Message`, user role, containing all 3 key+description
  lines, no values.
- `TestMemoryIndexAssemblerCapAndFooter` — source with > MaxEntries → rendered output
  is capped, oldest trimmed, footer present.
- `TestMemoryIndexAssemblerNilSourceIsNoop` — `Src: nil` → nil messages, nil error.
- `TestMemoryIndexAssemblerFailsSoft` — source returning an error → nil messages, nil
  error (memory is best-effort context).
- `TestMultiAssemblerConcatenatesInOrder` — RootAssembler (AGENTS.md via memfs) then
  MemoryIndexAssembler → AGENTS.md message precedes the index message.

**`internal/adapter/memory/tools_test.go`**
- `TestRememberAcceptsDescriptionAndEchoesIndexLine`.
- `TestRecallStillFetchesFullValue` (regression: tier-1 load by key unchanged).

**`engine/agent/` (integration, mock provider)**
- Extend the existing full-cycle / instruction-prepend test: with a non-empty store
  and the composed assembler, turn-0 conversation contains the index user message
  *before* the first user prompt, and the StablePrefix is byte-identical to the
  no-memory case (cache invariant — the index is NOT in the system prefix). This is
  the test that proves D2.

**Gauntlet #6 (prompt-cache):** the existing prompt-cache test must stay green —
the index lives outside `prompt.Build`, so `StablePrefix` is unchanged. Assert it.

---

## 10. Risks / open questions

- **R1 — index is run-start-stale.** The index is computed at turn 0 and not
  refreshed mid-run, so a `Remember` early in a long multi-turn run is not reflected
  in the *index* until the next run (the value is recallable immediately; only the
  index line lags). Mitigation: Remember's result echoes the new index line
  (§4), so the model sees its own write immediately without needing the index
  refreshed. Accept; revisit only if it bites. (Tied to D2.)
- **R2 — description quality.** Derived descriptions (first line of value) can be
  poor if the model writes prose-first values. Mitigation: the Remember tool doc
  steers toward an explicit one-line `description`; the consolidator can tighten
  later (deferred). Accept.
- **R3 — cap trimming hides entries.** Over D4's cap, oldest entries drop off
  tier-0. They are still Recall-able by key/prefix, and the footer says so, but the
  model cannot *see* the key to Recall it. For a curated per-project store, hitting
  200 entries signals the consolidator should be on. Accept; the cap is the honest
  bound and the footer is the escape hatch. **(Task 3 — `MEMORY-TIER2.md` — closes
  this with the read-only BM25 `SearchMemory` tool, which searches ALL entries
  (incl. trimmed ones) by keyword and ships on by default whenever memory is
  enabled; it also flags that the cap is reachable in normal use only because
  consolidation is OFF by default on both surfaces.)**
- **R4 — D1 reversal cost.** If you later choose file-per-fact (D1), the on-disk
  format changes and a real migration appears. The interface (`Index`,
  `RememberEntry`) and the prompt seam are format-agnostic, so only the adapter
  changes — the domain/prompt/app blast radius is unchanged. The derived-index
  choice is cheap to reverse.
- **Open — D1/D2/D3/D4** above are the decisions to confirm before implementation.


---

*Part of the [design docs](../design/README.md). Read in order: [Memory enabled by default on the embedded mecatui server](0008-memory-on-by-default.md) ← MEMORY-TIERING → [Tier-2 / semantic memory recall — assessment + buildable design](0010-semantic-memory-recall.md).*
