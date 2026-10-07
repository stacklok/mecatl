# Reading map

These pages explain how Mecatl works today, for people building a mental model of the
code. They describe current behavior only; designs that aren't built yet are in
[drafts](drafts/README.md). For where to change what, read the `AGENTS.md` file
in the directory you're working in.

## Contributors

Start with the foundations, in order:

| Step | Page | What it answers |
| --- | --- | --- |
| 1 | [Architecture overview](architecture.md) | What Mecatl is, its layers, how one prompt flows, where code lives, and the platform principles |
| 2 | [Domain model](architecture/domain-model.md) | The `Session` aggregate, its state machine, events, and why tool history must stay paired |
| 3 | [Ports](architecture/ports.md) | The interfaces the loop consumes, the tool contract, and the `Environment` seam |
| 4 | [Agent loop](architecture/agent-loop.md) | How a run is driven, how tool calls are dispatched, and how approvals pause and resume |

Then read the chapters for the area you're changing. Each stands alone after the
foundations.

| Page | What it answers |
| --- | --- |
| [Context and compaction](architecture/context-and-compaction.md) | How a long run stays inside the context window |
| [Subagents and teams](architecture/subagents-and-teams.md) | How work is delegated to child loops, fork-join, and teams, and what children may not do |
| [Governance](architecture/governance.md) | How permissions, posture, workspace trust, environment scrubbing, hooks, and guardrails decide what runs |
| [Providers](architecture/providers.md) | How sessions bind to providers and models, and how the model router decides |
| [Observability](architecture/observability.md) | Diagnostics, audit, and events; persistence and the session lease; provider resilience |
| [API surface](architecture/api-surface.md) | How gRPC, HTTP/SSE, and ACP share one service; watches, scheduled tasks, and clients |
| [Deployment and hardening](architecture/deployment-and-hardening.md) | Edge authentication, caller identity and ownership, credentials, and deployment shapes |
| [Extensibility](architecture/extensibility.md) | MCP and the broker, skills and slash commands, and web retrieval |
| [Memory](architecture/memory.md) | Cross-session memory, the user model, and evidence-backed reflection |
| [MicroVM environments](architecture/microvm-environments.md) | Server-owned placement and the local microVM runtime |

Also for contributors:

- [Developing the `mecatui` terminal UI](tui.md): client boundaries and UI conventions.
- [Performance regression tracking](perf-tracking.md) and
  [measuring performance](perf-measurement-survey.md): benchmarks, gates, and live
  diagnosis.
- [Qualify local microVM source builds](architecture/microvm-environments.md#qualify-local-microvm-source-builds):
  contributor procedure for the macOS development path.
- The [formal domain model](architecture/mecatl.modelith.md), generated from its `.yaml`.
- [User-docs authoring contract](../user-docs/_README.md) and
  [style guide](../user-docs/_STYLE.md), for public documentation.

## Operators

Start with [Deploy and operate Mecatl](../user-docs/operating/index.md). Use the
[lightweight server guide](../user-docs/operating/mecated.md), then the
[Kubernetes tutorial](../user-docs/operating/kubernetes.md) and
[shared deployment guide](../user-docs/operating/mecak8s.md). The
[capability matrix](../user-docs/features/get-oriented/capability-matrix.md)
compares availability; public operator pages own procedures and prerequisites.

## Library consumers

| Step | Page |
| --- | --- |
| 1 | [Building on Mecatl](../user-docs/building/index.md) |
| 2 | [`engine/session`](../engine/session), the domain entry point |
| 3 | [Extension points](../user-docs/building/go/extension-points/index.md) |
| 4 | [`engine/COMPATIBILITY.md`](../engine/COMPATIBILITY.md), the stability contract |
| Adapters | [`adapters/COMPATIBILITY.md`](../adapters/COMPATIBILITY.md): published stores and the remote driver |
