---
sidebar_position: 1
title: Build with Mecatl
description: Embed the Go engine or integrate applications and CI through Mecatl APIs.
---

# Build with Mecatl

Build an agent into your Go application, connect a client to a Mecatl service,
or automate a bounded task in CI. Choose the integration that matches the
lifecycle your application should own.

## Embed the Go engine

The [Go engine track](go/index.md) starts with a working agent, explains the
objects your application holds, and develops that example into a production
host. Continue to the extension points when you need custom tools, providers,
permissions, or persistence. The compatibility guide explains what to review
when upgrading your dependencies.

## Connect a client application

Use the [TypeScript SDK](typescript-sdk/index.md) for Node.js, Bun, Deno, or a
browser application. Its tutorial runs one prompt against a private offline
daemon; the task guides cover operator-owned services, sessions, approvals,
and durable activity.

For another language or a custom transport integration, start with
[Connect with gRPC or HTTP](grpc-http.md). Exact protocol behavior belongs in
[Reference](/reference/index.md). If your wrapper owns the server process, use
[private daemon hosting](local-daemon.md) for startup and shutdown.

## Automate CI work

[CI integration](ci/index.md) runs a single bounded task and produces a patch,
summary, and exit status. Your workflow supplies credentials and decides how
to publish the result.

If your task is to host a shared service rather than integrate an application,
start with [Deploy and operate Mecatl](/operating/index.md).
