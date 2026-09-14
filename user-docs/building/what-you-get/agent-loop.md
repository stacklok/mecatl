---
sidebar_position: 1
title: The agent loop
description:
  Understand how the Mecatl engine runs turns, dispatches tools, records events,
  and finishes sessions.
---

# The agent loop

`Engine.Run` starts a background loop and returns a `*Run` handle. Use the
handle's event channel to follow the run and answer permission requests. The
loop calls the model, runs tools, records their results, and repeats until the
session ends.

## The event stream

`Run.Events()` returns every observable event in order and closes when the run
ends. Range over the channel to follow a session from start to finish.

Events you will see, roughly in order:

|Event|When it fires|
|-|-|
|`session.init`|Once, before the first turn|
|`turn.start`|Beginning of each LLM turn|
|`message.delta`|Streaming text fragments from the model|
|`tool.call`|A tool call is about to execute|
|`tool.result`|A tool finished and returned a result|
|`permission.ask`|The loop has paused for approval|
|`result`|The run ended; includes usage and the stop reason|

The sequence for a single turn with one tool call looks like this:

```mermaid
sequenceDiagram
  participant C as Client
  participant E as Engine
  participant L as LLMProvider
  participant D as dispatch
  participant T as Tool
  C->>E: Run(ctx, sess, env, RunRequest{Text: "fix the bug"})
  E->>E: Begin turn
  E->>L: Stream(LLMRequest)
  L-->>E: Stream response
  E-->>C: message.delta
  L-->>E: Request Read tool
  E->>D: Dispatch Read
  D->>D: Check permission and hooks
  D-->>C: tool.call
  D->>T: Execute(call, env)
  T-->>D: ToolResult
  D-->>C: tool.result
  D->>D: Run post-tool hooks
  D->>E: Record result
  E->>E: Start next turn
  E-->>C: result
```

---

## Turn structure

Each turn follows this sequence:

1. **Check limits.** The run ends if it has a stop reason, is cancelled, or has
   reached its token budget.
