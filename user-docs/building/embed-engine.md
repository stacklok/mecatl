---
sidebar_position: 10
title: Embed the engine directly
description:
  Embed the Mecatl engine in your Go service and wire its adapters in process.
---

# Embed the engine directly

Embed Mecatl when you want the agent loop to run inside your Go service. You own
the binary, lifecycle, adapters, and transport, with no separate daemon or gRPC
connection.

For the shortest working example, start with
[Build your first agent](/building/getting-started/first-agent.md). This guide
covers the additional choices an embedding application owns.

## When to choose this

Choose in-process embedding when:

- You want the agent loop as a library component in a service, worker, or IDE
  backend.
- You want to select provider SDKs and other dependencies explicitly.
- You already have a runtime (HTTP server, worker loop, queue consumer) and want
  the agent to live inside it.
- You want to supply a custom `port.LLMProvider`, such as an internal model
  proxy, router, or test double.

Choose a prebuilt binary when you want Mecatl to assemble authentication, TLS,
metrics, configuration flags, and transports.

## Dependency footprint

The engine is the separate Go module
`github.com/stacklok/mecatl/engine`. Its runtime dependencies cover concurrency,
glob matching, YAML parsing, HTML parsing, shell parsing, and cron expressions.
It does not pull in provider SDKs, gRPC, the terminal UI, Kubernetes clients, or
Mecatl's root module.

## Engine, session, and run

An embedding application works with three core objects:

|Object|Lifetime|Responsibility|
|-|-|-|
|`*agent.Engine`|Reuse across compatible sessions|Runs the loop with a provider, tool catalog, permission policy, and hooks.|
|`*session.Session`|Persist across runs|Holds conversation history, state, limits, usage, and the execution-environment identity.|
|`*agent.Run`|One active run|Streams ordered events, accepts permission verdicts, and supports cancellation.|

`Engine.Run` starts the loop in the background. Consume `Run.Events()` until
the channel closes, and answer `session.EvPermissionAsk` events with
`Run.Approve`. A restored session can continue with any compatible engine.

The engine and session remain independent. Subagents and team members use the
same engine and session model with narrower tools, permissions, and limits.

## Adding the dependency

```sh
go get github.com/stacklok/mecatl/engine@latest
```

The engine module is self-contained. Add an opt-in provider module separately
if you want one of Mecatl's provider implementations.

## Minimum wiring

Configure the engine through `agent.Deps`. A useful run needs an LLM provider,
a tool catalog, a permission policy, and a model identifier. Hooks,
persistence, timing, and prompt helpers are optional.

The following example uses in-memory reference adapters:

```go
package main

import (
    "context"
    "fmt"
    "time"

    "github.com/stacklok/mecatl/engine/adapter/memfs"
    "github.com/stacklok/mecatl/engine/adapter/memledger"
    "github.com/stacklok/mecatl/engine/adapter/memstore"
    "github.com/stacklok/mecatl/engine/adapter/mockllm"
    "github.com/stacklok/mecatl/engine/adapter/permpolicy"
    "github.com/stacklok/mecatl/engine/adapter/permstore"
    "github.com/stacklok/mecatl/engine/agent"
    "github.com/stacklok/mecatl/engine/governance"
    "github.com/stacklok/mecatl/engine/prompt"
    "github.com/stacklok/mecatl/engine/session"
    "github.com/stacklok/mecatl/engine/tool"
)

func main() {
    ctx := context.Background()

    // Register only the tools the model needs.
    cat := tool.NewCatalog()
    // cat.MustRegister(myTool)

    // Decide whether each tool call runs, asks for approval, or is denied.
    policy := permpolicy.NewPolicy([]governance.Rule{
        {Scope: governance.ScopeManaged, Tool: "Read",  Effect: governance.Allow},
        {Scope: governance.ScopeManaged, Tool: "Write", Effect: governance.Ask},
    }, permstore.New())

    // Assemble the engine.
    eng := agent.NewEngine(agent.Deps{
        LLM:     mockllm.New(mockllm.TextTurn("Hello, world.")), // replace with your provider
        Catalog: cat,
        Policy:  policy,
        // Nil hooks use the no-op behavior.
        Store:   memstore.New(),
        PromptConfig: prompt.Config{
            Env: prompt.Env{
                Cwd:   "/workspace",
                OS:    "linux",
                Model: "my-model",
                Date:  time.Now().Format("2006-01-02"),
                Mode:  string(session.ModeDefault),
            },
        },
        Model: "my-model",
    })

    // Use the same environment identity for the session and tool environment.
    ref := session.EnvironmentRef{
        Kind: session.EnvKindMem, ID: "/workspace", Revision: "example-v1",
    }
    sess := session.New(
        "my-session-id",
        session.ModeDefault,
        ref,
        session.Limits{MaxTurns: 20, MaxToolCalls: 50, MaxConsecutiveFailures: 3},
        time.Now(),
    )
    ws := memfs.NewWorkspace("/workspace")

    // Start the run and consume every event.
    env := tool.MustEnvironment(ref, ws, memledger.New(), nil)
    run := eng.Run(ctx, sess, env, agent.RunRequest{Text: "Summarize the project."})
    for ev := range run.Events() {
        fmt.Printf("%s %v\n", ev.Type, ev.Text)

        // Surface permission asks to your UI in a production application.
        if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
            run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
        }
    }
}
```

