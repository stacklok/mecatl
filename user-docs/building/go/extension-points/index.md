---
slug: /building/extension-points
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

Filesystem and execution types live in `engine/tool`, where tools use them
directly:

|Type|Responsibility|
|-|-|
|`tool.FileSystem`|Underlying filesystem operations|
|`tool.Workspace`|Version-aware reads and conditional writes|
|`tool.Environment`|A workspace, read ledger, environment identity, and optional command runner|
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

## Reflection integration

The optional `learning` package selects bounded evidence from a run and can
produce evidence-backed reflection proposals. `learning.MaterializeEvidence`
returns canonical evidence and an immutable manifest, or a content-free reason
for abstaining. Persist the manifest with a staged proposal so review uses the
same evidence rather than rerunning selection.

The package defines interfaces for learned-skill validation, content-addressed
versions, evaluation, review, and activation. Reference adapters provide
in-memory storage and validation. Your embedding application owns proposal
persistence, review authorization, scheduling, and transport. Use the
[learning API](https://pkg.go.dev/github.com/stacklok/mecatl/engine/learning)
for symbols and the
[evidence contract](https://github.com/stacklok/mecatl/blob/main/docs/architecture/memory.md#evidence-backed-reflection)
for selection and output validation.

## Next steps

- [Implement an LLM provider](llm-provider.md).
- [Implement session storage](session-store.md).
- [Implement a permission policy](permission-policy.md).

## Related information

- [Session leases](session-lease.md) coordinate concurrent writers.
- [Project rules](project-rules.md) supply project and user guidance.
