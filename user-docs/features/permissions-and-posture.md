---
sidebar_position: 300
title: Permissions and posture
description:
  Control approvals, project trust, guardrails, and autonomous Mecatl operation.
---

# Permissions and posture

Configure two independent safety controls:

1. **Permissions** decide whether a tool call is allowed, needs approval, or is
   denied before it runs.
2. **Guardrails** are an optional model-backed checker that inspects selected
   tool arguments and results. They are independent of the permission rules.

Permissions always apply. Guardrails require an operator-configured checker
model.

For evaluator, authority-set, and custom-policy contracts, see
[Permissions and guardrails for builders](/building/what-you-get/permissions.md).

## Availability

Permissions and posture apply to `mecated`, `mecak8s`, `mecatequi`, and the
embedded server hosted by `mecatui`. A connected `mecatui` uses the posture and
permission configuration of the remote server; local embedded-server flags do
not apply to `connect` sessions.

Interactive clients such as `mecatui` can answer approval requests. For headless
servers and one-shot jobs, configure the permissions required by the workload
before it starts.

## Choose a posture

`--posture` selects the operator posture ladder:

|Posture|Behavior|
|-|-|
|`strict`|Default for interactive `mecated`; read-only calls are allowed and mutating calls use the permission rules, normally asking before they run. Project trust is not granted by the posture.|
|`trusted`|Interactive roots admit trusted project instructions and project permission allows, but mutating calls still use the normal approval rules.|
|`auto`|Enables the allow-all posture for the main agent and children while keeping deny rules and deliberately configured asks effective. Child substitution defenses remain enabled.|
|`yolo`|Extends `auto` by allowing child command substitutions, backticks, and heredoc-style substitutions that `auto` keeps behind the child safety floor. Use only for isolated, disposable, single-tenant deployments.|

`--yolo`, `--trust-project`, and operator-global `posture:` settings can raise
the posture tier; the highest tier wins. A project file cannot raise it.

Deployment defaults differ: `mecated` is interactive and defaults to `strict`,
while unattended `mecak8s` and `mecatequi` deployments commonly select `auto`.
Check the individual command's help before assuming a default.

For example:

```sh
# Interactive, fail-closed default.
mecated serve --posture strict

# Unattended single-tenant deployment; review the trust implications first.
mecated serve --headless --posture auto
```

Allow-all postures are refused when running as root unless the deployment
explicitly declares an isolated sandbox with `MECATL_SANDBOX=1` or
`IS_SANDBOX=1`.

## Choose a permission mode

A permission mode controls one session. It works alongside the deployment
posture and configured rules.

|Mode|Behavior|
|-|-|
|`default`|Uses the normal permission policy: read-only work is usually allowed, while operations that can mutate state normally ask first.|
|`plan`|Exposes a read-only toolset so the model can inspect the workspace and prepare a plan without changing it.|
|`accept-edits`|Automatically allows `Edit` and `Write` when they would otherwise hit only the built-in approval floor. Shell commands and other tools still use the normal policy, and configured asks and denies still win.|

In `mecatui`, press `shift+tab` to cycle the active session through **default →
plan → accept-edits → default**. You can remap the `ModeSwitch` action in the
client keymap or with `--keymap`; see [Keybindings](/mecatui/keybindings.md).

To select the initial mode when launching `mecatui`, use `--mode`:

```sh
mecatui --workspace "$PWD" --mode plan
mecatui --workspace "$PWD" --mode accept-edits
```

The header displays the active mode. Changing modes does not bypass configured
permission rules or guardrails.

## Approve and constrain work

The built-in permission floor allows read-only exploration and asks before
operations that can mutate state:

|Default effect|Tools|
|-|-|
|Allow|`Read`, `ListDir`, `Grep`, `Glob`, `WebFetch`, `WebSearch`, and read-only `Subagent` exploration|
|Ask|`Shell`, `Edit`, `Write`, `Team`, and `SkillDraft`|

A matching `deny` always wins. Otherwise an `ask` wins over an `allow`, and
higher configuration scopes break ties. A configured allow can loosen only the
built-in default ask; it cannot suppress a configured ask or any deny.

When a call is denied, the model receives a reason and can adapt. When a call
needs approval, an interactive client shows the call and lets the operator:

- allow this call once;
- allow the matching call for the session; or
- deny it.

An allow-always decision never overrides a deny or configured ask. Headless
deployments have no human approval channel, so configure their required access
before starting work.

### Plan mode

Plan mode denies `Edit`, `Write`, and mutating Shell commands. Read-only
exploration remains available so the model can inspect the workspace and prepare
a plan. For each current presentation, the model calls `PresentPlan` once and
stops for review before leaving plan mode. Choosing iterate/deny ends that run;
your next prompt supplies feedback, and a revised or unchanged plan requires a
new `PresentPlan` call and fresh approval. Later chat assent never starts
execution by itself.

