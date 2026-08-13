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

**User model and live operator profile.** A SECOND `memory.Store` — user-scoped and
**cross-project** (`<xdg>/mecatl/usermodel`, distinct from the per-project store) —
holds durable facts about the operator. Standard composition exposes the portable
RememberUser/RecallUser/SearchUserModel tools and, for lifecycle-capable stores,
InspectUserMemory/ForgetUserMemory/UndoUserMemory. The same store satisfies
`prompt.OperatorProfileSource` through its existing `List` shape: the loop reloads
current valid `user/` facts for every main, subagent, team-member, and lead-synthesis
provider request and renders a bounded, JSON-structured `<operator-profile-data>` block
only in the volatile system suffix. Internal checker/reviewer/judge engines omit it. It
never enters conversation history, never changes the cache-stable prefix, and can refresh after a tool write in the same run. The public
`prompt.UserModelAssembler` remains available for engine compatibility but standard
composition no longer wires its turn-0 user fragment. Project memory keeps only its
value-omitting turn-0 index.

This user model is **operator-scoped and process-global**, not keyed by the
authenticated caller. A multi-tenant deployment must isolate user-model stores
(and normally processes) per operator; caller identity does not provide
per-principal user-model isolation. Exact detail is exposed only through the same
already-authorized user-model surface and does not widen its callers.

`tool.MemoryLifecycleStore` is an additive capability beside the unchanged six-method
`tool.MemoryStore`. Revisions have opaque versions and active/superseded/deleted
states. Remember with no expected version remains unconditional last-write-wins; a
non-empty expected version enables CAS and stale versions conflict. Forget appends a
tombstone, and Undo appends compensation. The local adapter lazily materializes legacy `memory.json`
entries and commits current state plus history under the same flock and atomic rename
(no sidecar transaction). Driver lifecycle RPCs are additive and advertised through
a one-time capability negotiation bounded by a fixed five-second ceiling (shorter caller
deadlines still win); old drivers return a base-only client, retain all
six ordinary operations and profile loading through `List`, and do not register
Inspect/Forget/Undo. Both reference stores retain 64 revisions per key. The local store also
bounds fields to 64 KiB and caps the store at 4096 keys / 8 MiB. Retention records when the
oldest predecessor was truncated, so Undo stops without mutation at that boundary instead of
mistaking it for proof that the retained target created the key.

Remember is floor-Allow as before; Recall/Search/Inspect/Undo are floor-Allow and
Forget is floor-Ask. All are config-overridable. Automatic review is controlled by
`learning.mode` (`off` by default); turning it off does not remove explicit tools or
the live profile. In `auto`, the completed-trajectory observer writes accepted facts
through RememberUser and preserves the source session attribution. Consolidation is
independently operator-scheduled and deliberately uses only the six base-store operations,
so local and old remote stores execute the same coherent plan.

Every final model/wire/TUI projection first uses the shared
`tool.CanonicalMemoryText` representation: it repairs invalid UTF-8 and strips the exact
invisible/bidi format and non-LF/tab control set before both classification and rendering.
High-confidence secret-shaped values and descriptions are then withheld. All base and lifecycle Remember paths validate
both fields, including high-confidence Bearer/assignment/quote/short-label and indented
PEM wrappers. New lifecycle writes additionally enforce the strict namespaced key grammar.
Model-authored user facts pass a narrow directive/role override check before persistence,
and the final profile/detail boundaries repeat both checks for imported, migrated, or
remote entries; ordinary facts, useful Unicode, and security prose remain accepted.
Composition rejects direct or symlink-aliased project-memory/user-model directories,
and profile enumeration accepts only valid `user/` keys.

## Prerequisites

- [The agent loop that injects the memory index](agent-loop.md)

## Follow-on reading

- Return to the [reading map](../READING.md) and choose another topic branch.

## Related

- [Context & compaction](context-and-compaction.md)

---

[← Architecture guide](../architecture.md)
