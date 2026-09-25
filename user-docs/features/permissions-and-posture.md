---
sidebar_position: 300
title: Permissions and posture
description:
  Control approvals, project trust, guardrails, and autonomous Mecatl operation.
---

# Permissions and posture

Configure two independent safety controls:

1. **Permissions** decide whether a tool call is allowed, needs approval, or is
   denied before it runs. A single launch-time setting, the _permission mode_,
   selects how much Mecatl allows without asking.
2. **Guardrails** are an optional model-backed checker that inspects selected
   tool arguments and results. They are independent of the permission rules.

Permissions always apply. Guardrails require an operator-configured checker
model, and the allow-all permission modes require you to decide whether you
want one.

For evaluator, authority-set, and custom-policy contracts, see
[Permissions and guardrails for builders](/building/what-you-get/permissions.md).

## Availability

Permissions and posture apply to `mecated`, `mecak8s`, `mecatequi`, and the
embedded server hosted by `mecatui`. A connected `mecatui` uses the posture and
permission configuration of the remote server. Under `mecatui connect`, the
local `--permission-mode` flag accepts only `plan`, `default`, and
`accept-edits`, which choose the mode of the sessions that client creates.

Interactive clients such as `mecatui` can answer approval requests. For headless
servers and one-shot jobs, configure the permissions required by the workload
before it starts.

## Choose a permission mode

Pass `--permission-mode` when you start `mecated`, `mecatui`, `mecak8s`, or
`mecatequi`. Deny rules and configured asks win in every mode, including
`yolo`.

|Mode|Project instructions and rules|Edits|Shell and other mutations|Command substitution in subagents|
|-|-|-|-|-|
|`plan`|Only with a trust source|Denied|Denied|Not applicable, read-only|
|`default`|Only with a trust source|Ask|Ask|Ask|
|`accept-edits`|Only with a trust source|Allowed|Ask|Ask|
|`trusted`|Loaded|Ask|Ask|Ask|
|`trusted-accept-edits`|Loaded|Allowed|Ask|Ask|
|`auto`|Loaded|Allowed|Allowed|Adjudicated by a reviewer model when headless|
|`yolo`|Loaded|Allowed|Allowed|Allowed|

