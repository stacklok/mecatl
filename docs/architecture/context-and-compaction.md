# Context management & the compaction cascade

> Part of the [mecatl architecture guide](../architecture.md).

**What this covers:** the token budget (`MaxRunTokens`), `TokenCounter`/`Compactor` seams, the compaction cascade (cost-first tiered), back-snap to recent user turns, forward-snap past leading tool results, and the `ValidateToolPairing` abort guard.

**Prerequisites:** [the agent loop](agent-loop.md) — the loop triggers compaction at the turn boundary.

**Follow-on:** return to the [reading map](../READING.md) and choose another topic branch. **Related:** [memory](memory.md) covers cross-session recall, which is independent of compaction.

Two seams keep a long run inside the model's context window:

- **`TokenCounter`** (`engine/agent/tokencount.go`) estimates message-slice token cost.
  The default `HeuristicTokenCounter` (≈chars/4) needs no dependencies; the
  offline **`tokenizer.Counter`** (`internal/adapter/tokenizer`, tiktoken BPE
  tables embedded — no network, no CGO) is the accurate swap-in, selected with
  `--tokenizer=tiktoken`.
- **`Compactor`** (`engine/agent/compaction.go`) compresses the conversation once it
  crosses the trigger ratio. The default `HeuristicCompactor` is single-summary:
  it preserves the goal + touched file paths, truncates large tool bodies, and
  keeps the last N messages. The swap-in `CascadeCompactor` (`engine/agent/cascade.go`,
  `--compaction=cascade`) runs a **cheapest-first tiered cascade** —
  snip → strip tool bodies → collapse large file bodies → summarize — stopping as
  soon as the slice fits the token budget, with trigger/target **hysteresis** so
  it does not thrash near the threshold.

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
