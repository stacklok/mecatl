---
sidebar_position: 3
title: Permissions & guardrails
description:
  Understand Mecatl permissions, approvals, trust, and guardrails for agent
  actions.
---

# Permissions & guardrails

This is the builder-facing reference for permission evaluation, delegated
authority, and custom policy integration. For operator choices, configuration,
and posture selection, see
[Permissions and posture](/features/permissions-and-posture.md).

Mecatl checks tool calls in two independent layers:

|Layer|Decision|Default|
|-|-|-|
|Permission rules|Whether a call can run: `allow`, `ask`, or `deny`|On with a built-in ruleset|
|Model-backed guardrails|Whether matched input or output content is safe|Off until you configure a checker model|

## Delegated authority

Delegated children carry an **authority set** that records which capabilities
the parent delegated. Authority is separate from permissions, which decide
whether a call can run, and ownership, which decides who can access a session.

A root session starts with the tools in its composed catalog. A child receives
only what survives this calculation:

```text
parent authority
∩ child runtime posture
∩ managed specialist ceiling, when eligible
∩ optional call-level narrowing
− one delegation hop
```

For example, a parent with `Read`, `Grep`, and `Write` can delegate a reviewer
with only `Read` and `Grep`. The child cannot regain `Write`. Its authority set
persists with the session and must remain within the parent's current set when
the child resumes.

### Agent definitions and authority ceilings

A definition's `tools:` allowlist scopes a specialist from any source. Only a
definition loaded from an explicit operator directory establishes a durable
authority ceiling. Configure that directory with `--agents-dir`:

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

The allowlist can remove capabilities from the parent but cannot grant a
capability the parent lacks. Definitions from project, user, or driver sources
scope their specialist but do not create an independent authority grant.

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

A bound session without an evaluator fails closed. Selecting `noop` is an
explicit choice, not a fallback.

### Cedar policy boundaries

Cedar loads one operator-owned policy file at startup. A missing or invalid file
prevents `mecated` from starting. Keep it outside project-controlled
directories.

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

Cedar requires verified session owner identity. Deployments that intentionally
run ownerless sessions should use `local`, or configure caller identity before
selecting Cedar.

### MCP capabilities

MCP grants stay narrow. `CallMcpWithQuery` is evaluated against the concrete
tool it addresses, such as `mcp__github__list_pull_requests`, rather than
receiving blanket access to a server. MCP resource operations similarly spend a
separate per-server resource capability while preserving their operation, such
as `ReadMcpResource`, for policy evaluation. Granting one GitHub MCP tool
therefore does not grant every GitHub MCP tool.

Authority failures are fail-closed. A denial tells the model that authority
refused the call. An unavailable evaluator is reported separately and never
treated as an approval.

### Deployment checklist

- Choose the evaluator explicitly.
- Load authority-defining agents from an operator-owned `--agents-dir`.
- Treat `tools:` as a ceiling, not a grant.
- Keep Cedar policies outside project-controlled paths and require verified
  caller identity.
- Test delegation and resume with the deployment's actual policy.

---

## Layer 1 — the permission rule engine

Every tool call is evaluated against a merged set of rules. A rule is
`{Scope, Tool, Pattern, Effect}`: `Effect` is `allow`, `ask`, or `deny`; an
empty `Tool` matches any tool; an empty `Pattern` matches any arguments,
otherwise it is a shell-style glob over the canonicalized command/argument
string.

### Resolution: deny-dominant, then scope

A decision resolves in this order:

1. **Effect dominance: `deny` → `ask` → `allow`.** A `deny` in _any_ scope beats
   an `ask` or `allow` _anywhere_ — a deny is absolute and final. Otherwise an
   `ask` beats an `allow`.
2. **Scope breaks same-effect ties** (highest precedence first):
   `Managed > CLI > LocalProject > SharedProject > User > BuiltinDefault`.
3. **No matching rule → `ask`** — the safe default. The harness never silently
   allows an unconfigured call.

There is **one narrow exception** to "ask beats allow": a higher-scope
configured **Allow** may loosen _only_ the built-in `BuiltinDefault` Ask floor
(for example, allowing `Shell(go test:*)` relaxes the built-in Shell ask). It
can **never** suppress a _configured_ Ask, and it can never out-rank a deny in
any scope.

A `deny` or `ask` carries a human-readable reason: surfaced to the model on a
deny (so it can adapt) and to the client on an ask.

### The scope hierarchy

Scopes are where a rule comes from, highest precedence first:

