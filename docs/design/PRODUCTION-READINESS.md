# Production Readiness — status & roadmap

> The **single source of truth for mutable status** (per [ADR 0002](../adr/0002-documentation-lifecycle.md)).
> Design docs record *why* and are frozen; current behaviour lives in
> [`docs/architecture.md`](../architecture.md); shipped/deferred state lives here.
> Goal: a complete, production-ready harness with **no open deferrals** except items
> explicitly marked *Optional feature* (not a production blocker) with a rationale.
> Status legend: ✅ Done · 🔨 In progress · ⛔ Open (to close) · 🟦 Optional feature.

## Design records → status

One row per [design record](./README.md). Status is here; the *why* is in the linked
record; current behaviour is in the linked [architecture](../architecture.md) doc. Per-area production checklists follow below.

| Subsystem | Status | Design record | Architecture |
|---|---|---|---|
| Multi-provider / multi-model | ✅ P0+P1+live listing & metadata · ⛔ disk cache (P2) · ⛔ secrets/OAuth/per-client keys (P3) | [MULTI-PROVIDER.md](../adr/0016-multi-provider.md) | [providers](../architecture/providers.md) |
| OpenAI Responses adapter | ✅ shipped (research brief frozen) | [OPENAI-RESPONSES-API.md](../adr/0017-openai-responses-api.md) | [providers](../architecture/providers.md) |
| Agent definitions (Tier-1 specialists) | ✅ shipped · ⛔ per-agent memory write path · ⛔ `local` tier | [AGENT-DEFINITIONS.md](../adr/0013-agent-definitions.md) | [subagents & teams](../architecture/subagents-and-teams.md) |
| Agent teams (kernel, supervisor, coordination) | ✅ shipped (substrate) · ⛔ mutating-fork join strategies · ⛔ `TeamStore` restart durability | [AGENT-TEAMS-SPIKE.md](../adr/0014-agent-teams.md) | [subagents & teams](../architecture/subagents-and-teams.md) |
| Background subagents + per-child cancel | ✅ shipped · ⛔ session-scoped detach (v2) | [BACKGROUND-SUBAGENTS.md](../adr/0015-background-subagents.md) | [subagents & teams](../architecture/subagents-and-teams.md) |
| Parallelism — fork-join | ✅ shipped · ✅ dirty-aware read-only fork (uncommitted-state overlay) · ⛔ submodule-pointer overlay (best-effort) | [dirty-aware-readonly-fork.md](../adr/0033-dirty-aware-readonly-fork.md) | [parallelism](../architecture/parallelism.md) |
| Worktree binding (mecatui) | ✅ shipped · ⛔ per-worktree trust re-prompt | [worktree-binding.md](../adr/0032-worktree-binding.md) | [parallelism](../architecture/parallelism.md) |
| Memory defaults (on-by-default) | ✅ shipped | [MEMORY-DEFAULTS.md](../adr/0008-memory-on-by-default.md) | [memory](../architecture/memory.md) |
| Tiered memory (tier-0 index + BM25) | ✅ tier-0 index + BM25 `SearchMemory` · ⛔ semantic / embedding recall | [MEMORY-TIERING.md](../adr/0009-tiered-memory.md) · [MEMORY-TIER2.md](../adr/0010-semantic-memory-recall.md) | [memory](../architecture/memory.md) |
| Soul / persona + user-model | ✅ Phase 1 + 2a + 2b + Phase 3 items 1–3 | [SOUL-SPIKE.md](../adr/0011-soul-and-user-model.md) | — |
| Compaction (heuristic + cascade) | ✅ shipped | [COMPACTION.md](../adr/0012-compaction.md) | [context & compaction](../architecture/context-and-compaction.md) |
| System-prompt enhancement | ✅ §7a shipped (`agencyDelta`, tool-discipline hints, `<env>`) · ✅ output-economy default (`defaultTone` rewrite, ADR 0041) | [SYSTEM-PROMPT-RESEARCH.md](../adr/0024-system-prompt-research.md) · [OUTPUT-ECONOMY.md](../adr/0041-output-economy-default-prompt.md) | — |
| Guardrails (LLM-backed tool-content inspection) | ✅ shipped | [0021](../adr/0021-guardrails.md) · [0046](../adr/0046-guardrails-slot-enable.md) · [0049](../adr/0049-guardrails-remove-maxchecks.md) · [0050](../adr/0050-guardrails-remove-maxcontentbytes.md) · [0051](../adr/0051-guardrails-advisory-tui-visibility.md) · [0052](../adr/0052-guardrails-checker-down-toggle.md) · [0053](../adr/0053-guardrails-default-block.md) | [hooks & guardrails](../architecture/hooks-and-guardrails.md) |
| Allow-all / posture ladder | ✅ shipped · ⛔ managed-scope kill-switch · ⛔ `auto`+reviewer posture | [ALLOW-ALL-POSTURE.md](../adr/0022-allow-all-posture.md) | [deployment & hardening](../architecture/deployment-and-hardening.md) |
| Workspace trust | ✅ Phases 0/1/2a/2b/2c · ⛔ Phase 3 (descoped) | [WORKSPACE-TRUST-SPIKE.md](../adr/0023-workspace-trust.md) | [deployment & hardening](../architecture/deployment-and-hardening.md) |
| Driver seams (remote stores/sources) | ✅ Phases A–C2 · ⛔ workspace/FS driver (sketch only) | [DRIVERS.md](../adr/0005-driver-seams.md) | [observability](../architecture/observability.md) |
| Cloud-native arc | ✅ Phases 0–4 (Phase 4 = cross-process single-writer via `port.SessionLease`: memlease / flocklease / gRPC-driver / k8slease backends; byte-identical default when unwired) | [CLOUD-NATIVE.md](../adr/0027-cloud-native.md) | [observability](../architecture/observability.md) |
| Engine as importable module + stability contract + event-sourced rehydration | ✅ shipped | [engine-module.md](../adr/0036-engine-module.md) (own Go module via `go.work`) · [engine-stability-contract.md](../adr/0037-engine-stability-contract.md) (`COMPATIBILITY.md` + api-compat gate) · [event-sourced-rehydration.md](../adr/0038-event-sourced-rehydration.md) (`eventsource.Fold` + `EvUserPrompt`); also covers clock-injectability | [overview](../architecture.md) |
| Diagnostics (injected `port.Diagnostics`) | ✅ shipped | [DIAGNOSTICS.md](../adr/0020-diagnostics.md) | [observability](../architecture/observability.md) |
| Perf observability (live admin/MCP) | ✅ Phases 1+2 · 🟦 Phase 3 (fleet/Pyroscope, optional) | [perf-observability.md](../adr/0018-perf-observability.md) | [observability](../architecture/observability.md) |
| Perf tracking (offline regression gate) | ✅ Phases 0–4 · ⛔ Phases 5–6 (deferred-until-justified) | [perf-tracking.md](../adr/0019-perf-tracking.md) | [observability](../architecture/observability.md) |
| UX discoverability (mecatui) | ✅ shipped | [UX-DISCOVERABILITY.md](../adr/0025-ux-discoverability.md) | — |
| Clipboard image paste (mecatui `ctrl+v`) | ✅ shipped | [CLIPBOARD-IMAGE-PASTE.md](../adr/0026-clipboard-image-paste.md) | — |
| mecatequi (single-shot GitHub Action) | ✅ shipped (v1 forge glue) | [MECATEQUI.md](../adr/0028-mecatequi.md) | [overview](../architecture.md) |
| mecak8s (storage-free k8s-native agent) | ✅ shipped (MVP) · ⛔ CRD/Operator · ⛔ HPA (custom-metrics on active-runs) · ⛔ managed Redis (ElastiCache/MemoryStore — manifest swap, no code) · ⛔ Redis auth · ⛔ fix `mecated`'s unbounded `GracefulStop` (pre-existing, follow-up) | [mecak8s.md](../adr/0048-mecak8s.md) · [MECAK8S-PLAN.md](./MECAK8S-PLAN.md) | [overview](../architecture.md) |
| ACP adapter (editor stdio surface) | ✅ Phase 1+2 + bounded Phase 3 + multimodal shipped · ⛔ Phase 3 long-tail (rule persistence, grep-over-buffers, fs/* on resume) | [0001-acp-adapter.md](../adr/0001-acp-adapter.md) | [api surface](../architecture/api-surface.md) |
| _Historical / retired_ | — | [ARCHITECTURE.md](../adr/0004-v1-architecture.md) · [STEP-CHAIN.md](../adr/0006-v1-step-chain.md) · [TWELVE-PATTERNS-AUDIT.md](../adr/0007-twelve-patterns-audit.md) · [REPOMAP-TREE-SITTER.md](../adr/0029-repomap-tree-sitter.md) | — |

## Security

| Item | Status | Notes / definition of done |
|---|---|---|
| Permission gate (deny→ask→allow, scopes, compound-bash, plan mode) | ✅ | `governance` + `permpolicy`, tested |
| Bash gate substitution/newline-safe | ✅ | hardened post-review |
| Workspace path-escape containment | ✅ | `osfs` via `os.Root` |
| Model-based layer-2 risk classifier | ✅ | `permclassify` (opt-in, monotonic, fail-safe) |
| Hooks (full lifecycle fired, exit 0/2) | ✅ | all 6 phases fire |
| **API authentication + rate limiting** | ✅ | bearer (`--auth-token`/`MECATL_AUTH_TOKEN`, constant-time) + optional TLS/mTLS (`--tls-cert`/`--tls-key`/`--client-ca`) gRPC interceptors + HTTP middleware; per-client + global token-bucket rate limit (`--rate-limit`/`--rate-burst`, bounded/idle-evicting); off-loopback-no-auth WARNING (`internal/adapter/server/authn.go`) |
| OS-level sandbox (process trust) | ⏸️ Deferred | Explicitly deferred (2026-05-29). The `CommandRunner` port is the seam; a Landlock(+seccomp) wrapper drops in later without touching the loop. Bash is also fully optional (shell-less deploys avoid the surface entirely), so this is not a blocker for those. |
| Secrets handling (no key logging) | ✅ | key via env, never logged |
| MCP transport restriction (no stdio) | ✅ | streaming-HTTP only |
| Supply-chain hygiene (per-module vuln scan, dependabot, SHA-pinned actions) | ✅ | per-module `govulncheck` (engine STRICT, no allowlist / root fail-closed reachable-vuln gate via `.github/scripts/govulncheck-gate.go` + a documented 2-CVE docker allowlist reachable only through `internal/` ToolHive); `.github/dependabot.yml` for both modules + github-actions; every action SHA-pinned. Issue #118 |

## Reliability

| Item | Status | Notes |
|---|---|---|
| Stop conditions (turns/tool-calls/failures) | ✅ | enforced + default limits |
| Provider retry/backoff + circuit breaker | ✅ | `llmresilience` |
| Provider error surfaced to client | ✅ | `ResultPayload.Error` |
| **Auto-resume persisted sessions after restart** | ✅ | `GetSession`/`Approve`/`Cancel` fall back to `SessionStore.Load`; persist at create, on entering `awaiting`, and at run end (engine `Store` + `Service.Persist`). With `--store-dir` (jsonlstore) a session survives restart and is loadable — `mecatui` defaults this on at a per-workspace dir under `$XDG_STATE_HOME/mecatui/sessions` (issue #79), `mecated` leaves it off by default. Boundary: an in-flight *stream* is NOT resumed across restart (the `*agent.Run` is in-memory). Since cloud-native Phase 2, an `Approve` against a runless-but-stored session that died while `awaiting` **re-enters the loop at the ask** (`Service.resumeFromAwaiting`); `ErrNoActiveRun` (HTTP 409 / gRPC FailedPrecondition) is returned only for `Approve` against a non-awaiting state and for `Cancel` against any runless session. See `CLOUD-NATIVE.md` Phase 2 |
| Graceful shutdown | ✅ | gRPC GracefulStop + HTTP Shutdown |

## Observability

| Item | Status | Notes |
|---|---|---|
| Prometheus metrics + `/metrics` | ✅ | `telemetry` |
| Per-tool logging + JSONL replay | ✅ | `ToolCallRecorder` (the port formerly named `Logger`) + `jsonlstore` |
| OTel span model (run/turn/tool) | ✅ | `telemetry` |
| OTLP exporter wiring | ✅ | `telemetry.Setup` builds/installs an OTLP TracerProvider; wired in `mecated` via `--otlp-endpoint`/`--otlp-protocol`/`--otlp-insecure` (no-op when empty) |
| **Health endpoints** (`/healthz`,`/readyz`, gRPC health) | ✅ | HTTP `/healthz` (liveness) + `/readyz` (readiness) mounted outside auth/rate-limit; standard `grpc_health_v1` SERVING (`internal/adapter/server/health.go`). `deploy/` can switch TCP→httpGet probes |

## Context management

| Item | Status | Notes |
|---|---|---|
| Compaction seam (`Compactor`) | ✅ | pluggable |
| Single-summary heuristic compaction | ✅ | default |
| Real tokenizer + tiered compaction cascade | ✅ | `TokenCounter` seam (heuristic default + offline `tiktoken` adapter); `CascadeCompactor` snip→strip→collapse→summarize behind the `Compactor` seam, with trigger/target hysteresis (0.8/0.6). Opt-in via `--compaction=cascade`/`--tokenizer=tiktoken`; defaults unchanged |

## Harness patterns (12) — pluggability

| Pattern | Status | Notes |
|---|---|---|
| 1 persistent instructions, 6 plan/act, 7 subagents, 11 single-purpose tools | ✅ | complete + pluggable |
| 2 scoped context assembly | ✅ | `InstructionAssembler` seam (default root) |
| 9 progressive tool disclosure | ✅ | `Disclosable`+`ToolSearch` seam (default off) |
| 10 command risk classification | ✅ | layer-1 rules + layer-2 `permclassify` |
| 12 lifecycle hooks | ✅ | all phases fire |
| 5 progressive compaction | ✅ | `Compactor` seam + `HeuristicCompactor` (default) and `CascadeCompactor` (tiered) |
| 8 fork-join parallelism | ✅ | `tool.WorkspaceForker` + `internal/adapter/forker` (git-worktree/copy isolation) + `agent.NewParallelTool` (parallel isolated branches, join); wired in `mecated` (`--enable-parallel`) |
| **3 tiered memory** | ✅ | `tool.MemoryStore` seam + file-backed `internal/adapter/memory` (Remember/Recall/SearchMemory tools, per-project, conservative descriptions); ON by default per-project (`MEMORY-DEFAULTS.md`) — only consolidation stays opt-in |
| 4 dream/sleep consolidation | ✅ | `internal/adapter/dream` — conservative MemoryStore+LLM consolidator (merge dupes / drop stale, never invents keys, fail-safe), `RunPeriodically`; opt-in via `--memory-consolidate-interval` |

## Deployment

| Item | Status | Notes |
|---|---|---|
| ko build + PSS-restricted manifests | ✅ | `.ko.yaml`, `deploy/` |
| Health probes in manifests | ✅ | `deploy/deployment.yaml` uses `httpGet` probes against `/healthz` (liveness) and `/readyz` (readiness); see `deploy/README.md` |
| Config file (vs flags only) | ✅ | file-based PERMISSION config shipped (issue #13): `internal/adapter/permconfig` loads `.mecatl/settings.yaml` (+ imports Claude-Code `settings.json`), RE-RESOLVED PER SESSION against each session's workspace root via a `permpolicy.RuleResolver`. Tiered scopes (project < user), trust-gated project allows (`--trust-project`), conventional discovery (`--permissions-conventional`, ON), Claude import (`--import-claude-permissions`), explicit files (`--permission-config`). Broader (non-permission) config-file surface remains flags-only |

## Other / future features (Optional — not production blockers)

| Item | Status | Rationale |
|---|---|---|
| Multi-vendor model routing | ✅ | SHIPPED — server-side provider registry + native Anthropic Messages adapter + embedded models.dev catalog + OpenRouter, with per-session provider/model routing and capability intersection. See `docs/adr/0016-multi-provider.md` |
| Repo map (tree-sitter PageRank) | ❌ removed | The Aider-style repo-map tool was **retired and removed** — its WASM tree-sitter binding leaked (~23 MB/session) and hung after ~160 files. See `docs/adr/0029-repomap-tree-sitter.md`. May return later from a clean design |
| Slash commands | ✅ | `prompt.CommandExpander` + `DirCommandExpander` (`.mecatl/commands`/`.claude/commands` templates); `--commands-dir`/`--enable-commands`. (Skills since shipped too: Skill/SkillDraft tools + the `engine/tool` `SkillSource` port + the `/skills` browser.) |
| Live OpenAI validation | ✅ | validated against Sonnet 4.5 via OpenRouter (full tool-calling loop) |
| Fuzz tests (bash splitter, SSE decoder) | ✅ | native Go fuzzers + Taskfile `fuzz` target; security invariants asserted; no crashers found |

## Close-out plan (waves)

All waves bar #2 are complete; #2 (OS sandbox) is the one deliberately-deferred item.

1. ✅ **Server hardening** — auth + rate limit + health endpoints + auto-resume.
2. ⏸️ **OS sandbox** — Landlock(+seccomp) `CommandRunner` adapter. *(deferred — the `CommandRunner` seam is in place; Bash is also fully optional.)*
3. ✅ **Context** — tokenizer + compaction cascade.
4. ✅ **Observability** — OTLP exporter wiring; **fuzz** the parsers.
5. ✅ **Patterns** — fork-join (8); tiered memory (3) [+ optional dream (4)].
6. ✅ **Panel review** each wave; final gauntlet + live e2e.

Optional features (🟦) are left as documented seams unless requested.


---

*Part of the [design docs](./README.md). Related: [TWELVE-PATTERNS-AUDIT.md](../adr/0007-twelve-patterns-audit.md), [ARCHITECTURE.md](../adr/0004-v1-architecture.md).*
