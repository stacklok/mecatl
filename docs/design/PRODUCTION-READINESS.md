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
| Multi-provider / multi-model | ✅ P0+P1+live listing & metadata · ✅ same-provider history carryover (issue #20) · ✅ experimental manual `openai-codex` subscription token (ADR 0215) · ✅ internal opaque credential-store substrate consumed by MCP profiles with local encrypted-file Store and explicit read-only environment Reader (issues #519/#542) · ⛔ disk cache (P2) · ⛔ key acquisition/per-client routing/remote stores or Kubernetes Secret `resourceVersion` CAS backend (P3) | [MULTI-PROVIDER.md](../adr/0016-multi-provider.md) · [0215](../adr/0215-openai-subscription-manual-token.md) · [0218](../adr/0218-credential-store.md) · [0221](../adr/0221-read-only-credential-source.md) | [providers](../architecture/providers.md) · [credential store](../architecture.md#internal-credential-store) |
| MCP authorization-code client | ✅ official Go SDK constrained public profile qualified offline (ADR 0219) · ✅ exact unreleased SDK transport-semantics pin prevents rejected-call replay and includes bounded cancellation/failed-connect cleanup (ADR 0223; migrate only to a verified containing tag) · ✅ adapter controller, mutable/read-only credential sources, loopback runtime, strict operator profiles, three-root runtime wiring, and `mecated mcp login` for mutable local stores (ADRs 0220, 0221, 0112, and 0113) · ✅ hermetic login→two-process warm restore→refresh rotation→reconnect acceptance gate (#524) · ⛔ DCR and enforceable pre-presentation upstream SDK metadata-profile gates; ACP cannot supply OAuth profiles/authorization, and no per-session/inline/discovered OAuth | [0219](../adr/0219-mcp-oauth-sdk-profile.md) · [0220](../adr/0220-mcp-oauth-controller.md) · [0221](../adr/0221-read-only-credential-source.md) · [0112](../adr/0112-mcp-oauth-loopback-runtime.md) · [0113](../adr/0113-operator-mcp-auth-profiles.md) · [0223](../adr/0223-mcp-sdk-transport-error-semantics.md) | [extensibility](../architecture/extensibility.md) |
| Provider-side conversation prompt caching | ✅ shipped: anthropic 4-slot breakpoint budget + uniform TTL, openai/openrouter dialect-gated `prompt_cache_key`/`prompt_cache_retention`/`cache_control`, openaichat dormant-but-tested · ⛔ operator-supplied cache key · ⛔ `settings.yaml` TTL key · ⛔ Anthropic 1h TTL via OpenRouter | [0100](../adr/0100-provider-prompt-caching.md) | [providers](../architecture/providers.md) |
| OpenAI Responses adapter | ✅ shipped (research brief frozen) | [OPENAI-RESPONSES-API.md](../adr/0017-openai-responses-api.md) | [providers](../architecture/providers.md) |
| Agent definitions (Tier-1 specialists) | ✅ shipped · ⛔ per-agent memory write path · ⛔ `local` tier | [AGENT-DEFINITIONS.md](../adr/0013-agent-definitions.md) | [subagents & teams](../architecture/subagents-and-teams.md) |
| Agent teams (kernel, supervisor, coordination) | ✅ shipped (substrate) · ⛔ mutating-fork join strategies · ⛔ `TeamStore` restart durability | [AGENT-TEAMS-SPIKE.md](../adr/0014-agent-teams.md) | [subagents & teams](../architecture/subagents-and-teams.md) |
| Background subagents + per-child cancel | ✅ shipped · ⛔ session-scoped detach (v2) | [BACKGROUND-SUBAGENTS.md](../adr/0015-background-subagents.md) | [subagents & teams](../architecture/subagents-and-teams.md) |
| Background Bash commands (issue #23 commands half) | ✅ shipped (`background: true` on Bash + `BashStatus`, run-scoped) · ⛔ foreground→background mid-flight (Ctrl+B, v2) · ⛔ session-scoped detach (v2) · ⛔ `bashcmd.*` wire events / TUI fleet pane (v2) · ⛔ output paging (v2) | [0201-background-bash.md](../adr/0201-background-bash.md) | [ports](../architecture/ports.md) · [subagents & teams](../architecture/subagents-and-teams.md) |
| Parallelism — fork-join | ✅ shipped · ✅ dirty-aware read-only fork (uncommitted-state overlay) · ⛔ submodule-pointer overlay (best-effort) | [dirty-aware-readonly-fork.md](../adr/0033-dirty-aware-readonly-fork.md) | [parallelism](../architecture/parallelism.md) |
| Worktree binding (mecatui) | ✅ shipped · ⛔ per-worktree trust re-prompt | [worktree-binding.md](../adr/0032-worktree-binding.md) | [parallelism](../architecture/parallelism.md) |
| Memory defaults (on-by-default) | ✅ shipped | [MEMORY-DEFAULTS.md](../adr/0008-memory-on-by-default.md) | [memory](../architecture/memory.md) |
| Tiered memory (tier-0 index + BM25) | ✅ tier-0 index + BM25 `SearchMemory` · ⛔ semantic / embedding recall | [MEMORY-TIERING.md](../adr/0009-tiered-memory.md) · [MEMORY-TIER2.md](../adr/0010-semantic-memory-recall.md) | [memory](../architecture/memory.md) |
| Soul / persona + user-model | ✅ Phase 1 + 2a + 2b + Phase 3 items 1–3 · ✅ optional learning invocation seam (#507) · ✅ live operator profile + reversible memory lifecycle + remote/TUI inspection (#508) · ✅ evidence-backed reflection, durable proposals, bounded standard coordinator, review/auto promotion, explicit API, and TUI review (#509) · ✅ evaluated/versioned agent-owned skills, validated/evaluated automatic activation, synchronous policy pipeline, live atomic catalog, CAS API, and TUI linkage (#510, ADR 0224) | [SOUL-SPIKE.md](../adr/0011-soul-and-user-model.md) · [0106](../adr/0106-optional-learning-seam.md) · [0107](../adr/0107-operator-profile-memory-lifecycle.md) · [0109](../adr/0109-staged-learning-proposals.md) | [memory](../architecture/memory.md) |
| Compaction (heuristic + cascade) | ✅ shipped | [COMPACTION.md](../adr/0012-compaction.md) | [context & compaction](../architecture/context-and-compaction.md) |
| System-prompt enhancement | ✅ §7a shipped (`agencyDelta`, tool-discipline hints, `<env>`) · ✅ one preserved `defaultTone`; output-economy tier/control removed (ADR 0086; parse-compat shim deleted by ADR 0089 — clean break, no window) | [SYSTEM-PROMPT-RESEARCH.md](../adr/0024-system-prompt-research.md) · [reasoning rebalance](../adr/0054-reasoning-rebalance-default-prompt.md) · [control removal](../adr/0086-remove-output-economy-control.md) | — |
| Guardrails (LLM-backed tool-content inspection) | ✅ shipped | [0021](../adr/0021-guardrails.md) · [0046](../adr/0046-guardrails-slot-enable.md) · [0049](../adr/0049-guardrails-remove-maxchecks.md) · [0050](../adr/0050-guardrails-remove-maxcontentbytes.md) · [0051](../adr/0051-guardrails-advisory-tui-visibility.md) · [0052](../adr/0052-guardrails-checker-down-toggle.md) · [0053](../adr/0053-guardrails-default-block.md) | [hooks & guardrails](../architecture/hooks-and-guardrails.md) |
| Plan-approval gate (explore-plan-act, ADR 0007 pattern 6) | ✅ shipped — PresentPlan tool → `PlanOriginated` ask → `ApprovePlan` RPC → `StopPlanApproved` + mode flip; opt-in `--plan-mode-auto-approve` | [0069](../adr/0069-plan-approval-gate.md) | [agent loop](../architecture/agent-loop.md) |
| Allow-all / posture ladder | ✅ shipped · ⛔ managed-scope kill-switch · ⛔ `auto`+reviewer posture | [ALLOW-ALL-POSTURE.md](../adr/0022-allow-all-posture.md) | [deployment & hardening](../architecture/deployment-and-hardening.md) |
| Workspace trust | ✅ Phases 0/1/2a/2b/2c · ⛔ Phase 3 (descoped) | [WORKSPACE-TRUST-SPIKE.md](../adr/0023-workspace-trust.md) | [deployment & hardening](../architecture/deployment-and-hardening.md) |
| Driver seams (remote stores/sources) | ✅ Phases A–C2 · ⛔ workspace/FS driver (sketch only) | [DRIVERS.md](../adr/0005-driver-seams.md) | [observability](../architecture/observability.md) |
| Cloud-native arc | ✅ Phases 0–4 (Phase 4 = cross-process single-writer via `port.SessionLease`: memlease / flocklease / gRPC-driver / k8slease backends; byte-identical default when unwired) · ✅ Phase 5 = scheduled tasks (`port.ScheduleStore` + `internal/adapter/scheduler` + `buildScheduler`; at-most-once via claim-before-fire; ON by default on a schedule-capable store, `--no-scheduler` opt-out — ADR 0073) · ✅ Phase 2a wire API shipped (issue #232: gRPC `ScheduleService` 10 RPCs + peer REST routes + `EvScheduleFired`/`Skipped`/`Failed` events; `FireNow`/`GetFire`/`ListFires` pull-only outcome channel) · ✅ Phase 2b metrics shipped (the declarative settings.yaml `schedules:` block + the `mecated schedules` CLI it also shipped were REMOVED by ADR 0073 — the in-chat `Schedule` tool is the management surface) · ✅ Phase 3a shipped (issue #234: `mecatui /schedule` overlay — list/inspect/pause/resume/fire-now/delete; NL→cron deferred to v2) · ✅ Phase 3b shipped (mecatui in-overlay Create form + NL→cron client-side compiler `cmd/mecatui/schedparse`) · ✅ `sched--` GC retention family shipped (ADR 0059 decision #7 Phase-2: `WithSessionID` override on `CreateSessionWithProfile` mints a `sched--`-prefixed fire-session id; `ScheduleFireRetention` age pass + `--schedule-fire-retention` flag, 7d default whenever unset — the scheduler is on by default) · ✅ Phase 2c shipped (issue #236: one-shot crash-loss retry `OneShotRetry`/`OneShotMaxRetries`/`OneShotRetryCount` via the optional `ScheduleOneShotReArmer` interface + carried-context `CarryContext` fenced-untrusted preamble via `agent.FenceUntrusted`/`NeutraliseFraming`) · ✅ driver `ScheduleStoreService` shipped (issue #257: `contracts/proto/mecatl/driver/v1/schedule_store.proto` + `internal/adapter/grpcdriver/schedulestore.go` client/server wrappers over `port.ScheduleStore` + `port.ScheduleOneShotReArmer`; `--schedule-store-url` INDEPENDENT override replacing `ScheduleStore()` discovery; validated by the `scheduleconformance`-over-bufconn suite) · ⛔ remaining v2 deferred items (issue #236: `context_from`, event/webhook triggers, per-fire egress allowlist, wake-on-change) | [CLOUD-NATIVE.md](../adr/0027-cloud-native.md) · [SCHEDULED-TASKS.md](../adr/0059-scheduled-tasks.md) | [scheduled tasks](../architecture.md#scheduled-tasks) |
| Engine as importable module + stability contract + event-sourced rehydration | ✅ shipped | [engine-module.md](../adr/0036-engine-module.md) (own Go module via `go.work`) · [engine-stability-contract.md](../adr/0037-engine-stability-contract.md) (`COMPATIBILITY.md` + api-compat gate) · [event-sourced-rehydration.md](../adr/0038-event-sourced-rehydration.md) (`eventsource.Fold` + `EvUserPrompt`); also covers clock-injectability | [overview](../architecture.md) |
| Diagnostics (injected `port.Diagnostics`) | ✅ shipped | [DIAGNOSTICS.md](../adr/0020-diagnostics.md) | [observability](../architecture/observability.md) |
| Perf observability (live admin/MCP) | ✅ Phases 1+2 · 🟦 Phase 3 (fleet/Pyroscope, optional) | [perf-observability.md](../adr/0018-perf-observability.md) | [observability](../architecture/observability.md) |
| Perf tracking (offline regression gate) | ✅ Phases 0–4 · ⛔ Phases 5–6 (deferred-until-justified) | [perf-tracking.md](../adr/0019-perf-tracking.md) | [observability](../architecture/observability.md) |
| UX discoverability (mecatui) | ✅ shipped | [UX-DISCOVERABILITY.md](../adr/0025-ux-discoverability.md) | — |
| Clipboard image paste (mecatui `ctrl+v`) | ✅ shipped | [CLIPBOARD-IMAGE-PASTE.md](../adr/0026-clipboard-image-paste.md) | — |
| mecatequi (single-shot GitHub Action) | ✅ shipped (v1 forge glue) · ✅ OPT-IN OTLP push telemetry (metrics + traces, flush-before-exit, ADR 0098) | [MECATEQUI.md](../adr/0028-mecatequi.md) · [0098](../adr/0098-headless-telemetry.md) | [overview](../architecture.md) |
| mecak8s (storage-free k8s-native agent) | ✅ shipped (MVP) · ✅ OPT-IN `/metrics` loopback scrape + OTLP push (ADR 0098) · ⛔ CRD/Operator · ⛔ HPA (custom-metrics on active-runs) · ⛔ managed Redis (ElastiCache/MemoryStore — manifest swap, no code) · ⛔ Redis auth · ⛔ fix `mecated`'s unbounded `GracefulStop` (pre-existing, follow-up) | [mecak8s.md](../adr/0048-mecak8s.md) · [0098](../adr/0098-headless-telemetry.md) · [MECAK8S-PLAN.md](./MECAK8S-PLAN.md) | [overview](../architecture.md) |
| ACP adapter (editor stdio surface) | ✅ Phase 1+2 + bounded Phase 3 + multimodal shipped · ⛔ Phase 3 long-tail (rule persistence, grep-over-buffers, fs/* on resume) | [0001-acp-adapter.md](../adr/0001-acp-adapter.md) | [api surface](../architecture/api-surface.md) |
| Conversation fork (peer session from a history snapshot) | ✅ shipped · ✅ effort override (mid-conversation effort switch, keeps the transcript — [0068](../adr/0068-effort-change-via-fork.md)) · ⛔ cross-provider/model fork (v2: replay-blob stripping) · ⛔ workspace-branching fork · ⛔ fork-from-event-log-at-arbitrary-point · ⛔ fork lineage (`forked_from` label) | [0065-conversation-fork.md](../adr/0065-conversation-fork.md) | [overview](../architecture.md) |
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
| **OIDC caller identity** | ✅ attribution via shipped `toolhive-core/authn` v0.0.39; ✅ default 1h JWKS-staleness bound (503 after an unavailable refresh); ⛔ per-caller authorization/isolation (#368); ⛔ per-token revocation | `--oidc-max-jwks-staleness=0` deliberately restores unbounded cached-key availability; JWKS cache is process-local and reconstructible, never persisted. |
| OS-level sandbox (process trust) | ⏸️ Deferred | Explicitly deferred (2026-05-29). The `CommandRunner` port is the seam; a Landlock(+seccomp) wrapper drops in later without touching the loop. Bash is also fully optional (shell-less deploys avoid the surface entirely), so this is not a blocker for those. |
| Secrets handling (no key logging) | ✅ | key via env, never logged |
| MCP transport restriction (no stdio) | ✅ | streaming-HTTP only; standalone SSE GET enabled for server-initiated notifications (ADR 0057) |
| Supply-chain hygiene (per-module vuln scan, dependabot, SHA-pinned actions) | ✅ | per-module `govulncheck` (engine STRICT, no allowlist / root fail-closed reachable-vuln gate via `.github/scripts/govulncheck-gate.go` + a documented 2-CVE docker allowlist reachable only through `internal/` ToolHive); `.github/dependabot.yml` for both modules + github-actions; every action SHA-pinned. Issue #118 |

## Reliability

| Item | Status | Notes |
|---|---|---|
| Stop conditions (turns/tool-calls/failures) | ✅ | enforced + default limits |
| Provider semantic retry/backoff + circuit breaker | ✅ | `llmresilience`: tentative non-text chunks buffer until meaningful text or clean completion; only typed retryable+precommit transparently replays. Typed disposition/progress, prompt-free failed-step retry, persisted retry intent, and one bounded mecatui auto-retry shipped in issue #409 / ADR 0239 |
| Provider error surfaced to client | ✅ | `ResultPayload.Error` |
| **Auto-resume persisted sessions after restart** | ✅ | `GetSession`/`Approve`/`Cancel` fall back to `SessionStore.Load`; persist at create, on entering `awaiting`, and at run end (engine `Store` + `Service.Persist`). With `--store-dir` (jsonlstore) a session survives restart and is loadable — `mecatui` defaults this on at a per-workspace dir under `$XDG_STATE_HOME/mecatui/sessions` (issue #79), `mecated` leaves it off by default. Boundary: an in-flight *stream* is NOT resumed across restart (the `*agent.Run` is in-memory). Since cloud-native Phase 2, an `Approve` against a runless-but-stored session that died while `awaiting` **re-enters the loop at the ask** (`Service.resumeFromAwaiting`); `ErrNoActiveRun` (HTTP 409 / gRPC FailedPrecondition) is returned only for `Approve` against a non-awaiting state and for `Cancel` against any runless session. See `CLOUD-NATIVE.md` Phase 2 |
| Graceful shutdown | ✅ | gRPC GracefulStop + HTTP Shutdown |
| MCP client reconnect on connection drop | ✅ | client-side; one bounded reconnect/retry for concrete session loss only; structured JSON-RPC 400/404 and HTTP 429/502/503/504 remain one-call failures and are never replayed (ADRs 0056, 0114); consumes `notifications/*` over the standalone SSE stream (ADR 0057) |

## Observability

| Item | Status | Notes |
|---|---|---|
| Prometheus metrics + `/metrics` | ✅ | `telemetry` |
| Per-tool logging + JSONL replay | ✅ | `ToolCallRecorder` (the port formerly named `Logger`) + `jsonlstore` |
| OTel span model (run/turn/tool) | ✅ | `telemetry` |
| OTLP exporter wiring | ✅ | `telemetry.Setup` builds/installs an OTLP TracerProvider + an optional OTLP METRICS push `PeriodicReader`; wired in `mecated` via `--otlp-endpoint`/`--otlp-protocol`/`--otlp-insecure` (no-op when empty); wired in the headless mains (mecatequi push, mecak8s push + `/metrics` scrape) via `internal/cliconfig.HeadlessTelemetry` (ADR 0098) |
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
| 8 fork-join parallelism | ✅ | `tool.EnvironmentForker` + `internal/adapter/forker` (git-worktree/copy isolation) + `agent.NewParallelTool` (parallel isolated branches, join); wired in `mecated` (`--enable-parallel`) |
| **3 tiered memory** | ✅ | unchanged `tool.MemoryStore` base + optional versioned `MemoryLifecycleStore`; portable project/user Remember/Recall/Search and conditional Inspect/Forget/Undo; flocked single-document lazy migration/history/tombstones; live per-request operator profile; additive remote lifecycle RPCs and read-only TUI detail (ADR 0107) |
| 4 dream/sleep consolidation | ✅ | `internal/adapter/dream` — strict plans contain exact-duplicate and synthesized-replacement operations over existing keys. Automatic schedules remain off by default and apply only byte-identical exact duplicates through the local adapter's atomic retirement operation. The capability-gated mecatui `/dream` workflow separately spends one provider call to generate an inspectable plan, then applies or dismisses the whole retained plan; approved synthesis atomically rewrites its displayed survivor and tombstones its displayed sources per operation. Independent operations may yield a partial receipt. Plans are bounded, TTL-limited, process-local, lost on restart/wrong replica, and unavailable under ownership enforcement; no recall-usage telemetry is collected. |

## Deployment

| Item | Status | Notes |
|---|---|---|
| ko build + PSS-restricted manifests | ✅ | `.ko.yaml`, `deploy/` |
| Health probes in manifests | ✅ | `deploy/deployment.yaml` uses `httpGet` probes against `/healthz` (liveness) and `/readyz` (readiness); see `deploy/README.md` |
| Config file (vs flags only) | ✅ | file-based PERMISSION config shipped (issue #13): `internal/adapter/permconfig` loads `.mecatl/settings.yaml` (+ imports Claude-Code `settings.json`), RE-RESOLVED PER SESSION against each session's workspace root via a `permpolicy.RuleResolver`. Tiered scopes (project < user), trust-gated project allows (`--trust-project`), conventional discovery (`--permissions-conventional`, ON), Claude import (`--import-claude-permissions`), explicit files (`--permission-config`). A STRICT, VERSIONED daemon topology file (`internal/adapter/daemonconfig`, `daemon.yaml`) is loaded ONLY when `mecated serve --config PATH` is explicit — no auto-load; scaffolded/validated by `mecated config daemon init`/`validate` (issue #338, ADR 0088); carries no auth token (env/CLI only). Other (non-permission, non-topology) config-file surfaces remain flags-only |

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
