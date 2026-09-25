---
sidebar_position: 3
title: Permissions and guardrails
description:
  Understand Mecatl permissions, approvals, trust, and guardrails for agent
  actions.
---

# Permissions and guardrails

Mecatl combines permission rules with optional model-backed guardrails. This
page explains evaluation, delegated authority, and custom policy integration.
For operator configuration and posture selection, see
[Permissions and posture](/features/permissions-and-posture.md).

Mecatl checks tool calls in two independent layers:

|Layer|Decision|Default|
|-|-|-|
|Permission rules|Whether a call can run: `allow`, `ask`, or `deny`|On with a built-in ruleset|
|Model-backed guardrails|Whether matched input or output content is safe|Off until you configure a checker model|

## Delegated authority

Each child receives an authority set that limits which capabilities it can use.
Permissions still decide whether an authorized call can run, and ownership
decides who can access the session.

A root session starts with the tools in its composed catalog. A child receives
only what survives this calculation:

```text
parent authority
∩ child runtime posture
∩ managed specialist ceiling, when eligible
∩ optional call-level narrowing
− one delegation hop
```

A reviewer delegated only `Read` and `Grep` cannot regain the parent's `Write`
capability. The authority set persists and must still fit within the parent's
current authority when the child resumes.

### Agent definitions and authority ceilings

A definition's `tools:` list limits a specialist from any source. Only a
definition from an explicit operator directory establishes a durable authority
ceiling. Configure it with `--agents-dir`:

```sh
mecated serve --agents-dir /etc/mecatl/agents
```

For example, `/etc/mecatl/agents/code-reviewer.md` can contain:

```md
---
name: code-reviewer
description: Reviews source code without changing it.
tools: [Read, Grep]
disallowedTools: [Write, Shell]
---

Review the requested code and return findings with file and line references.
```

The list can remove parent capabilities but cannot add them. Project, user, and
driver definitions scope a specialist without granting independent authority.

### Choosing an evaluator

`mecated` selects the authority evaluator at startup:

```sh
mecated serve --authority-evaluator=local
mecated serve --authority-evaluator=noop
mecated serve --authority-evaluator=cedar --cedar-authority-policy=/etc/mecatl/authority.cedar
```

|Evaluator|Use it when|Behavior|
|-|-|-|
|`local` (default)|You want delegated authority enforced without an external policy language.|Allows only exact capabilities in the carried set.|
|`noop` (explicit)|A local or demo deployment deliberately disables authority enforcement.|Accepts well-formed authority requests. It is never a fallback for a missing evaluator.|
|`cedar` (opt-in)|You need operator-owned rules over an already-authorized capability, such as a workspace path boundary.|Checks the carried set first, then lets Cedar add a denial. It cannot grant an omitted capability.|

A bound session without an evaluator fails closed.

### Cedar policy boundaries

Cedar loads an operator-owned policy file at startup. A missing or invalid file
prevents startup. Keep it outside project-controlled directories.

A minimal policy that prevents reads below a protected subtree is:

```cedar
permit(principal, action, resource);

forbid(principal, action, resource)
when {
    resource.kind == "workspace_file" &&
    resource.path like "/workspace/vendor/*"
};
```

Cedar receives the capability, operation, delegation depth, non-secret session
identity, and the resolved workspace target for `Read`, `Edit`, and `Write`. It
does not receive raw arguments, file contents, credentials, or headers.

Cedar requires verified session ownership. Use `local` for ownerless sessions,
or configure caller identity before selecting Cedar.

### MCP capabilities

MCP grants apply to the addressed tool. For example, `CallMcpWithQuery` checks
`mcp__github__list_pull_requests`, not blanket GitHub server access. Resource
operations use separate per-server capabilities. Granting one MCP tool does not
grant the rest of its server.

Authority failures fail closed. Mecatl distinguishes a denied call from an
unavailable evaluator.

### Deployment checklist

- Choose the evaluator explicitly.
- Load authority-defining agents from an operator-owned `--agents-dir`.
- Treat `tools:` as a ceiling, not a grant.
- Keep Cedar policies outside project-controlled paths and require verified
  caller identity.
- Test delegation and resume with the deployment's actual policy.

---

## Layer 1: permission rules

Every tool call is evaluated against a merged set of rules. A rule is
`{Scope, Tool, Pattern, Effect}`: `Effect` is `allow`, `ask`, or `deny`; an
empty `Tool` matches any tool; an empty `Pattern` matches any arguments,
otherwise it is a shell-style glob over the canonicalized command/argument
string.

