---
sidebar_position: 1
title: Deploy and operate Mecatl
description:
  Deploy Mecatl as a service, Kubernetes workload, or single CI task.
---

# Deploy and operate Mecatl

Use this section when you own the Mecatl service, its state, credentials,
security policy, and execution environment. You can run a standalone daemon,
deploy disposable Kubernetes replicas, or run a single task in CI.

Start with
[Choose how to run Mecatl](./choose-deployment.md) if you
have not chosen one yet.

## Choose a deployment

|Deployment|Use it when you want to|
|-|-|
|[Run `mecated`](./mecated.md)|Provide a general-purpose remote service for terminal or API clients.|
|[Run `mecak8s`](./mecak8s.md)|Use Redis-backed sessions and Kubernetes Leases across disposable replicas.|
|[Run `mecatequi`](./mecatequi.md)|Execute one task in CI and return a patch, summary, and exit status.|

To see the Kubernetes model locally, follow
[Try Mecatl on Kubernetes](./kubernetes.md).

## Operate a deployment

- [Run your first local session](/mecatui/getting-started.md)
- [Configure a deployment](./settings.md)
- [Operate local session storage](./session-storage-operations.md)
- [Run the `mecatui` container image](./mecatui.md)
- [Run the Mecatl Studio web UI](./studio.md) (early access)
- [Monitor and diagnose Mecatl](./observability.md)

To run Mecatl inside your own application, see
[Build with Mecatl](/building/index.md).
