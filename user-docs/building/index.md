---
sidebar_position: 1
title: Building on Mecatl
description:
  Embed, extend, and deploy Mecatl using its Go engine, services, and adapter
  ports.
---

# Building on Mecatl

Build on Mecatl when you want to embed the agent loop, integrate a client, or
run agents as services on infrastructure you control. Mecatl provides an
importable Go engine, remote APIs, a standalone server, and a Kubernetes
deployment.

## Choose where to start

- [Build your first agent](/building/getting-started/first-agent.md) to embed
  the Go engine in a small application.
- [Use the TypeScript SDK](/building/getting-started/typescript-sdk.md) to
  connect a Node.js, Bun, or browser application.
- [Pick a deployment](/building/getting-started/deployment-decision.md) to
  compare the embedded engine, `mecated`, `mecak8s`, and `mecatequi`.
- [Try Mecatl on Kubernetes](/building/getting-started/kubernetes.md) to run two
  `mecak8s` replicas in a local Kind cluster.
- [Run the offline demo](/building/getting-started/demo.md) to see an engine
  turn without a model provider account.

## Explore by topic

- [What Mecatl provides](/building/what-you-get/engine-and-session.md) explains
  the engine, session model, agent loop, tools, permissions, hooks, and other
  built-in capabilities.
- [TypeScript SDK](/building/typescript-sdk/index.md) covers application
  connections, sessions and runs, approvals, durable activity, and callback
  tools.
- [Extension points](/building/extension-points/index.md) covers the Go ports
  for model providers, storage, permissions, tools, and other adapters.
- [Deployment guides](/building/deployment/index.md) cover embedding the engine,
  operating `mecated` or `mecak8s`, using `mecatequi` in CI, and connecting
  remote clients.
- [What is a cloud-native harness?](/building/cloud-native-harness.md) explains how
  Mecatl separates the agent process from durable state and execution.
- [API stability](/building/api-stability.md) identifies the supported Go
  packages and compatibility guarantees.
- [Reference](/reference/index.md) provides exact configuration fields and gRPC
  and HTTP/SSE contracts.
