# ADR 0107 — Live operator profiles and reversible memory lifecycle

- Status: Accepted
- Date: 2026-08-13
- Scope: prompt assembly, memory stores/tools, driver protocol, and user-model inspection
- Supersedes: none
- Superseded by: none

## Context

The user model was summarized as a turn-0 user-message fragment. That made current
operator facts stale during a multi-turn run, blurred operator data with conversation
history, and provided no safe model-facing way to inspect, forget, or undo a fact.
The original `tool.MemoryStore` and its six driver RPCs must remain compatible with
existing engine consumers and remote drivers. The local store must also keep its
single-file, flocked transaction boundary; a history sidecar would permit current
state and history to diverge after a crash.

## Decision

Load current `user/` facts through the consumer-local `prompt.OperatorProfileSource`,
which reuses the existing `MemoryStore.List` shape, for every main, subagent, team-member,
and lead-synthesis provider request. Render them only in the volatile system-prompt suffix;
internal checker/reviewer/judge engines remain profile-free. Keep `UserModelAssembler` public
for compatibility, but do not wire it in standard composition. Keep the project memory index
as an ephemeral turn-0 JSONL routing fragment; project values are never boot-injected.

Add `tool.MemoryLifecycleStore` as an optional capability beside the unchanged
`tool.MemoryStore`. `RememberVersioned` with an omitted expected version is an
unconditional last-write-wins update, matching ordinary Remember; a non-empty token
requests compare-and-swap and a stale token conflicts. Forget and Undo require explicit
current versions. Writes append immutable active, superseded, or deleted revisions.
Forget writes a tombstone; undo writes a compensating revision. The local adapter stores
current entries and history in the existing `memory.json` document under one
process/cross-process lock and one atomic rename. Legacy documents migrate lazily on
their first mutation.

Register the portable base memory tools for every `MemoryStore` and register
Inspect/Forget/Undo only when the lifecycle capability is present. Remember keeps its
existing floor Allow; Recall, Search, Inspect, and Undo are floor Allows; Forget is a
floor Ask. Every floor remains overridable by configured permission rules. Automatic
learning is independent: disabling it does not remove explicit tools or profile reads.

Extend `MemoryStoreService` additively with an explicit capability-negotiation RPC
and lifecycle RPCs. The original six RPCs remain unchanged, profile loading uses old
`List`, and consolidation stays wholly on those six operations, so old drivers retain
ordinary overwrite and profile behavior. The lifecycle wrapper and tools are exposed
only when the remote positively reports the complete lifecycle capability; an old
`UNIMPLEMENTED` capability response is base-only, never a partial lifecycle plan.

Extend the existing `GetUserModel` read surface with an optional exact-key detail.
The TUI fetches it lazily after selection and exposes no mutation RPC; Forget and Undo
continue through ordinary tools and their permission checks. All model/wire/terminal
projections repair UTF-8 and structurally encode memory data. Writes reject
high-confidence credentials in values or descriptions, including common wrappers;
model-authored user facts additionally reject narrow role/directive overrides. The
final profile and detail boundaries repeat those checks for imported, migrated, and
remote data without suppressing ordinary Unicode preferences or security discussion.
Revision provenance is limited to the source session ID; finer evidence handles are
outside this decision.

## Consequences

The cache-stable prompt prefix remains byte-identical while facts can change between
turns and sessions. Operator facts do not enter conversation history or compaction.
Memory deletion is reversible and concurrent writes fail with visible version
conflicts. Local history increases `memory.json` size, and lifecycle-aware remote
drivers must implement four additional RPCs. Old drivers remain useful for ordinary
operations and profile reads, but lifecycle mutations report their unavailable
capability honestly.

The exact-detail API is intentionally read-only. Operators can inspect exact values
in the TUI, but all changes still pass through the same model tool, dispatcher, hook,
and permission path as any other mutation.

## See also

- [Memory architecture](../architecture/memory.md)
- [Agent loop](../architecture/agent-loop.md)
- [Extension points](../architecture/extensibility.md)
- [Operator guide](../usage/skills-soul-usermodel.md)
- [Cloud resource inventory](./0027-cloud-native.md)
