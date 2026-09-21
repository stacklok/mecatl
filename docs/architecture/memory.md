# Memory — cross-session recall & consolidation

> Part of the [mecatl architecture guide](../architecture.md).

**What this covers:** `tool.MemoryStore` (Remember/Recall/SearchMemory across sessions), the file-backed `memory` adapter, the opt-in `dream` consolidation service, the user model (RememberUser/RecallUser/SearchUserModel — cross-project operator FACTS), and staged evidence reflection.

**Prerequisites:** [the agent loop](agent-loop.md) — the loop injects the memory index each run.

**Follow-on:** return to the [reading map](../READING.md) and choose another topic branch. **Related:** [context & compaction](context-and-compaction.md) covers per-run context management, which is independent of memory.

`tool.MemoryStore` (`RememberEntry`/`Recall`/`List`/`Forget`/`Index`/`Search`) is
the seam for conservative, **per-project** memory (every implementation must pass
the shared `engine/adapter/memconformance` conformance suite). The file-backed
`internal/adapter/memory` persists entries scoped to a project directory and exposes them to
the model as the **Remember**, **Recall**, and **SearchMemory** tools (opt-in via
`memory.Register`, `--memory-dir`). `internal/adapter/dream` supplies both automatic and
manual consolidation. Planning sorts keys, selects a bounded rotating window, sends whole
values and descriptions to the configured model, and accepts one strict bare JSON object with
exactly two operation families: exact duplicates and synthesized replacements. Both families
must identify existing survivor/source keys; synthesis also carries the complete proposed
replacement value and description. Unknown/missing members, trailing content, repeated or
cross-role keys, unchanged synthesis, and standalone deletion are rejected.

Candidate rotation is process-local and bounded by entry count and aggregate input bytes.
For a stable finite set in one continuously running consolidator, every fitting entry gets a
turn. Insertions, removals, oversized entries, and restart can change the order; the cursor
resets on restart. Plans bind the exact inspected lifecycle versions.

**Automatic maintenance** remains exact-duplicate-only. It requires the local store's internal
atomic duplicate-retirement operation, compares the survivor and source versions, and
requires byte-identical active value and description. One source is tombstoned per transaction,
the survivor is never rewritten, lifecycle history is retained, and base/convergence-only
remote stores skip application. `--memory-consolidate-interval` and
`--user-model-consolidate-interval` remain separate opt-in schedules and default to zero.
Periodic diagnostics contain counts only.

**Manual maintenance** is an explicit human review, not reflection or learning. mecatui
`/dream` chooses project memory or the cross-project user model, warns that generation sends
the selected bounded values and descriptions to the configured model and spends tokens, then
shows every exact-duplicate or synthesized-replacement operation. Model-authored replacement and
reason fields that contain hidden controls or Unicode format characters are rejected before the plan
is retained; review and mutation use the same accepted bytes. Stored participant text is not rewritten
and is rendered as quoted, per-line-prefixed data. Apply and dismiss are
whole-plan decisions. Approved synthesis atomically compares the bound inspected versions for
the displayed participants, rewrites
the displayed survivor to the displayed replacement, and tombstones all displayed sources
for that operation. Independent operations continue after conflicts/failures, so the receipt
can be partial; no grouped transaction or grouped undo is claimed.

Manual plans have opaque random IDs in a bounded Build-owned registry (64 records, at most 8
pending per target), expire after 10 minutes with lazy cleanup, and retain no plan/review
content after a terminal decision. Same decisions are idempotent. A same-decision retry while apply
is still running reports in-progress and remains retryable; an opposite decision is non-retryable, with
fresh-plan generation offered only after the old decision is terminal. Plans are not durable or
cross-replica. `NOT_FOUND` after restart, expiry, or wrong-replica routing cannot retrieve a receipt and
offers explicit fresh generation. Only an indeterminate transport failure preserves the exact plan ID
and decision for explicit same-decision retry because the first request may already have applied. No
error path offers the opposite decision. Manual review requires a planner
and a target store implementing both reviewed atomic operations, and is unavailable while
ownership enforcement is enabled. It does not change automatic schedule flags and collects
no recall-usage telemetry.

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

`tool.MemoryStore` is the mandatory lifecycle/CAS contract. Revisions have opaque
versions and active/superseded/deleted states. Remember without an expected
version is create-only; updates require the exact current version. Forget and Undo
also require the exact current version: copy the opaque token verbatim from an
exact Recall result, Inspect result, or mutation receipt in the same scope. Never
guess or interpret it. Forget appends a tombstone, and Undo appends compensation.
The local adapter stores current state plus history in `memory-v2.json` under the
same `memory-v2.lock` flock and atomic rename (no sidecar transaction). Older
`memory.json` documents are left untouched and invisible. Both reference stores
retain 64 revisions per key. The local store also bounds fields to 64 KiB and caps
the store at 4096 keys / 8 MiB. Retention records when the oldest predecessor was
truncated, so Undo stops without mutation at that boundary instead of mistaking
it for proof that the retained target created the key.

Remember is floor-Allow as before; Recall/Search/Inspect/Undo are floor-Allow and
Forget is floor-Ask. All are config-overridable. Completed-trajectory reflection is
controlled by `learning.mode` (`off` by default): Off attaches no automatic observer,
Review stages bounded evidence-backed proposals without a memory write, and Auto stages
through the same repository before conservatively promoting eligible non-conflicting
facts with source-session attribution. Durable admitted attempts are claimed before
source/provider setup. Missing, deleted, unauthorized, or invalid source evidence
terminally records only `evidence_unavailable`; a merely incomplete terminal event
sequence or transient provider setup keeps the claim as persisted exponential backoff and reaches `retry_exhausted` after three
failed setup claims across restarts, rather than cycling on the discovery interval.
Standard non-off composition selects a durable
automatic-admission ledger, so global count/token bounds, cooldown, and deduplication
are advertised only after that ledger is successfully selected. An unwired or
unhealthy ledger retains ADR-0114's process-local limitation and is never presented
as globally bounded. Explicit reflection remains available in Off via a lazy path. Project candidates are staged only for the exact admitted configured root.
Proposal detail re-checks source ownership and evidence digests and exposes only a bounded,
redacted canonical preview before approval. Consolidation is independently maintained: automatic schedules remain off by default and
retire only byte-identical duplicates through the local atomic operation. Manual `/dream`
review may show exact duplicates and synthesized replacements for whole-plan apply/dismiss;
its version-bound plans are process-local, expire, and require regeneration after restart or
a wrong-replica decision. Neither surface collects recall-usage telemetry or changes
completed-trajectory learning.

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
