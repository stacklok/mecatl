---
sidebar_position: 1
slug: /
title: Mecatl documentation
description: Run AI agents as cloud-native workloads on infrastructure you control.
---

# Mecatl documentation

Mecatl is an open source, cloud-native agent harness for running AI agents as
production workloads on infrastructure you control. It provides the agent loop,
tools, permissions, and session state.

## Cloud-native by design

Mecatl keeps the agent loop independent of the client and execution environment.
Run the same core locally, as a remote service, or across Kubernetes replicas
without replacing the agent loop.

With `mecak8s`, agent pods are disposable. Redis stores session state and event
history, while Kubernetes Leases coordinate session ownership across replicas.
Choose the model providers, clients, storage backends, and execution environments
that fit your infrastructure.

## Choose a component

|Component|Use it when you want to|
|-|-|
|[`mecatui`](/mecatui/index.md)|Work interactively in a terminal. It can start a private embedded server or connect to an existing one.|
|[`mecated`](/building/deployment/mecated.md)|Run the general-purpose server for terminal clients or gRPC and HTTP/SSE integrations.|
|[`mecak8s`](/building/deployment/mecak8s.md)|Run Mecatl on Kubernetes with session state in Redis and coordination through Kubernetes Leases.|
|[`mecatequi`](/building/deployment/mecatequi.md)|Run one task in CI and return a patch, summary, and exit status.|
|[Go engine](/building/deployment/embed-engine.md)|Embed the agent loop in your own Go application and supply its adapters.|

Start with `mecatui` if you are new to Mecatl. You can use the same terminal
client when you move the server into a separate process or Kubernetes.

## How local and remote sessions differ

Bare `mecatui` starts a private `mecated` server in the same process. This
embedded server uses your local workspace, provider credentials, storage, and
permission settings.

`mecatui connect ADDRESS` connects to a separately running `mecated` or
`mecak8s` server. That server controls the workspace, provider credentials,
storage, and permissions. The client displays the session and sends your input.

A **workspace** is the project directory in which the agent can inspect files
and run tools. A **session** is an ongoing conversation and its working context.
Keep each session focused on one project and task.

## Start here

1. [Install Mecatl](/install.md).
1. [Run your first local session](/mecatui/getting-started.md).
1. [Connect to a separate server](/mecatui/remote-servers.md).
1. [Try Mecatl on Kubernetes](/building/getting-started/kubernetes.md).

To build an application on the Go engine or APIs, start with
[Building on Mecatl](/building/index.md). For exact configuration and protocol
details, use the [reference](/reference/index.md).
