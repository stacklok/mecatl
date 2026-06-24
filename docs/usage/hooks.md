## 8. Hooks

Lifecycle hooks let an external command observe or veto agent actions. A hook is
a phase → shell-command map; each command is run as `<shell> -c <command>`
(default shell `/bin/sh`), with the JSON `HookEvent` written to its **stdin**.

### Phases

| Phase | Fires |
| --- | --- |
| `SessionStart` | once when a session begins |
| `UserPromptSubmit` | when the user submits a prompt |
| `PreToolUse` | before a tool runs — a block aborts the call |
| `PostToolUse` | after a tool runs |
| `Stop` | when the main loop stops |
| `SubagentStop` | when a subagent loop stops |

> v1 fully implements `PreToolUse` and `PostToolUse`; the others are defined and
> wired as the injection seam. **`mecated` ships with no global hooks configured
> by default** (`hookexec.New(nil)`, in the shared composition layer
> `internal/app`), so every event is allowed. Global hooks are configured in Go
> by passing a populated `map[governance.HookPhase]string` to
> `hookexec.New(...)`. Separately, an **agent definition** may carry a per-def
> `hooks:` map executed through the same `hookexec` runner for that child's
> lifecycle phases — note this is **ungated shell on the harness host** (no
> permission ask), which is why agent-def sources are a trust boundary (see the
> `--agents-dir` / `--agent-source-url` notes in §3 and §6).

### The stdin contract

The hook receives a JSON `HookEvent` on stdin:

```json
{
  "Phase": "PreToolUse",
  "Tool": "Bash",
  "Input": { "command": "rm -rf build" },
  "SessionID": "8867bdea940108c1dd82d13d3fb7fc61"
}
```

### The exit-code contract

| Exit code | Outcome |
| --- | --- |
| `0` | **allow** — the message (if any) is read from stdout |
| `2` | **block** — the action is vetoed; the reason is read from stdout (preferred) or stderr |
| any other | **error** — the hook itself failed; surfaced to the caller |

A single invocation is bounded by a timeout (default 30 s).

### Example hook script

A `PreToolUse` hook that blocks any `Bash` command containing `rm -rf`:

```sh
#!/bin/sh
# pretooluse-guard.sh — exit 2 to block, 0 to allow.
event="$(cat)"                       # the HookEvent JSON arrives on stdin
if printf '%s' "$event" | grep -q 'rm -rf'; then
  echo "blocked: 'rm -rf' is not permitted by policy"   # reason -> client/model
  exit 2
fi
exit 0
```

Wire it (in `internal/app` — `build.go`, where `hookexec.New(nil)` is today):

```go
hooks := hookexec.New(map[governance.HookPhase]string{
    governance.PhasePreToolUse: "/path/to/pretooluse-guard.sh",
})
```

