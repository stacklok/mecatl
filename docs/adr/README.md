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
- [0072 — The acceptance-plan spine](./0072-acceptance-plan-spine.md)

### Architecture & implementation
- [0001 — Agent Client Protocol (ACP) adapter](./0001-acp-adapter.md)
- [0004 — v1 architecture](./0004-v1-architecture.md) *(historical)*
- [0005 — Driver seams](./0005-driver-seams.md) *(skill-asset materialization/read-root decision superseded by 0108)*
- [0006 — v1 step-chain](./0006-v1-step-chain.md) *(historical)*
- [0007 — Twelve-patterns audit](./0007-twelve-patterns-audit.md) *(historical)*
- [0027 — Cloud-native arc](./0027-cloud-native.md)
- [0104 — Session families get a bounded, injective, non-reversible physical name](./0104-session-family-physical-naming.md)
- [0217 — Session discovery uses durable kind metadata and an authoritative transcript](./0217-session-discovery-continuation.md)
- [0226 — Session storage separates current state, indexed metadata, and maintenance](./0226-session-storage-maintenance.md)
- [0239 — Semantic stream retry and failed-step retry transport](./0239-semantic-stream-retry.md)
- [0243 — Local JSONL durability boundaries](./0243-jsonl-durability.md)
- [0245 — Safe build diagnostics](./0245-safe-build-diagnostics.md)
- [0229 — Redis migration uses fenced renewable ownership and indexed coverage](./0229-redis-migration-fencing.md) *(superseded by 0230)*
- [0230 — Redis migration ownership and coverage are proved before mutation](./0230-redis-migration-atomic-ownership-and-coverage.md) *(superseded by 0231)*
- [0231 — Redis readiness requires exact owner-index coverage](./0231-redis-owner-index-exact-coverage.md)
- [0207 — Operator-owned exact context-window overrides](./0207-context-window-overrides.md)
- [0209 — Attribute schedule origins through the run context](./0209-schedule-origin-run-context.md) *(supersedes ADR 0075's origin-binding mechanism)*
- [0218 — Internal encrypted credential-store substrate](./0218-credential-store.md)
- [0219 — Qualify the official MCP SDK authorization-code profile](./0219-mcp-oauth-sdk-profile.md)
- [0220 — Adapter-local MCP OAuth controller](./0220-mcp-oauth-controller.md)
- [0221 — Read-only environment credential source and OAuth refresh posture](./0221-read-only-credential-source.md)
- [0112 — Host-owned loopback MCP OAuth login](./0112-mcp-oauth-loopback-runtime.md)
- [0113 — Operator MCP authentication profiles and explicit login](./0113-operator-mcp-auth-profiles.md)
- [0223 — Pin MCP transport-error semantics that do not replay rejected calls](./0223-mcp-sdk-transport-error-semantics.md)
- [0232 — Steer-while-running: inject a user message into an in-flight run](./0232-steer-while-running.md)
- [0251 — Multimodal steer preserves prompt content](./0251-multimodal-steer.md)
- [0252 — HTTP steer endpoint: `POST /v1/sessions/{id}/steer`](./0252-http-steer-endpoint.md)
- [0036 — `engine/` is its own Go module (monorepo via `go.work`)](./0036-engine-module.md)
- [0037 — Engine public-API stability contract](./0037-engine-stability-contract.md)
- [0038 — Event-sourced SessionStore rehydration (the reference fold)](./0038-event-sourced-rehydration.md)
- [0043 — Ephemeral turn-0 instruction fragments](./0043-ephemeral-turn0-instruction-fragments.md)
- [0044 — Host-supplied askID discriminator (cross-process-reconstructable askID)](./0044-host-supplied-askid-discriminator.md)
- [0047 — Absolute path resolution inside the workspace root](./0047-absolute-path-resolution.md) *(skill read-root carve-out superseded by 0108)*
- [0108 — Read skill assets on demand by logical name](./0108-on-demand-logical-skill-assets.md) *(supersedes only ADR 0005/0047's skill-asset materialization/read-root decisions)*
- [0048 — mecak8s: Kubernetes-native agent harness (storage-free, managed-service state)](./0048-mecak8s.md)
- [0233 — Secure external Redis on the shared connection layer](./0233-secure-external-redis.md)
- [0237 — Listener-scoped workspace authority](./0237-listener-scoped-workspace-authority.md)
- [0260 — Remote mecatui OIDC client authentication](./0260-remote-mecatui-oidc.md) *(supersedes 0253–0256)*
- [0258 — Bounded scoped HTTPS keep-alive reuse for OIDC](./0258-bounded-scoped-https-keepalive-oidc.md) *(supersedes ADR 0235's keep-alive policy and ADR 0260's credential-recovery classification only)*
- [0253 — Activity-gated remote OIDC refresh](./0253-activity-gated-remote-oidc-refresh.md) *(superseded by 0260)*
- [0254 — Recover remote TUI authentication without replaying ownership-ambiguous work](./0254-tui-reauth-owner-recovery.md) *(superseded by 0260)*
- [0255 — Safe target logout for remote mecatui OIDC](./0255-remote-mecatui-logout.md) *(superseded by 0260)*
- [0256 — Remote mecatui OIDC client login](./0256-remote-mecatui-oidc-login.md) *(superseded by 0260)*
- [0257 — Remote logout provider budget](./0257-remote-mecatui-logout-budget.md) *(supersedes ADR 0260's logout provider budget only)*
- [0059 — Scheduled tasks](./0059-scheduled-tasks.md)
- [0065 — Conversation fork: peer session from a history snapshot](./0065-conversation-fork.md)
- [0073 — Schedule tool](./0073-schedule-tool.md)
- [0074 — Many loops per server; scheduler shape](./0074-many-loops-scheduler-shape.md)
- [0075 — Fire-result delivery: a scheduled fire reports back into the originating chat](./0075-fire-result-delivery.md) *(origin-binding mechanism superseded by 0209)*
- [0209 — Attribute schedule origins through the run context](./0209-schedule-origin-run-context.md) *(supersedes 0075's wrapper-state origin binding)*
- [0097 — Scheduled fires are observable in-flight: a first-class persisted lifecycle stage](./0097-scheduled-fire-inflight-state.md)
- [0096 — mecatui live-feed reconnect: client-owned backoff + durable catch-up, no server cursor](./0096-live-feed-reconnect.md)
- [0103 — mecatui seed prompt (`-p`/`--prompt`, `--prompt-file`)](./0103-mecatui-seed-prompt.md)
- [0076 — The schedule manager is store-shaped and pre-Service; the shared catalog carries the Schedule tool](./0076-schedule-shared-catalog.md)
- [0081 — `RulesSource` port for `.claude/rules` discovery](./0081-rules-source-port.md)
- [0203 — Neutral permanent-provider-error signal (`port.PermanentError` → `ResultPayload.Permanent` → `EvRecoverNotice`)](./0203-permanent-provider-error-signal.md)
- [0099 — External transcript import (`mecated import`)](./0099-external-transcript-import.md)

### Memory & context
- [0008 — Memory on by default](./0008-memory-on-by-default.md)
- [0009 — Tiered memory](./0009-tiered-memory.md)
- [0010 — Semantic memory recall](./0010-semantic-memory-recall.md)
- [0011 — Soul & user-model](./0011-soul-and-user-model.md)
- [0012 — Compaction](./0012-compaction.md)
- [0259 — Count the full request and expose durable manual compaction](./0259-full-request-and-manual-compaction.md) *(supersedes ADR 0025's `/compact` deferral only)*
- [0106 — Optional completion-learning seam](./0106-optional-learning-seam.md)
- [0107 — Operator-profile memory lifecycle](./0107-operator-profile-memory-lifecycle.md)
- [0109 — Evidence-backed reflection and durable staged learning](./0109-staged-learning-proposals.md)
- [0227 — Dream consolidation safety boundary](./0227-dream-consolidation-safety-boundary.md)
- [0228 — Manual dream review](./0228-manual-dream-review.md)
- [0110 — Evaluated, versioned agent-owned skills](./0110-evaluated-agent-owned-skills.md) *(superseded by 0111)*
- [0111 — Hardened publication and recovery for agent-owned skills](./0111-hardened-agent-owned-skill-publication.md)

### Core tools & shell
- [0201 — Background Bash commands](./0201-background-bash.md)
- [0208 — Execution environments and version-aware file mutation](./0208-execution-environment.md) *(runtime-seam deferral superseded by 0211; version protocol authoritative)*
- [0211 — Execution-environment runtime seam](./0211-execution-environment-runtime-seam.md) *(supersedes 0208 decisions 1–3; phase-3 persistence deferral superseded by 0214)*
- [0214 — Execution-environment persistence and reattachment](./0214-environment-persistence.md) *(supersedes 0211 decision 6 only)*

### Agents, teams & delegation
- [0013 — Agent definitions](./0013-agent-definitions.md)
- [0014 — Agent teams](./0014-agent-teams.md)
- [0015 — Background subagents](./0015-background-subagents.md)
- [0033 — Dirty-aware read-only fork (uncommitted-state overlay)](./0033-dirty-aware-readonly-fork.md)
- [0039 — Parallel single-branch auto-merge](./0039-parallel-auto-merge.md)
- [0040 — Writable Subagent mode + serialized merge-back](./0040-writable-subagent-and-serialized-merge.md)
- [0058 — Writable named-specialist Subagent (`mode:"read-write"` + `agent`)](./0058-writable-named-specialist-subagent.md)
- [0066 — Route unpinned agent-defs and writable explorers; explicit `inherit` is the pin](./0066-route-unpinned-and-writable-delegations.md)
- [0077 — Direct-write writable Subagent (no fork, no merge-back)](./0077-direct-write-subagent.md)
- [0079 — Converge delegation observability on two tiers (bounded previews for Subagent/Parallel)](./0079-delegation-observability-convergence.md)
- [0200 — A failed delegated child is resumable (Recover, not refuse)](./0200-resume-a-failed-subagent.md)
- [0242 — Route unpinned writable named specialists](./0242-route-unpinned-writable-named-specialists.md)
- [0248 — SDK compatibility discovery and the typed error contract](./0248-sdk-compatibility-and-error-contract.md)
- [0249 — Durable run identity: a host-minted `run_id`](./0249-durable-run-identity.md)
- [0250 — Durable cursors and the session watch transport](./0250-durable-cursors-and-watch.md)

### Providers & APIs
- [0016 — Multi-provider](./0016-multi-provider.md)
- [0017 — OpenAI Responses API](./0017-openai-responses-api.md) *(research)*
- [0030 — Layered model-selection heuristics](./0030-model-selection-heuristics.md)
- [0031 — Semantic subagent model router](./0031-subagent-model-router.md) *(enable model superseded by 0042)*
- [0034 — Extend the model router to team members and Parallel branches](./0034-team-parallel-model-routing.md)
- [0035 — Surface the per-delegation model for ALL children, not just routed ones](./0035-per-delegation-model-surface.md)
- [0042 — Taxonomy-gated subagent model router (enable by config, not a flag)](./0042-taxonomy-gated-model-router.md)
- [0064 — Auto-detect the ToolHive LLM gateway proxy as a native provider](./0064-toolhive-llm-gateway-provider.md)
- [0102 — ToolHive LLM gateway DIRECT mode (in-process OIDC token injection)](./0102-toolhive-direct-mode.md)
- [0067 — OpenAI Chat Completions adapter (OpenCode Go provider)](./0067-openai-chat-completions-adapter.md)
- [0068 — Change reasoning effort via conversation fork (keep the transcript)](./0068-effort-change-via-fork.md)
- [0071 — Seamless model switch: always keep the conversation](./0071-seamless-model-switch.md)
- [0083 — Routing reason on delegation-start events](./0083-routing-reason-on-delegation-start.md)
- [0093 — Ship the real LLM provider adapters as opt-in Go modules under provider/](./0093-provider-modules.md)
- [0210 — OpenRouter downstream-provider steering](./0210-openrouter-downstream-provider-steering.md)
- [0215 — OpenAI subscription with a manual access token](./0215-openai-subscription-manual-token.md)
- [0216 — Correlate provider requests with the active session](./0216-provider-session-correlation-header.md)

### MCP
- [0056 — MCP client reconnect](./0056-mcp-client-reconnect.md)
- [0057 — MCP server notifications](./0057-mcp-server-notifications.md)
- [0063 — MCP structured results: fail-closed + CallMcpWithQuery](./0063-mcp-structured-failclosed-callmcpwithquery.md)
- [0078 — MCP typed tool results](./0078-mcp-typed-tool-results.md)
- [0218 — Internal encrypted credential-store substrate](./0218-credential-store.md)
- [0219 — Qualify the official MCP SDK authorization-code profile](./0219-mcp-oauth-sdk-profile.md)
- [0220 — Adapter-local MCP OAuth controller](./0220-mcp-oauth-controller.md)
- [0221 — Read-only environment credential source and OAuth refresh posture](./0221-read-only-credential-source.md)
- [0112 — Host-owned loopback MCP OAuth login](./0112-mcp-oauth-loopback-runtime.md)
- [0113 — Operator MCP authentication profiles and explicit login](./0113-operator-mcp-auth-profiles.md)
- [0223 — Pin MCP transport-error semantics that do not replay rejected calls](./0223-mcp-sdk-transport-error-semantics.md)

### Performance & diagnostics
- [0018 — Perf observability](./0018-perf-observability.md)
- [0019 — Perf tracking](./0019-perf-tracking.md)
- [0020 — Diagnostics](./0020-diagnostics.md)
- [0045 — Explicit-bucket latency histograms (zero-config quantiles on `/metrics`)](./0045-explicit-bucket-latency-histograms.md)
- [0098 — Telemetry for the headless binaries (mecatequi, mecak8s)](./0098-headless-telemetry.md)

### Governance & trust
- [0241 — Canonical untrusted-content fences live in governance](./0241-governance-fence-ownership.md)
- [0021 — Guardrails](./0021-guardrails.md)
- [0049 — Remove the guardrails per-session checker call-count cap](./0049-guardrails-remove-maxchecks.md)
- [0050 — Remove the guardrails oversized-content inspection skip](./0050-guardrails-remove-maxcontentbytes.md)
- [0051 — Surface advisory guardrail findings to the TUI](./0051-guardrails-advisory-tui-visibility.md)
- [0052 — Global guardrails checker-down posture toggle](./0052-guardrails-checker-down-toggle.md)
- [0053 — Flip guardrails default mode from advisory to block](./0053-guardrails-default-block.md)
- [0060 — Add Bash to the default guardrail rule set with a read-only pre-filter](./0060-guardrails-bash-default.md)
- [0061 — Human one-shot guardrail override (`/guardrail-allow`)](./0061-guardrails-human-override.md) *(superseded by 0062)*
- [0062 — Out-of-band approve-once for guardrail blocks](./0062-guardrails-approve-once.md)
- [0046 — Guardrails slot enables (configure = enable)](./0046-guardrails-slot-enable.md)
- [0080 — Guardrail-routed path-escape checking (composition pre-check, auto-only)](./0080-guardrail-routed-escape-checking.md)
- [0069 — Plan-approval gate](./0069-plan-approval-gate.md)
- [0070 — Model-visible affordance gate](./0070-model-visible-affordance-gate.md)
- [0022 — Allow-all posture](./0022-allow-all-posture.md)
- [0023 — Workspace trust](./0023-workspace-trust.md)
- [0092 — Project-trust suppression pin](./0092-no-project-trust-pin.md) *(superseded by 0095)*
- [0094 — Opt-in project ingestion: two-axis positive grants](./0094-opt-in-project-ingestion.md) *(superseded by 0095)*
- [0095 — Root-aware project trust](./0095-root-aware-project-trust.md) *(the authoritative #359 trust decision; supersedes 0092 + 0094)*
- [0202 — Diagnostic-only posture reporting](./0202-diagnostic-only-posture-reporting.md) *(orthogonal reporting surface; relates to 0095)*
- [0204 — Caller identity: accept a principal, thread it everywhere, record the owner](./0204-caller-identity-threading.md) *(agent-identity Track A; audit-trail phase)*
- [0234 — Derived delegation authority behind an evaluator port](./0234-authority-evaluator-port.md)
- [0205 — Bound cached JWKS staleness](./0205-bounded-jwks-staleness.md)
- [0206 — Ship reusable OIDC caller identity as an opt-in module](./0206-oidc-authn-module.md)
- [0212 — Enforce caller ownership at every application access path](./0212-caller-ownership-enforcement.md) *(agent-identity Track A; application isolation)*
- [0213 — Enforce caller ownership at remote driver boundaries](./0213-driver-caller-ownership.md) *(B-lite follow-up to application isolation)*
- [0225 — Operator settings validation command](./0225-operator-settings-validation.md)
- [0024 — System-prompt research](./0024-system-prompt-research.md) *(research)*
- [0041 — Output-economy default prompt](./0041-output-economy-default-prompt.md)
- [0054 — Reasoning rebalance of the default-tone prompt](./0054-reasoning-rebalance-default-prompt.md)
- [0086 — Remove the output-economy control surface](./0086-remove-output-economy-control.md) *(supersedes 0041; item 4 superseded by 0089)*
- [0089 — CLI clean break: one canonical spelling per action](./0089-cli-clean-break-grammar.md) *(supersedes 0087 + 0086 decision item 4)*
- [0055 — Reasoning-effort knob](./0055-reasoning-effort.md)

### UX & forge
- [0025 — UX discoverability](./0025-ux-discoverability.md)
- [0026 — Clipboard image paste](./0026-clipboard-image-paste.md)
- [0028 — mecatequi (single-shot GitHub Action)](./0028-mecatequi.md)
- [0082 — Factory MCP wiring for the one-shot mains](./0082-factory-mcp-wiring.md)
- [0090 — Per-server opt-in for plain-http token-bearing MCP endpoints](./0090-mcp-insecure-http-optin.md)
- [0032 — First-class worktree binding for a session](./0032-worktree-binding.md)
- [0087 — Staged mecatui transport migration](./0087-mecatui-staged-transport-migration.md) *(superseded by 0089)*
- [0088 — Explicit daemon.yaml (listener topology config)](./0088-daemon-config-file.md)
- [0222 — mecatui: ctrl+t routes by ask type; full-screen ask-args view](./0222-mecatui-ask-args-view.md)
- [0247 — mecatui generated status lines](./0247-mecatui-status-line.md)

### Retired
- [0029 — Repo-map tree-sitter](./0029-repomap-tree-sitter.md) *(retired)*
