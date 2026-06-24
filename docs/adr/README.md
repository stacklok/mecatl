# Architecture Decision Records

This folder holds mecatl's **decision and design records** as numbered ADRs. Each
records *why* a thing is shaped the way it is, captured at a point in time and then
**frozen** — to change a decision you write a new ADR that supersedes the old one (add
a `Superseded by:` line to the old, a `Supersedes:` line to the new); you don't rewrite
a landed record. Copy [`template.md`](./template.md) to start one. Number monotonically.

Current behaviour lives in [`docs/architecture.md`](../architecture.md) (the living
reference); shipped/deferred status lives in
[PRODUCTION-READINESS.md](../design/PRODUCTION-READINESS.md) (the single tracker).
Documentation/citation conventions are in [`docs/design/README.md`](../design/README.md).

> History: ADRs 0004–0029 were the former `docs/design/*` design records, consolidated
> into the ADR scheme by [ADR 0003](./0003-consolidate-design-records-as-adrs.md).

## Index

### Process & conventions
- [0002 — Documentation lifecycle](./0002-documentation-lifecycle.md)
- [0003 — Consolidate design records as ADRs](./0003-consolidate-design-records-as-adrs.md)

### Architecture & implementation
- [0001 — Agent Client Protocol (ACP) adapter](./0001-acp-adapter.md)
- [0004 — v1 architecture](./0004-v1-architecture.md) *(historical)*
- [0005 — Driver seams](./0005-driver-seams.md)
- [0006 — v1 step-chain](./0006-v1-step-chain.md) *(historical)*
- [0007 — Twelve-patterns audit](./0007-twelve-patterns-audit.md) *(historical)*
- [0027 — Cloud-native arc](./0027-cloud-native.md)
- [0036 — `engine/` is its own Go module (monorepo via `go.work`)](./0036-engine-module.md)
- [0037 — Engine public-API stability contract](./0037-engine-stability-contract.md)
- [0038 — Event-sourced SessionStore rehydration (the reference fold)](./0038-event-sourced-rehydration.md)
- [0044 — Host-supplied askID discriminator (cross-process-reconstructable askID)](./0044-host-supplied-askid-discriminator.md)
- [0048 — mecak8s: Kubernetes-native agent harness (storage-free, managed-service state)](./0048-mecak8s.md)

### Memory & context
- [0008 — Memory on by default](./0008-memory-on-by-default.md)
- [0009 — Tiered memory](./0009-tiered-memory.md)
- [0010 — Semantic memory recall](./0010-semantic-memory-recall.md)
- [0011 — Soul & user-model](./0011-soul-and-user-model.md)
- [0012 — Compaction](./0012-compaction.md)

### Agents, teams & delegation
- [0013 — Agent definitions](./0013-agent-definitions.md)
- [0014 — Agent teams](./0014-agent-teams.md)
- [0015 — Background subagents](./0015-background-subagents.md)
- [0039 — Parallel single-branch auto-merge](./0039-parallel-auto-merge.md)
- [0040 — Writable Subagent mode + serialized merge-back](./0040-writable-subagent-and-serialized-merge.md)
- [0041 — Direct-write writable Subagent (no fork, no merge-back)](./0041-direct-write-subagent.md)

### Providers & APIs
- [0016 — Multi-provider](./0016-multi-provider.md)
- [0017 — OpenAI Responses API](./0017-openai-responses-api.md) *(research)*
- [0030 — Layered model-selection heuristics](./0030-model-selection-heuristics.md)
- [0031 — Semantic subagent model router](./0031-subagent-model-router.md) *(enable model superseded by 0042)*
- [0034 — Extend the model router to team members and Parallel branches](./0034-team-parallel-model-routing.md)
- [0035 — Surface the per-delegation model for ALL children, not just routed ones](./0035-per-delegation-model-surface.md)
- [0042 — Taxonomy-gated subagent model router (enable by config, not a flag)](./0042-taxonomy-gated-model-router.md)

### Performance & diagnostics
- [0018 — Perf observability](./0018-perf-observability.md)
- [0019 — Perf tracking](./0019-perf-tracking.md)
- [0020 — Diagnostics](./0020-diagnostics.md)

### Governance & trust
- [0021 — Guardrails](./0021-guardrails.md)
- [0049 — Remove the guardrails per-session checker call-count cap](./0049-guardrails-remove-maxchecks.md)
- [0050 — Remove the guardrails oversized-content inspection skip](./0050-guardrails-remove-maxcontentbytes.md)
- [0051 — Surface advisory guardrail findings to the TUI](./0051-guardrails-advisory-tui-visibility.md)
- [0052 — Global guardrails checker-down posture toggle](./0052-guardrails-checker-down-toggle.md)
- [0022 — Allow-all posture](./0022-allow-all-posture.md)
- [0023 — Workspace trust](./0023-workspace-trust.md)
- [0024 — System-prompt research](./0024-system-prompt-research.md) *(research)*

### UX & forge
- [0025 — UX discoverability](./0025-ux-discoverability.md)
- [0026 — Clipboard image paste](./0026-clipboard-image-paste.md)
- [0028 — mecatequi (single-shot GitHub Action)](./0028-mecatequi.md)
- [0032 — First-class worktree binding for a session](./0032-worktree-binding.md)

### Retired
- [0029 — Repo-map tree-sitter](./0029-repomap-tree-sitter.md) *(retired)*
