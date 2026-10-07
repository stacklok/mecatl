---
sidebar_position: 1
title: Deploy and operate Mecatl
description:
  Start a Mecatl service and operate a shared deployment for your team.
---

# Deploy and operate Mecatl

Use these guides when you own the Mecatl service, its credentials, access
policy, execution environment, and durable state. The clients connect to the
service; they use the model access and workspaces you provide.

## Start with a lightweight server

[Run `mecated`](./mecated.md) to separate the agent service from its clients.
Start one instance, configure model access, and connect a client. Add local
persistence when you need to resume sessions across restarts.

This is the simplest way to learn the service boundary or operate one controlled
instance. Its process and storage are yours to maintain.

## Operate a shared team deployment

Use [`mecak8s`](./mecak8s.md) for a shared team, including small teams. It
supplies Redis-backed durable state, Kubernetes lease coordination, and
disposable agent replicas. Configure broker-backed MCP profiles to add its
separate single-replica broker; see
[MCP access and broker operations](./mecak8s/identity-and-client-access.md#configure-mcp-server-access).
Start with [Try Mecatl on Kubernetes](./kubernetes.md) to see a session survive
replacement of its pod, then follow the deployment and maintenance guides.

|Operating model|What you own|
|-|-|
|One `mecated` instance|Host lifecycle, workspace, local or configured session storage, and client access.|
|Shared `mecak8s` deployment|Kubernetes deployment, Redis, access and network policy, execution placement, and capacity.|
|Custom multi-replica `mecated` composition|Shared storage, a lease backend, routing, and replica lifecycle that `mecak8s` otherwise supplies.|

Shared deployments are intended for trusted teams. Caller ownership and
permissions govern access, while filesystem isolation depends on the workspaces
and execution environments you provision. See
[Caller identity](/features/security-and-execution/caller-identity.md) and
[Execution environments](/features/security-and-execution/execution-environments.md).

## Configure and maintain the service

- [Configure Mecatl](./settings.md) explains settings files, flags, and their
  ownership.
- [Operate local session storage](./session-storage-operations.md) covers
  single-host recovery.
- [Deploy Studio](./studio.md) provides a browser client for your deployment.
- [Collect metrics, traces, and diagnostics](./observability.md) covers
  operational visibility.
- [Local microVM environments](./microvm-environments.md) describes the
  qualified execution path and its prerequisites.

The [server CLI reference](/reference/server-cli.md) lists the complete server
commands and flags. The
[capability matrix](/features/get-oriented/capability-matrix.md) compares
behavior and availability across deployment forms.

To integrate Mecatl into an application or CI workflow, follow
[Build with Mecatl](/building/index.md).
