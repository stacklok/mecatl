---
slug: /features/hooks
sidebar_position: 340
title: Hook system
description:
  Understand hook phases and how hooks observe, transform, or block agent
  actions.
---

# Hook system

Use lifecycle hooks to observe agent activity, block an action, or rewrite a
prompt, tool call, or result. Operators deploy hooks, and the model cannot
change or disable them. For operator configuration, see
[Permissions and posture](/features/security-and-execution/permissions-and-posture.md).

## Hook phases

|Phase|When it fires|Can block?|Can mutate?|Mutation target|
|-|-|-|-|-|
|`SessionStart`|Before the first prompt is recorded|Yes, aborts the run|No||
|`UserPromptSubmit`|After command expansion, before recording|Yes, ends the run|Yes|Prompt text|
|`PreToolUse`|After permission approval, before execution|Yes, returns an error instead of running|Yes|Tool arguments|
|`PostToolUse`|After execution, before emitting the result|No|Yes|Tool result|
|`Stop`|When a run ends|No|No||
|`SubagentStop`|When a child run ends|No|No||
|`TeammateIdle`|When a team member becomes idle|No|No||
|`TaskCreated`|Before the team supervisor creates a task|Yes, prevents creation|No||
|`TaskCompleted`|Before a team member marks a task complete|Yes, prevents completion|No||

An execution error in `SessionStart` or `UserPromptSubmit` ends the run. A
`PreToolUse` execution error prevents the tool call and returns an error result
to the model; the run can continue. A `PostToolUse` error becomes an annotation
and leaves the already-executed call intact.

## Shell hook contract

Each hook is a shell command run as `<shell> -c <command>` (default `/bin/sh`).
The JSON `HookEvent` is written to the process's stdin:

```json
{
  "Phase": "PreToolUse",
  "Tool": "Shell",
  "Input": { "command": "rm -rf build" },
  "SessionID": "8867bdea940108c1dd82d13d3fb7fc61"
}
```

`Input` is phase-specific. For `PreToolUse` it is the tool's raw arguments JSON.
For `PostToolUse` it is `{"content": "...", "is_error": false}`. For
`UserPromptSubmit` it is `{"prompt": "..."}`.

### Exit codes

|Exit code|Outcome|
|-|-|
|`0`|Allow. Stdout can contain a message or mutation envelope.|
|`2`|Block. Mecatl reads the reason from stdout, then stderr.|
|Any other value|Report a hook error. The phase determines whether the run aborts.|

A single invocation is bounded by a 30-second timeout.

### Mutation envelope

On exit 0, if stdout is a JSON object, it is parsed as a control envelope:

```json
{
  "mutated": <phase-specific payload>,
  "message": "optional human-readable note"
}
```

`mutated` must have the same shape as `Input` for that phase. For `PreToolUse`
it replaces the tool's arguments before execution. For `PostToolUse` it replaces
the result the model and client see. For `UserPromptSubmit` it replaces the
recorded prompt text.

A non-object stdout value is treated as a message, and the original payload is
unchanged.

## Block example: guard Shell against `rm -rf`

```sh
#!/bin/sh
# Use this script as a PreToolUse hook for Shell.
event="$(cat)"
if printf '%s' "$event" | grep -q '"rm -rf'; then
  echo "blocked: 'rm -rf' is not permitted by policy"
  exit 2
fi
exit 0
```

Exit 2 returns an error `ToolResult` without running the command.

## Mutation examples

### Rewrite the prompt before it is recorded

This `UserPromptSubmit` hook removes an API key pattern before the prompt
reaches the model or session store:

```sh
#!/bin/sh
event="$(cat)"
prompt="$(printf '%s' "$event" | python3 -c "import sys,json; print(json.load(sys.stdin)['Input']['prompt'])")"
clean="$(printf '%s' "$prompt" | sed 's/sk-[A-Za-z0-9]\{32,\}/[REDACTED]/g')"
printf '%s' "$clean" | \
  python3 -c "import json,sys; print(json.dumps({'mutated': {'prompt': sys.stdin.read()}}))"
exit 0
```

Mecatl records and sends the mutated prompt.

### Redact a secret from a tool result

This `PostToolUse` hook redacts AWS access key IDs from shell output while
preserving the result's error status:

```sh
#!/bin/sh
python3 -c '
import json, re, sys
result = json.load(sys.stdin)["Input"]
result["content"] = re.sub(r"AKIA[A-Z0-9]{16}", "[REDACTED_KEY]", result["content"])
print(json.dumps({"mutated": result}))
'
```

The client stream and model history both receive the redacted result.

## Permission policy evaluates original args

For `PreToolUse`, the permission policy evaluates the original arguments. It
does not evaluate rewritten arguments again because operator hooks are trusted.
A hook can therefore widen a call beyond what the original permission decision
covered. Keep security controls in the permission policy when a hook must not
bypass them.

<span id="guardrails-a-built-in-model-backed-hook" />

## Model-backed guardrails

Guardrails provide a separate content-review path around tool execution. They
inspect effective actions after hook rewriting and effective results before
release to the client or model. Configure a checker model to enable them. See
[Permissions and posture](/features/security-and-execution/permissions-and-posture.md#guardrails)
for coverage, enforcement modes, and approval behavior.

## Next steps

- [Permissions and posture](/features/security-and-execution/permissions-and-posture.md)
  for permission evaluation and the model-backed guardrail checker.
- [HookRunner extension point](/building/go/extension-points/hook-runner.md) to
  implement a custom hook runner.
