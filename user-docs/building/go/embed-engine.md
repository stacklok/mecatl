---
slug: /building/embed-engine
sidebar_position: 4
title: Embed the engine directly
description: Supply lifecycle, persistence, and adapters for an in-process Go agent.
---

# Embed the engine directly

Embed Mecatl when the agent loop should run inside your Go service. Build on
[the first-agent tutorial](first-agent.md), then replace its reference adapters
with the model provider, tools, and durable storage your application needs.
The [object model](engine-and-session.md) explains the lifetimes of the objects
used below.

## Assemble your dependencies

Install the engine and the provider module your service uses:

```sh
go get github.com/stacklok/mecatl/engine@latest
```

Pin compatible engine and provider versions in your application's `go.mod`.
Provider modules are opt-in dependencies; the engine itself imports no provider
SDKs. Use [API stability](api-stability.md) and the provider release notes when
upgrading.

Construct `agent.Deps` at your application's composition root. Register only
the tools required by the task and choose an explicit permission policy.
The tutorial's `mockllm`, `memfs`, and `memstore` adapters are useful for offline
tests; production adapters implement the same
[extension interfaces](extension-points/index.md).

## Own the run lifecycle

Keep the request context alive while the run executes, and consume every event.
Forward permission asks to an authenticated approval client or supply a policy
that resolves unattended work. `Run.Approve` handles ordinary tool permission
asks; plan and guardrail approvals have dedicated resolution contracts.

On shutdown, cancel active runs and continue draining their event streams.
Wait for run completion before closing storage or other adapters used by the
loop. A closed stream can indicate external authorization parking, so inspect
`Run.Outcome()` and persisted session state before reporting a terminal result.
Your application owns live event delivery and reconnect behavior.

## Persist sessions and reattach environments

Set `Deps.Store` to a durable `port.SessionStore` when work must survive process
restart. `memstore` persists only for the life of its process. Load the saved
session and reattach the same exact environment identity before starting another
run; use aggregate lifecycle methods to reopen terminal sessions or resume
pending approvals.

[SessionStore and EventLog](extension-points/session-store.md) covers supplied
JSONL and Redis adapters, lifecycle ownership, and custom persistence. A durable
event log supports replay for clients; session snapshots retain provider-private
fields needed for faithful model replay.

For more than one process, coordinate session writers through a
[session lease](extension-points/session-lease.md). Your application also owns
authentication, verified caller identity, and environment selection. The
[cloud-native harness explanation](/cloud-native-harness.md) describes how these
responsibilities can be separated when you later introduce a service boundary.

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

## Optional reflection integration

An embedding can use the `learning` package to materialize bounded evidence and
stage reflection proposals. Your host owns persistence, review authorization,
scheduling, and transport. See [reflection integration](extension-points/index.md#reflection-integration)
for the interfaces and evidence contract.

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
|Session store backends (JSONL, Redis)|Not included in the engine module. `memstore` is. Import `github.com/stacklok/mecatl/adapters/jsonlstore` or `github.com/stacklok/mecatl/adapters/redisstore` from the separate published adapters module; see [supplied storage backends](extension-points/session-store.md#use-a-supplied-backend).|

If you need several of these capabilities, use `mecated`, which assembles them
for you. See
[Run mecated standalone](/operating/mecated.md).

## Next steps

- [Implement extension points](extension-points/index.md) for your application's adapters.
- [Review API stability](api-stability.md) before upgrading the engine.
- [Operate a prebuilt server](/operating/index.md) if you want supplied authentication,
  transports, and deployment integration.

## Related topics

<span id="contextual-guardrail-extension-contracts" />

[PermissionPolicy](/building/go/extension-points/permission-policy.md)

<span id="optional-evidence-reflection" />

[Extension points and ports](/building/go/extension-points/index.md)
