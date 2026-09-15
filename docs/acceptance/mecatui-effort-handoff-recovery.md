# Mecatui effort-handoff recovery — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — this repairs a client-local asynchronous session handoff with observable recovery guarantees, without changing durable architecture, public contracts, or authority.
**Decision record:** None — the correction applies the existing target-adopt-before-source-retire ownership pattern to the private mecatui effort switch.
**Phase:** mecatui session handoff reliability
**Status:** proposed, 2026-09-14. Combined candidate prepared after issue investigation and independent adversarial and scope reviews.
**Delivery:** Combined. This is one compact client-state-machine correction; gRPC/protobuf, exported Go APIs/interfaces, tool schemas, CLI/config, events/persistence, and security/authority are unaffected, and a separate plan PR would add no interface-review value.
**Expected tasks:** 1
**Combined rationale:** One implementation task can retain the source during `/effort` successor hydration, use the existing handoff correlation mechanism, clean an unused target, and remove the now-unneeded no-session retry path. Existing private Bubble Tea test seams cover the resulting success, failure, and stale-result behavior; a separate plan PR would expose no additional interface boundary.
**Issue:** [stacklok/mecatl#1486](https://github.com/stacklok/mecatl/issues/1486).
**Approved baseline:** Not applicable — this plan and implementation form the single human-reviewed Combined candidate.

An `/effort` selection forks the active server-side conversation to a successor, then fetches the
existing UI metadata subset for that successor before adopting it. The source remains bound and
usable until that succeeds. A fork failure leaves the source alone; a target-hydration failure
best-effort closes the returned target ID and restores source interaction. On success, the client
preserves its already-correct local conversation, adopts successor metadata, and only then
best-effort closes the source. A stale ready result cleans its unused target rather than leaking it.

## Human decisions

- [x] **Failed-handoff preference persistence:** retain the selected reasoning-effort preference for future sessions even when the current source remains active at its previously effective effort. — Decision: preserve the user’s explicit selection; the source remains usable without falsely claiming that its effective effort changed.

## Interface contract

- **gRPC / protobuf:** None — existing `ForkSession`, `GetSession`, and `CloseSession` RPCs and all generated messages remain unchanged.
- **Exported Go APIs / interfaces:** None — only private `cmd/mecatui/ui` messages, state transitions, and test-double observations change; client interfaces retain their signatures.
- **Tool schemas:** None — `/effort` retains its current picker interaction and sends the same fork request; no model-facing tool changes.
- **CLI / config:** None — no flags, settings, defaults, or persisted configuration schema changes. The existing selection save remains concurrent with the effort handoff.
- **Events / persistence:** None — no session snapshot, event, log, or durable field changes. A failed target is best-effort closed through the existing client RPC.
- **Security / authority:** None — existing session ownership, caller affinity, opaque IDs, source/target isolation, and authorization checks remain server-enforced. The client only uses IDs it received from its own fork request.
- **Compatibility / migration:** Backward-compatible client recovery correction. Existing servers require no migration; older clients retain the destructive hydration-failure behavior.

## In scope — 1 scenario, in implementation order

### Scenario 1 — effort handoff adopts a successor without losing its source

The effort picker already forks its conversation server-side. The client retains the source
projection while the successor is hydrated, disarms only the source live feed, and blocks input
with `phaseConnecting`. It reuses the existing session-switch request token to ignore obsolete
outcomes. This follows the existing target-adopt-before-source-close pattern in
[`cmd/mecatui/ui/models.go`](../../cmd/mecatui/ui/models.go) and the UI/client boundary in [the
architecture guide](../architecture.md). The target metadata is the existing
`client.SessionSnapshot` subset; effort switching neither reloads a transcript nor widens an RPC.

**Acceptance:**
- AC1.1: During a current effort handoff, the source binding and projection remain unchanged, source live observation is disarmed, and input cannot start a run. A failed fork or failed target hydration returns the source to idle, re-arms its live feed, restores prompt usability, and permits subsequent work against the source without requiring retry state.
  - verify: `TestMecatuiEffortHandoffRecovery_Scenario1_FailuresRetainUsableSource`
- AC1.2: After a successful `ForkSession`, the client fetches metadata using the returned target ID. A target-hydration failure attempts to close that target ID only; no failure path closes or unbinds the source.
  - verify: `TestMecatuiEffortHandoffRecovery_Scenario1_HydrationFailureCleansExactTarget`
- AC1.3: A current successful hydration preserves the local conversation, resets successor-derived UI state, adopts all target metadata already represented by `client.SessionSnapshot` (preserving current capabilities when an older server omits them), then schedules best-effort source closure. A source-close failure cannot roll back the adopted target.
  - verify: `TestMecatuiEffortHandoffRecovery_Scenario1_AdoptsTargetBeforeClosingSource`
- AC1.4: A stale ready result best-effort closes only its unused target; stale failures are inert and cannot alter the current source/target binding or handoff state. The saved effort selection remains available for a later session after any failed handoff, while the retained source displays its prior effective effort.
  - verify: `TestMecatuiEffortHandoffRecovery_Scenario1_StaleReadyCleansTargetAndStaleFailureIsInert`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Server-side fork atomicity, retries, or transaction semantics | Server/session lifecycle work | This client correction uses existing RPCs and treats hydration failure as a recoverable client handoff failure. |
| Model-switch carryover refactoring or transcript hydration for effort switches | Separate UI reliability work | Model carryover has provider-state/transcript requirements distinct from effort metadata hydration; extracting a shared abstraction is premature. |
| Guaranteed remote cleanup after a failed `CloseSession` | Separate server lifecycle proposal | Cleanup remains best effort under the existing RPC contract; tests prove the attempt and preserve the usable source. |
| New session-switch token type or public handoff interface | Separate API/UI design work | The private existing session-switch token provides the required stale-result correlation. |

## Definition of done

1. `task lint`, `task test`, `task docs`, and `task api:check` pass.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green.
4. Offline UI tests execute returned Bubble Tea commands and prove target IDs used for hydration and cleanup, not only reducer fields.
5. The sole Combined implementation PR carries this plan, reports the seven interface-contract categories unchanged, and updates the plan to `landed` only after all gates pass.
6. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- `CloseSession(target)` is best effort. A cleanup failure must not close or unbind the usable source; durable remote cleanup/reconciliation would require a separate server contract.
- `GetSession` returns the existing UI metadata subset rather than a transcript or response ID. The client correlates the returned command to its own fork-returned target ID and existing request token; it does not imply a new server-side validation protocol.
