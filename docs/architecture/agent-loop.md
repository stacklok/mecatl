# The agent loop

The agent loop in `engine/agent` turns one prompt into a finished run: it calls the
model, dispatches the tool calls the model asks for, records the results, and
repeats. This page explains how the loop is ordered and why. For the behavior a
library caller or client sees, read
[the agent loop user guide](../../user-docs/features/sessions/agent-loop.md).

## Engine and run

`agent.NewEngine` builds an `Engine` from `agent.Deps` and gives every optional seam a
network-free default, so a bare engine works offline. `Engine.Run` returns a `*Run`
handle at once and drives the loop on a background goroutine. The handle carries the
event channel, which closes exactly once, and the live controls: resolve an ask,
cancel the run or one child, and steer.

Every run ends in exactly one terminal state and emits exactly one `result` event
with cumulative usage. Clean early endings (budget, no progress, plan approved, plan
iterate) are completed runs, not errors, so the next prompt can reopen the session.

## The drive algorithm

`Engine.drive` opens the run and `Engine.runLoop` repeats the turn. Resuming a parked
ask reuses the same `runLoop`, so a fresh prompt and a resumed approval cannot drift.

1. **Open.** Emit `session.init` first. On a session's first turn, fire the blocking
   `SessionStart` hook; a block ends the run before the prompt is recorded.
2. **Record the prompt.** Expand slash commands, fire the blocking `UserPromptSubmit`
   hook on the expanded text (it may block or rewrite it), and record the result
   through the session aggregate. Media parts bypass both steps unchanged.
3. **Boundary injections.** Record the notice for finished background children,
   scheduled-task results queued for this session, and any pending steer. History
   here never ends between a tool call and its result, so injected messages are
   always provider-legal.
4. **Stop checks.** End the run on a recorded stop reason, a tripped session limit
   (turns, tool calls, consecutive failures), cancellation, or a spent token budget.
5. **Build and compact.** Assemble the complete `port.LLMRequest`, then compact
   history if it is near the context window; see
   [context and compaction](context-and-compaction.md).
6. **Stream the turn.** Relay text deltas and collect tool calls, reasoning, usage,
   and the stop reason into one assistant message. Usage is recorded even when the
   stream fails, so budgets reflect real spend.
7. **No tool calls.** A turn with text ends the run. An empty turn on a benign stop
   gets a bounded nudge (two by default) before the run ends with `no_progress`. A
   non-benign provider stop, such as truncation or refusal, is reported as is.
8. **Dispatch.** Run the tool calls, record their results, save, and loop to step 3.

The run token budget (`Deps.MaxRunTokens`, off by default) counts input plus output
tokens of main and router usage. It is checked only at the turn boundary, so a turn
in flight always finishes, and a per-run override can only tighten it. Each child
engine applies the ceiling to its own session, so a delegation tree can spend more.

## Tool dispatch

`Engine.dispatch` walks the calls in the order the model emitted them. Each maximal
run of consecutive read-only calls forms a batch that executes concurrently; any
other call runs alone. Results are recorded in the original call order, whichever
goroutine finishes first. Reads commute and writes do not: a `Read` racing an `Edit`
of the same file can see a half-applied change, so mutations run serially, in model
order, and each sees the state the model expected.

A known `ReadOnly()` call still leaves the batch if its tool implements
`tool.DispatchSerial`, if it can request external authorization (which can park the
run), or if it reports that this specific call mutates the parent workspace through
the unexported `parentMutatingCaller` interface.

That last case is the barrier for direct-write children. `Subagent` and `Parallel`
report `ReadOnly()` so read-only fan-out keeps batching. But a read-write `Subagent`
call, or a single-branch `Parallel` call that auto-merges, changes the parent's real
tree. Inside a read batch, a sibling `Read` or `Grep` could see the parent
half-written, so that call runs alone like any mutating tool. The barrier orders
sibling calls within one run only; state shared across runs needs its own locking.

Each call passes the permission policy, then the `PreToolUse` hook (a rewritten call
is evaluated again), then the delegated-authority check at execution. A read batch
clears every call's gates first, asking at most one permission at a time, and only
then executes. `PostToolUse` hooks and the incoming-result review can rewrite a
result; the event stream, audit log, and model all see the rewritten one. A tool
error, unknown tool, or denied gate becomes an error result for the model and never
aborts the run. [Governance](governance.md) explains how each gate decides.

## Permission pause and resume

On Ask, `Engine.surfaceAsk` registers a one-slot channel in the run's `askRegistry`
before it moves the session to `awaiting` and emits `permission.ask`, so a verdict
that arrives right after the event cannot be lost. The loop blocks until a verdict
arrives or the run is cancelled.

