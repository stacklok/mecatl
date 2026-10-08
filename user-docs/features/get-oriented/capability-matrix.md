---
slug: /features/capability-matrix
sidebar_position: 10
title: Capability and deployment matrix
description:
  Choose the Mecatl deployment that provides the capabilities you need.
---

# Capability and deployment matrix

Choose a deployment based on its workspace, storage, approval, and API support.
Most capabilities come from the shared engine and server.

## Shared server capabilities

When configured, `mecated` and `mecak8s` provide the same core capabilities:

- the agent loop, core tools, permissions, approvals, posture, and model
  routing;
- project instructions, rules, skills, commands, soul, memory, named agents, and
  streaming-HTTP MCP sources;
- durable sessions, event logs, scheduling, Subagents, Parallel, Teams, and
  multimodal input; and
- gRPC and HTTP/SSE session APIs.

Some features require additional configuration, model support, project trust, or
durable storage. A connected client uses the server's capabilities.

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

For deployment tasks, start with
[Deploy and operate Mecatl](/operating/index.md). This matrix compares
capabilities; the operator journey explains the service lifecycle, durability,
and coordination choices.

## Workspace and Shell execution

Filesystem tools and Shell operate in the namespace where the harness runs. In
`mecak8s`, that normally means the pod's workspace and command environment. A
remote client does not upload or share its local checkout. See
[Execution environments](/features/security-and-execution/execution-environments.md)
for the workspace, runner, no-FS, child-environment, and reattachment model.

## ACP editor integration

ACP is a local `mecated` integration. Its session uses the server's configured
placement. The editor's required `cwd` must match that placement; it cannot
select a different workspace. When the editor supports read/write access, ACP
can install a shell-less editor-buffer environment for the session. Both new and
loaded sessions can use this overlay when the editor advertises support.

## Feature availability at runtime

Optional feature availability depends on the server's configuration and
registered tools. Check the capability snapshot returned when you create a
session rather than assuming a feature is enabled because a deployment can
support it.

## Related information

- [Deployment decision](/operating/index.md)
- [Permissions and posture](/features/security-and-execution/permissions-and-posture.md)
- [Execution environments](/features/security-and-execution/execution-environments.md)