In `mecatui`, **Esc** from the plan review means iterate/deny. The guarded
**Ctrl+C** quit path instead cancels the run; it is not a neutral dismissal and
does not record a deny verdict. The next prompt recovers the cancelled session,
which remains in plan mode, before a fresh plan review can be presented. Hiding
a client review does not itself clear an ask that remains pending on the server.
Headless plan approval is denied by default; `--plan-mode-auto-approve` is an
explicit operator opt-in and should be treated as an autonomous approval
capability.

## Configure permission rules

Permission configuration is available in YAML files with a `permissions:`
section:

```yaml
permissions:
  allow:
    - 'Read'
    - 'Shell(go test:*)'
  ask:
    - 'Shell(git push:*)'
  deny:
    - 'Shell(rm:*)'
  subagent:
    deny:
      - 'Shell(gh pr merge:*)'
    allow:
      - 'Shell(go vet:*)'
```

Rules use `Tool(pattern)` syntax. A bare tool name applies to the whole tool;
patterns use shell-style glob matching. The `permissions:` section parses
strictly, so a misspelled key causes the file to be skipped rather than silently
changing the policy.

The configuration scopes, from highest to lowest precedence, are:

1. managed policy;
2. explicit `--permission-config` files;
3. `.mecatl/settings.local.yaml`;
4. `.mecatl/settings.yaml`;
5. the user-global `settings.yaml`; and
6. the built-in default floor.

Project permission files are resolved per session against that session's
workspace. Project `deny` and `ask` rules remain effective, but project `allow`
rules require project trust.

Use `--import-claude-permissions` to import supported rules from Claude Code
`settings.json`. The import is lossy and fail-safe: unsupported or ambiguous
rules are dropped or demoted to approval rather than widening access.

## Project trust

Project trust controls whether Mecatl admits project-provided authority:

- permission `allow` rules;
- project instructions and rules;
- project skills, soul, and named agents; and
- the read-only child shell and workspace inspection posture.

Trust can come from an explicit operator flag, a trusted-workspace setting, an
undrifted remembered trust decision, or the interactive posture floor. A
headless root does not gain project trust merely because it uses `trusted`,
`auto`, or `yolo`; it needs an explicit trust source.

On an untrusted headless checkout, `--posture auto` can allow admitted tools
without loading project steering or enabling a read-only child shell.
`--trust-project` asserts that you trust the repository and its `.git` metadata.

## Guardrails

Guardrails add content inspection around selected tool calls. Configure a
checker model to enable them:

```sh
mecated serve --guardrails-model gpt-5.6-luna
```

The checker reviews exact effective actions before execution and already-produced
results before delivery. A configured checker with no custom rule list uses the
expanded default enforcing set for Shell, local mutation/read/search, web, MCP,
and delegation tools; the same applicable rules bind workers. Operators can
configure advisory behavior instead. Guardrails remain active in headless
deployments and are not a replacement for permission rules.

Mecatui's `/guardrails` command shows the active checker and session-specific
coverage. `/posture` reports permission posture and checker state separately, including
off/setup guidance, advisory or enforcing when on, and unknown when an older or
unavailable server cannot establish status. Action findings offer Run once, an exact session-only repeat grant when
version-binding is complete, or Cancel. Result findings offer Release once or
Cancel; release delivers the same held result without rerunning side effects.

Guardrail configuration is operator-tier only. A project repository cannot
weaken or disable the operator's checker. Checker outage is fail-closed by
default and is displayed as an operational failure, not an unsafe finding;
operators may explicitly choose continue-with-warning. Sanitization and
checker-authored replacement actions are not supported.

See the [guardrails reference](/building/what-you-get/permissions.md) for
matchers, modes, and checker failure handling.

## Limitations

- `strict` preserves the built-in read-allow and mutate-ask floor.
- `auto` and `yolo` preserve denies and configured asks.
- Headless server deployments can leave main-session asks waiting for a client.
  [`mecatequi` cancels its one-shot run](/building/deployment/mecatequi.md#headless-posture-and-permission-asks)
  when an ask surfaces. Headless child asks use the fail-safe child path instead
  of waiting for a client.
- Project files are untrusted by default. Do not enable project trust for a
  repository whose instructions, hooks, skills, or Git metadata you have not
  reviewed.
- Permission rules and guardrails are different controls: an allow does not
  disable guardrails, and a guardrail advisory does not change the tool's
  permission result.
- `--posture`, `--trust-project`, and permission configuration on a local
  `mecatui` invocation do not change a remote server used through `connect`.

## Next steps

- [Permissions and guardrails](/building/what-you-get/permissions.md) for the
  detailed rule-resolution and approval reference.
- [Project instructions and rules](./project-instructions-and-rules.md) for
  project-ingestion behavior.
- [Execution environments](./execution-environments.md) for workspace and shell
  isolation.
