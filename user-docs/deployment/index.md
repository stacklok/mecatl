---
sidebar_position: 1
title: Deployment overview
---

# Deployment overview

mecatl ships in four deployment shapes. Each shape is a different composition root over the same `engine/agent` loop — pick the one that matches your operational model and persistence requirements. They are not interchangeable; a mecak8s pod is not a mecated pod with a flag flipped.

:::note[Need help deciding?]

The [Pick your deployment shape](/getting-started/deployment-decision.md) guide in Getting Started walks the full decision tree with trade-offs for each shape.

:::

---

## Quick reference

| Shape | When to choose | Key dependency | Persistence model |
|---|---|---|---|
| **Embed** | You own the binary; want the loop in-process with fine-grained control over every port | `github.com/stacklok/mecatl/engine` (`golang.org/x/sync` + `doublestar` + `robfig/cron/v3` at runtime) | You implement `port.SessionStore` |
| **mecated** | Standalone server with interactive clients (TUI, IDE), or a controlled service deployment | A running process; PV for durable sessions | In-memory (default), JSONL on disk (`--store-dir`), or gRPC driver |
| **mecak8s** | Kubernetes with no persistent volumes, multi-replica, disposable pods | Redis StatefulSet + `coordination.k8s.io` RBAC | Redis — no local state; k8s `Lease` for single-writer enforcement |
| **mecatequi** | Single-shot CI: one prompt → git-diff patch → exit | LLM provider key + a GitHub Actions runner | None — stateless per run |

---

## Sub-pages

- [**Embed the engine directly**](embed-engine.md) — import `github.com/stacklok/mecatl/engine`, wire the port interfaces yourself, and compose `app.Build` into your own binary without taking mecatl's heavy require cone.

- [**Run mecated standalone**](mecated.md) — configure and operate the `mecated` composition root: flags, TLS/auth, `--store-dir` persistence, session leasing for multi-replica deployments, Prometheus/OTel, and graceful shutdown.

- [**Cloud-native k8s with mecak8s**](mecak8s.md) — deploy the `cmd/mecak8s` composition root using the `deploy/mecak8s/` kustomize base; covers the Redis StatefulSet, RBAC requirements for `leases`, pod drain, and lease release on SIGTERM.

- [**Single-shot CI with mecatequi**](mecatequi.md) — adopt the `mecatequi-reusable.yml` reusable workflow, understand the split-privilege job graph (agent job holds no write token; publish job applies the patch as data), and read `stop-reason` + `non-empty-diff` from action outputs correctly.

- [**Drive via gRPC / HTTP**](grpc-http.md) — the wire protocol: the gRPC `Converse` stream, the HTTP/SSE surface, the `ResumeApproval` frame for permission verdicts, and the `POST /v1/sessions/{id}/approve` endpoint.

---

## What's next

- [Pick your deployment shape](/getting-started/deployment-decision.md) — full decision tree with trade-offs before you commit to a shape.
- [The agent loop](/what-you-get/agent-loop.md) — understand what the engine does once it is running, regardless of which composition root you chose.
- [Permissions & guardrails](/what-you-get/permissions.md) — the posture ladder (`--posture strict|trusted|auto|yolo`) and workspace trust behave identically across all four shapes; the only deployment-specific difference is headless vs interactive defaults.
