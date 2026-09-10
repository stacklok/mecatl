---
sidebar_position: 1
title: Deployment overview
description: Choose how to embed, run, or operate Mecatl across local and cloud environments.
---

# Deployment overview

Mecatl ships several composition roots over the same `engine/agent` loop. Use
[the deployment decision guide](/building/getting-started/deployment-decision.md)
to choose a shape; this page is the directory of detailed deployment guides.

The main operational distinction is simple: `mecated` is the general
client/server deployment, `mecak8s` externalizes state for disposable Kubernetes
pods, and mecatui provides an interactive terminal skin over an embedded server.

- [**Install Mecatl**](/install.md) — get the released `mecatui` and `mecated` executables from Homebrew or a signed release archive, and verify them before they reach a host.

- [**Embed the engine directly**](embed-engine.md) — import `github.com/stacklok/mecatl/engine`, wire the port interfaces yourself, and compose `app.Build` into your own binary without taking Mecatl's heavy require cone.

- [**Configure Mecatl**](settings.md) — choose the operator settings and secret files, daemon topology configuration, command flags, and client-only mecatui settings for each deployment shape.

- [**Run mecated standalone**](mecated.md) — configure and operate the `mecated` composition root: flags, TLS/auth, `--store-dir` persistence, session leasing for multi-replica deployments, Prometheus/OTel, and graceful shutdown.

- [**Operate local session storage**](session-storage-operations.md) — tested systemd user-service and launchd examples, daemon-owned retention, management capability truth, and the quiesced backup/migration/restore runbook.

- [**Cloud-native k8s with mecak8s**](mecak8s.md) — deploy the `cmd/mecak8s` composition root using the `deploy/helm/mecak8s/` Helm chart; covers the external Redis requirement, RBAC requirements for `leases`, pod drain, and lease release on SIGTERM.

- [**Single-shot CI with mecatequi**](mecatequi.md) — adopt the `mecatequi-reusable.yml` reusable workflow, understand the split-privilege job graph (agent job holds no write token; publish job applies the patch as data), and read `stop-reason` + `non-empty-diff` from action outputs correctly.

- [**Drive via gRPC / HTTP**](grpc-http.md) — the wire protocol: the gRPC `Converse` stream, the HTTP/SSE surface, the `ResumeApproval` frame for permission verdicts, and the `POST /v1/sessions/{id}/approve` endpoint.

- [**mecatui container image (brood-box)**](mecatui.md) — the `ghcr.io/stacklok/mecatl/mecatui` container image: built + signed on release, carries a brood-box agent manifest, importable via `bbox agents import`.

---

## What's next

- [Pick your deployment shape](/building/getting-started/deployment-decision.md) — full decision tree with trade-offs before you commit to a shape.
- [The agent loop](/building/what-you-get/agent-loop.md) — understand what the engine does once it is running, regardless of which composition root you chose.
- [Permissions & guardrails](/building/what-you-get/permissions.md) — the posture ladder (`--posture strict|trusted|auto|yolo`) and workspace trust behave identically across all four shapes; the only deployment-specific difference is headless vs interactive defaults.
