---
sidebar_position: 1
title: Deployment overview
description:
  Choose how to embed, run, or operate Mecatl across local and cloud
  environments.
---

# Deployment overview

Mecatl runs the same agent loop as an embedded library, standalone service,
Kubernetes workload, or single CI task. Choose the form that matches who owns
the process, state, and execution environment.

Start with
[Pick your deployment](/building/getting-started/deployment-decision.md) if you
have not chosen one yet.

## Choose a deployment

|Deployment|Use it when you want to|
|-|-|
|[Embed the Go engine](./embed-engine.md)|Run the agent loop inside your application and supply its adapters.|
|[Run `mecated`](./mecated.md)|Provide a general-purpose remote service for terminal or API clients.|
|[Run `mecak8s`](./mecak8s.md)|Use Redis-backed sessions and Kubernetes Leases across disposable replicas.|
|[Run `mecatequi`](./mecatequi.md)|Execute one task in CI and return a patch, summary, and exit status.|

To see the Kubernetes model locally, follow
[Try Mecatl on Kubernetes](/building/getting-started/kubernetes.md).

## Operate and integrate

- [Run your first local session](/mecatui/getting-started.md)
- [Configure a deployment](./settings.md)
- [Operate local session storage](./session-storage-operations.md)
- [Connect clients through gRPC or HTTP/SSE](./grpc-http.md)
- [Run the `mecatui` container image](./mecatui.md)
