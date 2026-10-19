# Context and compaction

A long run eventually produces more history than the model's context window holds.
Before each model turn, the [agent loop](agent-loop.md) measures the complete request
and, when it nears the window, compacts the persisted conversation. This page explains
how the window and the token estimate are produced, what each compactor does, and why
compaction must never split a tool call from its result. The operator-facing settings
are in [context windows](../../user-docs/features/sessions/context-windows.md).

## The window

Composition resolves the window for the exact provider and model at the point of
use (`internal/app/provider_discovery.go`); the user guide above lists the precedence
and fallback, and [providers](providers.md) covers discovery. With no usable window,
the server rejects the run with `context_window_unavailable` before recording the
prompt. The engine rereads the window through `Deps.ContextWindow` on every check,
and a non-positive window disables automatic compaction.

## Counting tokens

`agent.TokenCounter` (`engine/agent/tokencount.go`) estimates tokens for text and
messages, including framing, reasoning, tool calls, and media parts. The default
`HeuristicTokenCounter` divides bytes by four; the offline tiktoken counter in
`internal/adapter/tokenizer` is more accurate. Neither must be exact, but both must
be deterministic, or compaction decisions would not be reproducible. A tool result
counts as the larger of its flattened text and its typed parts, so a typed-only
result is never free.

## When compaction runs

At step 5 of each turn, `Engine.maybeCompact` (`engine/agent/loop.go`) estimates the
request it is about to send: the rendered system prompt, the instruction fragments,
the persisted conversation, and every advertised tool's name, description, and
schema. Compaction runs when that estimate reaches `Deps.CompactionRatio` of the
window, 0.8 by default.

Only the persisted conversation can shrink; the system prompt, fragments, and tool
schemas are irreducible. A compactor that accepts a budget, such as the cascade, is
told to fit history into 0.6 of the window minus that overhead. The gap between the
trigger and the 0.6 target stops the next turn from triggering again at once.
After a pass, the loop swaps only the message suffix of the built request, so the
system prompt and tool definitions stay byte-identical for the prompt cache. Each
pass emits a `compaction` event and a log-only `compaction.archive` event holding
the replaced messages, so the event log keeps the full history
([observability](observability.md)).

## The compactors

`agent.Compactor` (`engine/agent/compaction.go`) is the seam; composition picks one
per engine. `HeuristicCompactor`, the default, makes no model call. It keeps system
messages and the first genuine user message (the goal), adds one message listing
every file path touched so far, and keeps the recent tail with long tool bodies
truncated.

`CascadeCompactor` (`engine/agent/cascade.go`) also keeps the goal, touched paths,
and recent tail, and applies cheaper tiers first, stopping once history fits:

1. **Snip** drops the older half of the middle segment.
2. **Strip** truncates large tool-result bodies.
3. **Collapse** replaces large file bodies with a pointer; the model can reread them.
4. **Summarize** asks the compaction-slot model for a structured summary of the
   middle, framed as data rather than instructions, and records it as compaction
   usage. Without a wired provider the cascade stays deterministic and stops at 3.

Both compactors move the start of the kept tail back, within a fixed lookback, to
include the most recent user turns. In tool-heavy stretches the last few messages
are all tool traffic, and a count-only cut would drop the user's current request.

## Keeping tool history paired

Providers reject a tool result without its matching call, or a call without its
result. History like that fails on every later replay, so one bad compaction would
leave the session unusable. Both compactors therefore move the cut forward past any
leading tool results as their last step, so the tail never opens on an orphan. Each
then checks its output with `session.ValidateToolPairing` and, on failure, returns
the original history with `agent.ErrCompactionWouldOrphan`. The shared admission
step checks the candidate again, and `Session.ReplaceHistory` refuses an unpaired
slice as the final backstop.

## Manual compaction

`Engine.CompactSession` (`engine/agent/manual_compaction.go`) runs the configured
compactor once, whatever the estimate, with the same admission rules but the
compactor's own budget. The aggregate allows it only at a turn boundary, never while
a run is in progress or awaiting approval. The server's `CompactSession` operation
also limits it to main chat sessions, serializes it with run starts and the session
lease, and saves the snapshot before appending the compaction events; a failed
append only logs a warning. The [gRPC API reference](../../user-docs/reference/grpc-api.md)
lists the call.

## Failure modes

Compaction is best-effort and never fails a run by itself.

- **Compactor error or unpaired output:** the loop keeps the existing history, logs
  a warning, and continues. A failed tier-4 summary aborts the whole pass the same
  way.
- **No reduction:** an empty, unchanged, or no-smaller candidate is discarded, so
  irreducible overhead cannot make history grow by stacking summaries.
- **Still over the window:** when overhead alone exceeds the trigger, or the best
  candidate is still too large, the loop sends the request anyway. If the provider
  rejects it as too long, the run fails like any other provider error.

## Related

- [The agent loop](agent-loop.md)
- [Providers](providers.md)
- [Observability](observability.md)
- [Context windows](../../user-docs/features/sessions/context-windows.md)