|Scope|Source|Trust|
|-|-|-|
|`Managed`|enterprise/admin floor|always honoured; nothing below overrides its deny|
|`CLI`|each `--permission-config <file>`|fully trusted (the operator's own)|
|`LocalProject`|`<workspace>/.mecatl/settings.local.yaml` (gitignored, personal)|**trust-gated**|
|`SharedProject`|`<workspace>/.mecatl/settings.yaml` (checked-in, shared)|**trust-gated**|
|`User`|`$XDG_CONFIG_HOME/mecatl/settings.yaml`|fully trusted (the operator's own)|
|`BuiltinDefault`|the built-in floor (read-allow / mutate-ask)|n/a — lowest precedence|

Project files are re-resolved **per session** against each session's workspace
root, and the resolver revalidates its cache on the config files' mtime/size —
so a `deny` added mid-process takes effect on the next call, not at restart. Two
sessions running in different repos under the same server get different
decisions for the same tool call.

### The default ruleset

The built-in floor allows read-only exploration and asks before anything that
can mutate:

|Tool|Default effect|
|-|-|
|`Read`, `ListDir`, `Grep`, `Glob`, `WebFetch`, `WebSearch`, `Subagent`|`allow`|
|`Shell`, `Edit`, `Write`, `Copy`, `Move`, `Remove`, `Team`, `SkillDraft`|`ask`|

(The memory tools, the synthetic `soul:apply` action, and the read-only child
observability tools are also floor-scoped allows — pre-approved but overridable
by any higher-scope config.) Read-only exploration runs uninterrupted; anything
that can mutate the workspace pauses for approval. `Team` asks because it can
spawn mutating members, unlike the read-only `Subagent` explorer.

### Effects: allow, ask, deny

- **`allow`** — the call runs without prompting.
- **`ask`** — the call pauses and is surfaced to the client for an approval
  decision (see [The `permission.ask` flow](#the-permissionask-flow)). An
  approval can be **allow-once** (this call only) or **allow-always** (learned
  for the session — see below).
- **`deny`** — the call never runs; the model receives the deny reason and
  adapts.

**Allow-once vs allow-always.** When a client approves an ask, it chooses the
duration. _Allow-once_ clears only the current call. _Allow-always_ feeds the
permission policy's `Learn` path, which derives a per-session rule so the same
call is not re-asked. A learned allow is consulted at the **lowest** scope only
— it can never override a deny or a configured ask.

### Compound Shell and substitution safety

For `Shell`, the evaluator splits a compound command line (`&&`, `||`, `;`, `|`,
a bare `&`, newlines, honouring quotes) and requires **every** sub-command to
pass; the **worst** outcome wins. So `git status && rm -rf /` inherits the
deny/ask from the `rm` segment even if `git status` alone would be allowed.

Any segment containing command/process substitution or subshell grouping
(`$(...)`, backticks, `<(...)`, `(`/`{` grouping) — which could smuggle a hidden
inner command past the splitter — is floored at **`ask`**. An allow rule for the
outer literal can never silently approve a concealed command. (The substitution
floor can be loosened by posture — see below — but only for read-only inners.)

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
    - 'Shell(rm:*)' # deny wins absolutely, in any scope — binds children too
  subagent:
    deny:
      - 'Shell(gh pr merge:*)' # tighten a child's Shell beyond the main rules
    allow:
      - 'Shell(go vet:*)' # clears this from a child's substitution-floored ask
```

Each entry is a rule spec `Tool(pattern)` or a bare `Tool`. Config rules use
**glob** semantics; the `prefix:*` / `prefix:` form is normalised to a `prefix*`
glob. The `permissions:` subtree parses **strictly** — an unknown key (a typo
like `alow:`) is a loud parse error and the whole file is skipped (and logged),
never silently ignored; the `subagent:` subtree inside it parses just as
strictly, on its own.

### Importing Claude Code's permissions

`--import-claude-permissions` reads rules from a project or user
`.claude/settings.json`. The import logs lossy conversions and never widens an
allow:

- `WebFetch(domain:x)` in an allow list is demoted to `ask` — a domain/substring
  match is too risky to auto-allow without you seeing it at least once.
- A bare `WebSearch` allow imports verbatim, no demotion (its payload is a query
  string, not an arbitrary fetch).
- A `Read(~/...)` pattern imports but stays inert — the `~` is left unexpanded,
  so it never matches the absolute path a tool actually resolves to.
- Anything the importer can't parse is dropped, not guessed at.

`deny` and `ask` rules import unchanged. Embedded `mecatui` servers enable this
behavior and `--permissions-conventional` by default.

### The posture ladder

Posture controls how much the harness self-authorizes. Set it with
`--posture <strict|trusted|auto|yolo>` or the operator-global `posture:`
setting. `--trust-project` aliases `trusted`; `--yolo` aliases `yolo`.

|posture|allow-all (no mutate-ask prompts)|child substitution floor|project trust|use it for|
|-|-|-|-|-|
|`strict` (**default**, fail-closed)|off|gated|(your own `--trust-project`)|interactive / untrusted repos|
|`trusted`|off|gated|**on**|a repo you trust, still want prompts|
|`auto`|**on** (main + children)|**gated** (injection defence **on**)|on|the recommended unattended default|
|`yolo`|**on** (main + children)|**loosened** (injection defence **off**)|on|a disposable, isolated, single-tenant sandbox|

Use `auto` for unattended operation when the deployment sandbox can tolerate
automatic mutations. It retains the child substitution check that `yolo`
disables.

Allow-all loosens only the built-in mutation prompt. These constraints remain at
every posture:

- A `deny` in any scope (including `Managed`) still wins — deny-dominance is
  absolute.
- Any **deliberately configured** `ask` still asks. Allow-all never suppresses a
  configured ask, so a misconfigured ask can still block an unattended run (the
  startup warning says so).
- Plan-mode hard-denies still fire first.

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
- the project **soul** (`<workspace>/.mecatl/soul.md` — a repo rewriting the
  agent's persona);
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

## Layer 2 — model-backed guardrails

Guardrails use a separate, tool-less model to inspect matched tool content:

- `PreToolUse` checks outbound arguments for exfiltration before execution.
- `PostToolUse` checks inbound results for prompt injection before the agent
  reads them.

### Verdicts: block, sanitize, advisory

|Mode|Behavior|
|-|-|
|`block`|Stops a pre-tool call or replaces a post-tool result with an error.|
|`sanitize`|Replaces arguments or results with the checker's `sanitized_content`. Invalid or oversized replacements become blocks.|
|`advisory`|Logs and displays a finding without changing what the tool or model receives.|

Use `sanitize` only with a checker you trust because its output replaces the
original content.

### PostToolUse block rewrites, it does not veto

A `PostToolUse` check runs after the tool, so it cannot undo the operation. A
block replaces the result with `is_error: true`. Recorded history, client
events, and the model all receive that replacement. Only a `PreToolUse` block
prevents execution.

### Recovering from a block: approve-once

A `PreToolUse` block creates the same interactive approval flow as a permission
ask:

- **Deny** — the call never runs; the model receives the block reason and
  adapts.
- **Allow once** — the call runs, this time only.
- **Allow and don't ask again** runs the call and creates an in-memory,
  session-scoped waiver for the exact tool and normalized arguments. It does not
  survive restart.

A pending approval can resume after restart. A headless deployment resolves an
unanswered block as a terminal block.

Under `yolo`, all guardrail rules become advisory. `strict`, `trusted`, and
`auto` retain enforcement.

### Configuring guardrails

Guardrails remain off until you configure a checker model. With a model and no
explicit rules, Mecatl uses this block ruleset:

|Tool matcher|Phases|Mode|
|-|-|-|
|`WebSearch`|pre + post|block|
|`WebFetch`|post|block|
|`mcp__*` (all MCP tools)|pre + post|block|
|`Shell`|pre|block (read-only commands skip the checker)|

Local filesystem tools are not matched by default. The `Shell` matcher skips
commands that Mecatl can classify as read-only and inspects everything else. Its
rubric checks for data exfiltration, remote-code execution, irreversible remote
actions, destructive local actions, and persistence through credentials or
startup files. Ordinary source edits, builds, tests, and local file operations
are considered safe.

Set `defaultMode: advisory` to start the default set in observe-only mode and
tune up from there.

```yaml
# ~/.config/mecatl/settings.yaml  (user-global only — NOT a checked-in project file)
guardrails:
  model: gpt-5-mini # configuring a model is the opt-in; default rules apply
  minContentBytes: 16 # skip a short INBOUND (post) result; outbound (pre) args are always inspected
  rules: # an explicit list REPLACES the default set
    - match: 'WebFetch' # inbound injection on fetched pages
      phases: ['post'] # "pre" = outbound args, "post" = inbound result; omit = both
      mode: block
    - match: 'mcp__*' # all MCP tools, both directions
      mode: advisory # observe first, tune later
    - match: 'Shell' # outbound exfil in shell args
      phases: ['pre']
      mode: sanitize # trusts the checker's rewrite — use only with a trusted checker
      failClosed: true # a checker outage treats the content as UNSAFE (default is fail-OPEN)
```

A matcher uses the tool name. Exact matches outrank `prefix*`, which outranks
`*`. A tool without a matching rule is unchecked. Checker errors fail open by
default and produce a warning; set `failClosed: true` to treat an error as
unsafe.

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

- **deny** — the call never runs; the model receives the deny and adapts.
- **allow-once** — the call runs this time only.
- **allow-always** — the call runs and the policy _learns_ a per-session rule so
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

- Child engines default to **allow-all** at a built-in floor — everything runs
  except substitution-floored commands and anything a configured deny/ask gates.
- A top-level **`deny`** binds children too (a deny only ever tightens, so it
  binds everywhere). Top-level `allow`/`ask` are main-only (children are already
  allow-all).
- A `subagent:` block in the config carries child-scoped rules: `subagent: deny`
  / `subagent: ask` tighten a child command; `subagent: allow` clears a child's
  _substitution-floored_ ask (and only when the hidden `$(...)` inners
  independently classify as read-only).
- Under `auto` and `yolo` posture, the allow-all rule is pushed to children too.
  The difference between the two tiers is the **child substitution floor**:
  `auto` keeps it gated (a child's `$(...)` resolves through the child-ask model
  — injection defence on); `yolo` loosens it (a child's substitution auto-runs —
  defence off).

A project's `subagent:` allows are themselves trust-gated, exactly like its main
allows.

---

## What's next?

- [Hook system](hooks.md) — the lifecycle phases the guardrail checker
  decorates, and how to write your own pre/post-tool and lifecycle hooks.
- [PermissionPolicy extension point](/building/extension-points/permission-policy.md)
  — implement the port to replace Layer 1's rule logic with your own.
