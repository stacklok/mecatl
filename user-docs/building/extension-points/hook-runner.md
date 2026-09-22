---
sidebar_position: 5
title: HookRunner
description:
  Observe, transform, approve, or block agent lifecycle events with HookRunner.
---

# HookRunner

`port.HookRunner` handles lifecycle events around prompts, tool calls, agent
teams, and run termination. Implement it to apply dynamic policy, redact tool
results, record audit data, or notify another system.

## The interface

```go
type HookRunner interface {
    Run(
        ctx context.Context,
        event governance.HookEvent,
    ) (governance.HookOutcome, error)
}
```

Mecatl calls `Run` synchronously for every configured phase. A nil `HookRunner`
disables hooks.

```go
type HookEvent struct {
    Phase     HookPhase
    Tool      string
    Input     json.RawMessage
    SessionID string
    CallID    string
}

type HookOutcome struct {
    Block       bool
    Message     string
    Mutated     json.RawMessage
    AskApproval bool
}
```

`Tool` and `CallID` are set for tool-use phases. `Input` has a shape defined by
the phase.

`Block` stops phases that support a veto. `Message` explains the outcome.
`Mutated` replaces a supported input or result when it contains valid JSON in
the required shape.

`AskApproval` changes a `PreToolUse` block into an interactive permission
request. It has no effect on other phases or on headless runs. A runner can also
implement `port.HookApprovalLearner` to remember an "allow always" verdict.

## Hook phases

|Phase|When it runs|Input|Effect of Block|Mutation|
|-|-|-|-|-|
|`SessionStart`|Before the first prompt|Empty|Aborts the run|None|
|`UserPromptSubmit`|After command expansion, before recording the prompt|Prompt object|Rejects the prompt|Replaces the prompt|
|`PreToolUse`|After permission approval, before execution|Tool arguments|Prevents execution|Replaces tool arguments|
|`PostToolUse`|After execution|Arguments and result|Adds an annotation|Replaces the result|
|`Stop`|When the main loop terminates|Stop reason|No effect|None|
|`SubagentStop`|When a subagent stops|Stop reason|No effect|None|
|`TeammateIdle`|When a team member becomes idle|Team-member data|No effect|None|
|`TaskCreated`|Before a team task is created|Task data|Prevents creation|None|
|`TaskCompleted`|Before a team task is completed|Task data|Prevents completion|None|

`Stop`, `SubagentStop`, and `TeammateIdle` are best-effort notifications. Mecatl
ignores block outcomes. If their caller context is already canceled, Mecatl uses
a detached, five-second context so the notification can still run.

## Return an outcome

Return `HookOutcome{}` to allow the action unchanged.

For a veto, set `Block: true`. Errors from `SessionStart`, `UserPromptSubmit`,
and `PreToolUse` also fail closed. A `PreToolUse` block produces a model-visible
tool error so the model can choose another action.

To change a supported payload, return valid JSON in `Mutated`:

- `UserPromptSubmit`: `{"prompt":"..."}`
- `PreToolUse`: replacement tool arguments
- `PostToolUse`: `{"content":"...","is_error":true}`

The permission policy is not run again after a trusted hook changes `PreToolUse`
arguments. Treat hook implementations as trusted code.

Mecatl ignores malformed mutation JSON and emits a client-visible informational
hook event.

### Redact a tool result

A `PostToolUse` block cannot undo a tool that has already run or hide its
result. It adds an annotation to the client event stream.

Use a mutation to replace unsafe output:

```json
{
  "content": "blocked by guardrail: unsafe content detected",
  "is_error": true
}
```

Mecatl records, emits, and sends the rewritten result to the model. The original
tool output does not enter those paths.

## Use the shell hook runner

`internal/adapter/hookexec` maps phases to shell commands in the shipped
applications:

```go
hooks := hookexec.New(map[governance.HookPhase]string{
    governance.PhasePreToolUse:   "/usr/local/bin/check-call",
    governance.PhasePostToolUse:  "/usr/local/bin/redact-result",
    governance.PhaseSessionStart: "/usr/local/bin/audit-session",
})
```

The runner writes `HookEvent` as JSON to the command's standard input. It uses
`/bin/sh` and a 30-second timeout by default. On POSIX systems, cancellation or
timeout terminates the hook's process group, including child processes.

|Exit code|Outcome|
|-|-|
|`0`|Allow|
|`2`|Block|
|Other nonzero value|Hook error|

For exit code 0, plain standard output becomes `Message`. Output that begins
with `{` can return a control envelope:

```json
{
  "mutated": {
    "content": "redacted",
    "is_error": false
  },
  "message": "Removed a credential from the result."
}
```

For exit code 2, standard output supplies the block message, with standard error
as a fallback. `hookexec.WithTimeout` and `hookexec.WithShell` override the
defaults.

## Implement a runner

Keep synchronous hooks fast because they add latency to the run. Use the
caller's context and return promptly after cancellation.

```go
type AuditHooks struct {
    sink AuditSink
}

func (h AuditHooks) Run(
    ctx context.Context,
    event governance.HookEvent,
) (governance.HookOutcome, error) {
    if err := h.sink.Record(ctx, event); err != nil {
        return governance.HookOutcome{}, err
    }
    return governance.HookOutcome{}, nil
}
```

Mecatl fails closed on errors from `SessionStart`, `UserPromptSubmit`, and
`PreToolUse`. Errors from `TaskCreated` and `TaskCompleted` fail open so a
broken hook cannot stop team coordination. `PostToolUse` errors leave the tool
result unchanged, and notification errors have no effect on the run.

The shipped guardrail checker also uses this interface. It blocks unsafe
outbound calls in `PreToolUse` and replaces unsafe inbound results in
`PostToolUse`.

## Next steps

- [Configure hooks](/features/security-and-execution/hooks.md).
- [Implement a permission policy](permission-policy.md).
- [Add a custom tool](tool-catalog.md#add-a-custom-tool).
