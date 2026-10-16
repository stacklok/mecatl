---
slug: /features/permissions-and-posture
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
model, and the `auto` permission mode requires an enforcing one.

For a custom authorization implementation, see
[PermissionPolicy](/building/go/extension-points/permission-policy.md).

## Availability

Permissions and posture apply to `mecated`, `mecak8s`, `mecatequi`, and the
embedded server hosted by `mecatui`. A connected `mecatui` uses the posture and
permission configuration of the remote server. Under `mecatui connect`, the
local `--permission-mode` flag accepts only `plan`, `default`, and
`accept-edits`, which choose the mode of the sessions that client creates.

An ACP editor connected to `mecated acp` starts each session in the server's
default mode, and its mode picker offers only the modes the server admits.

Interactive clients such as `mecatui` can answer approval requests. For headless
servers and one-shot jobs, configure the permissions required by the workload
before it starts.

## Choose a permission mode

Pass `--permission-mode` when you start `mecated`, `mecatui`, `mecak8s`, or
`mecatequi`. Deny rules and configured asks win in every mode, including
`yolo`.

|Mode|Edits|Shell and other mutations|Command substitution in subagents|Guardrails checker|
|-|-|-|-|-|
|`plan`|Denied|Denied|Not applicable, read-only|Optional|
|`default`|Ask|Ask|Ask|Optional|
|`accept-edits`|Allowed|Ask|Ask|Optional|
|`auto`|Allowed|Allowed|Adjudicated by a reviewer model when headless|Required, enforcing|
|`yolo`|Allowed|Allowed|Allowed|Optional, advisory only|

