---
title: Build with the Go engine
description: Progress from a first Go agent to a production embedding and custom adapters.
sidebar_position: 1
---

# Build with the Go engine

Import `github.com/stacklok/mecatl/engine` to run the agent loop inside your Go
application. You own the process, lifecycle, model adapter, tools, and persistence.
The engine is a separate module whose imports do not bring in provider SDKs,
gRPC, the terminal UI, or Kubernetes clients.

Start with [Build your first agent](first-agent.md), which runs offline in a
small Go application. Then follow the track:

1. [Engine, session, run, and environment](engine-and-session.md) explains the
   reusable engine, persisted conversation, live run handle, and execution context.
2. [Embed the engine directly](embed-engine.md) covers production lifecycle,
   persistence, model dependencies, and host responsibilities.
3. [Extension points](extension-points/index.md) shows how to supply custom
   adapters through Go interfaces.
4. [API stability](api-stability.md) explains the compatibility promise and
   versioning you should review before an upgrade.

Use [pkg.go.dev](https://pkg.go.dev/github.com/stacklok/mecatl/engine) to look up
exact Go symbols. The optional [offline demo](demo.md) shows tools, approvals,
teams, and subagents from a repository checkout after you have tried the tutorial.