### Resolution: deny-dominant, then scope

A decision resolves as follows:

1. **Effect:** `deny` beats `ask`, which beats `allow`, across all scopes.
2. **Scope breaks same-effect ties** (highest precedence first):
   `Managed > CLI > LocalProject > SharedProject > User > BuiltinDefault`.
3. **Fallback:** no match resolves to `ask`.

A configured allow at a higher scope can loosen only a `BuiltinDefault` ask. It
cannot override a configured ask or any deny.

A deny reason goes to the model; an ask reason goes to the client.

### The scope hierarchy

Scopes are where a rule comes from, highest precedence first:

|Scope|Source|Trust|
|-|-|-|
|`Managed`|Enterprise or administrator floor|Always honored|
|`CLI`|each `--permission-config <file>`|fully trusted (the operator's own)|
|`LocalProject`|`<workspace>/.mecatl/settings.local.yaml` (gitignored, personal)|**trust-gated**|
|`SharedProject`|`<workspace>/.mecatl/settings.yaml` (checked-in, shared)|**trust-gated**|
|`User`|`$XDG_CONFIG_HOME/mecatl/settings.yaml`|fully trusted (the operator's own)|
|`BuiltinDefault`|Built-in read-allow and mutate-ask floor|Lowest precedence|

Mecatl resolves project files for each session workspace and refreshes changed
configuration before the next call. Sessions in different projects can receive
different decisions from one server.

### The default ruleset

The built-in floor allows read-only exploration and asks before anything that
can mutate:

|Tool|Default effect|
|-|-|
|`Read`, `ListDir`, `Grep`, `Glob`, `WebFetch`, `WebSearch`, `Subagent`|`allow`|
|`Shell`, `Edit`, `Write`, `Copy`, `Move`, `Remove`, `Team`, `SkillDraft`|`ask`|

Memory tools, `soul:apply`, and read-only child observability are also built-in
allows that higher scopes can override. `Team` asks because it can create
mutating members.

For a main session on a local filesystem, `Read` and `ListDir` can access an
external absolute target when policy authorizes it. The `auto` and `yolo`
postures allow this read-only access; `strict` and `trusted` ask first. Configured
asks and denies still take precedence. External access remains unavailable to
children and virtual workspace backends, and `/proc`, `/sys`, and `/dev` remain
blocked. For an external `ListDir`, choose a specific directory: listing the
filesystem root `/` outside the workspace is unsupported.

### Effects: allow, ask, deny

- **`allow`** runs the call without prompting.
- **`ask`** pauses and asks the client for a decision. The client can allow this
  call once or learn an allow for the session.
- **`deny`** returns the reason to the model without running the call.

Allow-once applies to the current call. Allow-always learns a session rule at
the lowest scope, so it cannot override a deny or configured ask.

### Compound Shell and substitution safety

For `Shell`, Mecatl splits compound commands and applies the most restrictive
decision across all parts. A permitted `git status` does not allow a following
`rm -rf /`.

Command substitution, process substitution, and subshell grouping have an `ask`
floor because they can conceal commands. Posture can loosen this floor only for
read-only inner commands.

### Plan mode

Plan mode denies `Edit`, `Write`, and non-read-only `Shell` before rule
evaluation. Read-only operations continue through the normal rules.

The model exits plan mode by calling `PresentPlan`, which creates a separate
plan-approval ask. An interactive client can approve in `default` mode, approve
in `acceptEdits` mode, or request another plan iteration. Headless deployments
deny this ask unless the operator enables `--plan-mode-auto-approve`. The flag
approves only a plan that the model explicitly presented. See the
[gRPC API reference](/reference/grpc-api.md) and
[HTTP and SSE API reference](/reference/http-sse-api.md) for wire behavior.

### Configuring rules

`.mecatl/settings.yaml` (checked-in, shared), `.mecatl/settings.local.yaml`
(gitignored, personal), and the user-global file all share the same shape:

```yaml
permissions:
  allow:
    - 'Shell(go test:*)' # the "prefix:*" form, normalised to the glob "go test*"
    - 'Shell(go build*)' # native glob form
    - 'Read' # bare tool name = tool-wide
  ask:
    - 'Shell(git push:*)'
  deny:
    - 'Shell(rm:*)' # a deny in any scope also binds children
  subagent:
    deny:
      - 'Shell(gh pr merge:*)' # tighten a child's Shell beyond the main rules
    allow:
      - 'Shell(go vet:*)' # clears this from a child's substitution-floored ask
```

Each entry is `Tool(pattern)` or a bare `Tool`, with glob matching. Mecatl
normalizes `prefix:*` and `prefix:` to `prefix*`. Unknown permission keys cause
the file to be skipped and logged. The nested `subagent:` block also parses
strictly.

### Importing Claude Code's permissions

`--import-claude-permissions` reads rules from a project or user
`.claude/settings.json`. The import logs lossy conversions and never widens an
allow:

- `WebFetch(domain:x)` in an allow list becomes `ask` because a domain substring
  match is too risky to auto-allow without you seeing it at least once.
- A bare `WebSearch` allow remains an allow.
- A `Read(~/...)` pattern remains inert because `~` is not expanded.
- Anything the importer can't parse is dropped, not guessed at.

Denies and asks import unchanged. Embedded `mecatui` enables this import and
`--permissions-conventional` by default.

### The posture ladder

Posture controls how much the harness self-authorizes. Set it with
`--posture <strict|trusted|auto|yolo>` or the operator-global `posture:`
setting. `--trust-project` aliases `trusted`; `--yolo` aliases `yolo`.

|Posture|Automatic mutation|Child substitution checks|Project trust|Use it for|
|-|-|-|-|-|
|`strict` (default)|Off|On|Explicit only|Interactive or untrusted projects|
|`trusted`|Off|On|On|Trusted projects that still need prompts|
|`auto`|On|On|On|Unattended work in a sandbox|
|`yolo`|On|Off|On|Disposable, isolated, single-tenant sandboxes|

Use `auto` for unattended operation when the deployment sandbox can tolerate
automatic mutations. It retains the child substitution check that `yolo`
disables.

Automatic mutation loosens only the built-in mutation prompt. Every posture
still honors:

- Every deny.
- Every configured ask.
- Plan-mode write restrictions.

:::warning[Sandbox required for allow-all posture]

Posture bypasses approval prompts, not operating-system isolation. Use `auto` or
`yolo` only in a single-tenant sandbox with bounded network and filesystem
access. The process refuses an allow-all posture as root unless
`MECATL_SANDBOX=1` or `IS_SANDBOX=1` is set.

:::

Every agent-facing shell receives a credential-scrubbed environment, including
under `auto` and `yolo`.

### Out-of-workspace filesystem access (the path-escape posture)

`Read`, `Write`, and `Edit` are rooted at the session workspace. Posture decides
whether the main session can access a resolved path outside that root:

|Posture|Read escape|Write escape|
|-|-|-|
|`yolo`|allow|allow|
|`auto`|allow|**ask**|
|`strict` / `trusted`|**ask**|**ask**|

- An approval names the exact path and applies once. It never creates a learned
  rule.
- Configured `deny` and `ask` rules still win, and plan mode still denies
  writes.
- `/proc`, `/sys`, and `/dev` remain denied at every posture.
- Child agents never receive out-of-workspace access, even when they share the
  parent's workspace.
- Access retains the normal symlink containment checks.

At `auto`, `guardrails.escape: true` can send each escape through the configured
guardrail model. An unsafe result is denied; a checker failure falls back to the
write approval. See the
[configuration reference](/reference/configuration.md#guardrails).

### Workspace trust

Trust controls whether Mecatl admits project-provided authority:

- the project's permission **ALLOW** rules (auto-approval the repo grants
  itself);
- the project **soul**, `<workspace>/.mecatl/soul.md`;
- the **project tier** of agent definitions, slash commands, and skills under
  `<workspace>/.mecatl/*` and `<workspace>/.claude/*`.

An untrusted project can still use built-in tools, user configuration, and
approval prompts. Project `deny` and `ask` rules always apply because they only
tighten access. Project `allow` rules, soul, agents, commands, and skills
require trust.

Configure trust three ways:

- `--trust-project` trusts the current project for one invocation.
- `trustedWorkspaces:` lists trusted absolute paths in user-global
  `settings.yaml`:

  ```yaml
  # ~/.config/mecatl/settings.yaml  (the operator's own, fully-trusted file)
  trustedWorkspaces:
    - /home/me/src/my-project
    - /home/me/work/known-good-repo
  ```

  Mecatl compares cleaned, absolute, symlink-resolved paths.

- Embedded `mecatui` prompts when it first encounters project authority. A
  remembered decision is stored in `trust.yaml`. `mecated` reads that registry
  but never prompts.

A remembered decision is tied to the project's soul, agents, commands, and
skills. Changing those files returns the project to untrusted. Editing project
permission rules does not trigger another prompt.

A corrupt `settings.yaml` or `trust.yaml` resolves to untrusted.

#### Project-tier ingestion on headless roots (the opt-in design)

On a headless root, posture alone never trusts the project. Use
`--trust-project`, `trustedWorkspaces:`, or a current remembered decision to
admit project steering and read-only child Shell access.

---

## Layer 2: model-backed guardrails

Guardrails use a separate, tool-less model to inspect matched tool content:

- `PreToolUse` checks outbound arguments for exfiltration before execution.
- `PostToolUse` checks inbound results for prompt injection before the agent
  reads them.

### Assessments and enforcement

The contextual reviewer returns **acceptable**, **prohibited**, or **unresolved**.
Inspection health is separate: an operational checker failure is an outage, not proof
that content is unsafe. Each rule has one of two modes:

- **`block`** enforces. Before execution, an action finding can stop or ask. After
  execution, a result finding is held privately before it reaches history, events,
  the client, or the working model.
- **`advisory`** leaves the call/result unchanged and shows the completed finding or
  unresolved inspection to the operator.

Sanitization and checker-authored replacement actions are not supported. Missing
evidence can make an assessment unresolved, but does not itself make the action or
content intrinsically unsafe.

### Human choices: action versus result release

For an enforcing **action** review, Mecatui offers:

- **Run once** — execute this exact effective call once;
- **Don't ask again** — only when the exact action, target, environment revision,
  and relevant dependencies can be version-bound; the grant is guardrail-only,
  session/process-local, and invalidates on a material change; or
- **Cancel** — do not run the tool.

For an enforcing **inbound result** review, Mecatui offers only **Release once** or
**Cancel**. The tool has already run. Release consumes the exact privately-held result
and runs no tool, hook, reviewer, or side effect again. It allows the model to read the
content; embedded instructions remain untrusted and no later action is approved.
Loss, timeout, disconnect, unattended enforcement, or restart safely withholds the
result; there is no re-execution recovery.

`/guardrails` shows the checker route and effective action/inbound rules for the active
session's assembled tool catalog. `/posture` keeps permission posture separate and adds
the effective checker state: off includes setup guidance, on says advisory or enforcing,
and an unavailable or older server is reported as unknown rather than off or healthy.
Review cards distinguish a prohibited finding, a completed unresolved inspection,
and an operational checker outage. Operational failures use a closed reason such as
`provider_failure`, `timeout`, or `evidence_failure`; checker responses, prompts,
evidence, and backend error text are not displayed. `/guardrails` reports the
latest reason and returns to completed assessment status after a successful
inspection. Human rationale is
bounded live-only detail: it is owner-authorized, never persisted in session events or
snapshots, and disappears when the root run is cleaned up.

Under `yolo`, all guardrail rules become advisory. `strict`, `trusted`, and
`auto` retain enforcement.

### Configuring guardrails

Guardrails remain off until you configure a checker model. With a model and no
explicit rules, Mecatl uses this block ruleset:

|Tool matcher|Action review|Inbound review|Default mode|
|-|-|-|-|
|`Shell`|yes|yes|block|
|`Edit`, `Write`, `Copy`, `Move`, `Remove`|yes|no|block|
|`Read`, `ListDir`, `Grep`, `Glob`|no|yes|block|
|`WebSearch`, `WebFetch`|yes|yes|block|
|`mcp__*`, `CallMcpWithQuery`|yes|yes|block|
|`FetchMcpResource`|no|yes|block|
|`Subagent`, `Parallel`, `Team`|yes|no|block|

The same applicable defaults bind worker calls. A rule still resolves against the
actual tool catalog assembled for that session; `/guardrails` is the authoritative
status view. The implementation does not claim that protocol tests prove a chosen
checker model detects every prompt injection, secret, or dynamic Shell dependency.
Use release-validation evidence before making an efficacy claim.

A confidently read-only Shell command skips only **action** review; inbound Shell
results remain covered. Every contextual review uses the fixed harness-owned
safety, authority, provenance, evidence, and structured-output rubric. A rule's
optional `prompt` adds operator task-risk context beneath that rubric; it cannot
replace or weaken the fixed contract.

Set `defaultMode: advisory` to start the default set in observe-only mode and
tune up from there.

```yaml
# ~/.config/mecatl/settings.yaml (user-global only)
guardrails:
  model: gpt-5-mini # configuring a model is the opt-in; default rules apply
  taskWindow: 2 # last 1–3 accepted genuine root prompts; default 1
  rules: # an explicit list REPLACES the default set
    - match: 'WebFetch' # inbound injection on fetched pages
      phases: ['post'] # "pre" = outbound args, "post" = inbound result; omit = both
      mode: block
    - match: 'mcp__*' # all MCP tools, both directions
      mode: advisory # observe first, tune later
    - match: 'Shell' # outbound exfil in shell args
      phases: ['pre']
      mode: block
      prompt: 'Treat publishing externally as high risk unless the current user task explicitly requires it.'
      failClosed: true # explicit per-rule override; checker outage is fail-closed by default
```

A matcher keys on the tool **name** only (exact > `prefix*` > `*`, most-specific
wins); a tool with no matching rule is unchecked. `guardrails.rules[].prompt` adds operator task-risk context beneath the fixed
harness safety, authority, provenance, evidence, and output contract; it cannot
replace that rubric. Checker outage is fail-closed
by default: bounded recovery is followed by a human boundary when interactive,
or action denial/result withholding when unattended. Explicit
`onCheckerDown: warn` continues with a visible operational warning; it never
relabels the outage as a prohibited finding. A completed acceptable verdict
passes.

The operator-only `taskWindow` setting selects the last one to three accepted genuine
root prompts supplied as task context (default `1`). Prior genuine prompts and accepted
in-run steers count; harness nudges, summaries, injected fragments, and worker goals do
not. Values outside the range are clamped.

Unlike the headless-only ask reviewer, guardrails fire on the main loop
regardless of `--headless`.

:::warning[Operator-tier only]

Mecatl reads `guardrails:` only from user-global settings and CLI flags. It
ignores a project-tier block with a warning. Set the model with
`--guardrails-model` or a bound `guardrail` model slot. Use `--guardrails=off`
as the deployment-wide kill switch.

:::

---

## The `permission.ask` flow

When Layer 1 resolves to `ask`, the call pauses and emits `permission.ask` with
the tool name, bounded and redacted arguments, and reason. The client returns:

- **deny:** the call never runs; the model receives the deny and adapts.
- **allow-once:** the call runs this time only.
- **allow-always:** the call runs and the policy learns a per-session rule so
  the same call is not re-asked. A learned allow lives at the lowest scope and
  can never override a deny or a configured ask.

A pending ask can resume after process restart when the verdict arrives.

Headless deployments deny unresolved asks. The optional headless-only
`--subagent-ask-reviewer` adds a one-turn, tool-less model review for child
asks. It can approve once, never learn an allow, and fails closed. Configured
asks do not reach the reviewer. After three consecutive denials or failures in
one run, later asks skip the reviewer; an approval resets that count.

---

## Subagents and the permission model

Children (subagent explorers, team members, parallel branches) get their **own**
scoped ruleset, distinct from the main engine's:

- Child engines default to an allow-all built-in floor. Everything runs except
  substitution-floored commands and anything a configured deny/ask gates.
- A top-level **`deny`** binds children too (a deny only ever tightens, so it
  binds everywhere). Top-level `allow`/`ask` are main-only (children are already
  allow-all).
- A `subagent:` block in the config carries child-scoped rules: `subagent: deny`
  / `subagent: ask` tighten a child command; `subagent: allow` clears a child's
  _substitution-floored_ ask (and only when the hidden `$(...)` inners
  independently classify as read-only).
- Under `auto` and `yolo` posture, the allow-all rule is pushed to children too.
  The difference between the two tiers is the **child substitution floor**:
  `auto` keeps it gated through the child-ask model; `yolo` lets child
  substitutions run automatically.

A worker command held only by the built-in Shell substitution or grouping floor can
receive one permission-specific contextual review when an enforcing `Shell` action
rule matches. A completed acceptable assessment authorizes that exact execution once.
It does not learn a permission or skip the normal action review before execution.
Configured asks, plan mode, system temporary scope, advisory rules, skipped rules, and
unmatched rules continue through the ordinary interactive or headless ask path.

A project's `subagent:` allows are themselves trust-gated, exactly like its main
allows.

---

## What's next

- [Hook system](hooks.md) for lifecycle phases and custom hooks.
- [PermissionPolicy extension point](/building/extension-points/permission-policy.md)
  to replace permission-rule logic with your own.
