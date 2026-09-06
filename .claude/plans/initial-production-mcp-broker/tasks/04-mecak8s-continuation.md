---
id: 04-mecak8s-continuation
title: Mecak8s remote selection and Stage 3 protected-call continuation
blocked_by: [02-remote-failure-semantics, 03-broker-auth-callback-service]
status: in-progress
attempt: 1
branch: plan-initial-production-mcp-broker/04-mecak8s-continuation-attempt-1
worktree: .scratch/worker-initial-production-mcp-broker-04-mecak8s-continuation-attempt-1
issue: ""
retries: 0
last_error: ""
accumulator: acc/initial-production-mcp-broker
---

# Task brief

Wire `mecak8s` to select the remote `internal/mcpbroker.Service` without constructing ToolHive locally. Implement bounded projected-token-file credentials reread per RPC, TLS CA and expected DNS name configuration, no anonymous downgrade, and real factory wiring. Preserve existing Stage 3 `PreparedRun`/`ClaimAuthorization`: only opaque authorization references cross the broker; presentation stays live. Confirmed state loss can invalidate a cached attachment only in the pre-prompt enrollment path, never in a live continuation. Exercise end-to-end offline with a real gRPC boundary and fixture ToolHive handlers.

## Acceptance criteria

- AC3.1: An offline end-to-end test drives protected-call pending → browser presentation → ToolHive callback completion → granted recheck through the remote adapter and receives the protected tool result on the original run.
  - verify: `TestInitialProductionMCPBroker_Scenario3_ProtectedCallContinuation`
- AC3.2: The existing prepared continuation dispatches its exact parked call at most once through the broker adapter, pairs all deferred siblings, and admits no client-supplied replacement arguments or success status; this does not claim the upstream effect occurred exactly once.
  - verify: `TestInvariant_remote_broker_continues_exact_parked_call`
- AC3.3: Pending, denied, expired, interrupted, cancelled, unknown, binding-mismatch, and broker-unavailable outcomes preserve the existing deterministic Stage 3 settlement behavior.
  - verify: `TestInitialProductionMCPBroker_Scenario3_TerminalOutcomeParity`
- AC3.4: Presentation URLs are returned only live, never persisted in session snapshots/events or broker protocol records intended for reattachment, and no credential material appears in tool specs, arguments, results, events, snapshots, or diagnostics.
  - verify: `TestInvariant_remote_broker_keeps_presentation_and_credentials_private`
- AC3.5: Anonymous configured MCP profiles and authenticated workspace enrollment continue to work through the same remote service without introducing a second callback or authorization workflow.
  - verify: `TestInitialProductionMCPBroker_Scenario3_ProfileParity`
- AC3.6: The mounted browser ingress carries ToolHive's complete fixed callback prefix and the separately configured final callback path through the same lifecycle; missing, malformed, expired, duplicate, replayed, or cross-enrollment state is generically rejected without code exchange, catalogue publication, unrelated-state mutation, or sensitive response/log data.
  - verify: `TestInitialProductionMCPBroker_Scenario3_CallbackRoutingAndReplay`
- AC5.5: `mecak8s` can select the remote broker using operator-configured address, TLS CA trust, **expected TLS DNS server name**, expected audience, and a reloadable workload-token source; missing identity in authenticated mode refuses startup and never downgrades to anonymous transport.
  - verify: `TestInitialProductionMCPBroker_Scenario5_Mecak8sComposition`
- AC5.7: A running mecak8s client uses a rotated workload token on a later RPC after the original token expires, without restart, cached-bearer reuse, or anonymous fallback.
  - verify: `TestInitialProductionMCPBroker_Scenario5_WorkloadTokenRotation`
