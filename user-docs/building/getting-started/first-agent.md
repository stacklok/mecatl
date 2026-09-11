---
sidebar_position: 0
title: Build your first agent
description: Install the importable Mecatl engine and build a working Go agent.
---

# Build your first agent

This is the shortest path from a clean Go module to a working Mecatl agent. You
will create an `agent.Engine`, give it a session-scoped `tool.Environment`, run
one prompt, and consume the resulting events.

The engine is an importable Go module. It does not start a server or choose your
provider, workspace, persistence, authentication, or observability for you. Your
application supplies those pieces through `agent.Deps` and the engine ports.

## What this path covers

1. Run a deterministic offline agent with the reference `mockllm` provider.
2. Add a custom tool through `tool.Catalog`.
3. Require approval through `PermissionPolicy` and resolve it from the host.
4. Choose the next extension point, including a real provider or child
   delegation.

The first example is deliberately offline and requires no API key. The richer
[`mecademo` walkthrough](/building/getting-started/demo.md) remains useful when
you want to see tools, approval, teams, and background Subagents together.

## Before you start

You need Go 1.26.6 or newer. The first-agent example is designed to run from a
clean external module and imports only the public `github.com/stacklok/mecatl/engine`
module and its reference adapters.

## First agent: offline and deterministic

Create a clean Go module, add the engine dependency, and save this as `main.go`:

```go
package main

import (
    "context"
    "fmt"
    "time"

    "github.com/stacklok/mecatl/engine/adapter/memfs"
    "github.com/stacklok/mecatl/engine/adapter/memledger"
    "github.com/stacklok/mecatl/engine/adapter/mockllm"
    "github.com/stacklok/mecatl/engine/adapter/permpolicy"
    "github.com/stacklok/mecatl/engine/adapter/permstore"
    "github.com/stacklok/mecatl/engine/agent"
    "github.com/stacklok/mecatl/engine/session"
    "github.com/stacklok/mecatl/engine/tool"
)

func main() {
    ws := memfs.NewWorkspace("/workspace")
    env := tool.MustEnvironment(
        session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "example-v1"},
        ws,
        memledger.New(),
        nil,
    )
    eng := agent.NewEngine(agent.Deps{
        LLM: mockllm.New(mockllm.TextTurn("Hello from your first agent.")),
        Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, permstore.New()), Model: "mock",
    })
    sess := session.New(
        "first-agent",
        session.ModeDefault,
        session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "example-v1"},
        session.Limits{},
        time.Now(),
    )
    run := eng.Run(context.Background(), sess, env, agent.RunRequest{Text: "Say hello."})
    for event := range run.Events() {
        if event.Type == session.EvResult && event.Result != nil {
            fmt.Println(event.Result.Text)
        }
    }
}
```

