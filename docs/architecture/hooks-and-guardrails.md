# Hooks & guardrails

> Part of the [mecatl architecture guide](../architecture.md).

**What this covers:** the hook lifecycle phases (SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, Stop, SubagentStop, team phases), per-tool and run-level placement, mutation semantics, model-backed guardrails (`internal/adapter/modelhook`), and operator-tier-only guardrail config.

**Prerequisites:** [the agent loop](agent-loop.md) — the loop fires the hooks.

**Follow-on:** [deployment & hardening](deployment-and-hardening.md) — guardrails are operator-tier config.

Hook lifecycle phases (`engine/governance/hookevent.go`): `SessionStart`,
`UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `Stop`, `SubagentStop`, plus the
three team-coordination phases `TeammateIdle`, `TaskCreated`, `TaskCompleted`. The
core six fire from the loop — the per-tool pair from `dispatch.go`, the run-level
trio from `engine/agent/hooks.go`, and `SubagentStop` from the Subagent tool; the team
phases fire from the supervisor.

Per-tool placement (`dispatch.go`):
- **PreToolUse** (`preHook`) runs after permission clears, before execution, for
  every call (read-batch and serial). On a hook **block** it emits a `hook`
  event and substitutes an error `ToolResult` (the tool does not run). A hook
  execution error is surfaced to the model as a block annotation rather than
  aborting the run; a cancelled context is the one case that ends the run. A
  non-empty `HookOutcome.Mutated` payload on an allow — interpreted symmetrically
  with the PreToolUse `HookEvent.Input`, i.e. the tool's raw **arguments JSON** —
  **rewrites the call's args before execution**: the loop builds a fresh
  `session.ToolCall` (same CallID and tool Name, new Args) and executes that. A
  malformed (non-JSON) mutation is ignored (original args stand) and a notice
  event is emitted. **Permission ordering (trust):** the policy is evaluated on
  the **original, pre-mutation** args; the mutated args are **not**
  re-permission-checked. This is deliberate — a PreToolUse hook is
  operator-deployed and more trusted than the model, so it may rewrite a call
  past the policy that gated the model's request (matching Claude Code).
- **PostToolUse** (`postHook`) runs best-effort after execution and **before** the
  `tool.result` event is emitted (the emit was moved after `postHook` for this
  reason). A block there only annotates (the tool already ran; a block neither
  undoes nor suppresses the result), and a hook execution error is ignored —
  neither aborts. A non-empty `HookOutcome.Mutated` payload — interpreted
  symmetrically with the PostToolUse `HookEvent.Input`, i.e. the result object
  `{"content", "is_error"}` — **rewrites the result**: the loop builds a fresh
  `session.ToolResult` (same CallID; `NewToolError` when `is_error`, else
  `NewToolResult`). A malformed (non-JSON) mutation is ignored (original result
  stands) with a notice event. Because the **effective** (rewritten) result is what
  is both emitted and returned/recorded, the client stream and the model's history
  agree — there is no hidden divergence. **Trust:** a PostToolUse hook is
  operator-deployed and trusted, so it may rewrite what the model sees the tool
  returned (e.g. redact secrets).

Before recording or emitting a tool result, `engine/agent/dispatch.go` repairs
its effective text with `session.RepairToolResult` after post-hook rewriting.
Pre-hook blocks bypass execution, so `preHook` separately repairs the hook's
message and rewritten arguments on arrival; `json.Valid` does not establish
UTF-8 validity. Proto mappers retain their own text-repair backstops. Binary
payloads and secret-shaped credentials stay byte-exact.

Run-level placement (`engine/agent/hooks.go`). Both pre-prompt phases are **blocking
run-level gates** that fail safe — a block, or a hook **execution error**, ends
the run before any model call with `StopError`, emits a `hook` event, and still
fires `Stop`:
- **SessionStart** (`fireSessionStart`) runs once at the very start of a run,
  before the prompt is recorded; a block (or hook error) **aborts** the run.
- **UserPromptSubmit** (`fireUserPromptSubmit`) runs on the (command-expanded)
  prompt text **before** `RecordUserPrompt`; a block (or hook error) ends the run.
  A non-empty `HookOutcome.Mutated` payload — interpreted symmetrically with the
  `{"prompt": ...}` `HookEvent.Input` — **replaces the effective prompt**: the
  mutated text is what is recorded into the session and sent to the model (a
  malformed mutation is ignored, original text stands). Ordering is preserved:
  command expansion runs first, then the hook, then recording; first-turn
  instruction assembly is unchanged.
- **Stop** (`fireStop`) runs exactly once at the terminal end of any run path,
  even if `ctx` is already cancelled (it is a terminal notification). The Subagent
  subagent mirrors this with **SubagentStop**.

Exit-code semantics live in the `hookexec` adapter
(`internal/adapter/hookexec/hookexec.go`): the `HookEvent` is JSON-serialized to
the hook process's **stdin**; **exit 0 = allow**, **exit 2 = block**
(`HookOutcome.Block = true`, message from stdout/stderr). On an **allow**, if the
hook's stdout is a JSON **object** it is parsed as a control envelope —
`{"mutated": <raw payload>, "message": "..."}` — and `mutated` becomes
`HookOutcome.Mutated` (the rewritten prompt/args payload the loop applies). Plain
(non-JSON-object) stdout is treated as a message exactly as before, so existing
hooks are unaffected. This is what lets **real shell hooks** emit mutations (not
just custom Go `HookRunner` adapters).

### Contextual model-backed guardrails (`engine/agent`, `internal/app`)

A dedicated checker reviews two distinct jobs on configured `(phase, tool)` rules:

- **action** reviews the exact effective call after trusted mutation and deterministic
  permission re-evaluation, before execution; and
- **inbound** reviews the already-produced effective result before recording, event,
  client, or working-model delivery.

The fixed rubric requires affirmative evidence of attempted authority crossing or
redirection. Imperatives, ordinary issue requirements, admitted project instructions,
and quoted attack examples are not findings by themselves. The reviewer may read only
bounded review-local evidence handles and must treat their content as untrusted data.
The checker engine has no ordinary tools and cannot recursively invoke guardrails.

Rules use `block` or `advisory`; sanitize and checker-authored replacement actions do not
exist. An interactive action finding offers **Run once**, **Don't ask again** only when
the exact action and dependencies are version-bound, or **Cancel**. An enforcing inbound
finding holds the exact produced result privately and offers **Release once** or
**Cancel**. Release consumes that same result without rerunning the tool or side effects
and authorizes reading, never following embedded instructions.

Machine status is emitted without checker prose or reviewed content. Bounded human
concern/source/next-action text is available only from the owner-authorized, process-local
detail RPC while the delegation-root run is live. Mecatui's `/guardrails` view reports
the configured checker and the actual session catalog/rule coverage; an operational
inspection failure is shown as an outage, never as a finding. The transient registry and
held results disappear on run cleanup, cancellation, disconnect, shutdown, or restart.

Mecatui derives presentation only from the machine review fields. A completed,
acceptable review with disposition `execute` or `release_result` is retained but hidden
while conversation details are collapsed. `ctrl+t` reveals those benign hook and live
detail blocks. Every missing, unknown, unresolved, prohibited, failed, advisory, held,
denied, warning, or approval-related outcome remains visible; checker prose never controls
visibility.

Guardrails are operator-tier configuration. The checker provider/model pair is captured
at Build: a scalar `models.slots.guardrail` uses the deployment default provider, while
the strict object form names both a configured provider and model selector. The same
applicable rules bind main and worker calls; only the checker engine is inert. See
[ADR 0363](../adr/0363-contextual-investigative-guardrails.md).

## Prerequisites

- [The agent loop that fires the hooks](agent-loop.md)

## Follow-on reading

- [Deployment & hardening — operator-tier config](deployment-and-hardening.md)

## Related

- [The API surface](api-surface.md)

---

[← Architecture guide](../architecture.md)
