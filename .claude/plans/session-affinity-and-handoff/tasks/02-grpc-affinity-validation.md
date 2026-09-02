---
id: 02-grpc-affinity-validation
title: gRPC session-affinity validation
blocked_by: [01-session-header-contract]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/session-affinity-and-handoff
---

# Task brief

Add one server-side gRPC metadata validator using the shared engine-port header contract, then inventory every session-bound unary and server-streaming `HarnessService` entry and apply it before lookup, registry access, lease acquisition, or mutation. Bind `Converse` from metadata before its first receive-driven run entry, compare the first prompt/retry frame to that binding, and keep all later controls attached to the established session.

**Likely scope:** `internal/adapter/server/grpc.go`, a focused affinity helper/test file, gRPC integration fixtures, and the existing service-method inventory guard if appropriate. Do not edit protobufs or generated code.

**Invariants:** missing metadata is the compatibility floor; exactly one legal value is accepted; duplicate, illegal, and mismatched metadata returns non-disclosing `InvalidArgument`; authentication and ADR-0212 ownership remain independent and authoritative; failure performs no provider call and emits no run event. Use bufconn/in-process offline tests and strict failing-test-first TDD.

## Acceptance criteria

- AC2.1: Every session-bound unary and server-streaming RPC accepts one legal
  `X-Mecatl-Session-ID` value only when it equals the authoritative request session ID
  byte-for-byte; missing metadata remains compatible.
  - verify: `TestSessionAffinityAndHandoff_Scenario2_GRPCUnaryAndServerStreamMatrix`

- AC2.2: Duplicate metadata values, an illegal value, or a mismatch fail with
  `InvalidArgument` before session lookup or mutation, and the status message contains
  neither the metadata value nor the request value.
  - verify: `TestADR_0290_GRPCHeaderFailureIsNonDisclosing`

- AC2.3: `Converse` validates metadata before creating or attaching run state, then
  requires the first prompt or retry frame's session ID to equal it byte-for-byte;
  failure emits no run event and performs no provider call.
  - verify: `TestSessionAffinityAndHandoff_Scenario2_ConversePreStreamAndFirstFrame`

- AC2.4: After a valid first frame, approval, cancel, child-cancel, steer, and
  steer-cancel frames remain bound to that established session without adding a
  protobuf field or accepting a second session identity.
  - verify: `TestADR_0290_ConverseControlsStaySessionBound`

- AC2.5: A missing header keeps existing `Converse` and unary/server-stream behavior
  byte-compatible.
  - verify: `TestADR_0290_GRPCMissingHeaderCompatibility`