Whether Mecatl loads project instructions, rules, and allow rules is the
separate [project trust](#project-trust) decision; a permission mode never
grants it. Use `yolo` only for isolated, disposable, single-tenant deployments.

A main-session external path can be addressed absolutely in any mode, subject
to its escape permission decision. Only `yolo` also serves external relative
`../` paths for `Read`, `ListDir`, `Write`, and `Edit`; in every other mode,
those relative paths remain workspace-confined even after approval. Configured
denies and asks still apply. Pseudo-filesystems (`/proc`, `/sys`, `/dev`) are
never served through these tools. Children, namespace tools, `Glob`, and `Grep`
remain confined to the workspace; symlink escapes are not a substitute for `../`.

Every root defaults to `default`, except `mecak8s`, which defaults to `auto`.
The [`mecatequi` GitHub Action](/building/ci/mecatequi.md) also passes
`auto` unless you set its `permission-mode` input.

```sh
# Interactive: honor this project's rules and auto-accept edits.
mecatui --workspace "$PWD" --permission-mode accept-edits --trust-project

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

Each mode sets two things:

|Mode|New sessions start in|Server ceiling: the most permissive mode a session may switch to|
|-|-|-|
|`plan`|`plan`|`accept-edits`|
|`default`|`default`|`accept-edits`|
|`accept-edits`|`accept-edits`|`accept-edits`|
|`auto`|`auto`|`auto`|
|`yolo`|`yolo`|`yolo`|

The ceiling is fixed at startup and applies to every session the server hosts.
Within it, a client can switch its own session, and the new mode takes full
effect: switching from `yolo` to `default` restores ordinary approvals.

### Change the mode after launch

In `mecatui`, press `shift+tab` to cycle through every mode: **default → plan →
accept-edits → auto → yolo → default**. You can remap the `ModeSwitch` action
in the client keymap or with `--keymap`; see
[Keybindings](/mecatui/keybindings.md).

What happens when the cycle lands on a mode depends on the server ceiling:

- **At or below the ceiling.** The mode applies to the session immediately.
- **Above the ceiling.** The mode is unavailable until the server restarts with
  a higher `--permission-mode`. The session keeps its current mode, which the
  header continues to show, and you can keep typing, sending, and queueing
  prompts. The footer says what the mode needs. `auto` also needs an enforcing
  guardrails checker, such as `guardrails.model` in your user-global
  `settings.yaml`. Press `shift+tab` again to move on.

The server also refuses `auto` when it has no enforcing guardrails checker. The
session keeps its current mode, and the footer gives the server's reason.

A session waiting on an approval cannot change mode, except to recover from a
refused mode as described below. Answer the pending request first: it resolves under the mode it was raised in, and the new mode applies
from the next run. A mode change never stops a command that has already
started.

If the server restarts with a lower ceiling, or without the checker `auto`
needs, a stored session in a mode it no longer admits refuses to run, resume an
approval, fork, or clear until its owner switches it to an admitted mode. Its
history is unchanged. If that session is waiting on an approval, switch it to an
admitted mode lower than its stored one: the server cancels the pending request
without running it, then applies the mode, and your next prompt continues the
conversation. Servers that support this advertise the `refused_mode_recovery`
feature identifier.

The restart a ceiling change needs depends on how you connect:

- With the embedded server, quit and relaunch `mecatui` with the new
  `--permission-mode` value.
- Under `mecatui connect`, the server operator changes `mecated`'s
  configuration and restarts it. Relaunching the client changes nothing.

The `mecatui` header shows the session mode, which changes as you cycle; `yolo`
appears as a red `YOLO` pill. The top-right corner shows whether the server
trusts the project: `trusted` or
`untrusted`. Project trust is independent of the mode. An older server that
does not report trust shows no trust label.

Press `?` in `mecatui` to see every mode, the two things each one sets, and the
restart that applies to your connection.

### Check the resolved mode at startup

Every root logs one `permission mode` line at startup. Its fields report:

- `permission_mode`: the resolved mode.
- `session_mode` and `max_allowed_mode`: where new sessions start and the
  server ceiling.
- `guardrails_checker`: exactly one of `enforcing`, `advisory`, or `disabled`,
  with a `guardrails_checker_reason` when it is not enforcing.
- `subagent_ask_reviewer`: whether the
  [subagent ask reviewer](#subagents-under-auto) is on, and why.

`mecatui` writes its startup diagnostics to its log file rather than the
terminal.

### Deprecated flags

`--posture`, `--yolo`, the `mecatui` `--mode` flag, and the `posture:` settings
key still work and resolve to the equivalent mode. Each logs a deprecation
warning naming the `--permission-mode` replacement, and they will be removed in
a future release. Passing `--permission-mode` together with `--posture`,
`--yolo`, or `--mode` is a startup error. So is passing `--posture` or `--yolo`
while your operator `settings.yaml` sets `permissionMode:`. Drop the deprecated
flag to keep the settings key, or replace it with `--permission-mode`, which
overrides the key. `--trust-project` is not deprecated.

## Auto needs an enforcing checker

`auto` and `yolo` allow every tool without asking, which leaves a guardrails
checker as the only inspection of tool content. A server that starts in `auto`
refuses to start without an enforcing checker, and a session cannot be created
in or switched to `auto` without one. Configure one in either of these ways:

- Set a checker with `--guardrails-model <MODEL>`.
- Bind the `guardrail` model slot, with `--model-slot guardrail=<MODEL>` or
  `models.slots.guardrail` in `settings.yaml`.

`--guardrails off` disables any configured checker, so `auto` refuses to start
with it. To run every tool with no checker, choose `yolo`.

The `mecak8s` Helm chart and the `mecatequi` GitHub Action pass no guardrails
flag by default, so the checker configured in settings applies. Each can also
name a checker model or pass the kill-switch. See [Guardrails for mecak8s](/operating/mecak8s.md#choose-a-guardrails-checker)
and the [`mecatequi` action inputs](/building/ci/mecatequi.md#headless-posture-and-permission-asks).

In a session running in `yolo` mode, a configured checker is advisory only. It
has no pre-tool veto, no approve-once human ask, and no fail-closed behavior when
the checker fails, so a checker outage looks the same as a clean result. The
startup line reports it as `advisory`. Choose `auto` when you want the checker to
block findings. On a server started with `yolo`, a session you switch to `auto`
or a lower mode keeps the checker enforcing.

## Headless roots and project trust

Without a trust source, every mode starts without loading project content.
`auto` and `yolo` also log a warning that project instructions and rules are
withheld; tools are still allowed without asking. The deprecated
`--posture trusted` refuses to start on a headless root with no trust source.

## Subagents under auto

Subagents, `Parallel` branches, and team members started from an `auto` or
`yolo` session run in that same mode. A team member whose agent definition sets
`permissionMode` keeps that mode instead. Children of a `plan`, `default`, or `accept-edits` session
run in `default` mode.

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

Clients can read the reviewer state from the server capabilities as
`subagent_ask_reviewer`: `on`, `off`, or `inert` when a reviewer is configured
but an interactive client answers the requests. An older server leaves it unset.
`mecatui`'s `/posture` shows the state when the server reports it.

## Upgrade from the posture model

Earlier releases fixed allow-all for the whole process with `--posture`. Allow-all
now belongs to each session's mode. This changes behavior that existing clients
and deployments can observe:

- **Sessions created without a mode** start in the server's default mode, which
  is `auto` or `yolo` on a server started that way. Previously, a gRPC session
  created without a mode started in `default`. A client that cannot handle the
  `auto` or `yolo` mode value in a session must pass an explicit mode.
- **An explicit `default` session gets ordinary approvals** even on a server
  started with `auto` or `yolo`. Previously, such a session inherited the
  process-wide allow-all. Request `auto` or `yolo` for autonomous sessions.
- **`auto` refuses to start without an enforcing checker**, including with
  `--guardrails off`. A deployment that ran `--posture auto` without a checker
  must configure one or choose `--permission-mode yolo`. The `mecak8s` chart
  default and the `mecatequi` action default are `auto`, so they need the same
  choice.
- **A mode never grants project trust.** `--permission-mode` no longer loads
  project instructions and rules on interactive roots the way `--posture` did.
  Pass `--trust-project` or add the workspace to `trustedWorkspaces`.
- **Stored sessions** keep their recorded mode. A session recorded in a mode the
  upgraded server does not admit refuses to run until its owner switches it to
  an admitted mode. If you roll back to a release without session-scoped
  modes, that release still loads `auto` and `yolo` sessions but treats them
  as `default`: they get ordinary approvals unless that server's own
  `--posture` allows all tools, and gRPC reports them as `DEFAULT`. It keeps
  the stored mode, so upgrading again restores it and admits it again.

Clients detect a server with session-scoped modes through the
`session_permission_modes` feature identifier. The API major version is
unchanged. Against an older server without that feature, `mecatui` cycles only
`plan`, `default`, and `accept-edits`, and labels the header mode with the
server's `auto` or `yolo` posture, which still applies to every session there.

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

A pattern matches the argument value that the tool runs with:

| Tool | Argument the pattern matches |
|-|-|
| `Shell` | `command` |
| `Read`, `Write`, `Edit`, `Remove`, `ListDir`, `Grep` | `path` |
| `Glob` | `pattern` |
| `WebFetch` | `url` |
| `WebSearch` | `query` |
| `Copy`, `Move` | `source` and `destination`, each evaluated separately; the stricter result applies |

A call without that argument, such as a `Grep` call without a `path`, matches
only rules without a pattern. For MCP and other tools, a pattern matches the
first of `path`, `file_path`, `pattern`, `url`, and `query` that the call
carries. A `deny` or `ask` pattern that matches any of the others also applies
to the call.

Mecatl rejects a tool call whose arguments repeat a key, or spell an argument in
a different letter case, before any rule, hook, or approval request sees the
call. The model receives an error and can resend the call with each argument
spelled as in the tool schema.

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

## Delegated authority

Each child receives an authority set that can only narrow the parent's
capabilities. The child runtime posture, an operator-owned specialist ceiling,
and call-level restrictions can remove capabilities. A child cannot regain a
capability omitted by its parent. Derivation consumes one remaining delegation
hop before allocating the child runtime. The child persists its derived set;
resuming checks that it remains contained by the current parent's authority,
without consuming another hop.

An agent definition's `tools:` and `disallowedTools:` fields narrow that
specialist. Definitions from an explicit operator `--agents-dir` can establish a
durable ceiling; project and user definitions remain subject to the parent's
authority.

`mecated` uses the `local` authority evaluator by default. The opt-in `cedar`
evaluator can add denials based on operator-owned policy, but it cannot grant a
missing capability. The explicit `noop` evaluator is intended for local or demo
deployments. A bound session without its configured evaluator fails closed.

Cedar requires verified owner identity. Its operator-owned policy is loaded once
at startup; an absent, unreadable, or invalid policy prevents startup.
Permission policy and authority are independent checks: approving a tool call
cannot restore a capability excluded from the child's set or denied by Cedar.
See
[Configure a Cedar authority policy](/operating/mecated/secure-and-expose.md#configure-a-cedar-authority-policy)
for the daemon setup and a workspace-boundary example.

MCP authority applies to the addressed tool, such as
`mcp__github__list_pull_requests`, rather than granting access to an entire MCP
server. Resource operations use a separate capability for each server; granting
a tool does not grant access to that server's resources.

## Project trust

Project trust controls whether Mecatl admits project-provided authority:

- permission `allow` rules;
- project instructions and rules;
- project skills, soul, and named agents; and
- the read-only child shell and workspace inspection posture.

Trust can come from `--trust-project`, a `trustedWorkspaces` entry in the
user-global `settings.yaml`, or an undrifted remembered trust decision. A
permission mode never grants trust; only the deprecated `--posture` and
`posture:` key still raise it on an interactive root. Without trust, Mecatl can
still read and search the workspace as data. See
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

Binding the `cheap` model tier also enables guardrails when the `guardrail` slot has
no binding of its own, because that slot falls back to `cheap`.

The checker reviews exact effective actions before execution and
already-produced results before delivery. A configured checker with no custom
rule list uses the expanded default enforcing set for Shell, local
mutation/read/search, web, MCP, and delegation tools; the same applicable rules
bind workers. Operators can configure advisory behavior instead. Guardrails
remain active in headless deployments and are not a replacement for permission
rules.

`mecatui`'s `/guardrails` command shows the active checker and session-specific
coverage. `/posture` reports permission posture and checker state separately,
including off/setup guidance, advisory or enforcing when on, and unknown when an
older or unavailable server cannot establish status. When a review needs your
decision, the approval prompt shows its explanation. Action reviews offer **Run
once** or **Cancel**, plus **Don't ask again** when the server can bind approval
to that exact action in this session. Result reviews offer **Release once** or
**Cancel**; release delivers the same held result without rerunning side
effects. For conversation visibility and success diagnostics, see
[client debug surfaces](/mecatui/troubleshooting.md#enable-client-debug-surfaces).

|Mode|Behavior|
|-|-|
|`block`|For action reviews, stops or asks before execution. For inbound reviews, withholds the result until release.|
|`advisory`|Reports a finding without changing the tool call or result.|

The default rules cover Shell, local mutation and read tools, web, MCP, and
delegation tools. A confidently read-only Shell command skips action review, but
its result remains covered. An explicit rules list replaces the default set.
Exact tool-name matches take precedence over prefix matches and the `*`
wildcard.

A result review runs after the tool, so it cannot undo the operation. An
enforcing result review holds the result before the client, model, or session
history receives it. Releasing it delivers the same result without rerunning the
tool or its side effects.

Each review has up to 90 seconds for evidence authorization, dependency
snapshots, evidence preparation, and checker attempts combined. This limit uses
cooperative context cancellation. A reviewer or I/O operation that ignores
cancellation can return later, but a late acceptable result cannot approve an
action or release a held result. Human approval waits and subsequent tool
execution are outside the review limit.

Guardrail configuration is operator-tier only. A project repository cannot
weaken or disable the operator's checker. Checker outage is fail-closed by
default and is displayed as an operational failure, not an unsafe finding;
operators may explicitly choose continue-with-warning. Checker-authored
replacement actions are not supported.

## Limitations

- `strict` preserves the built-in read-allow and mutate-ask floor.
- `auto` and `yolo` preserve denies and configured asks.
- Headless server deployments can leave main-session asks waiting for a client.
  [`mecatequi` cancels its one-shot run](/building/ci/mecatequi.md#headless-posture-and-permission-asks)
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
  `mecatui connect` refuses `--permission-mode auto` or `yolo`. The server
  operator owns that server's ceiling.

## Next steps

- [PermissionPolicy](/building/go/extension-points/permission-policy.md) to
  replace the supplied permission policy in an embedding.
- [Project instructions and rules](/features/agent-behavior/project-instructions-and-rules.md)
  for project-ingestion behavior.
- [Execution environments](/features/security-and-execution/execution-environments.md)
  for workspace and shell isolation.
