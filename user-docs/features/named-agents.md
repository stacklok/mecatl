---
sidebar_position: 200
title: Define named agents
description: Define specialist agents with their own instructions, tools, and model settings.
---

# Define named agents

A named agent is a reusable specialist profile. It gives a delegated child a
name, instructions, and optional execution settings instead of repeating the
same setup for every call.

Unlike a one-off `Subagent` call, a named agent is discovered and configured
before the delegation runs. The caller then provides the specific task.

## Availability

Named agent definitions are available to:

- `mecated` and `mecak8s` deployments;
- mecatui's embedded server;
- the `Subagent` tool; and
- agent-team member roles.

`mecatequi` does not expose an agent-source configuration path, so its shipped
one-shot command cannot select a named agent definition.

## Define an agent

Create a Markdown file named `<name>.md` with YAML frontmatter. `name` and
`description` are required; the Markdown body becomes the specialist's
instructions.

For example, create `reviewer.md`:

```md
---
name: reviewer
description: Review a change for correctness and missing tests.
tools:
  - Read
  - Grep
  - Glob
model: inherit
permissionMode: plan
maxTurns: 8
---

Review the requested change against the repository's conventions. Identify
concrete correctness risks and missing tests. Do not modify files.
```

The most useful fields are:

| Field | Purpose |
| --- | --- |
| `name` | Name passed to `Subagent(agent=...)` or a team member's `AgentType`. |
| `description` | Short routing summary that is always available when choosing the specialist. |
| `tools` | Allowlist of core tools for the specialist. |
| `disallowedTools` | Removes tools after the allowlist/default set is applied. |
| `model` | Model alias or ID; empty or `inherit` keeps the parent model. |
| `provider` | Provider ID; empty inherits the session provider. |
| `permissionMode` | Specialist mode such as `default`, `plan`, or `acceptEdits`. |
| `maxTurns` / `maxToolCalls` | Per-run limits for this specialist. |
| `skills` | Skills to preload into its instructions. |
| `mcpServers` | Configured server references or inline streamable-HTTP servers. |
| `memory` | Optional read-only `user` or `project` memory tier. |
| `hooks` | Per-definition lifecycle hook commands. |
| `color` | Display hint only; it does not affect execution. |

A definition's body is instructions to the specialist. Keep it focused on the
role, expected output, and boundaries. Do not put credentials in frontmatter,
headers, or the body.

Definitions are bounded: descriptions are capped at 2000 bytes and bodies at
32 KiB. Unknown frontmatter keys are ignored for forward compatibility.

## Choose where definitions are discovered

By default, conventional discovery checks these locations in descending
precedence:

1. directories passed with `--agents-dir`;
2. `<workspace>/.mecatl/agents`;
3. `<workspace>/.claude/agents`;
4. `$XDG_CONFIG_HOME/mecatl/agents` (usually `~/.config/mecatl/agents`); and
5. `~/.claude/agents`.

Earlier sources win when two definitions have the same name. Use
`--agents-dir DIR` for an explicit operator-managed directory; repeat the flag
for multiple directories. Disable conventional discovery with
`--agents-conventional=false` when required.

A remote source can replace local discovery:

```sh
mecated serve --agent-source-url agents.example.internal:8443
```

The remote agent-definition source is snapshotted when the server starts. It is
mutually exclusive with `--agents-dir`, and an unreachable configured source is
a startup failure.

## Trust and execution boundaries

Project definitions under the workspace are project-provided instructions. They
are admitted only when the workspace has project trust. User-global and
explicit operator-managed definitions are not subject to the project trust
switch.

Treat an agent source as part of the harness trust boundary:

- the definition body steers the specialist like project instructions;
- a definition can select tools, a model, provider, skills, and MCP servers;
- per-definition hooks execute on the harness host without an ordinary tool
  permission prompt; and
- an inline MCP definition may use streamable HTTP, but stdio and command-based
  MCP entries are rejected.

A definition's `memory: project` is also project-trust-gated and is read-only in
this version. MCP headers are secret-shaped and are not displayed in agent
inventories or snapshots.

## Run a named agent

### Delegate from the model

The main model invokes the `Subagent` tool with the definition name:

```json
{
  "agent": "reviewer",
  "prompt": "Review the current change and report the highest-risk issue first."
}
```

The specialist receives its definition instructions plus this task. The
`agentId` in the result identifies the child session for inspection or a later
resume when child persistence is configured. A named specialist is still a
child run: its workspace, shell, limits, and mutability follow the selected
mode and deployment posture.

A per-call `model` override can rebuild a named read-only specialist on another model when
the deployment supports the agent model factory. A named `agent` and `model`
combination is a scoped specialist override, not a change to the parent's
session model.

For a `mode: "read-write"` named specialist, omit the call's `model`. If the definition also
omits its frontmatter `model:`, an enabled semantic router may choose the model while retaining
the specialist's scoped tools and instructions and its direct-write access to the parent
workspace. Set `model: inherit` (or another definition model) to pin it and bypass routing.
An unavailable routed target falls back to the specialist's ordinary resolved model; a
definition that switches provider or uses inline MCP is not eligible for this routed writable
path. Explicit `read-write` + `agent` + `model` remains invalid.

### Use in a team

A team member can select the definition by its `AgentType`. This reuses the
profile without copying the Markdown body. The team supplies the member's role
briefing and task; the definition supplies its specialist configuration.

The same definition can be used by a direct `Subagent` delegation and a team
member. Each path keeps its own lifecycle, limits, and mutability rules.

## Named agents versus one-off subagents

| Use a named agent when… | Use a one-off subagent when… |
| --- | --- |
| the role will be reused; | the task is unique; |
| the same tools and instructions should apply repeatedly; | the caller can describe the role completely in one prompt; |
| a team needs a stable specialist type; | no persistent discovery or configuration is needed; |
| the role needs its own model, skills, or hooks. | a fresh read-only exploration is enough. |

A named definition does not make a child automatically writable. Mutability is
selected by the delegation call and supported deployment path, and remains
subject to the server's permission and trust policy.

## Limitations

- Definitions are discovered as a startup snapshot. Changes require rebuilding
  or restarting the server before they are available.
- Project definitions and project memory require project trust.
- Duplicate names resolve by source precedence; the lower-precedence definition
  is not merged into the winner.
- An agent definition cannot use stdio MCP. Inline MCP is streamable HTTP only.
- Per-definition hooks execute host-side commands; only use definitions
  from sources you trust.
- A named specialist's model/provider selection does not change the parent
  session's provider or model.
- `memory: user` and `memory: project` are read-only; named agents do not get a
  general memory-write path from this feature.

## Next steps

- [Agent definitions extension point](/building/extension-points/agent-definitions.md)
  for the complete field and source contract.
- [Subagents, teams and parallel](/building/what-you-get/subagents-teams-parallel.md)
  for delegation behavior and child lifecycle.
- [Project instructions and rules](./project-instructions-and-rules.md) for
  workspace trust and other project-provided steering.
