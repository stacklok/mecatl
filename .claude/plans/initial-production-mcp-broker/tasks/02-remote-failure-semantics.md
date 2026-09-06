---
id: 02-remote-failure-semantics
title: Remote broker incarnation, cancellation, and ambiguous execution semantics
blocked_by: [01-remote-contract]
status: done
attempt: 1
branch: plan-initial-production-mcp-broker/02-remote-failure-semantics-attempt-1
worktree: .scratch/worker-initial-production-mcp-broker-02-remote-failure-semantics-attempt-1
issue: ""
retries: 0
last_error: ""
accumulator: acc/initial-production-mcp-broker
---

# Task brief

Extend the remote adapter only. Pin finite dial/RPC deadlines, classify confirmed state loss separately from transport `Unavailable`, reject stale handle/binding operations, and add bounded orphan-handle reclamation without logical deletion. Cancellation releases one operation only. Never retry a possibly dispatched Execute: a lost response becomes a fixed model-visible ambiguous outcome; pre-dispatch transport retry must not invoke handler/proxy twice. Add concurrency/restart/cleanup tests. Do not rebind live authorization here.

## Acceptance criteria

- AC4.1: Connection establishment and per-RPC deadlines are finite configuration values; transient establishment failure returns unavailable, and a later fresh attachment can reconnect only while the same broker process/incarnation remains authoritative.
  - verify: `TestInitialProductionMCPBroker_Scenario4_TransientReconnect`
- AC4.2: Replacing the broker process while a call is authorizing produces deterministic interruption without executing the protected call or replacing its opaque binding.
  - verify: `TestInitialProductionMCPBroker_Scenario4_RestartInterruptsPendingCall`
- AC4.3: A stale client attachment cannot commit, execute, present, observe, cancel, or delete against a different broker incarnation.
  - verify: `TestInvariant_initial_broker_rejects_stale_incarnation`
- AC4.4: Caller cancellation and deadlines propagate across the transport and release only the affected operation; they do not globally close a shared attachment. Explicit close and bounded orphan-handle cleanup close attachments, and the test joins all associated streams/goroutines within the configured deadline without claiming the upstream effect stopped.
  - verify: `TestInitialProductionMCPBroker_Scenario4_CancellationAndCleanup`
- AC4.5: No test, documentation, deployment label, readiness condition, or API description claims replica interchangeability, callback failover, restart durability, exactly-once external effects, or HA.
  - verify: `TestInvariant_initial_broker_makes_no_distributed_claims`
- AC4.6: If a tool RPC reaches the broker but its response is lost, the remote adapter returns a distinct model-visible ambiguous-outcome failure and never automatically retries the tool, rebinds the attachment, recreates authorization, or reports success; a later retry is a new ordinary tool action. The transport proof permits a transparent gRPC retry only when it fails before application dispatch, and proves neither the handler nor proxy is invoked twice after dispatch.
  - verify: `TestInitialProductionMCPBroker_Scenario4_AmbiguousToolResultIsNotReplayed`
- AC4.7: After broker restart, a persisted pre-prompt workspace enrollment discards its stale outer correlation and may begin one fresh enrollment, while a live protected-call authorization never rebinds to the new broker incarnation.
  - verify: `TestInitialProductionMCPBroker_Scenario4_PrePromptReenrollmentOnly`
