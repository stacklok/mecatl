---
sidebar_position: 1
title: Building on Mecatl
description: Embed, extend, and deploy Mecatl using its Go engine, services, and adapter ports.
---

# Building on Mecatl

Mecatl is a cloud-native harness for building agentic systems. It ships as a runnable service (`mecated`, `mecak8s`) and as an importable Go engine (`github.com/stacklok/mecatl/engine`) with a deliberate port model: you wire in the adapters your stack needs and the loop, tools, permissions, hooks, and resilience are already there.

This guide is for teams building **on** Mecatl — embedding the engine, writing adapters, deploying to k8s, or wiring Mecatl into their own product.

---

## Doc map

### Getting Started

| Doc | What it covers |
|-----|----------------|
| [Build your first agent](/building/getting-started/first-agent.md) | Install the engine and build a working Go agent from a clean module |
| [See it in 60 seconds](/building/getting-started/demo.md) | Run `mecademo` offline and watch a real engine turn |
| [Pick your deployment shape](/building/getting-started/deployment-decision.md) | Decision guide: embed vs service vs CI runner |
| [Use the TypeScript SDK](/building/getting-started/typescript-sdk.md) | Connect Node, Bun, or browser applications through the published package |

### What you get out of the box

_Everything here you do not have to build._

| Doc | What it covers |
|-----|----------------|
| [Engine & session model](/building/what-you-get/engine-and-session.md) | The three core objects: Engine, Session, Run — and how they relate |
| [The agent loop](/building/what-you-get/agent-loop.md) | Turn structure, compaction, max-turns, cancellation |
| [Core tools](/building/what-you-get/core-tools.md) | Read, Write, Edit, Bash, Search, and the tool catalog |
| [Permissions & guardrails](/building/what-you-get/permissions.md) | Layer-1 rule engine, layer-2 model classifier, workspace trust |
| [Hook system](/building/what-you-get/hooks.md) | Pre/post-tool, pre/post-turn, session lifecycle |
| [Memory & knowledge](/building/what-you-get/memory.md) | Cross-session memory, dream consolidation, user model |
| [MCP client](/building/what-you-get/mcp-client.md) | Streaming-HTTP MCP, namespaced tools, reconnect |
| [Subagents, teams & parallel](/building/what-you-get/subagents-teams-parallel.md) | One-shot Subagent, Parallel fork-join, Team crew coordination |
| [Observability & resilience](/building/what-you-get/observability.md) | OTel traces, LLM resilience decorator, slow-turn ring |
| [Scheduled tasks](/building/what-you-get/scheduled-tasks.md) | Autonomous cron/one-shot runs, at-most-once slot claiming across replicas |

### Extension points

_The port interfaces — implement one to swap a capability without touching the loop._

| Doc | What it covers |
|-----|----------------|
| [Overview & the port model](/building/extension-points/index.md) | Hexagonal design, seam table, reference adapters |
| [LLMProvider](/building/extension-points/llm-provider.md) | Bring your own model backend |
| [SessionStore & EventLog](/building/extension-points/session-store.md) | Persistence: in-memory → jsonl → Redis → gRPC driver |
| [SessionLease](/building/extension-points/session-lease.md) | Distributed locking: flock → k8s → gRPC driver |
| [HookRunner](/building/extension-points/hook-runner.md) | Custom lifecycle hooks |
| [PermissionPolicy](/building/extension-points/permission-policy.md) | Custom permission logic |
| [Tool catalog](/building/extension-points/tool-catalog.md) | Add tools, MCP servers, and skills |
| [Agent definitions](/building/extension-points/agent-definitions.md) | Named specialist agent profiles |

### Deployment shapes

_From smallest to largest._

| Doc | What it covers |
|-----|----------------|
| [Overview](/building/deployment/index.md) | Decision tree, trade-off summary |
| [Embed the engine directly](/building/deployment/embed-engine.md) | Import `engine/`, wire adapters in-process |
| [Run mecated standalone](/building/deployment/mecated.md) | Single-binary server, gRPC + HTTP-SSE |
| [Cloud-native k8s with mecak8s](/building/deployment/mecak8s.md) | Redis + k8s lease, disposable pod, storage-free binary |
| [Single-shot CI with mecatequi](/building/deployment/mecatequi.md) | One prompt → patch + exit code, forge-agnostic |
| [Drive via gRPC / HTTP](/building/deployment/grpc-http.md) | Remote clients, the driver protocol, streaming |
| [mecatui container image (brood-box)](/building/deployment/mecatui.md) | Signed multi-arch container image importable as a brood-box agent |

### Cloud-native kit properties

| Doc | What it covers |
|-----|----------------|
| [Disposable process, externalized state, durable record](/building/cloud-native-kit.md) | The three properties explained; the four delivery phases |

### API stability

| Doc | What it covers |
|-----|----------------|
| [What's stable and what's not](/building/api-stability.md) | The seven stable packages, the compat gate, what `engine/adapter/` does NOT promise |