`eng.Run` returns immediately while the loop runs in the background. Consume
`run.Events()` until the channel closes. See
[The agent loop](/features/agent-loop.md) for the full event
taxonomy and permission flow.

## Ports and configuration

The following fields are the main integration points in `agent.Deps`:

|Field|Type|Required?|Reference adapter|Notes|
|-|-|-|-|-|
|`LLM`|`port.LLMProvider`|Yes|`engine/adapter/mockllm` for tests; bring your own for production|Implement `Stream` and `Capabilities`. See `engine/port/llm.go`.|
|`Catalog`|`*tool.Catalog`|Yes|`tool.NewCatalog()` and `cat.MustRegister(...)`|Register only the tools your agent should use.|
|`Policy`|`port.PermissionPolicy`|Yes|`engine/adapter/permpolicy` and `engine/adapter/permstore`|`permpolicy.NewPolicy(rules, permstore.New())` is the standard wiring.|
|`Hooks`|`port.HookRunner`|No|None|Nil hooks use the no-op behavior.|
|`Store`|`port.SessionStore`|No|`engine/adapter/memstore`|Nil disables persistence. Use `memstore.New()` for in-process persistence.|
|`Clock`|`port.Clock`|No|`engine/adapter/wallclock`|Nil disables tool-call timing.|
|`Model`|`string`|Yes|None|Sent on every `LLMRequest`. Must match your provider's model identifier.|
|`PromptConfig`|`prompt.Config`|No|None|Seeds the stable system prompt prefix. Most embeddings set `Env.Cwd`, `Env.Model`, `Env.Date`, and `Env.Mode`.|

Optional fields with non-trivial defaults:

|Field|Default behaviour|
|-|-|
|`Compactor`|`HeuristicCompactor`, which trims the conversation at the context-window threshold.|
|`TokenCounter`|`HeuristicTokenCounter`, a character-based estimate.|
|`ContextWindow`|Nil, which disables compaction. Return the model's token window to enable it.|
|`MaxNoProgressNudges`|`2`. The loop sends up to two continuation nudges before `StopNoProgress`.|
|`MaxRunTokens`|`0`, which disables the per-engine token limit.|
|`Instructions`|`prompt.RootAssembler`, which looks for `AGENTS.md` or `CLAUDE.md` at the workspace root.|

### Token budgets with delegation

`Deps.MaxRunTokens` applies independently to each engine. The main engine,
subagents, parallel branches, team members, and lead synthesis inherit the
configured value, but each counts only its own session usage. A delegation tree
can therefore exceed the configured value in aggregate.

For teams, `agent.WithTeamTokenBudget` sets a separate aggregate budget. Mecatl
checks it between rounds. Crossing it prevents another round but allows the
current round and lead synthesis to finish. It does not count work outside that
team.

## Optional evidence reflection

The optional `learning` package selects bounded evidence from a run and can
produce evidence-backed reflection proposals. `learning.MaterializeEvidence`
returns canonical evidence and an immutable manifest, or a content-free reason
for abstaining. Persist the manifest with a staged proposal so later review uses
the same evidence rather than rerunning selection.

The package also defines learned-skill lifecycle interfaces for validation,
content-addressed versions, evaluation, review, and activation. Reference
adapters cover in-memory storage and validation. You can replace each interface
with your own implementation.

An embedding application owns proposal persistence, review authorization,
scheduling, and transport. See the
[architecture guide](https://github.com/stacklok/mecatl/blob/main/docs/architecture.md#evidence-backed-reflection)
for the evidence and output-validation contract.

## What you do not get

An embedding application is responsible for the capabilities outside the
engine:

|Capability|Status|
|-|-|
|HTTP / gRPC server|Not included. Wire your own transport and relay events.|
|Authentication|Not included. Add authentication in your application.|
|TLS|Not included.|
|Prometheus metrics|Not included. Wire `port.ToolCallRecorder` and `port.Diagnostics` to your own observability stack.|
|Kubernetes manifests|Not included.|
|CLI flags|Not included. Set the corresponding `Deps` fields in code.|
|Provider adapters|Import `github.com/stacklok/mecatl/provider/openai`, `provider/openaichat`, or `provider/anthropic` as separate modules. Each adds only its provider SDK and the engine.|
|Session store backends (JSONL, Redis)|Not included in the engine module. `memstore` is. For durable or Redis-backed storage, import the root module's adapters.|

If you need several of these capabilities, use `mecated`, which assembles them
for you. See
[Run mecated standalone](/operating/mecated.md).

## go.work for monorepo development

The engine is a separate Go module inside the Mecatl monorepo, connected via
`go.work`. If you develop against a local checkout of Mecatl rather than the
published module, set up a `go.work` in your own repo's parent:

```sh
# Run from the directory that contains your go.mod file.
go work init .
go work use /path/to/mecatl/engine
```

The resulting `go.work` file:

```text
go 1.27

use .
use /path/to/mecatl/engine
```

`go build` and `go test` now resolve the engine from the local checkout. If your
team develops against both repositories, commit `go.work.sum` with `go.work`.

:::note[go.work is local-only]

Use `go.work` for local development. A published module should depend on a
tagged release or use a `replace` directive in `go.mod` during development.

:::

## Next steps

- [Understand the agent loop](/features/agent-loop.md) and its event
  lifecycle.
- [Configure permissions and posture](/features/permissions-and-posture.md).
- [Review API stability](/building/api-stability.md) before depending on the
  exported engine surface.