"Loaded" applies to an interactive root, or to a headless root that has a trust
source. See [Project trust](#project-trust) for the trust sources and
[Headless roots and project trust](#headless-roots-and-project-trust) for how
each mode behaves without one. Use `yolo` only for isolated, disposable,
single-tenant deployments.

Every root defaults to `default`, except `mecak8s`, which defaults to `auto`.
The [`mecatequi` GitHub Action](/building/deployment/mecatequi.md) also passes
`auto` unless you set its `permission-mode` input.

```sh
# Interactive: honor this project's rules and auto-accept edits.
mecatui --workspace "$PWD" --permission-mode trusted-accept-edits

# Unattended single-tenant deployment with a guardrails checker.
mecated serve --headless --permission-mode auto --guardrails-model <MODEL>
```

To set a default for every launch, add the key to your user-global
`settings.yaml`:

```yaml title="~/.config/mecatl/settings.yaml"
permissionMode: accept-edits
```

An explicit `--permission-mode` flag outranks the key. Mecatl ignores a
`permissionMode:` key in a project's `.mecatl/settings.yaml` and logs a warning,
so a repository cannot choose its own permission mode. `--trust-project`
combines with any mode: it supplies project trust, and at `plan`, `default`, or
`accept-edits` it also raises the posture to `trusted`.

### What a mode sets

Each mode sets two things with different lifetimes:

|Mode|Posture: process-wide, fixed at startup|Session mode: where each new session starts|
|-|-|-|
|`plan`|`strict`|`plan`|
|`default`|`strict`|`default`|
|`accept-edits`|`strict`|`accept-edits`|
|`trusted`|`trusted`|`default`|
|`trusted-accept-edits`|`trusted`|`accept-edits`|
|`auto`|`auto`|`default`|
|`yolo`|`yolo`|`default`|

The _posture_ applies to every session the server hosts and cannot change while
the process runs. The _session mode_ is only a starting point; a client can
change it for its own session.

### Change the mode after launch

In `mecatui`, press `shift+tab` to cycle the active session through **default →
plan → accept-edits → default**. You can remap the `ModeSwitch` action in the
client keymap or with `--keymap`; see [Keybindings](/mecatui/keybindings.md).

Cycling changes only the session mode, never the posture. If you start in
`auto` and cycle to `plan` and back to `default`, every tool is still allowed
without asking. The `mecatui` header shows the session mode and, whenever the
posture is above `strict`, the posture as well (`trusted`, `auto`, or `yolo`).

`mecatui` has no control that selects `trusted`, `trusted-accept-edits`,
`auto`, or `yolo` at runtime. Those modes set the posture, so they need a
restart of the process that hosts the server:

- With the embedded server, quit and relaunch `mecatui` with the new
  `--permission-mode` value.
- Under `mecatui connect`, the server operator changes `mecated`'s
  configuration and restarts it. Relaunching the client changes nothing.

Press `?` in `mecatui` to see every mode, the two things each one sets, and the
restart that applies to your connection.

### Check the resolved mode at startup

Every root logs one `permission mode` line at startup. Its fields report:

- `permission_mode`: the resolved mode.
- `posture` and `session_mode`: the two values the mode set, each with a
  `_scope` field stating its lifetime.
- `guardrails_checker`: exactly one of `enforcing`, `advisory`, or `disabled`,
  with a `guardrails_checker_reason` when it is not enforcing.
- `subagent_ask_reviewer`: whether the
  [subagent ask reviewer](#subagents-under-auto) is on, and why.

 `mecatui` writes its startup diagnostics to
its log file rather than the terminal.

### Deprecated flags

`--posture`, `--yolo`, the `mecatui` `--mode` flag, and the `posture:` settings
key still work and resolve to the equivalent mode. Each logs a deprecation
warning naming the `--permission-mode` replacement, and they will be removed in
a future release. Passing `--permission-mode` together with `--posture`,
`--yolo`, or `--mode` is a startup error. `--trust-project` is not deprecated.

## Allow-all modes need a checker decision

`auto` and `yolo` allow every tool without asking, which leaves a guardrails
checker as the only inspection of tool content. They refuse to start until you
make one of these choices:

- Set a checker with `--guardrails-model <MODEL>`.
- Bind the `guardrail` model slot, with `--model-slot guardrail=<MODEL>` or
  `models.slots.guardrail` in `settings.yaml`.
- Run without a checker on purpose with `--guardrails off`.

The `mecak8s` Helm chart and the `mecatequi` GitHub Action always make this
choice for you: they pass the checker model you configure, or `--guardrails off`
when you leave it empty. See [Guardrails for mecak8s](/building/deployment/mecak8s.md#choose-a-guardrails-checker)
and the [`mecatequi` action inputs](/building/deployment/mecatequi.md#headless-posture-and-permission-asks).

At `yolo`, a configured checker is advisory only. It has no pre-tool veto, no
approve-once human ask, and no fail-closed behavior when the checker fails, so a
checker outage looks the same as a clean result. The startup line reports it as
`advisory`. Choose `auto` when you want the checker to block findings.

## Headless roots and project trust

A headless root never gains project trust from its permission mode. The modes
behave differently when no trust source is present:

- `trusted` and `trusted-accept-edits` refuse to start, because project trust is
  what they add. Pass `--trust-project`, add the workspace to
  `trustedWorkspaces` in your user-global `settings.yaml`, or rely on a
  remembered trust decision. Otherwise choose a mode that does not name trust.
- `auto` and `yolo` start and log a warning that project instructions and rules
  are withheld. Tools are still allowed without asking.
- `plan`, `default`, and `accept-edits` start without loading project content.

On an interactive root, `trusted` and `trusted-accept-edits` grant project trust
directly.

## Subagents under auto

Under `auto`, subagents keep the command-substitution guard that the main agent
loses. A subagent Shell command containing `$(...)`, backticks, or a subshell
that Mecatl cannot prove read-only is not run automatically, even though the
main agent would run the same command. `yolo` removes that guard for subagents
too.

An interactive client receives these requests as ordinary approval prompts. A
headless `auto` or `yolo` deployment has nobody to ask, so the _subagent ask
reviewer_ is on by default there. The reviewer is a tool-free model that decides
a subagent permission request nobody else can answer. An approval applies only
to that request, and each decision spends tokens.

The reviewer uses the `ask-reviewer` model slot, or the session model when the
slot is unbound. If neither resolves, subagent requests are denied and Mecatl
logs a warning naming the fix. To keep the reviewer off and deny these requests,
pass `--subagent-ask-reviewer off`. Configured deny and ask rules still win over
the reviewer.

The reviewer is separate from the guardrails checker. The checker inspects tool
arguments and results; the reviewer decides pending subagent permission
requests.

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

Trust can come from `--trust-project`, a `trustedWorkspaces` entry in the
user-global `settings.yaml`, an undrifted remembered trust decision, or a
`trusted`, `trusted-accept-edits`, `auto`, or `yolo` mode on an interactive
root. A headless root needs one of the first three; see
[Headless roots and project trust](#headless-roots-and-project-trust).

`--trust-project` asserts that you trust the repository and its `.git` metadata.
Without trust, a headless `auto` deployment still allows admitted tools, but it
loads no project steering and gives read-only subagents no Shell.

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
- `--trust-project` and permission configuration on a local `mecatui`
  invocation leave a remote server used through `connect` unchanged, and
  `mecatui connect` refuses a `--permission-mode` that sets a posture. The
  server operator owns that server's posture.

## Next steps

- [Permissions and guardrails](/building/what-you-get/permissions.md) for the
  detailed rule-resolution and approval reference.
- [Project instructions and rules](./project-instructions-and-rules.md) for
  project-ingestion behavior.
- [Execution environments](./execution-environments.md) for workspace and shell
  isolation.