The same source is available at [`examples/first-agent/main.go`](https://github.com/stacklok/mecatl/blob/main/examples/first-agent/main.go).
Run it from a clean module rather than from the Mecatl checkout:

```sh
mkdir first-agent && cd first-agent
go mod init example.com/first-agent
go get github.com/stacklok/mecatl/engine@latest
go run .
```

It prints:

```text
Hello from your first agent.
```

The example uses the offline `mockllm` adapter, so it needs no API key or network.
The `tool.Environment` binds the workspace and optional command runner that tools
would use; this example passes no runner because it has no shell tool.

## First tool: register a custom tool

A tool is a small implementation of `tool.Tool` registered on the catalog passed
to `agent.Deps`. The [first-agent-tool example](https://github.com/stacklok/mecatl/blob/main/examples/first-agent-tool/main.go)
uses a scripted mock turn to call a `Ping` tool and prints:

```text
tool.call
tool.result
result
```

The tool receives the model's `session.ToolCall` and the session-scoped
`tool.Environment`, then returns a model-visible `session.ToolResult`. Its
`ReadOnly` value tells the dispatcher whether it may run alongside other
read-only calls. The example allows `Ping` in its permission policy; the next
step changes that rule to require approval.

For the complete interface, catalog registration rules, MCP integration, and
progressive disclosure, see [Tool catalog](/building/extension-points/tool-catalog.md).

## Approval: let the host decide

A tool can require approval by returning `Ask` from the permission policy. The
[approval example](https://github.com/stacklok/mecatl/blob/main/examples/first-agent-approval/main.go)
uses the same `Ping` shape, but the policy asks before executing it. The host
consumes the event stream and resolves the ask:

```go
if event.Type == session.EvPermissionAsk && event.Ask != nil {
    run.Approve(event.Ask.AskID, session.VerdictAllowOnce)
}
```

It produces this sequence:

```text
tool.call
permission.ask
approval
tool.result
result
```

`AllowOnce` executes only this call. `AllowAlways` learns a session-scoped rule
for the matching call. `Deny` skips execution and sends a model-visible error
result back to the loop. A headless host must supply its own policy or verdict
strategy; an approval request is not an automatic grant.

For rule evaluation, scopes, and deny-dominant behavior, see [PermissionPolicy](/building/extension-points/permission-policy.md)
and [Permissions and posture](/features/permissions-and-posture.md).

## Real provider: OpenRouter

Once the offline path works, replace `mockllm` with the public OpenAI-compatible
provider module configured for OpenRouter:

```sh
go get github.com/stacklok/mecatl/provider/openai@latest
export OPENROUTER_API_KEY='your-key-from-a-secret-manager'
go run .
```

Copy the [OpenRouter example](https://github.com/stacklok/mecatl/blob/main/examples/first-agent-openrouter/main.go)
into the clean module first. It reads `OPENROUTER_API_KEY` from the environment and defaults to
`openai/gpt-5.6-luna`. Set `OPENROUTER_MODEL` to another OpenRouter model ID when
needed. This path makes a real network request and may incur provider charges;
never put the key in source, command arguments, or documentation.

The engine remains provider-neutral. The public `provider/openai` adapter supplies
the OpenAI Responses-compatible wire implementation and OpenRouter base URL;
retry/watchdog policy, persistence, authentication, transport, and observability
remain host responsibilities for a direct embedder.

## Compatibility for engine consumers

The stable contract is the exported API of the eight core engine packages. Read
[API stability](../api-stability.md) before upgrading or implementing an adapter.

- `engine/COMPATIBILITY.md` defines the compatibility policy.
- `engine/CHANGELOG.md` records intentional API additions and breaks.
- `engine/api/*.txt` contains the committed public-surface snapshots checked by
  `task api:check`.
- `engine/adapter/*` reference adapters are useful, but are not stable API; rely
  on the port interfaces in `engine/port` and `engine/tool` instead.

The engine is versioned independently from the host repository. A direct embedder
should pin the engine and provider module versions together and run the standalone
engine checks when upgrading.

## Where to go next

| You want to… | Read next |
| --- | --- |
| Understand `Engine`, `Session`, `Run`, and `Environment` | [Engine and session model](../what-you-get/engine-and-session.md) |
| Add a tool or inspect the catalog | [Tool catalog](../extension-points/tool-catalog.md) |
| Configure approval and denial rules | [PermissionPolicy](../extension-points/permission-policy.md) and [Permissions and posture](/features/permissions-and-posture.md) |
| Add lifecycle hooks | [HookRunner](../extension-points/hook-runner.md) and [Hook system](../what-you-get/hooks.md) |
| Persist sessions and event logs | [SessionStore and EventLog](../extension-points/session-store.md) |
| Use another model provider | [LLMProvider](../extension-points/llm-provider.md) |
| Run delegated child work | [Subagents, teams, and parallel](../what-you-get/subagents-teams-parallel.md) |
| Serve clients over gRPC or HTTP/SSE | [Drive via gRPC / HTTP](../deployment/grpc-http.md) |
| Understand the complete embedding boundary | [Embed the engine directly](../deployment/embed-engine.md) |
