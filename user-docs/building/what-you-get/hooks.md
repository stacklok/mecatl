---
sidebar_position: 4
title: Hook system
description:
  Understand hook phases and how hooks observe, transform, or block agent
  actions.
---

# Hook system

This is the builder-facing reference for lifecycle hooks and the `HookRunner`
contract. For the operator-facing permission and guardrail behavior surrounding
hooks, see [Permissions and posture](/features/permissions-and-posture.md).

Hooks run at fixed phases of the agent loop. They can block an action, rewrite
what the model sees, or observe an event. Operators deploy hooks; the model
cannot install, modify, or disable them. Every event starts as allowed.

## Hook phases

|Phase|When it fires|Can block?|Can mutate?|Mutation target|
|-|-|-|-|-|
|`SessionStart`|Once at the very start of a run, before the prompt is recorded|Yes — aborts the run|No|—|
|`UserPromptSubmit`|After command expansion, before the prompt is recorded|Yes — ends the run|Yes|The prompt text (`{"prompt": "..."}`)|
|`PreToolUse`|After permission clears, before the tool executes|Yes — substitutes an error result; the tool does not run|Yes|The tool's args JSON|
|`PostToolUse`|After the tool executes, before the result is emitted to the client or model|No — the tool already ran; a block only annotates|Yes|The result object (`{"content": "...", "is_error": false}`)|
|`Stop`|Once at the terminal end of any run path, even if the context is already cancelled|No — terminal notification only|No|—|
|`SubagentStop`|When a subagent's loop stops (mirrors `Stop` for child agents)|No — terminal notification only|No|—|
|`TeammateIdle`|When a team member goes idle between rounds|No|No|—|
|`TaskCreated`|When the team supervisor creates a task|No|No|—|
|`TaskCompleted`|When a team member completes a task|No|No|—|

`SessionStart` and `UserPromptSubmit` are fail-safe: a hook execution error (not
just exit 2) also ends the run. `PreToolUse` and `PostToolUse` treat execution
errors as annotations — neither aborts the run.

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
|`0`|Allow — stdout is read as an optional message or mutation envelope|
|`2`|Block — the action is vetoed; the reason is read from stdout (preferred) or stderr|
|anything else|Hook error — surfaced as an annotation or run abort depending on the phase|

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

A malformed (non-JSON-object) stdout is treated as a plain message and the
original payload stands — so hooks that only print a message or produce no
output at all are unaffected.

## Block example: guard Shell against `rm -rf`

```sh
#!/bin/sh
# pretooluse-guard.sh — wire as a PreToolUse hook for the Shell tool.
event="$(cat)"
if printf '%s' "$event" | grep -q '"rm -rf'; then
  echo "blocked: 'rm -rf' is not permitted by policy"
  exit 2
fi
exit 0
```

Exit 2 causes Mecatl to substitute an error `ToolResult` in place of running the
command. The model sees a tool failure, not a silent skip.

## Mutation examples

### Rewrite the prompt before it is recorded

A `UserPromptSubmit` hook that strips a leaked API key pattern from user input
before it reaches the model or the session store:

```sh
#!/bin/sh
event="$(cat)"
prompt="$(printf '%s' "$event" | python3 -c "import sys,json; print(json.load(sys.stdin)['Input']['prompt'])")"
clean="$(printf '%s' "$prompt" | sed 's/sk-[A-Za-z0-9]\{32,\}/[REDACTED]/g')"
python3 -c "import json,sys; print(json.dumps({'mutated': {'prompt': sys.stdin.read()}}))" <<< "$clean"
exit 0
```

The mutated prompt is what gets recorded into the session and sent to the model.

### Redact a secret from a tool result

A `PostToolUse` hook that scrubs AWS credentials from shell output before the
model sees it:

```sh
#!/bin/sh
event="$(cat)"
content="$(printf '%s' "$event" | python3 -c "import sys,json; print(json.load(sys.stdin)['Input']['content'])")"
clean="$(printf '%s' "$content" | sed 's/AKIA[A-Z0-9]\{16\}/[REDACTED_KEY]/g')"
python3 -c "
import json, sys
content = sys.stdin.read()
print(json.dumps({'mutated': {'content': content, 'is_error': False}}))
" <<< "$clean"
exit 0
```

Because the mutation happens before the result is emitted, the client stream and
the model's conversation history both show the redacted version — there is no
divergence.

## Permission policy evaluates original args

For `PreToolUse`, the permission policy runs on the **original, pre-mutation**
args. A hook that rewrites the args is not re-permission-checked after the
rewrite. This is deliberate: a hook is operator-deployed and is treated as more
trusted than the model. The practical consequence is that a hook can widen a
call past the policy that gated the model's original request — for example,
normalizing a path that would otherwise have triggered a confirmation. Don't use
this to bypass security controls you intend to enforce; use it to implement your
own operator-controlled transformations.

## Guardrails: a built-in model-backed hook

Mecatl also includes model-backed `PreToolUse` and `PostToolUse` hooks for
content that scripts cannot reliably classify, such as prompt injection in a
fetched page or possible secret exfiltration in tool arguments. These guardrails
remain off until you configure a checker model. See
[Permissions and guardrails](permissions.md#layer-2--model-backed-guardrails)
for the default matchers, enforcement modes, and approval flow.

## What's next

- [Permissions & guardrails](./permissions.md) — the other governance surface;
  controls what the model can request before hooks fire, and the model-backed
  guardrail checker.
- [Extension points — HookRunner](/building/extension-points/hook-runner.md) —
  how to implement a custom hook runner as a port adapter.
