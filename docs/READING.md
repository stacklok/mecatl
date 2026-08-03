# mecatl — Progressive reading map

This is the canonical reader map for this repo. It is a **self-contained index**
organized by audience. Pick the row that fits. Every link points to an existing
living guide (the code as it exists today) or a reference.

**Living docs** describe **current behavior** (architecture pages, the usage guide).
**`docs/design/IMPLEMENTATION-NOTES.md`** is the dense per-subsystem reference.
**ADRs** in `docs/adr/` are **frozen rationale on demand** — reach for them to
understand a decision's *why*, never as the primary introduction to a feature.

---

## Contributor / agent (core path)

### Foundation spine (read in order)

| Step | Page | What it answers |
| --- | --- | --- |
| 1 | [Architecture overview](architecture.md) | What is mecatl? How is it layered (hexagonal/DDD)? What lives where? |
| 2 | [The domain model](architecture/domain-model.md) | What are the core entities — Session, Conversation, Events, ToolCall, ToolResult? How does the state machine work? |
| 3 | [The ports](architecture/ports.md) | What seams does the loop consume (`LLMProvider`, `SessionStore`, `PermissionPolicy`, …)? What is the tool contract and the FS seam? |
| 4 | [The agent loop](architecture/agent-loop.md) | How does `Engine.Run` work? What is the drive algorithm, dispatch (read-parallel / mutate-serial), and permission pause/resume? |

### Topic branches

After the foundation spine, each architecture page stands alone — its prerequisite
is listed, and its follow-on reading is noted. Read any that cover your area.

| Page | What it answers | Prerequisite |
| --- | --- | --- |
| [Hooks & guardrails](architecture/hooks-and-guardrails.md) | How do lifecycle hooks fire (SessionStart, PreToolUse, …)? How do model-backed guardrail checks work? | [agent loop](architecture/agent-loop.md) |
| [Subagents & teams](architecture/subagents-and-teams.md) | How does the Subagent tool delegate to child loops? What per-call knobs exist (fork, read-write, background, resume)? How do agent teams coordinate? | [agent loop](architecture/agent-loop.md) |
| [Providers](architecture/providers.md) | How does the OpenAI adapter translate requests? How does multi-provider routing + model resolution work? What is the semantic model router? | [ports](architecture/ports.md) |
| [The API surface](architecture/api-surface.md) | What gRPC, HTTP/SSE, and ACP surfaces expose the loop? What is the engine-as-library stability contract? | [agent loop](architecture/agent-loop.md) |
| [Context & compaction](architecture/context-and-compaction.md) | How does the token budget + compaction cascade keep a long run inside the context window? | [agent loop](architecture/agent-loop.md) |
| [Memory](architecture/memory.md) | How does cross-session recall (Remember/Recall/SearchMemory) work? What is the user model? | [agent loop](architecture/agent-loop.md) |
| [Observability](architecture/observability.md) | What telemetry, persistence, and reliability seams exist? How do the event log, session lease, and remote drivers work? | [ports](architecture/ports.md) |
| [Parallelism](architecture/parallelism.md) | How does fork-join parallelism (the Parallel tool) work? How are team-member workspaces isolated? What is worktree binding? | [subagents & teams](architecture/subagents-and-teams.md) |
| [Extensibility](architecture/extensibility.md) | What MCP, skills, progressive disclosure, and engine-as-library seams exist? | [ports](architecture/ports.md) |
| [Deployment & hardening](architecture/deployment-and-hardening.md) | How is the server hardened (auth, rate limiting, health, graceful shutdown)? How do workspace trust, the posture ladder, and permission/bash governance work? | [API surface](architecture/api-surface.md) |

### After the architecture pages

- [Implementation notes](design/IMPLEMENTATION-NOTES.md) — the dense per-subsystem companion to the architecture pages (a reference, not a narrative).
- [ADR index](adr/README.md) — the frozen *why* archive; reach for it on demand to understand a decision's rationale.
- [Production readiness tracker](design/PRODUCTION-READINESS.md) — the live shipped/deferred status ledger.

---

## Operator

| Step | Page |
| --- | --- |
| 1 | [Project README](../README.md) — feature overview and quick start |
| 2 | [Install](usage/install.md) |
| 3 | [Quickstart](usage/quickstart.md) — the 60-second demo |
| 4 | [Running `mecated`](usage/mecated.md) — the server |
| Then | Any topic branch from the [usage guide](usage.md): guardrails, model routing, workspace trust, skills/soul/user-model, mecak8s, configuration, permissions, hooks, mecatequi CI, the gRPC/HTTP APIs, troubleshooting |

---

## Library consumer

| Step | Page |
| --- | --- |
| 1 | [User docs intro](https://github.com/stacklok/mecatl/blob/main/user-docs/intro.md) |
| 2 | [`engine/session`](../engine/session) — the domain entry point |
| 3 | [Extension points](https://github.com/stacklok/mecatl/blob/main/user-docs/extension-points/index.md) |
| 4 | [`engine/COMPATIBILITY.md`](../engine/COMPATIBILITY.md) — the stability contract |