2. **Compact when needed.** If the request approaches the context limit, the
   loop compresses persisted history before the next model call. See
   [Context limits and compaction](#context-limits-and-compaction).
3. **Call the model.** Mecatl streams the response and retries eligible failures
   that occur before output becomes visible.
4. **Run tools.** Mecatl dispatches requested tools, records their results, and
   starts another turn.
5. **Finish or continue.** Meaningful text without tool calls ends the run. An
   empty or reasoning-only response receives up to `MaxNoProgressNudges`
   continuation prompts before ending with `StopNoProgress`. Cancellation,
   refusal, truncation, and failure end immediately.

A tool error does not abort the run. It becomes an error result fed back to the
model, which can retry or choose a different path.

### Retrying a failed model step

A terminal result can include `retry_disposition` (`unknown`, `retryable`, or
`permanent`) and `stream_progress` (`unknown`, `precommit`, `visible`, or
`complete`). Missing fields do not indicate that retry is safe.

Use gRPC `RetryStart` or `POST /v1/sessions/{id}/retry` to repeat an eligible
step without adding a user message. Automate only bounded retries marked
`retryable` and `precommit`. Require a person to decide after visible output.

## Read-parallel / mutate-serial dispatch

When one turn requests multiple tools:

- Consecutive read-only calls are authorized in order, then run concurrently.
- Mutating calls and calls with unknown safety run alone.
- Results return to the model in their original order.

The dispatcher enforces these rules; clients do not need to coordinate calls.

## Permission pause/resume

When a tool call requires human or policy approval, the loop pauses and emits a
`permission.ask` event. The event carries an `askID`, the tool name, the
arguments, and the reason the call was flagged.

Your client returns one of three verdicts:

- **Allow once** runs the tool without learning a rule.
- **Allow always** runs the tool and learns a session-scoped allow rule so the
  same pattern will not ask again in this session.
- **Deny** returns a permission error to the model, which can adapt.

An abandoned request fails closed. Mecatl registers the request before emitting
the event, so an early verdict is not lost.

gRPC clients send `ResumeApproval` on the `Converse` stream. HTTP clients use
`POST /v1/sessions/{id}/approve`.

## Context limits and compaction

Before each turn, Mecatl estimates the complete model request, including
instructions, messages, tool results, and tool schemas. At 80% of the context
window by default, it compacts persisted history. Compaction cannot remove the
other request layers.

Two compaction strategies ship out of the box:

- **Heuristic** (default) preserves the goal, recent file paths, and recent
  messages while truncating large tool results.
- **Cascade** (`--compaction=cascade`) progressively removes low-value content
  and summarizes only when needed. Separate trigger and target thresholds avoid
  repeated compaction near the limit.

Both strategies guarantee:

- The latest user instruction and first user message remain verbatim.
- Retained history never begins with a tool result whose call was removed.
- Invalid compacted history is discarded in favor of the original history.

After automatic compaction, the run continues. In `mecatui`, `/compact` runs one
manual pass while idle and preserves visible scrollback. Cascade compaction can
use model tokens for summarization. See
[Context windows](/features/context-windows.md).

### Where "the model's context window" comes from

Mecatl resolves the context window from, in order:

1. `--context-window-override`.
1. An exact `models.context_windows` entry in user-global settings.
1. Live provider metadata.
1. The bundled models.dev catalog.
1. A 128K fallback.

It resolves the value before each check, so refreshed provider metadata applies
without a restart. Project settings cannot change context-window limits.

Use `--context-window-override` when provider metadata is wrong. It changes both
the compaction threshold and the `mecatui` context meter.

Until live metadata arrives, `mecatui` can show a token count without a context
bar. The bar appears after the refresh.

### Token budget

`--max-run-tokens` (`MaxRunTokens`) limits one session's input and output
tokens. Mecatl checks it between turns, so the current turn completes. Cache
tokens do not count, and the value is not a currency limit.

Crossing the budget completes the run with stop reason `budget`; you can reopen
the session. Each child enforces the limit against its own usage, so a
delegation tree can exceed the parent's limit. Per-call child limits can only
tighten the configured value.

:::note[Default]

`MaxRunTokens` defaults to `0`, which means unlimited.

:::

---

## Cancellation and terminal states

Every run ends in exactly one of three terminal states:

|State|Meaning|
|-|-|
|`completed`|The model or a limit ended the run cleanly. Stop reasons include `end_turn`, `budget`, `no_progress`, and `max_turns`.|
|`cancelled`|`Run.Cancel()` was called, or the context was cancelled|
|`failed`|An unrecoverable error (e.g. the LLM provider returned an error that exhausted retries)|

`Run.Cancel()` ends the run as `cancelled`, either during dispatch or at the
next turn boundary. Restart closes incomplete tool calls with error results so
the conversation remains valid.

### Restarting a session

A session that ends in any terminal state can be re-entered:

- **Completed:** `Reopen` returns the session to idle.
- **Cancelled:** `Interrupt` closes incomplete tool calls and returns to idle.
- **Failed:** `Recover` repairs the conversation and returns to idle. A
  permanent cause can make the next run fail again. When the server identifies a
  permanent failure, such as a 4xx response other than 408 or 429 or a context
  overflow, `mecatui` recommends starting a new session or changing the request.
  A recovery warning appears before the first turn of the retried run.

A persisted `running` snapshot can represent a crashed process. After acquiring
the run lock and lease, the service closes incomplete tool calls before
recovery. It does not reset a live run.

An `awaiting` session requires an approval verdict before a new prompt.

The service handles these recovery transitions when a client re-enters a
session.

## What's next

- [Permissions and guardrails](permissions.md) for how the permission rule
  engine and model-based guardrails work.
- [Hook system](hooks.md) for lifecycle hooks that fire before and after tool
  calls, prompts, and sessions.
- [Extension points](/building/extension-points/index.md) to replace providers,
  stores, policies, and other capabilities.
