---
id: 01-remote-contract
title: Neutral versioned broker protocol and remote attachment adapter
blocked_by: []
status: in-progress
attempt: 2
branch: plan-initial-production-mcp-broker/01-remote-contract-attempt-2
worktree: .scratch/worker-initial-production-mcp-broker-01-remote-contract-attempt-2
issue: ""
retries: 2
last_error: "verification resumed at operator direction; prior ENOSPC/timeout evidence retained in orchestration history"
accumulator: acc/initial-production-mcp-broker
---

# Task brief

Add `mecatl.broker.v1` and `internal/adapter/mcpbrokergrpc` around the existing root-internal `internal/mcpbroker` seam. Keep `engine/` untouched. Attach must return an opaque logical binding, a process-local attachment handle, frozen descriptors, and capabilities. Implement a bounded handle registry with idempotent Commit/Abort/Close and atomic `DeleteSessionIfBinding`; preserve existing unbound local delete compatibility. Map full ToolResult parts without JSON re-marshalling, reject malformed remote data, and never send credentials, OAuth state, persisted conversation calls, distributed owner data, or donor types. Use `task generate`; add focused offline conformance tests.

## Acceptance criteria

- AC1.1: A remote attachment returns the same opaque binding and complete frozen tool catalogue as the wrapped local attachment, including read-only/serial and authorization-capable behavior needed by dispatch.
  - verify: `TestInitialProductionMCPBroker_Scenario1_AttachmentParity`
- AC1.2: Commit, abort, close, and delete preserve the Stage 3 idempotency and local-close-versus-logical-delete distinction across the wire.
  - verify: `TestInitialProductionMCPBroker_Scenario1_LifecycleOutcomes`
- AC1.3: Tool arguments and results round-trip without lossy re-encoding, while malformed payloads, unknown versions, invalid outcomes, and invalid UTF-8 fail closed before entering session state.
  - verify: `TestInitialProductionMCPBroker_Scenario1_ToolRoundTrip`
- AC1.4: The broker protocol contains no raw pending conversation `ToolCall`, OAuth code, verifier, access token, refresh token, client secret, ToolHive storage type, or Redis generation/fence field.
  - verify: `TestInvariant_initial_broker_protocol_is_neutral_and_secret_free`
- AC1.5: `engine/` and `engine/port` gain no broker, protobuf, gRPC, ToolHive, or remote-transport dependency.
  - verify: `TestInvariant_initial_broker_preserves_engine_layering`
- AC1.6: The implementation depends on the current `internal/mcpbroker` contract and contains no donor `internal/broker` package, Redis broker store, fence/generation field, distributed owner credential, or distributed broker protocol type.
  - verify: `TestInvariant_initial_broker_excludes_distributed_donor_contract`
