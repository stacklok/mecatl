# Context management & the compaction cascade

> Part of the [mecatl architecture guide](../architecture.md).

**What this covers:** the token budget (`MaxRunTokens`), `TokenCounter`/`Compactor` seams, the compaction cascade (cost-first tiered), back-snap to recent user turns, forward-snap past leading tool results, and the `ValidateToolPairing` abort guard.

**Prerequisites:** [the agent loop](agent-loop.md) — the loop triggers compaction at the turn boundary.

**Follow-on:** return to the [reading map](../READING.md) and choose another topic branch. **Related:** [memory](memory.md) covers cross-session recall, which is independent of compaction.

Two seams keep a long run inside the model's context window. Composition resolves that
window once per use through one provider/model-exact precedence chain: the global
`--context-window-override`, operator-tier `models.context_windows`, positive live
metadata, the models.dev catalog, then the 128K fallback. Alias and slot routing happen
first, so configuration keys are final provider/model IDs; the same resolver feeds
compaction, engine introspection, session echoes, model listings, per-session engines,
and provider-bound children. The operator-owned exact-map decision is recorded in
[ADR 0207](../adr/0207-context-window-overrides.md).

- **`TokenCounter`** (`engine/agent/tokencount.go`) estimates model-visible request
  cost. The default `HeuristicTokenCounter` (about chars/4) needs no dependencies;
  the offline **`tokenizer.Counter`** (`internal/adapter/tokenizer/tokenizer.go`,
  tiktoken BPE tables embedded, no network or CGO) is the accurate swap-in selected
  with `--tokenizer=tiktoken`. Both count message framing, reasoning, tool calls,
  message parts, and typed tool-result parts. For a tool result they use the larger
  of flattened `Content` and `Parts`, which avoids double-counting alternate
  projections without treating typed-only results as free.
- **Automatic trigger.** Before every model turn, `engine/agent/loop.go`
  (`estimateRequestTokens`) estimates the complete `port.LLMRequest`: the rendered
  system prompt, ephemeral turn-0 fragments plus persisted conversation messages,
  and every advertised tool's name, description, JSON schema, and envelope overhead.
  At the default ratio, `maybeCompact` runs when that estimate reaches **0.8** of the
  resolved window. Only `Session.Conversation` is compactible. System instructions,
  ephemeral fragments, and tool definitions remain in the request, so they are
  irreducible overhead and can by themselves keep the estimate above the trigger.
  After a successful pass the loop rebuilds only the message suffix; the system and
  tool layers remain byte-for-byte unchanged.
- **`Compactor`** (`engine/agent/compaction.go`) compresses persisted conversation
  history once the trigger is crossed. The default `HeuristicCompactor` is
  single-summary: it preserves the goal + touched file paths, truncates large tool
  bodies, and keeps the last N messages. The swap-in `CascadeCompactor`
  (`engine/agent/cascade.go`, `--compaction=cascade`) runs a **cheapest-first tiered
  cascade**: snip, strip tool bodies, collapse large file bodies, then summarize. On
  automatic compaction it derives a request-local target from the same live window:
  complete request ≤ **0.6** of the window, minus measured irreducible system, fragment,
  and tool-schema overhead for the compactible persisted-history budget. The configured
  cascade budget remains the manual-pass behavior. This 0.8/0.6 trigger/target hysteresis
  avoids thrash and prevents a fixed 128k target from no-oping on a smaller live window.

  Both compactors **back-snap the kept-tail boundary to recent user turns** (the
  shared `snapCutToRecentUserTurn` helper) so the most-recent user instruction(s)
  survive verbatim instead of falling into the summarised head — the role-blind
  count-tail bug (during heavy tool use the last N messages are all assistant/tool,
  so the user's actual task was lost). The first user message stays pinned, the
  back-snap pulls up to `recentUserTurnsKept` recent user turns into the tail
  (bounded by `maxUserSnapLookback` so an ancient lone turn can't drag everything
  in), and tier-4 summarises older/superseded intent under a dedicated
  `## User instructions and intent` section (prior art: Codex, gemini-cli). They
  then **snap the kept-tail boundary past leading tool results** (the shared
  `snapCutToTurnBoundary` helper, applied LAST) so the preserved tail never STARTS on
  a `RoleTool` message whose matching assistant tool call was dropped into the head —
  an orphaned tool result draws a provider HTTP 400 on replay. As a final guard
  each compactor **self-validates** the assembled slice with
  `session.ValidateToolPairing` (bidirectional: no orphaned results, no dangling
  calls) and, on failure, **aborts to the original history** with the
  `agent.ErrCompactionWouldOrphan` sentinel; the loop treats it like any other
  compaction failure (keep the uncompacted history, WARN, continue). The aggregate
  itself backstops this: `Session.ReplaceHistory` rejects an unpaired slice.

## Manual compaction

Automatic compaction waits for the 0.8 trigger. A client can instead request one
forced pass through `engine/agent/manual_compaction.go` (`CompactSession`), regardless
of the current estimate. This is an out-of-band session operation, not a prompt or
model turn. The configured compactor still applies, so the cascade's summary tier can
make a compaction-slot model call and incur its normal cost.

`internal/adapter/server/service.go` (`CompactSession`) accepts owned main-chat
sessions at a turn boundary: idle, completed, cancelled, or failed. It rejects
running and awaiting sessions, delegation and scheduled sessions, and any same-process
live run. The per-session run-entry lock serializes it with prompt start, and a
configured mutation lease excludes another server replica. Rehydrated sessions use
their persisted provider/model/profile engine rather than the shared default.

An empty, identical, pairing-invalid, or non-reducing candidate is never applied (pairing
invalidity is reported; the other cases are successful no-ops). Automatic compaction uses
the same admission gate, so irreducible overhead cannot make short histories grow by
accumulating summaries. A manual no-op reports no change and the service performs no save
or event append. On a real change,
the service saves the compacted snapshot first, then appends the existing
`EvCompaction` notice and `EvCompactionArchive` in order. A save failure returns an
error before either event. Event-log append failure is best-effort after commit: it is
warned and does not roll back or retry the compacted snapshot, so the event log can
lack the notice or archive.

The operation is exposed as gRPC `CompactSession` and bodyless HTTP
`POST /v1/sessions/{id}/compact`. `ServerCapabilities.manual_compaction` lets clients
hide it when talking to an older server. Mecatui uses that bit for its bare `/compact`
built-in; the command is local control flow and never becomes model input. These
additive decisions are recorded in [ADR 0276](../adr/0276-full-request-and-manual-compaction.md).

## Prerequisites

- [The agent loop that triggers compaction](agent-loop.md)

## Follow-on reading

- Return to the [reading map](../READING.md) and choose another topic branch.

## Related

- [Memory — cross-session recall](memory.md)

- [Providers — the per-model token counter & window](providers.md)
- [Observability & persistence](observability.md) — `EvCompactionArchive` is the durable, non-destructive bridge: compaction emits the pre-compaction conversation to the event log.

---

[← Architecture guide](../architecture.md)
