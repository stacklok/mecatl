---
sidebar_position: 7
title: Tool catalog
description: Add custom tools and instruction bundles to the Mecatl agent loop.
---

# Tool catalog

`tool.Catalog` contains the tools the model can call. Build a catalog, register
your tools, and pass it to the agent engine.

## Register tools

```go
catalog := tool.NewCatalog()
catalog.MustRegister(readTool)

if err := catalog.Register(optionalTool); err != nil {
    if errors.Is(err, tool.ErrDuplicateTool) {
        // Choose which tool owns the name.
    }
    return err
}
```

`Register` returns `ErrDuplicateTool` when a name is already present.
`MustRegister` panics, which is useful when duplicate static registration is a
programming error.

Catalog queries return deterministic, name-sorted results:

|Method|Result|
|-|-|
|`Lookup(name)`|The tool registered under a name|
|`Tools()`|All registered tools|
|`Available(mode)`|Tools available in a permission mode|
|`Specs(mode)`|Full specifications for available tools|
|`AdvertisedSpecs(mode)`|Specifications after progressive disclosure|

Plan mode excludes mutating tools. A tool that implements `tool.PlanOnly` is
available only in plan mode.

## Implement a tool

Every tool implements three methods:

```go
type Tool interface {
    Spec() ToolSpec
    ReadOnly() bool
    Execute(
        ctx context.Context,
        call session.ToolCall,
        env Environment,
    ) (session.ToolResult, error)
}
```

`Spec` provides the name, model-facing description, and JSON Schema for
arguments. Explain when to use the tool, identify its important limits, and keep
the schema as narrow as the implementation.

`ReadOnly` controls dispatch concurrency. Mecatl runs adjacent read-only calls
concurrently and runs mutating calls one at a time. Return `false` if the tool
changes the workspace or other shared state.

`Execute` receives the model's call and the session environment. Return a
`ToolResult` with `IsError: true` for a failure the model can address. Reserve
the Go error for a harness failure that should stop the run.

### Add a custom tool

```go
package pingtool

import (
    "context"
    "encoding/json"

    "github.com/stacklok/mecatl/engine/session"
    "github.com/stacklok/mecatl/engine/tool"
)

type Tool struct{}

func (Tool) Spec() tool.ToolSpec {
    return tool.ToolSpec{
        Name:        "Ping",
        Description: "Return pong to verify that tool dispatch is working.",
        Schema: json.RawMessage(
            `{"type":"object","additionalProperties":false}`,
        ),
    }
}

func (Tool) ReadOnly() bool {
    return true
}

func (Tool) Execute(
    _ context.Context,
    call session.ToolCall,
    _ tool.Environment,
) (session.ToolResult, error) {
    return session.NewToolResult(call.ID, "pong"), nil
}
```

Register the tool before constructing the engine:

```go
catalog := tool.NewCatalog()
catalog.MustRegister(pingtool.Tool{})

engine := agent.NewEngine(agent.Deps{
    Catalog: catalog,
    // Supply the remaining dependencies.
})
```

Use `session.NewToolError` for a plain-text model-visible failure.
`session.NewToolResultWithParts` returns typed content blocks when the tool
needs more than text.

### Advertise a lightweight specification

Implement `tool.Disclosable` when a full tool schema is expensive to include on
every turn:

```go
type Disclosable interface {
    Tool
    Advertised() ToolSpec
}
```

`Advertised` returns a short description and minimal schema. `Spec` still
returns the complete definition for `ToolSearch`. Tools without this interface
always advertise their full specification.

## Add MCP tools

Mecatl registers tools from connected MCP servers as `mcp__<SERVER>__<TOOL>`.
Remote tools are treated as mutating unless the MCP server sets its
`readOnlyHint`.

Core and previously registered tools keep their names when an MCP tool collides.
The conflicting MCP tool is skipped and a diagnostic identifies the name.

The shipped applications can connect global MCP servers at startup and accept
session-scoped MCP servers from clients. Session-scoped connections close with
the session; global connections close with the application.

For connection options, authentication, resources, prompts, and failure
behavior, see [MCP client](/building/what-you-get/mcp-client.md).

## Provide skills

Skills are instruction bundles. They do not add executable tools or grant
permissions.

`tool.SkillSource` provides skill metadata, instructions, and optional assets:

```go
type SkillSource interface {
    ListSkills(
        ctx context.Context,
    ) ([]SkillMeta, error)

    SkillBody(
        ctx context.Context,
        name string,
    ) (string, error)

    ListSkillAssets(
        ctx context.Context,
        name string,
    ) ([]SkillAsset, error)

    ReadSkillAsset(
        ctx context.Context,
        skill string,
        asset string,
    ) ([]byte, error)
}
```

`ListSkills` returns a name-sorted, unique snapshot. Metadata stays available
for routing, while Mecatl loads the body only when the model activates a skill.
Return `ErrSkillNotFound` for an unknown skill and `ErrSkillAssetNotFound` for
an unknown asset.

Assets use logical slash-separated names such as `references/api.md`. Validate
them with `tool.ValidSkillAssetName`. The Skill tool reads one bounded textual
asset on demand. It rejects invalid UTF-8 and NUL bytes. It does not write
assets to the workspace or make them available to Shell.

`engine/adapter/skillfs` implements `SkillSource` for `SKILL.md` bundles.
Project-tier sources are included only for trusted workspaces. Filesystem paths
remain private to the adapter.

A skill is also available as a slash command. Local command files take
precedence over a skill with the same name, and skills take precedence over
driver commands and MCP prompts. See
[Skills, commands, and soul](/features/skills-commands-and-soul.md) for file
format and discovery behavior.

Validate a custom source with `engine/adapter/skillconformance`.

## Share catalog state safely

A tool registered in the application catalog is shared by every session. Keep
the tool stateless or make its dependencies safe for concurrent sessions.
Session-specific workspace and command execution are available through the
`tool.Environment` passed to `Execute`.

If a tool owns a session-scoped connection or credential, construct that tool in
your session engine factory and close its resources when the session ends. Do
not close application-wide resources from a session cleanup function.

## Record tool calls

`port.ToolCallRecorder` records tool arguments, results, queue time, and
execution time independently of the live event stream:

```go
type ToolCallRecorder interface {
    ToolCall(
        id session.SessionID,
        call session.ToolCall,
        result session.ToolResult,
        queued time.Duration,
        took time.Duration,
    )
}
```

`queued` measures time spent waiting in dispatch before execution. `took`
measures execution. Both are zero when the engine has no `Clock`.

The JSONL store writes tool-call records to a separate audit sidecar. Implement
this port to send structured tool audit data to another system.

## Next steps

- [Understand tool dispatch](/building/what-you-get/agent-loop.md).
- [Configure MCP servers](/building/what-you-get/mcp-client.md).
- [Implement lifecycle hooks](hook-runner.md).
