---
sidebar_position: 300
title: Permissions and posture
description: Control approvals, project trust, guardrails, and autonomous Mecatl operation.
---

# Permissions and posture

Mecatl has two separate safety controls:

1. **Permissions** decide whether a tool call is allowed, needs approval, or is
   denied before it runs.
2. **Guardrails** are an optional model-backed checker that inspects selected
   tool arguments and results. They are independent of the permission rules.

The permission rules are always present, including when no configuration file
exists. Guardrails are disabled until an operator configures a checker model.

For evaluator, authority-set, and custom-policy contracts, see [Permissions and
guardrails for builders](/building/what-you-get/permissions.md).

## Availability

Permissions and posture apply to `mecated`, `mecak8s`, `mecatequi`, and the
embedded server hosted by mecatui. A connected mecatui uses the posture and
permission configuration of the remote server; local embedded-server flags do
not apply to `connect` sessions.

Interactive clients such as `mecatui` can answer approval requests. For headless
servers and one-shot jobs, configure the permissions required by the workload
before it starts.

## Choose a posture

`--posture` selects the operator posture ladder:

| Posture | Behavior |
| --- | --- |
| `strict` | Default for interactive `mecated`; read-only calls are allowed and mutating calls use the permission rules, normally asking before they run. Project trust is not granted by the posture. |
| `trusted` | Interactive roots admit trusted project instructions and project permission allows, but mutating calls still use the normal approval rules. |
| `auto` | Enables the allow-all posture for the main agent and children while keeping deny rules and deliberately configured asks effective. Child substitution defenses remain enabled. |
| `yolo` | Extends `auto` by allowing child command substitutions, backticks, and heredoc-style substitutions that `auto` keeps behind the child safety floor. Use only for isolated, disposable, single-tenant deployments. |

`--yolo` and `--trust-project` are compatibility aliases that raise the
posture tier. The highest effective tier wins. An operator-global `posture:`
setting can also provide the baseline; a project file cannot raise the
operator's posture.

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

## Approve and constrain work

The built-in permission floor allows read-only exploration and asks before
operations that can mutate state:

| Default effect | Tools |
| --- | --- |
| Allow | `Read`, `ListDir`, `Grep`, `Glob`, `WebFetch`, `WebSearch`, and read-only `Subagent` exploration |
| Ask | `Shell`, `Edit`, `Write`, `Team`, and `SkillDraft` |

A matching `deny` always wins. Otherwise an `ask` wins over an `allow`, and
higher configuration scopes break ties. A configured allow can loosen only the
built-in default ask; it cannot suppress a configured ask or any deny.

When a call is denied, the model receives a reason and can adapt. When a call
needs approval, an interactive client shows the call and lets the operator:

- allow this call once;
- allow the matching call for the session; or
- deny it.

An allow-always decision is learned only at the lowest built-in scope. It never
overrides a deny or configured ask. Headless deployments do not have a human
approval channel, so configure explicit allows or choose an appropriate
posture before starting work.

### Plan mode

Plan mode denies `Edit`, `Write`, and mutating Shell commands. Read-only
exploration remains available so the model can inspect the workspace and prepare
a plan. In an interactive client, the model presents the completed plan for
review before leaving plan mode. Headless plan approval is denied by default;
`--plan-mode-auto-approve` is an explicit operator opt-in and should be treated
as an autonomous approval capability.

## Configure permission rules

Permission configuration is available in YAML files with a `permissions:`
section:

```yaml
permissions:
  allow:
    - "Read"
    - "Shell(go test:*)"
  ask:
    - "Shell(git push:*)"
  deny:
    - "Shell(rm:*)"
  subagent:
    deny:
      - "Shell(gh pr merge:*)"
    allow:
      - "Shell(go vet:*)"
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
workspace. Project `deny` and `ask` rules remain effective, but project
`allow` rules require project trust.

Use `--import-claude-permissions` to import supported rules from Claude Code
`settings.json`. The import is lossy and fail-safe: unsupported
or ambiguous rules are dropped or demoted to approval rather than widening
access.

## Project trust

Project trust is a separate positive decision from permission evaluation. It
controls whether project-provided authority is admitted, including:

- permission `allow` rules;
- project instructions and rules;
- project skills, soul, and named agents; and
- the read-only child shell and workspace inspection posture.

Trust can come from an explicit operator flag, a trusted-workspace setting, an
undrifted remembered trust decision, or the interactive posture floor. A
headless root does not gain project trust merely because it uses `trusted`,
`auto`, or `yolo`; it needs an explicit trust source.

This means `--posture auto` on an untrusted headless checkout can provide
allow-all behavior for the admitted tools without admitting attacker-controlled
project steering or a read-only child shell. Treat `--trust-project` as an
operator assertion that the repository and its `.git` metadata are trusted.

## Guardrails

Guardrails add content inspection around selected tool calls. Configure a
checker model to enable them:

```sh
mecated serve --guardrails-model gpt-5.6-luna
```

The checker can inspect outbound tool arguments and inbound tool results. A
configured checker with no custom rule list uses the default enforcing rule set;
operators can configure advisory behavior instead. Guardrails remain active in
headless deployments and are not a replacement for permission rules.

Guardrail configuration is operator-tier only. A project repository cannot
weaken or disable the operator's checker. A checker failure follows the
configured fail-open/fail-closed behavior, and unsafe or malformed sanitized
content is not silently accepted.

See the [guardrails reference](/building/what-you-get/permissions.md)
for matchers, modes, and checker failure handling.

## Limitations

- `strict` does not mean every call is denied; it preserves the built-in
  read-allow/mutate-ask floor.
- `auto` and `yolo` do not override a deny or a deliberately configured ask.
- Headless main-session asks can still wait indefinitely unless the deployment
  configures the required permissions; headless child asks use the fail-safe
  child path instead of waiting for a client.
- Project files are untrusted by default. Do not enable project trust for a
  repository whose instructions, hooks, skills, or Git metadata you have not
  reviewed.
- Permission rules and guardrails are different controls: an allow does not
  disable guardrails, and a guardrail advisory does not change the tool's
  permission result.
- `--posture`, `--trust-project`, and permission configuration on a local
  mecatui invocation do not change a remote server used through `connect`.

## Next steps

- [Permissions and guardrails](/building/what-you-get/permissions.md) for the detailed
  rule-resolution and approval reference.
- [Project instructions and rules](./project-instructions-and-rules.md) for
  project-ingestion behavior.
- [Execution environments](./execution-environments.md) for workspace and shell
  isolation.
- [Start and resume sessions](./start-and-resume-sessions.md) for session
  lifecycle.
