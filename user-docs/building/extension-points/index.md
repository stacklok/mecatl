---
sidebar_position: 1
title: Extension points and ports
description:
  Replace Mecatl providers, storage, policies, and other integrations through Go
  interfaces.
---

# Extension points and ports

Mecatl exposes Go interfaces around the agent loop. Implement the interface for
the capability you want to replace, then provide your adapter when you construct
the engine or service.

## How ports and adapters fit together

```mermaid
graph LR
    App["Your application"] --> Adapter["Your adapter"]
    Adapter --> Port["Mecatl interface"]
    Port --> Loop["Agent loop"]
```

The agent loop depends on interfaces and domain types. Concrete providers,
databases, policy engines, and operating-system integrations remain outside the
loop.

## Choose an extension point

|Interface|Implement it to|
|-|-|
|`LLMProvider`|Connect a model backend or inference service|
|`SessionStore`|Persist and reload session snapshots|
|`PrunableStore`|Add retention listing and deletion|
|`PermissionPolicy`|Make allow, ask, and deny decisions|
|`PermissionStore`|Persist learned permission rules per session|
|`HookRunner`|Run lifecycle hooks|
|`EventLog`|Store the durable event history for a session|
|`EventSink`|Relay live events or telemetry|
|`ToolCallRecorder`|Record tool-call audit data|
|`Diagnostics`|Send structured diagnostic logs to another system|
|`Clock`|Provide wall-clock time|
|`SessionLease`|Coordinate single-writer ownership across processes|

Filesystem and execution interfaces live in `engine/tool`, where tools consume
them directly:

|Interface|Responsibility|
|-|-|
|`tool.FileSystem`|Underlying filesystem operations|
|`tool.Workspace`|Version-aware reads and conditional writes|
|`tool.Environment`|A workspace, its identity, and an optional command runner|
|`tool.EnvironmentForker`|Create an isolated child environment|
|`tool.EnvironmentMerger`|Merge a child environment into its parent|

A custom `tool.Workspace` must also implement the optional
`tool.WorkspaceNamespace` interface to support `ListDir`, `Copy`, `Move`, and
`Remove`. Those tools report `tool.ErrFileOperationUnsupported` when the
workspace does not provide the required namespace operation.

Mecatl includes adapters for common deployments and in-memory implementations
for tests. Implement a port when those adapters do not meet your application's
requirements. You do not need a new port to select a model, configure permission
rules, register hooks, or add a tool to `tool.Catalog`.

## Provide adapters at the composition root

Construct adapters at the edge of your application and pass them to Mecatl. One
value can implement several interfaces. For example, a store can provide session
persistence, pruning, durable events, and tool-call recording.

```go
store, err := yourstore.New(cfg.DatabaseURL)
if err != nil {
    return nil, err
}

policy := permpolicy.NewPolicy(rules, permstore.New())

engine := agent.NewEngine(agent.Deps{
    LLM:              provider,
    Catalog:          catalog,
    Store:            store,
    Policy:           policy,
    Hooks:            hooks,
    ToolCallRecorder: store,
    Diagnostics:      diagnostics,
    Clock:            wallclock.Clock{},
})
```

The exact dependencies depend on the Mecatl version and the features your
application enables.

## Test your adapter

Mecatl provides conformance packages under `engine/adapter/` for stores, event
logs, leases, filesystems, content sources, memory, and schedules. Run the
matching suite against a fresh instance of your implementation:

```go
func TestStoreConformance(t *testing.T) {
    storeconformance.Run(t, func(t *testing.T) port.SessionStore {
        return yourstore.NewForTest(t)
    })
}
```

The extension-point pages identify the contract and conformance suite for each
interface.

## Next steps

- [Implement an LLM provider](llm-provider.md).
- [Implement session storage](session-store.md).
- [Implement a permission policy](permission-policy.md).
- [Implement a session lease](session-lease.md).
- [Supply project rules](project-rules.md).
