# Architecture Decision Records

This folder holds mecatl's **durable architecture decisions** as numbered ADRs. Create one
only when Architectural work introduces or supersedes a durable public/API compatibility,
persistence/data-ownership, security/trust, deployment/operator, module/system-boundary, or
cross-subsystem-invariant decision. Classification follows the decision and blast radius, not
diff size; see the canonical [development process](../development-process.md). Routine and
Bounded changes do not get ADRs merely to narrate the work, though they may cite existing
records.

Each ADR records *why* a thing is shaped the way it is at a point in time and is then
**frozen** — to change a decision, write a new ADR with a `Supersedes:` line and record the
supersession in this living index; never edit the landed record. Copy
[`template.md`](./template.md) to start one. Number monotonically.

Current behavior belongs in the owning [architecture topic](../READING.md) and
public guide. Track actionable work in issues and PRs rather than a second
hand-maintained status ledger. Follow the [documentation change review](../development-process.md#documentation-change-review)
for current ownership; historical references in ADRs do not require recreating
retired trackers or completed execution plans.
Documentation/citation conventions are in [`docs/design/README.md`](../design/README.md).

> History: ADRs 0004–0029 were the former `docs/design/*` design records, consolidated
> into the ADR scheme by [ADR 0003](./0003-consolidate-design-records-as-adrs.md).

## Index

### Process & conventions
- [0002 — Documentation lifecycle](./0002-documentation-lifecycle.md) *(user-facing ownership superseded by 0321)*
- [0003 — Consolidate design records as ADRs](./0003-consolidate-design-records-as-adrs.md)
- [0321 — Canonical user documentation ownership](./0321-canonical-user-documentation-ownership.md)
- [0331 — Host public documentation and pull request previews on Vercel](./0331-documentation-hosting-and-pr-previews.md) *(proposed)*
- [0072 — The acceptance-plan spine](./0072-acceptance-plan-spine.md) *(superseded by 0295)*
- [0295 — One scaled acceptance-plan spine for substantive work](./0295-unified-development-spine.md) *(single-PR decision superseded by 0306)*
- [0306 — Human-reviewed development contracts before implementation](./0306-human-reviewed-development-contracts.md)

### Architecture & implementation
- [0001 — Agent Client Protocol (ACP) adapter](./0001-acp-adapter.md)
- [0004 — v1 architecture](./0004-v1-architecture.md) *(historical)*
- [0005 — Driver seams](./0005-driver-seams.md) *(skill-asset materialization/read-root decision superseded by 0108)*
- [0006 — v1 step-chain](./0006-v1-step-chain.md) *(historical)*
- [0007 — Twelve-patterns audit](./0007-twelve-patterns-audit.md) *(historical)*
- [0027 — Cloud-native arc](./0027-cloud-native.md)
- [0104 — Session families get a bounded, injective, non-reversible physical name](./0104-session-family-physical-naming.md)
- [0217 — Session discovery uses durable kind metadata and an authoritative transcript](./0217-session-discovery-continuation.md)
- [0285 — Predictable actionable mecatui session handles](./0285-predictable-mecatui-session-handles.md) *(ordinary fixed escaped raw-ID-prefix handles; supersedes ADR 0217 decision 8 without changing debugger evidence/incarnation handles or their digests)*
- [0226 — Session storage separates current state, indexed metadata, and maintenance](./0226-session-storage-maintenance.md)
- [0239 — Semantic stream retry and failed-step retry transport](./0239-semantic-stream-retry.md)
- [0243 — Local JSONL durability boundaries](./0243-jsonl-durability.md)
- [0245 — Safe build diagnostics](./0245-safe-build-diagnostics.md)
- [0254 — Dedicated session debugger and per-instance admin transport](./0254-session-debugger-admin-transport.md)
- [0255 — Sanitized durable network-attempt evidence](./0255-sanitized-network-attempt-evidence.md)
- [0299 — Safe HTTP rejection display evidence](./0299-safe-http-rejection-display-evidence.md)
- [0256 — Target-bound related evidence and approval-gated reporting](./0256-session-debugger-evidence-and-reporting.md) *(partially superseded by 0257)*
- [0257 — Session debugger incarnation and disclosure hardening](./0257-session-debugger-hardening.md) *(incarnation identity and edges superseded by 0258)*
- [0258 — Cryptographic session and lineage incarnations](./0258-cryptographic-session-incarnations.md)
- [0320 — Isolated direct-edge lineage reads for `InspectSession`](./0320-inspect-session-lineage-read-isolation.md) *(proposed; preserves ADRs 0256–0258)*
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
- [0223 — Pin MCP transport-error semantics that do not replay rejected calls](./0223-mcp-sdk-transport-error-semantics.md) *(superseded by 0309)*
- [0309 — MCP closed-idle POST failures are ambiguous and never replayed](./0309-mcp-ambiguous-closed-idle-post.md)
- [0232 — Steer-while-running: inject a user message into an in-flight run](./0232-steer-while-running.md)
- [0251 — Multimodal steer preserves prompt content](./0251-multimodal-steer.md)
- [0252 — HTTP steer endpoint: `POST /v1/sessions/{id}/steer`](./0252-http-steer-endpoint.md)
- [0253 — SDK mocking testkit: vendor the proven unary pattern, defer streaming](./0253-sdk-mocking-testkit.md)
- [0279 — TypeScript SDK architecture: Connect-ES transport, protobuf-es codegen, in-repo pnpm project](./0279-typescript-sdk-architecture.md) *(supersedes ADR 0253 Decisions 1–2 in part)*
- [0288 — TypeScript SDK durable attachment: the watch envelope, the serializable cursor, and the reconnect authority](./0288-typescript-sdk-durable-attachment.md) *(prompt-free control deferral superseded by 0347)*
- [0292 — TypeScript SDK local daemon and callback tools](./0292-typescript-sdk-local-daemon-and-tools.md)
- [0304 — TypeScript SDK public surface completeness and v0.1.0 release](./0304-typescript-sdk-public-surface-and-release.md) *(package identity and GitHub Packages interim superseded by 0328; teams-only ergonomic-resource constraint superseded by 0347; 0313 superseded in full)*
- [0313 — Interim GitHub Packages distribution and 0.0.x versioning](./0313-interim-github-packages-typescript-sdk.md) *(superseded by 0328)*
- [0328 — Publish the TypeScript SDK to npmjs as `@stacklok-oss/mecatl-sdk`](./0328-typescript-sdk-npmjs-stacklok-oss.md) *(proposed)*
- [0339 - Deno uses the TypeScript SDK HTTP/SSE entry point](./0339-typescript-sdk-deno.md) *(supersedes ADR 0279 only for the supported-runtime set)*
- [0340 - Deno owns local daemons through Deno.Command](./0340-typescript-sdk-deno-command.md) *(supersedes ADR 0339 for the Deno public entry-point and local-process decisions)*
- [0341 - Deno reuses the ConnectRPC gRPC transport](./0341-typescript-sdk-deno-grpc.md) *(supersedes ADRs 0339 and 0340 for the HTTP-only transport and Node-compatibility exclusions)*
- [0347 — Run-ID-addressed prompt-free controls](./0347-run-id-addressed-prompt-free-controls.md) *(proposed; supersedes ADR 0288 Decision 6 and ADR 0304 Decision 3 in part)*
- [0348 — TypeScript SDK MCP authorization lifecycle](./0348-typescript-sdk-mcp-authorization-lifecycle.md) *(proposed; supersedes ADR 0304 Decision 3 only for the authorization-lifecycle resource)*
- [0349 - Cause-free TypeScript SDK malformed-success decoding](./0349-typescript-sdk-malformed-success-decoding.md) *(proposed; narrows decoder diagnostics at the HTTP successful-response boundary)*
- [0351 — Mecatl Studio: in-repo web UI behind a BFF over the published SDK](./0351-mecatl-studio-in-repo-web-ui.md) *(proposed; `apps/` workspace, one-origin image, `MECATL_*`/`STUDIO_*` split)*
- [0357 — Studio exposes coarse status and completes browser login in a popup](./0357-studio-anonymous-status-and-popup-login.md) *(proposed; anonymous status, authenticated runtime, and same-origin callback messaging)*
- [0342 - Gate runs on unresolved live context windows](./0342-context-window-admission.md) *(supersedes ADR 0016 only for pre-swap run admission)*
- [0356 — Durable context occupancy in session snapshots](./0356-durable-context-occupancy.md) *(proposed; extends ADR 0307 without changing lifetime-ledger or budget semantics)*
- [0346 - Prompt-cache breakpoints are protocol-native, never vendor-keyed](./0346-unified-prompt-cache-dialect.md) *(supersedes ADR 0100's prompt_cache_breakpoint deferral, its root cache_control dialect arm, and its OpenRouter TTL deferral; extends ADR 0334 to OpenRouter)*
- [0036 — `engine/` is its own Go module (monorepo via `go.work`)](./0036-engine-module.md)
- [0037 — Engine public-API stability contract](./0037-engine-stability-contract.md)
- [0038 — Event-sourced SessionStore rehydration (the reference fold)](./0038-event-sourced-rehydration.md) *(Decision 3 origin-opacity and no-public-replay clauses proposed to be superseded by 0337)*
- [0337 — Classify synthetic user-prompt origin at emission](./0337-synthetic-user-prompt-origin.md) *(proposed)*
- [0359 - Harness context source authority is independent of execution](./0359-harness-context-source-authority.md) *(proposed)*
- [0043 — Ephemeral turn-0 instruction fragments](./0043-ephemeral-turn0-instruction-fragments.md)
- [0044 — Host-supplied askID discriminator (cross-process-reconstructable askID)](./0044-host-supplied-askid-discriminator.md)
- [0047 — Absolute path resolution inside the workspace root](./0047-absolute-path-resolution.md) *(skill read-root carve-out superseded by 0108)*
- [0108 — Read skill assets on demand by logical name](./0108-on-demand-logical-skill-assets.md) *(supersedes only ADR 0005/0047's skill-asset materialization/read-root decisions)*
- [0048 — mecak8s: Kubernetes-native agent harness (storage-free, managed-service state)](./0048-mecak8s.md)
- [0233 — Secure external Redis on the shared connection layer](./0233-secure-external-redis.md)
- [0237 — Listener-scoped workspace authority](./0237-listener-scoped-workspace-authority.md)
- [0286 — Public and private OIDC issuers are two transports, not one policy](./0286-issuer-transport-split.md) *(supersedes ADR 0284's implicit CA-presence mode selection, and ADR 0277's private-only transport clauses)*
- [0277 — Remote mecatui OIDC client authentication](./0277-remote-mecatui-oidc.md) *(supersedes 0270–0273; its keyring-only credential backend-selection clauses are superseded by 0318)*
- [0318 — Headless mecatui credential backend selection](./0318-headless-mecatui-credential-backend-selection.md) *(Accepted; supersedes ADR 0277's keyring-only credential-backend selection clauses)*
- [0305 — OAuth protected-resource discovery for remote mecatui](./0305-oauth-protected-resource-discovery.md) *(scope-selection clarification proposed in 0316)*
- [0316 — Server-owned scopes for discovered mecatui login](./0316-server-owned-discovered-oidc-scopes.md) *(proposed; supersedes ADR 0305's scope-selection clauses only)*
- [0284 — Optional system trust for remote mecatui OIDC issuers](./0284-optional-system-trust-remote-oidc.md) *(supersedes ADR 0277's mandatory issuer-CA requirement only; mode selection superseded by 0286)*
- [0278 — mecak8s edge-terminated TLS](./0278-mecak8s-edge-terminated-tls.md) *(supersedes ADR 0240's provider-security gate scope only)*
- [0275 — Bounded scoped HTTPS keep-alive reuse for OIDC](./0275-bounded-scoped-https-keepalive-oidc.md) *(supersedes ADR 0235's keep-alive policy and ADR 0277's credential-recovery classification only)*
- [0270 — Activity-gated remote OIDC refresh](./0270-activity-gated-remote-oidc-refresh.md) *(superseded by 0277)*
- [0271 — Recover remote TUI authentication without replaying ownership-ambiguous work](./0271-tui-reauth-owner-recovery.md) *(superseded by 0277)*
- [0272 — Safe target logout for remote mecatui OIDC](./0272-remote-mecatui-logout.md) *(superseded by 0277)*
- [0273 — Remote mecatui OIDC client login](./0273-remote-mecatui-oidc-login.md) *(superseded by 0277)*
- [0274 — Remote logout provider budget](./0274-remote-mecatui-logout-budget.md) *(supersedes ADR 0277's logout provider budget only)*
- [0059 — Scheduled tasks](./0059-scheduled-tasks.md)
- [0065 — Conversation fork: peer session from a history snapshot](./0065-conversation-fork.md)
- [0073 — Schedule tool](./0073-schedule-tool.md)
- [0074 — Many loops per server; scheduler shape](./0074-many-loops-scheduler-shape.md)
- [0075 — Fire-result delivery: a scheduled fire reports back into the originating chat](./0075-fire-result-delivery.md) *(origin-binding mechanism superseded by 0209)*
- [0209 — Attribute schedule origins through the run context](./0209-schedule-origin-run-context.md) *(supersedes 0075's wrapper-state origin binding)*
- [0097 — Scheduled fires are observable in-flight: a first-class persisted lifecycle stage](./0097-scheduled-fire-inflight-state.md)
- [0096 — mecatui live-feed reconnect: client-owned backoff + durable catch-up, no server cursor](./0096-live-feed-reconnect.md)
- [0301 — Logical conversation anchors in mecatui](./0301-logical-conversation-anchors.md) *(proposed)*
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
- [0276 — Count the full request and expose durable manual compaction](./0276-full-request-and-manual-compaction.md) *(supersedes ADR 0025's `/compact` deferral only)*
- [0106 — Optional completion-learning seam](./0106-optional-learning-seam.md)
- [0107 — Operator-profile memory lifecycle](./0107-operator-profile-memory-lifecycle.md)
- [0109 — Evidence-backed reflection and durable staged learning](./0109-staged-learning-proposals.md)
- [0227 — Dream consolidation safety boundary](./0227-dream-consolidation-safety-boundary.md)
- [0228 — Manual dream review](./0228-manual-dream-review.md)
- [0110 — Evaluated, versioned agent-owned skills](./0110-evaluated-agent-owned-skills.md) *(superseded by 0111)*
- [0111 — Hardened publication and recovery for agent-owned skills](./0111-hardened-agent-owned-skill-publication.md)
- [0114 — Configurable learning-trigger policy](./0114-configurable-learning-trigger-policy.md) *(process-local coordinator/accounting decisions superseded where ADR 0259 is wired)*
- [0259 — Cloud-native learning uses durable, authoritative attempts](./0259-cloud-native-learning.md) *(Accepted)*

### Core tools & shell
- [0343 — Operator-configured command runners](./0343-operator-configured-command-runners.md) *(proposed)*
- [0281 — Managed temporary command leases and deterministic reaping](./0281-managed-temporary-command-leases.md) *(proposed)*
- [0282 — Managed workspace scratch cache](./0282-managed-workspace-scratch-cache.md) *(proposed; depends on 0281)*
- [0201 — Background Bash commands](./0201-background-bash.md)
- [0317 — Canonical Shell command tool](./0317-canonical-shell-command-tool.md) *(supersedes ADR 0201 decision D1 only)*
- [0324 — Internal Shell compatibility diagnostic](./0324-internal-shell-compatibility-diagnostic.md) *(supersedes ADR 0317 parser-placement decision only)*
- [0208 — Execution environments and version-aware file mutation](./0208-execution-environment.md) *(runtime-seam deferral superseded by 0211; version protocol authoritative)*
- [0211 — Execution-environment runtime seam](./0211-execution-environment-runtime-seam.md) *(supersedes 0208 decisions 1–3; phase-3 persistence deferral superseded by 0214)*
- [0214 — Execution-environment persistence and reattachment](./0214-environment-persistence.md) *(supersedes 0211 decision 6 only)*

### Agents, teams & delegation
- [0283 — Managed delegation-fork lifecycle](./0283-managed-delegation-fork-lifecycle.md) *(proposed; depends on 0281)*
- [0353 — Session-scoped agent identity](./0353-session-scoped-agent-identity.md) *(proposed)*
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
- [0330 — Isolated bounded Redis follow capacity](./0330-isolated-redis-follow-capacity.md) *(raises mecatl's Go floor to 1.27, replaces ADR 0233's old shared-client package locator, supersedes ADR 0250's shared-pool sizing deferral, and supersedes ADR 0240's no-force-close rule for isolated follow clients only)*

### Providers & APIs
- [0308 — Asynchronous session-title generation](./0308-session-title-generation-and-auxiliary-usage.md) *(proposed)*
- [0307 — Canonical durable token accounting and run-scoped budgets](./0307-canonical-durable-token-accounting.md)
- [0016 — Multi-provider](./0016-multi-provider.md)
- [0017 — OpenAI Responses API](./0017-openai-responses-api.md) *(research; its single-visible-text-part subsection is superseded by 0302)*
- [0302 — Project every OpenAI Responses visible text delta in SSE arrival order](./0302-openai-visible-text-delta-projection.md) *(supersedes only ADR 0017 §2's single-visible-text-part subsection)*
- [0030 — Layered model-selection heuristics](./0030-model-selection-heuristics.md)
- [0031 — Semantic subagent model router](./0031-subagent-model-router.md) *(enable model superseded by 0042)*
- [0034 — Extend the model router to team members and Parallel branches](./0034-team-parallel-model-routing.md)
- [0035 — Surface the per-delegation model for ALL children, not just routed ones](./0035-per-delegation-model-surface.md)
- [0042 — Taxonomy-gated subagent model router (enable by config, not a flag)](./0042-taxonomy-gated-model-router.md)
- [0352 — Jev as an explicit delegated-model router backend](./0352-jev-delegated-model-router.md) *(proposed; narrowly supersedes ADR 0031's LLM-only classifier construction when selected)*
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
- [0332 — Mecatui local provider enrollment preserves existing credential custody](./0332-mecatui-local-provider-enrollment.md) *(proposed; outcome-typed ordered commits)*

- [0333 — Unified provider configuration and Mecatui provider commands](./0333-unified-provider-configuration-and-mecatui-provider-commands.md) *(proposed; supersedes the `llm.endpoints` facade and unifies the local provider CLI)*

### MCP
- [0056 — MCP client reconnect](./0056-mcp-client-reconnect.md)
- [0057 — MCP server notifications](./0057-mcp-server-notifications.md) *(deferred “no live catalog mutation” decision proposed to be superseded by 0355; notification transport, bounded lazy-list, reconnect, and teardown decisions retained)*
- [0355 — Reconcile stale direct MCP source snapshots](./0355-mcp-source-reconciliation.md) *(proposed; preserves exact-name authority and supersedes 0057 only for its deferred “no live catalog mutation” decision)*
- [0063 — MCP structured results: fail-closed + CallMcpWithQuery](./0063-mcp-structured-failclosed-callmcpwithquery.md)
- [0078 — MCP typed tool results](./0078-mcp-typed-tool-results.md)
- [0218 — Internal encrypted credential-store substrate](./0218-credential-store.md)
- [0219 — Qualify the official MCP SDK authorization-code profile](./0219-mcp-oauth-sdk-profile.md)
- [0220 — Adapter-local MCP OAuth controller](./0220-mcp-oauth-controller.md)
- [0221 — Read-only environment credential source and OAuth refresh posture](./0221-read-only-credential-source.md)
- [0112 — Host-owned loopback MCP OAuth login](./0112-mcp-oauth-loopback-runtime.md)
- [0113 — Operator MCP authentication profiles and explicit login](./0113-operator-mcp-auth-profiles.md)
- [0223 — Pin MCP transport-error semantics that do not replay rejected calls](./0223-mcp-sdk-transport-error-semantics.md) *(superseded by 0309)*
- [0309 — MCP closed-idle POST failures are ambiguous and never replayed](./0309-mcp-ambiguous-closed-idle-post.md)
- [0323 — Broker-attached MCP query projection](./0323-target-governed-mcp-query.md)
- [0310 — Lazy ToolHive authorization for statically declared protected tools](./0310-lazy-toolhive-static-tools.md) *(pre-prompt-only authenticated discovery superseded by 0326)*
- [0311 — Per-upstream MCP broker OAuth grants](./0311-per-upstream-mcp-broker-oauth-grants.md) *(static-tool admission superseded by 0310)*
- [0312 — Confidential ToolHive broker client credentials](./0312-confidential-toolhive-broker-client.md)
- [0314 — Dynamic Client Registration for MCP broker upstreams](./0314-mcp-broker-dcr-client.md)
- [0325 — Durable Dynamic Client Registration for direct MCP profiles](./0325-direct-mcp-dcr.md)
- [0345 — Host-local direct MCP onboarding and credential custody](./0345-direct-mcp-onboarding.md) *(proposed)*
- [0326 — Lazy ToolHive grants refresh declared metadata](./0326-lazy-toolhive-metadata-refresh.md)
- [0335 — Idle-session MCP broker workspace refresh](./0335-idle-session-broker-workspace-refresh.md) *(Decision 6 superseded by 0358)*
- [0358 — Durable workspace-enrollment broker authority provenance](./0358-durable-workspace-enrollment-broker-authority.md) *(proposed; supersedes ADR 0335 Decision 6 only)*

### Performance & diagnostics
- [0018 — Perf observability](./0018-perf-observability.md)
- [0019 — Perf tracking](./0019-perf-tracking.md)
- [0020 — Diagnostics](./0020-diagnostics.md)
- [0045 — Explicit-bucket latency histograms (zero-config quantiles on `/metrics`)](./0045-explicit-bucket-latency-histograms.md)
- [0098 — Telemetry for the headless binaries (mecatequi, mecak8s)](./0098-headless-telemetry.md)
- [0338 — Product (adoption) metrics over OTLP](./0338-product-metrics.md)
- [0357 — Durable model-stream structural evidence](./0357-durable-model-stream-structural-evidence.md) *(accepted; implementation pending approved plan)*

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
- [0028 — mecatequi (single-shot GitHub Action)](./0028-mecatequi.md) *(hardcoded-ref pinning superseded by 0327)*
- [0327 — Self-repository refs for the mecatequi sibling actions](./0327-self-repository-action-refs.md) *(Accepted; supersedes ADR 0028's hardcoded-ref pinning decision only)*
- [0319 — Signed release archives and Homebrew tap distribution](./0319-release-archives-and-homebrew-tap.md) *(Accepted; recorded after implementation under an explicit spine waiver)*
- [0082 — Factory MCP wiring for the one-shot mains](./0082-factory-mcp-wiring.md)
- [0090 — Per-server opt-in for plain-http token-bearing MCP endpoints](./0090-mcp-insecure-http-optin.md)
- [0032 — First-class worktree binding for a session](./0032-worktree-binding.md)
- [0087 — Staged mecatui transport migration](./0087-mecatui-staged-transport-migration.md) *(superseded by 0089)*
- [0088 — Explicit daemon.yaml (listener topology config)](./0088-daemon-config-file.md)
- [0222 — mecatui: ctrl+t routes by ask type; full-screen ask-args view](./0222-mecatui-ask-args-view.md)
- [0247 — mecatui generated status lines](./0247-mecatui-status-line.md) *(superseded by 0289)*
- [0289 — Hardened status-command output and environment extension](./0289-hardened-status-command-boundary.md)
- [0344 — Mecatui-owned terminal titles](./0344-mecatui-terminal-title-controller.md) *(proposed)*
- [0280 — Automatic light theme selection in mecatui](./0280-mecatui-light-theme-autodetect.md)
- [0291 — Server-owned session placement](./0291-server-owned-session-placement.md)
- [0294 — End-to-end session correlation and affinity](./0294-session-correlation-and-affinity.md) *(proposed)*
- [0295 — One scaled acceptance-plan spine for substantive work](./0295-unified-development-spine.md) *(single-PR decision superseded by 0301)*
- [0296 — Opt-in local session context service](./0296-opt-in-local-session-context.md) *(proposed)*
- [0303 — Fail closed double-Escape clearing on enhanced key-event support](./0303-fail-closed-double-escape.md)
- [0336 — Draft-aware session inventory](./0336-draft-aware-session-inventory.md)
  *(proposed)*

### Retired
- [0029 — Repo-map tree-sitter](./0029-repomap-tree-sitter.md) *(retired)*
