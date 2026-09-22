---
sidebar_position: 8
title: Agent definitions
description:
  Define named specialists or supply them from a custom AgentDefSource.
---

# Agent definitions

Agent definitions configure named specialists with their own instructions,
tools, model, limits, hooks, MCP servers, and memory. The Subagent tool and
agent teams use the same definitions.

To configure and use named specialists, see
[Named agents](/features/agent-behavior/named-agents.md). This page covers the source interface
and custom integrations.

Implement `tool.AgentDefSource` to load definitions from a database, registry,
or another backend. Use `engine/adapter/agentfs` to load Markdown files.

## The AgentDef type

`tool.AgentDef` contains logical configuration rather than storage locations:

|Field|Purpose|
|-|-|
|`Name`|Stable name used by Subagent and team members|
|`Description`|One-line routing summary|
|`Body`|Specialist instructions|
|`Tools`|Optional tool allowlist|
|`DisallowedTools`|Tool names removed from the selected set|
|`Model`|Model alias, full ID, `inherit`, or empty|
|`Provider`|Provider ID, or empty to inherit|
|`PermissionMode`|Optional `default`, `plan`, or `acceptEdits` mode|
|`MaxTurns`|Per-run turn limit; zero uses the caller default|
|`MaxToolCalls`|Per-run tool-call limit; zero uses the caller default|
|`Skills`|Skill names whose bodies Mecatl preloads|
|`MCPServers`|Referenced or inline MCP servers|
|`Hooks`|Hook phase to shell-command mapping|
|`Memory`|Empty, `user`, or `project` memory tier|
|`Color`|Display hint with no execution effect|
|`Origin`|Admission tier set by the source|

`Description` is limited to `tool.MaxAgentDescriptionBytes` (2,000 bytes).
`Body` is limited to `tool.MaxAgentBodyBytes` (32 KiB). Apply these limits in
every source.

`Origin` is one of `explicit`, `project`, `user`, or `driver`. It describes the
admission tier for trust and diagnostics. It must not contain a path or URL.

## The AgentDefSource interface

```go
type AgentDefSource interface {
    ListAgentDefs(
        ctx context.Context,
    ) ([]AgentDef, error)
}
```

`ListAgentDefs` returns definitions sorted by name with no duplicates. Sources
have snapshot semantics: resolve their contents at construction and return a
stable list for their lifetime.

Validate a source with `engine/adapter/sourceconformance`:

```go
func TestAgentSource(t *testing.T) {
    sourceconformance.RunAgentSource(
        t,
        func(t *testing.T) tool.AgentDefSource {
            return newAgentSource(t)
        },
    )
}
```

The test factory must return a fresh source that serves exactly
`sourceconformance.AgentFixture`. The suite compares every definition field
except the source-specific `Origin` value and also checks stable ordering and
content limits.

## Use definition files

`engine/adapter/agentfs` loads flat Markdown files from one or more directories.
The file contains YAML front matter followed by the specialist's instructions:

```yaml
---
name: code-reviewer
description: Reviews a diff for correctness and test coverage.
tools: [Read, Grep, Glob]
disallowedTools: [Write]
model: sonnet
provider: openrouter
permissionMode: plan
maxTurns: 9
maxToolCalls: 25
skills: [refactoring]
hooks:
  PreToolUse: ./scripts/review-gate.sh
mcpServers:
  - github
  - name: issues
    url: https://issues.example.com/mcp
    headers:
      Authorization: 'Bearer ${TOKEN}'
memory: project
---
Review the requested change. Prioritize correctness, then test coverage.
```

`name` and `description` are required. The other fields are optional. `tools`
and `disallowedTools` accept a YAML list or a comma-separated string.
`mcpServers` accepts comma-separated names or a list of names and inline server
mappings.

An MCP entry containing only a name references a server already configured for
the main application. An inline entry supplies a name, streamable HTTP URL, and
optional headers. Mecatl rejects stdio and other inline transports but keeps the
rest of the definition.

Treat inline headers as secrets. Do not log them or include them in inventory,
diagnostic, or snapshot output.

## Resolve filesystem definitions

`agentfs.ResolveSources` searches these sources in descending precedence:

1. Paths configured with `--agents-dir`
1. `<WORKSPACE>/.mecatl/agents`
1. `<WORKSPACE>/.claude/agents`
1. `$XDG_CONFIG_HOME/mecatl/agents`
1. `~/.claude/agents`

The first definition with a given name wins. Project directories are included
only for a trusted workspace. User directories are always eligible. Missing
directories have no effect, and invalid files are skipped with diagnostics.

The adapter scans once when the source is created. Restart or rebuild the source
to pick up file changes.

A remote implementation can expose `mecatl.driver.v1.AgentSourceService` and
connect through `--agent-source-url`. This disables conventional filesystem
discovery. It cannot be combined with an explicit `--agents-dir`.

## Configure memory

The `memory` field controls a read-only `MEMORY.md` fragment for the specialist:

|Value|Location|
|-|-|
|Empty|No specialist memory|
|`user`|`$XDG_CONFIG_HOME/mecatl/agents-memory/<NAME>/`|
|`project`|`<WORKSPACE>/.mecatl/agents-memory/<NAME>/`|

Project memory is available only for trusted workspaces. Mecatl sanitizes the
definition name and confines resolved paths to the selected memory directory. It
injects a bounded memory fragment as untrusted data. The specialist cannot write
this memory through the memory setting.

## Invoke a named specialist

The model supplies the definition name to Subagent:

```text
Subagent(
  agent="code-reviewer",
  task="Review the diff in HEAD."
)
```

Named subagents are read-only by default. When `Shell` is available and the
workspace is trusted, the child inspects an isolated worktree. Read-only runs
remove mutating tools, including `Edit` and `Write`, even when the definition
names them.

A caller can add a model override for a read-only run. Definitions with inline
MCP servers do not support this combination because the temporary engine has no
owner for the inline connection.

```text
Subagent(
  agent="code-reviewer",
  model="opus",
  task="Review the diff in HEAD."
)
```

Use `mode="read-write"` to run a supported named specialist against the parent
workspace. Its tool allowlist still applies, and its calls use the main
session's permission policy. A writable specialist cannot also use a per-call
model override or inline MCP server.

Writable specialists edit the parent workspace directly. A canceled or failed
run can leave partial changes, so review the working tree after the call.

An unknown definition name returns the available names so the model can retry.

## Implement a custom source

When implementing `AgentDefSource`:

1. Validate required fields and normalize supported enum values.
1. Apply the description and body byte limits.
1. Stamp each definition with its admission tier.
1. Return a name-sorted, deduplicated snapshot.
1. Keep storage locators and secret headers out of diagnostics.
1. Run the source conformance suite.

Apply the project trust decision before constructing a source that can return
project-controlled definitions. A source's `Origin` label is observability data;
it does not enforce trust by itself.

## Next steps

- [Use subagents and teams](/features/agent-behavior/subagents-and-teams.md).
- [Provide skills through SkillSource](tool-catalog.md#provide-skills).
- [Configure project trust](/features/security-and-execution/permissions-and-posture.md).
