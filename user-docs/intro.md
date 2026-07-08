---
sidebar_position: 1
title: Building on mecatl
---

# Building on mecatl

mecatl is a cloud-native harness for building agentic systems. It ships as a runnable service (`mecated`, `mecak8s`) and as an importable Go engine (`github.com/stacklok/mecatl/engine`) with a deliberate port model: you wire in the adapters your stack needs and the loop, tools, permissions, hooks, and resilience are already there.

This guide is for teams building **on** mecatl — embedding the engine, writing adapters, deploying to k8s, or wiring mecatl into their own product.

---

## Doc map

### Getting Started

| Doc | What it covers |
|-----|----------------|
| [See it in 60 seconds](/getting-started/demo.md) | Run `mecademo` offline and watch a real engine turn |
| [Pick your deployment shape](/getting-started/deployment-decision.md) | Decision guide: embed vs service vs CI runner |

### What you get out of the box

_Everything here you do not have to build._

| Doc | What it covers |
|-----|----------------|
| [Engine & session model](/what-you-get/engine-and-session.md) | The three core objects: Engine, Session, Run — and how they relate |
| [The agent loop](/what-you-get/agent-loop.md) | Turn structure, compaction, max-turns, cancellation |
| [Core tools](/what-you-get/core-tools.md) | Read, Write, Edit, Bash, Search, and the tool catalog |
| [Permissions & guardrails](/what-you-get/permissions.md) | Layer-1 rule engine, layer-2 model classifier, workspace trust |
| [Hook system](/what-you-get/hooks.md) | Pre/post-tool, pre/post-turn, session lifecycle |
| [Memory & knowledge](/what-you-get/memory.md) | Cross-session memory, dream consolidation, user model |
| [MCP client](/what-you-get/mcp-client.md) | Streaming-HTTP MCP, namespaced tools, reconnect |
| [Observability & resilience](/what-you-get/observability.md) | OTel traces, LLM resilience decorator, slow-turn ring |
| [Scheduled tasks](/what-you-get/scheduled-tasks.md) | Autonomous cron/one-shot runs, exactly-once across replicas |

### Extension points

_The port interfaces — implement one to swap a capability without touching the loop._

| Doc | What it covers |
|-----|----------------|
| [Overview & the port model](/extension-points/index.md) | Hexagonal design, seam table, reference adapters |
| [LLMProvider](/extension-points/llm-provider.md) | Bring your own model backend |
| [SessionStore & EventLog](/extension-points/session-store.md) | Persistence: in-memory → jsonl → Redis → gRPC driver |
| [SessionLease](/extension-points/session-lease.md) | Distributed locking: flock → k8s → gRPC driver |
| [HookRunner](/extension-points/hook-runner.md) | Custom lifecycle hooks |
| [PermissionPolicy](/extension-points/permission-policy.md) | Custom permission logic |
| [Tool catalog](/extension-points/tool-catalog.md) | Add tools, MCP servers, and skills |
| [Agent definitions](/extension-points/agent-definitions.md) | Named specialist agent profiles |

### Deployment shapes

_From smallest to largest._

| Doc | What it covers |
|-----|----------------|
| [Overview](/deployment/index.md) | Decision tree, trade-off summary |
| [Embed the engine directly](/deployment/embed-engine.md) | Import `engine/`, wire adapters in-process |
| [Run mecated standalone](/deployment/mecated.md) | Single-binary server, gRPC + HTTP-SSE |
| [Cloud-native k8s with mecak8s](/deployment/mecak8s.md) | Redis + k8s lease, disposable pod, storage-free binary |
| [Single-shot CI with mecatequi](/deployment/mecatequi.md) | One prompt → patch + exit code, forge-agnostic |
| [Drive via gRPC / HTTP](/deployment/grpc-http.md) | Remote clients, the driver protocol, streaming |

### Cloud-native kit properties

| Doc | What it covers |
|-----|----------------|
| [Disposable process, externalized state, durable record](/cloud-native-kit.md) | The three properties explained; the four delivery phases |

### API stability

| Doc | What it covers |
|-----|----------------|
| [What's stable and what's not](/api-stability.md) | The seven stable packages, the compat gate, what `engine/adapter/` does NOT promise |
