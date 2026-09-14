---
sidebar_position: 10
title: Capability and deployment matrix
description:
  Choose the Mecatl deployment surface that provides the capabilities you need.
---

# Capability and deployment matrix

Choose a Mecatl deployment based on the differences that affect your
environment: where the workspace lives, how state is stored, whether a person
can approve requests, and which client or API surface you need. Most
capabilities come from the shared engine and server.

## Shared server capabilities

When enabled and configured, both `mecated` and `mecak8s` provide the same core
agent experience:

- the agent loop, core tools, permissions, approvals, posture, and model
  routing;
- project instructions, rules, skills, commands, soul, memory, named agents, and
  streaming-HTTP MCP sources;
- durable sessions, event logs, scheduling, Subagents, Parallel, Teams, and
  multimodal input; and
- gRPC and HTTP/SSE session APIs.

Individual features can still require configuration, a capable model, a trusted
workspace, or a storage backend. A connected client inherits the capabilities of
its server.

## Deployment differences

|Need|`mecated`|`mecak8s`|`mecatui` embedded|
|-|-|-|-|
|Run a general-purpose server|✓|✓|Starts one locally|
|Workspace and Shell namespace|Host-local workspace|Configured pod workspace or no-FS profile|Host-local workspace|
|gRPC API|✓|✓|Private Unix socket|
|HTTP/SSE API|✓|✓|No|
|Durable state|Optional configured backend|Redis-backed when configured|JSONL store by default|
|Kubernetes leases and drain handling|No|✓|No|
|Interactive permission approvals|Opt|Headless by default|✓|
|ACP editor integration|✓, `mecated acp` only|No|No|

## Choose a deployment

### Use `mecated` for a general client/server deployment

`mecated` is the general-purpose server for environments outside Kubernetes. It
serves gRPC and HTTP/SSE, can use local or configured storage, and supports
optional server features such as MCP, schedules, and ACP.

Use it when clients and the harness run as separate processes, when the harness
should work in a host-local workspace, when an operator needs the local
ACP/editor integration, or when you want to choose the storage and network
configuration directly.

### Use `mecak8s` for Kubernetes-native operation

`mecak8s` serves the same agent and API surfaces with cloud-native defaults:
Redis stores session state and durable events, Kubernetes leases coordinate
ownership across replicas, and readiness/drain behavior suits a deployment
controller.

When an operator configures a workspace, its filesystem tools and any Shell
command run in the harness pod namespace, not on a remote caller’s machine.
Without `--workspace`, `mecak8s` creates no-FS sessions by default. `mecak8s` is
headless by default, so unresolved permission asks need an explicit
headless-reviewer strategy or are denied. ACP is intentionally not available in
this deployment.

### Use embedded `mecatui` for local interactive work

Running bare `mecatui` starts an in-process server behind a private Unix gRPC
socket. It is an interactive local product: it can use local provider, posture,
trust, storage, and resilience configuration, and it surfaces permission asks in
the TUI.

Its durable store defaults to a per-workspace JSONL location under XDG state.
Use `--no-store` for in-memory state or `--store-dir` to choose another
location.

## Workspace and Shell execution

Filesystem tools and Shell operate in the namespace where the harness runs. In
`mecak8s`, that normally means the pod’s workspace and command environment, not
the client’s machine. A remote client does not upload or share its local
checkout. See [Execution environments](./execution-environments.md) for the
workspace, runner, no-FS, child-environment, and reattachment model.

## ACP editor integration

ACP is a local `mecated` integration. It creates a session using the editor
client’s working directory. When the editor supports read/write access, ACP can
install a shell-less editor-buffer environment for that session. ACP resume
restores conversation state but does not yet restore the editor-buffer override.

## Feature availability at runtime

Optional feature availability depends on the server's configuration and
registered tools. Check the capability snapshot returned when you create a
session rather than assuming a feature is enabled because a deployment can
support it.

## Related information

- [Features](./index.md)
- [Project instructions and rules](./project-instructions-and-rules.md)
- [Deployment decision](/building/getting-started/deployment-decision.md)
- [Workspace trust](/features/permissions-and-posture.md)
- [gRPC API](/reference/grpc-api.md)
- [HTTP/SSE API](/reference/http-sse-api.md)
