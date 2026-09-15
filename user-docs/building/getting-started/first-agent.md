---
sidebar_position: 0
title: Build your first agent
description: Install the importable Mecatl engine and build a working Go agent.
---

# Build your first agent

Build a small Go application that embeds the Mecatl engine, runs one prompt
against an offline model provider, and prints the result. The finished example
needs no API key or network connection.

## Prerequisites

You need Go 1.27 or newer.

## Create a Go module

Create a project outside the Mecatl repository and add the engine dependency:

```sh
mkdir first-agent
cd first-agent
go mod init example.com/first-agent
go get github.com/stacklok/mecatl/engine@latest
```

## Run an offline agent

Create `main.go` with the following code:

```go title="main.go"
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
    ref := session.EnvironmentRef{
        Kind: session.EnvKindMem,
        ID: "/workspace",
        Revision: "example-v1",
    }
    workspace := memfs.NewWorkspace("/workspace")
    environment := tool.MustEnvironment(ref, workspace, memledger.New(), nil)
    engine := agent.NewEngine(agent.Deps{
        LLM: mockllm.New(
            mockllm.TextTurn("Hello from your first agent."),
        ),
        Catalog: tool.NewCatalog(),
        Policy: permpolicy.NewPolicy(nil, permstore.New()),
        Model: "mock",
    })
    sess := session.New(
        "first-agent",
        session.ModeDefault,
        ref,
        session.Limits{},
        time.Now(),
    )

    run := engine.Run(
        context.Background(),
        sess,
        environment,
        agent.RunRequest{Text: "Say hello."},
    )
    for event := range run.Events() {
        if event.Type == session.EvResult && event.Result != nil {
            fmt.Println(event.Result.Text)
        }
    }
}
```

Run the application:

```sh
go run .
```

It prints:

```text
Hello from your first agent.
```

The example creates an in-memory workspace and binds it to the session through
`tool.Environment`. It passes no command runner because the empty tool catalog
contains no shell tool. The `mockllm` adapter returns a fixed response, which
makes this first run deterministic.

## Add tools and approvals

Register a custom tool on the `tool.Catalog` passed to `agent.Deps`. Each tool
receives a `session.ToolCall` and the session's `tool.Environment`, then returns
a model-visible `session.ToolResult`.

The
[custom tool example](https://github.com/stacklok/mecatl/blob/main/examples/first-agent-tool/main.go)
shows a `Ping` tool that the permission policy allows. The
[approval example](https://github.com/stacklok/mecatl/blob/main/examples/first-agent-approval/main.go)
changes that policy to `Ask` and resolves the request from the host:

```go
if event.Type == session.EvPermissionAsk && event.Ask != nil {
    run.Approve(event.Ask.AskID, session.VerdictAllowOnce)
}
```

`AllowOnce` applies only to the pending call. `AllowAlways` adds a matching rule
for the session, and `Deny` returns an error result to the model without running
the tool. A headless host must provide a policy or another way to resolve
approval requests.

## Connect a real model

The
[OpenRouter example](https://github.com/stacklok/mecatl/blob/main/examples/first-agent-openrouter/main.go)
replaces `mockllm` with the OpenAI Responses-compatible provider module. Copy
that example to `main.go`, then add the provider dependency and run it:

```sh
go get github.com/stacklok/mecatl/provider/openai@latest
export OPENROUTER_API_KEY='<OPENROUTER_API_KEY>'
go run .
```

The example uses `openai/gpt-5.6-luna` by default. Set `OPENROUTER_MODEL` to use
another OpenRouter model ID. This request may incur provider charges. Store the
API key in a secret manager, and keep it out of source code and command
arguments.

The engine supplies the agent loop but leaves storage, authentication,
transport, retry policy, and observability to the embedding application.

Pin the engine and provider module versions together, and review
[API stability](/building/api-stability.md) before upgrading.

## Next steps

- [Explore the engine and session model](/building/what-you-get/engine-and-session.md)
  to understand `Engine`, `Session`, `Run`, and `Environment`.
- [Implement the tool catalog](/building/extension-points/tool-catalog.md) to
  register local tools or connect MCP tools.
- [Embed the engine directly](/building/deployment/embed-engine.md) to plan a
  production host around the engine.

## Related information

- [PermissionPolicy](/building/extension-points/permission-policy.md)
- [API stability](/building/api-stability.md)
- [First-agent examples](https://github.com/stacklok/mecatl/tree/main/examples)