- **Allow once** runs the call.
- **Allow always** also asks the policy to learn a session rule for the same tool
  and exact pattern. A learned rule never overrides a deny or plan mode.
- **Deny** returns a `permission denied` error result so the model can adapt. Deny
  is the zero value, so an abandoned ask fails safe.
- **Cancel while waiting** ends the run as cancelled.

The pause survives a restart. The server saves the awaiting session when it relays
`permission.ask`, and shutdown keeps that snapshot awaiting. A verdict with no live
run goes through `Engine.ResumeApproval`, which continues the same run ID, checks
that the session still awaits the same ask, and resolves the pending call exactly
once. Siblings from that turn that never ran get a synthetic error result, because
their outcome was lost with the old process. Child asks surface through the parent
and route back to the child; see [subagents and teams](subagents-and-teams.md).

A tool that needs an external sign-in, such as MCP OAuth, parks the run on a pending
authorization the same way. `Run.Cancel` leaves such a parked run untouched so its
resumable handoff point stays intact.

## Plan-approval gate

In plan mode the model is read-only until an operator approves its plan.
`PresentPlan` (`engine/agent/presentplan.go`) implements `tool.PlanOnly`, so the
catalog advertises it only in plan mode. Its description, a plan-mode role suffix,
and the plan-mode prompt reminder all tell the model to present the plan, call
`PresentPlan` once, and stop. The dispatcher intercepts the call and raises a
plan-origin ask on the same ask path as permissions.

| Verdict | Run ends with | Mode afterward |
| --- | --- | --- |
| Allow once | `plan_approved` | default |
| Allow always | `plan_approved` | accept edits |
| Deny | `plan_iterate` | plan |

Both outcomes end the run, so the model never continues without new operator input.
The mode flips only once the session is terminal, because the aggregate rejects mode
changes mid-run. The ask records its plan origin, so a cross-process resume applies
the same mapping. The server's `ApprovePlan` operation resolves a parked plan ask
and, on approval, starts a fresh run with `agent.PlanApprovedProceedText`. A
headless engine denies the plan unless the operator opted into auto-approval.

## Steering a running run

A steer is operator input for a run that is still working. The run holds one pending
steer in an in-memory inbox (`engine/agent/steer.go`); a second steer appends to it.
The inbox drains only at step 3, so a steer never interrupts a model call or splits a
tool call from its result. It is recorded as an ordinary user message and echoed as
a `steer` event with exactly what the engine committed.

A steer is never silently dropped by the engine. A run that would end cleanly with a
steer pending takes another turn to consume it. A run ending for any other reason
records the pending steer into history before closing the inbox. Over gRPC, a steer
that misses the live run starts a follow-up run on the same stream; the HTTP control
targets one run ID and returns a conflict instead. An undrained steer is lost if the
process crashes, since only recorded history persists.

## System prompt assembly

`prompt.Build` returns a two-layer `prompt.Layered`. The provider adapter places a
prompt-cache breakpoint between the layers, so per-turn values stay out of the first.

- **Stable prefix:** safety rules first, ahead of any user-supplied text, then the
  role, the default tone, commit attribution guidance, tool-usage hints derived from
  the live tool set, and the tool inventory.
- **Volatile suffix:** the `<env>` block (working directory, model, mode, date, Git
  status), the operator profile, the project-instruction scope note, the plan-mode
  reminder, and a no-shell note when the environment has no command runner.

The default tone (`defaultTone` in `engine/prompt/builder.go`) asks for a concise
final answer and states that brevity never means less reading or reasoning. The
minimum-change ladder, read-before-edit, checks at trust boundaries, and confirming
hard-to-reverse actions stay on regardless. Composition in `internal/app` appends a
task-persistence clause to the role for every model, plus the plan-mode posture.

The optional `prompt.OperatorProfileSource`, wired by composition to the user-model
store, is reread before every request and rendered as a bounded JSONL block fenced as
data. A failed read warns once and reuses the run's last good snapshot; profile text
is never persisted into the conversation. Project instructions, rules, the soul file,
and the memory index are user-role fragments placed ahead of the persisted
conversation in each request, and are not written to session storage either. A host
can supply `Deps.PromptBuilder` to own the system prompt; the loop then adds none of
the default text.

## Related

- [The agent loop user guide](../../user-docs/features/sessions/agent-loop.md)
- [Ports](ports.md)
- [Context and compaction](context-and-compaction.md)
- [Governance](governance.md)
- [Subagents and teams](subagents-and-teams.md)
