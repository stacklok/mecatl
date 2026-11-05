---
slug: /features/permissions-and-posture
sidebar_position: 300
title: Permissions and posture
description:
  Control approvals, project trust, guardrails, and autonomous Mecatl operation.
---

# Permissions and posture

Mecatl applies three independent safety controls:

|Control|What it decides|When it applies|
|-|-|-|
|[Permissions](#choose-a-permission-mode)|Whether a tool call runs, needs approval, or is denied. The _permission mode_ sets how much runs without asking.|Always|
|[Project trust](#project-trust)|Whether Mecatl loads the workspace's own instructions, rules, skills, and allow rules.|Always; a workspace is untrusted by default|
|[Guardrails](#guardrails)|Whether a model-backed checker blocks or flags selected tool arguments and results.|When an operator configures a checker model; the `auto` mode requires an enforcing checker|

For a custom authorization implementation, see
[PermissionPolicy](/building/go/extension-points/permission-policy.md).

## Availability

Permissions and posture apply to `mecated`, `mecak8s`, `mecatequi`, and the
embedded server hosted by `mecatui`. A connected `mecatui` uses the posture and
permission configuration of the remote server. Under `mecatui connect`, the
local `--permission-mode` flag accepts only `plan`, `default`, and
`accept-edits`. It chooses the mode of the sessions that client creates. A local
`--trust-project` or permission configuration leaves the remote server
unchanged, because the server operator owns its configuration.

An ACP editor connected to `mecated acp` starts each session in the server's
default mode. Its mode picker offers only the modes the server admits.

Interactive clients such as `mecatui` can answer approval requests. A headless
server or one-shot job has nobody to ask, so configure the permissions the
workload needs before it starts.

## Choose a permission mode

When you start `mecated`, `mecatui`, `mecak8s`, or `mecatequi`, pass
`--permission-mode`:

|Mode|Edits|Shell and other mutations|Command substitution|Guardrails checker|
|-|-|-|-|-|
|`plan`|Denied|Denied|Not applicable, read-only|Optional|
|`default`|Ask|Ask|Ask|Optional|
|`accept-edits`|Allowed|Ask|Ask|Optional|
|`auto`|Allowed|Allowed|Ask; headless subagent requests go to a reviewer model|Required, enforcing|
|`yolo`|Allowed|Allowed|Allowed|Skipped by default; advisory when you opt in|

Deny rules and configured asks win in every mode, including `yolo`. Use `yolo`
only for isolated, disposable, single-tenant deployments. Grant
[project trust](#project-trust) separately, for example with `--trust-project`,
which works with any mode.

Every root (the `mecated`, `mecatui`, `mecak8s`, and `mecatequi` binaries)
defaults to `default`, except `mecak8s`, which defaults to `auto`.
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
`permissionMode:` key in a project's `.mecatl/settings.yaml` and logs a
warning, so a repository can't choose its own permission mode.

### What a mode sets

|Mode|New sessions start in|Server ceiling: the most permissive mode a session may switch to|
|-|-|-|
|`plan`|`plan`|`accept-edits`|
|`default`|`default`|`accept-edits`|
|`accept-edits`|`accept-edits`|`accept-edits`|
|`auto`|`auto`|`auto`|
|`yolo`|`yolo`|`yolo`|

The ceiling is fixed at startup and applies to every session the server hosts.
Within it, a client can switch its own session, and the new mode takes full
effect. For example, switching from `yolo` to `default` restores ordinary
approvals.

A session created without a mode starts in the server's default mode, which is
`auto` or `yolo` on a server started that way. A session created with a mode
gets that mode's behavior. An explicit `default` session gets ordinary
approvals, even on a server started with `auto` or `yolo`. To create an
autonomous session, request `auto` or `yolo`. A client that can't handle the
`auto` or `yolo` mode value must pass an explicit mode.

Servers with session-scoped modes advertise the `session_permission_modes`
feature identifier. Against an older server without it, `mecatui` cycles only
`plan`, `default`, and `accept-edits`. Its header shows the server's process-wide
`auto` or `yolo` posture, which applies to every session on that server.

:::info[Changed in v0.0.50]

In v0.0.50, each session's mode took over allow-all from the process-wide
`--posture`. When you upgrade from an earlier release, check these
differences:

- A gRPC session created without a mode starts in the server's default mode
  instead of `default`. An older TypeScript SDK sends `default` over HTTP when
  you give no mode, so its sessions start in `default` until you upgrade the
  SDK.
- An explicit `default` session on an `auto` or `yolo` server gets ordinary
  approvals instead of inheriting allow-all.
- Under `auto`, the main agent's unprovable `$(...)`, backtick, and subshell
  commands become approval requests, which a headless server leaves waiting for
  a client. If the main agent must run them unattended, use `yolo`.
- `--permission-mode` doesn't load project instructions and rules on
  interactive roots the way `--posture` did. Pass `--trust-project` or add the
  workspace to `trustedWorkspaces`.
- If you roll back to a release without session-scoped modes, it loads `auto`
  and `yolo` sessions as `default`. gRPC reports them as `DEFAULT`. They get ordinary approvals
  unless that server's own `--posture` allows all tools. The stored mode is
  kept, so upgrading again restores and admits it.

The API major version is unchanged.

:::

### Paths outside the workspace

In a main session, a tool can address a path outside the workspace by its
absolute path. Reads and listings outside the workspace run without asking in
`auto` and `yolo` and ask in other modes. Writes outside the workspace run
without asking only in `yolo`. Configured denies and asks still apply.

Only `yolo` also serves relative `../` paths outside the workspace for `Read`,
`ListDir`, `Write`, and `Edit`. In every other mode, those relative paths stay
confined to the workspace, even after approval. Mecatl denies the pseudo-filesystems
`/proc`, `/sys`, and `/dev` to these tools in every mode, including `yolo`. Children, namespace tools,
`Glob`, and `Grep` stay confined to the workspace in every mode. A symlink that
points outside the workspace doesn't bypass any of this confinement.

### Change the mode after launch

In `mecatui`, press `shift+tab` to cycle through every mode: **default → plan →
accept-edits → auto → yolo → default**. To remap the `ModeSwitch` action, edit
the client keymap or pass `--keymap`. See
[Keybindings](/mecatui/keybindings.md).

The server ceiling decides what happens when the cycle lands on a mode:

- **At or below the ceiling.** The mode applies to the session immediately.
- **Above the ceiling.** The mode is unavailable until the server restarts with
  a higher `--permission-mode`. The session keeps its current mode, and the
  header still shows it. You can keep typing, sending, and queueing prompts. The footer says what the mode needs. Press `shift+tab` again to move
  on.

`auto` also needs an enforcing guardrails checker, such as `guardrails.model`
in your user-global `settings.yaml`. Without one, the server refuses `auto`.
The session keeps its current mode, and the footer gives the server's reason.

A session waiting on an approval can't change mode, except to
[recover a refused session](#recover-a-refused-session). Answer the pending
request first. It resolves under the mode it was raised in, and the new mode
applies from the next run. A mode change never stops a command that has
already started.

The restart a ceiling change needs depends on how you connect:

- With the embedded server, quit and relaunch `mecatui` with the new
  `--permission-mode` value.
- Under `mecatui connect`, the server operator changes `mecated`'s
  configuration and restarts it. Relaunching the client changes nothing.

The `mecatui` header shows the session mode, which changes as you cycle. `yolo`
appears as a red `YOLO` pill. The top-right corner shows whether the server
trusts the project: `trusted` or `untrusted`. An older server that doesn't report trust shows no trust label.

Press `?` in `mecatui` to see every mode, where it starts sessions, its
ceiling, and the restart that applies to your connection.

### Recover a refused session

A server can restart with a lower ceiling, or without the checker that `auto`
needs. A stored session in a mode the restarted server doesn't admit then
refuses to run, resume an approval, fork, or clear. Its history is unchanged.
Its owner recovers it by switching it to an admitted mode:

- **Idle session.** Switch to any admitted mode.
- **Session waiting on an approval.** Switch to an admitted mode lower than the
  stored one. The server cancels the pending request without running it, then
  applies the mode. Your next prompt continues the conversation. Servers that
  support this advertise the `refused_mode_recovery` feature identifier.
- **Session waiting on an MCP server authorization.** Complete or cancel the
  authorization first. Mecatl records a result for the waiting tool call
  without running it or resuming the turn. The session then accepts a switch to
  an admitted mode.

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
warning that names its `--permission-mode` replacement. They will be removed in
a future release.

These combinations are startup errors:

- `--permission-mode` with `--posture`, `--yolo`, or `--mode`.
- `--posture` or `--yolo` while your operator `settings.yaml` sets
  `permissionMode:`. To keep the settings key, drop the deprecated flag. To
  override the key, replace the flag with `--permission-mode`.

`--trust-project` is not deprecated.

## Auto needs an enforcing checker

`auto` and `yolo` run edits and other mutations without asking, so a
guardrails checker is the only model-based inspection of tool content. A server that starts in `auto` refuses
to start without an enforcing checker. A session can't be created in or
switched to `auto` without one. Configure a checker in either of these ways:

- Set a checker with `--guardrails-model <MODEL>`.
- Bind the `guardrail` model slot, with `--model-slot guardrail=<MODEL>` or
  `models.slots.guardrail` in `settings.yaml`.

`--guardrails off` disables any configured checker, so `auto` refuses to start
with it. To run every tool with no checker, choose `yolo`.

The `mecak8s` Helm chart and the `mecatequi` GitHub Action default to `auto`,
so each needs a checker. Name a checker model, or confirm that a checker is
bound in the settings the deployment mounts. Under `auto`, the chart refuses
to render and the action fails the step until one of these is set. To run without
a checker, choose `yolo`. See
[Guardrails for mecak8s](/operating/mecak8s.md#choose-a-guardrails-checker)
and the
[`mecatequi` action inputs](/building/ci/mecatequi.md#headless-posture-and-permission-asks).

### Advisory checking under yolo

In a `yolo` session, a configured checker doesn't run, so tool calls never wait
for it. To keep its findings as warnings, set `guardrails.yoloAdvisory: true`
in your user-global `settings.yaml`.

The checker then reviews each matching call in the background. The tool runs,
and its result reaches the model without waiting. Findings appear as advisory
guardrail events. A checker failure doesn't block the tool. At most four reviews run
at once, and calls beyond that aren't reviewed. When a run ends, Mecatl cancels
its unfinished reviews and doesn't report their findings. The startup line
reports the checker as `advisory` and states whether `yolo` skips it.

To block on findings, choose `auto`. On a server started with `yolo`, a session
you switch to `auto` or a lower mode keeps the checker enforcing.

## Subagents under auto

Subagents, `Parallel` branches, and team members started from an `auto` or
`yolo` session run in that same mode. A team member whose agent definition sets
`permissionMode` keeps that mode instead. Children of a `plan`, `default`, or
`accept-edits` session run in `default` mode.

Under `auto`, the main agent and subagents keep the command-substitution guard.
When a Shell command contains `$(...)`, backticks, or a subshell that Mecatl
can't prove read-only, Mecatl turns it into an approval request. `yolo`
removes that guard for the main agent and subagents.

An interactive client receives these requests as ordinary approval prompts. A
headless `auto` or `yolo` deployment has nobody to ask, so the _subagent ask
reviewer_ is on by default there. This tool-free model decides subagent
approval requests that nobody else can answer. Each approval applies only to
its request. Each decision spends tokens.

The reviewer uses the `ask-reviewer` model slot, or the session model when the
slot is unbound. If neither resolves, Mecatl denies subagent requests and logs
a warning that names the fix. To turn the reviewer off and deny these requests,
pass `--subagent-ask-reviewer off`. Configured deny and ask rules win over the
reviewer.

The reviewer is separate from the guardrails checker. The checker inspects tool
arguments and results; the reviewer decides pending subagent approval
requests.

Clients read the reviewer state from the server capabilities as
`subagent_ask_reviewer`. The value is `on`, `off`, or `inert`. `inert` means a
reviewer is configured, but an interactive client answers the requests. An
older server leaves the value unset. `mecatui`'s `/posture` shows the state
when the server reports it.

## Approve and constrain work

The built-in permission floor allows read-only exploration and asks before
operations that can mutate state:

|Default effect|Tools|
|-|-|
|Allow|`Read`, `ListDir`, `Grep`, `Glob`, `WebFetch`, `WebSearch`, and read-only `Subagent` exploration|
|Ask|`Shell`, `Edit`, `Write`, `Team`, and `SkillDraft`|

A matching `deny` always wins. Otherwise an `ask` wins over an `allow`, and
higher configuration scopes break ties. A configured allow loosens only the
built-in default ask. Configured asks and every deny still apply.

When a call is denied, the model receives a reason and can adapt. When a call
needs approval, an interactive client shows the call and lets you:

- allow this call once;
- allow the matching call for the session; or
- deny it.

An allow-always decision never overrides a deny or configured ask.

### Plan mode

Plan mode denies `Edit`, `Write`, and mutating Shell commands. Read-only
exploration stays available, so the model can inspect the workspace and
prepare a plan. For each plan it presents, the model calls `PresentPlan` once and
stops for review before it leaves plan mode.

Choosing iterate/deny ends that run. Your next prompt supplies feedback. A
revised or unchanged plan needs a new `PresentPlan` call and fresh approval.
Agreement later in the chat never starts execution by itself.

In `mecatui`, **Esc** from the plan review means iterate/deny. The guarded
**Ctrl+C** quit path cancels the run instead of dismissing the review, and it
records no deny verdict.
The next prompt recovers the cancelled session, which stays in plan mode,
before a fresh plan review can appear. Hiding a review in the client leaves its
approval request pending on the server.

Headless plan approval is denied by default. `--plan-mode-auto-approve` is an
explicit operator opt-in that approves plans without a human, so treat it as an
autonomous approval capability.

## Configure permission rules

Write permission rules in the `permissions:` section of a YAML configuration
file:

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

Rules use `Tool(pattern)` syntax. A bare tool name applies to the whole tool.
Patterns use shell-style glob matching. The `permissions:` section parses
strictly, so a misspelled key causes Mecatl to skip the file rather than
silently change the policy.

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

Mecatl rejects a tool call that repeats an argument key or spells an argument
in a different letter case. The rejection happens before any rule, hook, or
approval request sees the call. The model receives an error and can resend the
call with each argument spelled as in the tool schema.

The configuration scopes, from highest to lowest precedence, are:

1. managed policy;
2. explicit `--permission-config` files;
3. `.mecatl/settings.local.yaml`;
4. `.mecatl/settings.yaml`;
5. the user-global `settings.yaml`; and
6. the built-in default floor.

Mecatl resolves project permission files per session, against that session's
workspace. Project `deny` and `ask` rules always apply, but project `allow`
rules require project trust.

Use `--import-claude-permissions` to import supported rules from Claude Code
`settings.json`. The import is lossy and fail-safe: Mecatl drops unsupported or
ambiguous rules, or demotes them to approval, rather than widening access.

## Delegated authority

Each child receives an authority set that can only narrow its parent's
capabilities. The child runtime posture, an operator-owned specialist ceiling,
and call-level restrictions can each remove capabilities. A child can't regain
a capability its parent omitted.

Deriving the set consumes one remaining delegation hop before Mecatl allocates
the child runtime. The child persists its derived set. On resume, Mecatl checks
that the set is still contained by the current parent's authority, without
consuming another hop.

An agent definition's `tools:` and `disallowedTools:` fields narrow that
specialist. Definitions from an explicit operator `--agents-dir` can establish a
durable ceiling; project and user definitions remain subject to the parent's
authority.

`mecated` uses the `local` authority evaluator by default. The opt-in `cedar`
evaluator adds denials from operator-owned policy and never grants a missing
capability. The explicit `noop` evaluator is intended for local or demo
deployments. A bound session without its configured evaluator fails closed.

Cedar requires verified owner identity. Mecatl loads its operator-owned policy
once at startup; an absent, unreadable, or invalid policy prevents startup.

Permission policy and authority are independent checks. A capability that the
child's set omits or that Cedar denies stays unavailable, even when you approve
the tool call. See
[Configure a Cedar authority policy](/operating/mecated/secure-and-expose.md#configure-a-cedar-authority-policy)
for the daemon setup and a workspace-boundary example.

MCP authority applies to the addressed tool, such as
`mcp__github__list_pull_requests`, rather than to an entire MCP server.
Resource operations use a separate capability for each server, so granting a
tool doesn't grant access to that server's resources.

## Project trust

Project trust controls whether Mecatl admits project-provided authority:

- permission `allow` rules;
- project instructions and rules;
- project skills, soul, and named agents; and
- the read-only child shell and workspace inspection posture.

Trust can come from `--trust-project`, a `trustedWorkspaces` entry in the
user-global `settings.yaml`, or an undrifted remembered trust decision. A
permission mode never grants trust. Only the deprecated `--posture` and
`posture:` key still raise it, and only on an interactive root. Without trust,
Mecatl can still read and search the workspace as data.

`--trust-project` asserts that you trust the repository and its `.git`
metadata. Before you trust a repository, review its instructions, hooks,
skills, and Git metadata.

On a headless root without a trust source, every mode starts without loading
project content. A headless `auto` or `yolo` deployment still allows admitted
tools without asking. It loads no project content, and read-only subagents get
no Shell. `auto` and
`yolo` log a warning that project instructions and rules are withheld. The
deprecated `--posture trusted` refuses to start on a headless root with no
trust source.

## Guardrails

Guardrails add content inspection around selected tool calls. They are
independent of permission rules: an allow doesn't disable guardrails, and a
guardrail advisory doesn't change the tool's permission result. Guardrails stay
active in headless deployments and complement permission rules rather than
replace them.

Configure a checker model to enable them:

```sh
mecated serve --guardrails-model gpt-5.6-luna
```

Binding the `cheap` model tier also enables guardrails when the `guardrail`
slot has no binding of its own, because that slot falls back to `cheap`.

The checker reviews the exact effective action before it runs and the
produced result before Mecatl delivers it. Without a custom rule list, a
configured checker uses the expanded default enforcing set. That set covers
Shell, local mutation, read, and search tools, web, MCP, and delegation tools.
The same rules apply to workers. A confidently read-only Shell command skips
action review, but its result is still reviewed. An operator can make the
default set advisory instead.

An explicit rules list replaces the default set. Exact tool-name matches take precedence over prefix
matches and the `*` wildcard.

Each rule runs in one of two modes:

|Mode|Behavior|
|-|-|
|`block`|For action reviews, stops or asks before execution. For inbound reviews, withholds the result until release.|
|`advisory`|Reports a finding without changing the tool call or result.|

A result review runs after the tool, so it can't undo the operation. An
enforcing result review holds the result before the client, model, or session
history receives it. Releasing it delivers the same result without rerunning
the tool or its side effects.

In `mecatui`, `/guardrails` shows the active checker and its coverage for the
session. `/posture` reports permission posture and checker state separately.
The checker state is off with setup guidance, advisory, enforcing, or unknown
when an older or unavailable server can't establish it.

When a review needs your decision, the approval prompt shows its explanation:

- Action reviews offer **Run once** or **Cancel**. They also offer **Don't ask
  again** when the server can bind approval to that exact action in this
  session.
- Result reviews offer **Release once** or **Cancel**.

For conversation visibility and success diagnostics, see
[client debug surfaces](/mecatui/troubleshooting.md#enable-client-debug-surfaces).

Each review has 90 seconds in total for evidence authorization, dependency
snapshots, evidence preparation, and checker attempts. A result that
arrives after the limit, from a reviewer or I/O operation that ignores
cancellation, doesn't approve an action or release a held result. The limit excludes human approval waits and the tool
execution that follows.

Guardrail configuration is operator-tier only, so a project repository can't
weaken or disable the operator's checker.

A checker outage fails closed by
default and appears as an operational failure rather than an unsafe finding.
Operators can choose continue-with-warning instead. The checker can't author
replacement actions.

## Limitations

A headless server leaves a main-session approval request waiting for a client
unless you configure the workload's permissions in advance.
[`mecatequi` cancels its one-shot run](/building/ci/mecatequi.md#headless-posture-and-permission-asks)
when an approval request surfaces. Headless child approval requests use the
fail-safe child path instead of waiting for a client.

## Next steps

- [PermissionPolicy](/building/go/extension-points/permission-policy.md) to
  replace the supplied permission policy in an embedding.
- [Project instructions and rules](/features/agent-behavior/project-instructions-and-rules.md)
  for project-ingestion behavior.
- [Execution environments](/features/security-and-execution/execution-environments.md)
  for workspace and shell isolation.
