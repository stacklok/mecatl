---
slug: /building/api-stability
sidebar_position: 6
title: API stability
description: Understand the Go engine compatibility promise and plan dependency upgrades.
---

# API stability

The importable `github.com/stacklok/mecatl/engine` module has an explicit
compatibility contract for its core Go packages. Review that contract and the
engine changelog when upgrading an embedding application. Engine version tags
use `engine/vX.Y.Z`, independently of the host repository's binary and image tags.

## The stable surface

The contract covers the **exported identifiers** of eight core packages:

|Package|Role|
|-|-|
|[`engine/session`](https://pkg.go.dev/github.com/stacklok/mecatl/engine/session)|Sessions, value objects, and events|
|[`engine/governance`](https://pkg.go.dev/github.com/stacklok/mecatl/engine/governance)|Permission rules, evaluation, and hook event types|
|[`engine/learning`](https://pkg.go.dev/github.com/stacklok/mecatl/engine/learning)|Evidence reflection and learned-skill lifecycle contracts|
|[`engine/tool`](https://pkg.go.dev/github.com/stacklok/mecatl/engine/tool)|Tools, the catalog, workspaces, and source interfaces|
|[`engine/prompt`](https://pkg.go.dev/github.com/stacklok/mecatl/engine/prompt)|Prompt assembly and discovery interfaces|
|[`engine/port`](https://pkg.go.dev/github.com/stacklok/mecatl/engine/port)|Interfaces consumed by the agent loop|
|[`engine/team`](https://pkg.go.dev/github.com/stacklok/mecatl/engine/team)|Agent-team domain types|
|[`engine/agent`](https://pkg.go.dev/github.com/stacklok/mecatl/engine/agent)|The loop, dispatch, delegation, and team supervision|

Every exported constant, variable, function, type, method, and struct field in
these packages is part of the contract.

## What is explicitly excluded

|Excluded|Reason|
|-|-|
|`engine/adapter/*`|Reference adapters, test doubles, and conformance suites. Their interfaces in `engine/port` and `engine/tool` are guarded, but adapter implementations can change in a minor release.|
|`engine/arch`|Test-support only: the layering proofs and the `arch.CorePackages` list.|
|Root module (`internal/`, `cmd/`, `contracts/`, `perf/`)|Outside the engine module boundary. No external compatibility promise applies.|

You can use reference adapters such as `mockllm` and `memfs` in tests, but treat
them as conveniences rather than stable dependencies. Build production
integrations against the guarded interfaces.

## Upgrade your application

While the engine is at v0.x, a minor release can include additive or breaking
API changes. Patch releases contain bug fixes without exported API changes.
After v1.0.0, breaking changes require a major version bump.

Read [the engine changelog](https://github.com/stacklok/mecatl/blob/main/engine/CHANGELOG.md)
for classified additions and breaks, update engine and provider dependencies to
compatible versions, and run your application's tests against its tools and
adapters. Use [pkg.go.dev](https://pkg.go.dev/github.com/stacklok/mecatl/engine)
for the exact symbols in the version you select.

The repository checks exported APIs and builds the engine independently of its
host module. Contributor snapshot updates and release classification are
maintained in the [engine compatibility policy](https://github.com/stacklok/mecatl/blob/main/engine/COMPATIBILITY.md).

## Persistence compatibility

A custom event-sourced store must preserve session reconstruction and account
for provider-private fields absent from public events. The complete
[load-from-events contract](extension-points/session-store.md#load-from-events)
belongs with persistence integration; use snapshots when exact provider replay
is required.

## Next steps

- [Implement extension points](extension-points/index.md) against the guarded interfaces.
- [Embed the engine](embed-engine.md) with production lifecycle and storage.

## Related topics

<span id="event-sourced-load-contract" />
<span id="required-reconstruction" />
<span id="replay-fidelity-limitation" />

[SessionStore and EventLog](/building/go/extension-points/session-store.md)
