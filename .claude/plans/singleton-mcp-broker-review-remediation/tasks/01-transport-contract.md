---
id: 01-transport-contract
title: Bounded broker receipts and structured transport contract
blocked_by: []
status: in-progress
attempt: 1
branch: "plan-singleton-mcp-broker-review-remediation/01-transport-contract-attempt-1"
worktree: ".scratch/worker-singleton-mcp-broker-review-remediation-01-transport-contract-attempt-1"
issue: ""
retries: 0
last_error: ""
accumulator: acc/singleton-mcp-broker-review-remediation
---

# Task brief

Complete the process-local gRPC contract: method-specific messages, structured reasons,
absolute bounded lifecycle and Execute receipts, request digest/correlation, stale-incarnation
coverage, hostile decoding, concurrency isolation, and real-factory model guidance. Update
ADR/living docs generated surfaces required by these code changes, but do not edit the shared
acceptance plan or task state.

## Acceptance criteria

- AC1.1: Concurrent or sequential duplicate Abort and Close requests execute the underlying operation once and receive the same terminal outcome until an absolute lease deadline that polling cannot extend; after reclamation they receive structured `state_unavailable`.
  - verify: `TestSingletonBrokerRemediation_Scenario1_LifecycleReceiptsAreAbsoluteAndReplayable`
- AC1.2: Duplicate Execute requests for the same incarnation, handle, call ID, tool, item identity, and argument digest atomically join or replay one immutable bounded receipt and dispatch once, including after loss of the first response. Polling cannot extend the absolute receipt lease; after reclamation the same request returns structured `state_unavailable` and never redispatches.
  - verify: `TestSingletonBrokerRemediation_Scenario1_ExecuteReceiptPreventsRedispatch`
- AC1.3: Reusing a call ID with different request content fails closed before tool dispatch, and a response whose call ID differs from the request is rejected before session state can consume it.
  - verify: `TestSingletonBrokerRemediation_Scenario1_CallIdentityAndResponseCorrelation`
- AC1.4: Only a recognized, method-bound structured response proving failure before the server dispatch marker remains an ordinary RPC error; cancellation, deadline, transport loss, absent response, malformed proof, and unknown reason or version after possible dispatch produce the fixed correlated ambiguous result without retry, hedge, rebind, or replay.
  - verify: `TestSingletonBrokerRemediation_Scenario1_DispatchClassificationIsConservative`
- AC1.5: Broker error identity uses structured reasons, preserves unknown reasons as protocol failures, and never infers state loss from status-message text.
  - verify: `TestSingletonBrokerRemediation_Scenario1_StructuredErrorReasons`
- AC1.6: Every stale-incarnation attachment, lifecycle, enrollment, authorization-control, Execute, observation, and bound-delete request is rejected before registry or logical broker state access.
  - verify: `TestSingletonBrokerRemediation_Scenario1_StaleIncarnationRejectedAcrossSurface`
- AC1.7: Raw arguments, JSON-object schemas, textual and binary result parts, and MIME tokens round-trip byte-exact where allowed; invalid UTF-8, malformed schemas, unknown outcomes, and malformed response shapes fail before state mutation.
  - verify: `TestSingletonBrokerRemediation_Scenario1_HostilePeerBoundary`
- AC1.8: Every broker RPC has method-specific request and response messages and the generated contract passes Buf lint and freshness checks.
  - verify: `TestInvariant_singleton_broker_method_specific_rpc_contract`
- AC1.9: Cancelling one active operation does not cancel a sibling, and active Execute versus Close, Abort, expiry, and shutdown races preserve one terminal owner without leaks or duplicate dispatch.
  - verify: `TestSingletonBrokerRemediation_Scenario1_ConcurrentCancellationAndLifecycleIsolation`
- AC1.10: The broker-enabled main and per-session engine factories place the unknown-outcome reconciliation instruction in their owned system-prompt layer, while broker-disabled engines do not receive it.
  - verify: `TestSingletonBrokerRemediation_Scenario1_BrokerRecoveryInstructionUsesRealFactories`
