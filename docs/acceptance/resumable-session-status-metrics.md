# Resumable session status metrics — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — adds a durable session-snapshot field and an additive public protobuf projection whose ownership and compatibility rules must remain stable across every store and client.
**Decision record:** [ADR 0356](../adr/0356-durable-context-occupancy.md)
**Phase:** session continuation
**Status:** in-progress, 2026-09-24. Implementation began from the merged Plan / Interface contract.
**Delivery:** Split. The persisted session aggregate and public `Session` protobuf projection require separate interface review before implementation.
**Expected tasks:** deferred to orchestration.
**Issue:** [stacklok/mecatl#1822](https://github.com/stacklok/mecatl/issues/1822).
**Plan PR:** [#1839](https://github.com/stacklok/mecatl/pull/1839).
**Approved baseline:** `19843a91f0f45dc1dd2cfdbb5595dd5d42270396`.

Any persisted session—main chat, subagent, Parallel branch, team member, or scheduled run—must retain its latest known context occupancy when it has completed an agent-loop turn. A resumed or selected prior main chat uses that authoritative snapshot to restore its status line; inspectors and future session surfaces can use the same historical datum without reconstructing it from a transcript or activity replay. A missing occupancy value from a legacy or pre-turn snapshot remains unknown; it is never guessed from lifetime totals.

## Human decisions

None — #1822 requires restoring status-line metrics on continuation, and the exact durable ownership, additive wire representation, legacy behavior, and client projection are recorded by ADR 0356.

## Interface contract

- **gRPC / protobuf:** Add presence-aware `ContextOccupancy latest_context_occupancy = 22` to `mecatl.v1.Session`; `ContextOccupancy` contains `int64 input_tokens = 1` and `bool estimated = 2`. Add additive `bool estimated = 3` to `TurnEnd`, mirroring `session.TurnEndPayload.Estimated`, so live and resumed meters preserve the same display-only estimate marker. It is the latest non-zero context-meter numerator established by a completed agent-loop turn in that session, with the existing display-only fallback-estimate marker. Every persisted session kind uses the field; it is absent only when no such value exists or an older server produced the snapshot. `GetSession` returns it in its existing `Session` snapshot; no RPC method or request changes.
- **Exported Go APIs / interfaces:** Add additive `session.Session` snapshot access and aggregate mutation for optional latest context occupancy on every persisted session kind, including durable `debug` sessions; the names and package documentation must make clear that it is display state, neither `TokenUsage` nor a run budget baseline. Extend every existing session snapshot persistence/restore value and store conformance surface to round-trip optional presence and value. Add the same optional value to `engine/adapter/eventsource.SessionMeta`, so an event-log-system-of-record host supplies stored snapshot metadata to `Fold` rather than reconstructing occupancy from its event stream. No existing signature changes.
- **Tool schemas:** None — status restoration is a mecatui projection of the existing session snapshot and does not add, remove, or alter model-visible tools.
- **CLI / config:** None — `--resume-latest`, `--resume`, and `/sessions` retain their current syntax and selection behavior; they display additional authoritative snapshot data when it is present.
- **Events / persistence:** After an assistant message is successfully recorded for a completed agent-loop turn in any persisted session kind, including `debug`, update optional latest context occupancy only when the `turn.end` display usage has a non-zero input count; preserve its estimate marker and leave the previous value untouched for zero-input, failed, cancelled, or auxiliary work. Persist and restore it through memstore, JSONL, Redis, remote-driver, and event-sourced restoration at existing coherent snapshot-save boundaries only. An event-log-system-of-record host persists/supplies it as `SessionMeta`, not an event-derived fold value. Each session’s `token_usage["main"].total` remains the canonical lifetime ledger. Do not add a durable event-log entry, per-turn save, or event-history reconstruction.
- **Security / authority:** `latest_context_occupancy` carries only numeric display state derived from an authenticated session’s turn event and does not alter session ownership, permission, project trust, credentials, or tool authority. Snapshot reads remain ownership-checked.
- **Compatibility / migration:** This is additive protobuf, event, HTTP, and snapshot data. Older clients ignore field 22 and `TurnEnd.estimated`; newer clients treat its absence as unknown context occupancy while still showing compatible durable lifetime usage. HTTP `GET /v1/sessions/{id}` carries optional `latest_context_occupancy` with the same `input_tokens`/`estimated` presence semantics as gRPC. Existing snapshots restore without rewrite or invented occupancy; they acquire the field only after a qualifying turn is included in an existing saved snapshot.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — Session snapshots durably retain latest context occupancy

The session aggregate already owns canonical lifetime usage, while `turn.end` has distinct per-turn display semantics and may carry a flagged display-only estimate ([ADR 0307](../adr/0307-canonical-durable-token-accounting.md), [ADR 0356](../adr/0356-durable-context-occupancy.md), [context architecture](../architecture/context-and-compaction.md)). The new datum must preserve that distinction and round-trip through all supported snapshot implementations rather than coupling a terminal client to an event log.

**Acceptance:**
- AC1.1: After a completed agent-loop turn successfully records its assistant message, a non-zero `turn.end` input count updates that session snapshot’s latest context occupancy with its estimate marker; a zero count retains any prior occupancy and does not create one.
  - verify: `TestResumableSessionStatusMetrics_Scenario1_ContextOccupancySnapshots`
- AC1.2: Main chats, scheduled runs, Subagent children, Parallel branches, team-member sessions, and durable debug sessions each retain their own latest context occupancy independently; session kind never suppresses persistence or causes one session’s occupancy to overwrite another’s.
  - verify: `TestResumableSessionStatusMetrics_Scenario1_AllSessionKindsRoundTrip`
- AC1.3: An event-log-system-of-record host preserves latest context occupancy as stored `eventsource.SessionMeta` supplied to `Fold`, not by scanning or inferring from events.
  - verify: `TestResumableSessionStatusMetrics_Scenario1_EventSourceMetadataRoundTrip`
- AC1.4: A failed or cancelled stream, title generation, routing/classifier work, and other auxiliary model activity do not replace latest context occupancy; each session’s durable lifetime `token_usage["main"].total` remains an element-wise aggregate across all main attempts and is never substituted for occupancy.
  - verify: `TestResumableSessionStatusMetrics_Scenario1_NonTurnWorkCannotReplaceOccupancy`
- AC1.5: Every in-tree session store and rehydration path preserves an absent or populated occupancy exactly at existing coherent snapshot-save boundaries. A snapshot save failure leaves the previously durable snapshot intact; an EventLog append outcome neither fabricates nor reconstructs occupancy.
  - verify: `TestResumableSessionStatusMetrics_Scenario1_StoreRoundTripAndFailure`

### Scenario 2 — Session snapshots expose authoritative status data compatibly

`Session.token_usage` is already the canonical durable accounting projection, `Session.resolved_model` is the authoritative source of the resolved context window, and ADR 0356 makes context occupancy separately presence-aware ([ADR 0356](../adr/0356-durable-context-occupancy.md), [`harness.proto`](../../contracts/proto/mecatl/v1/harness.proto)). The snapshot must carry each datum without adding a special status RPC.

**Acceptance:**
- AC2.1: `GetSession` returns resolved-model context-window metadata, each owned session’s lifetime main usage, and presence-aware latest context occupancy regardless of session kind.
  - verify: `TestResumableSessionStatusMetrics_Scenario2_GetSessionProjection`
- AC2.2: Generated protobuf and existing clients remain wire-compatible: an older snapshot/server that lacks context occupancy is accepted, and an older client ignores the additive field.
  - verify: `TestResumableSessionStatusMetrics_Scenario2_LegacySnapshotCompatibility`
- AC2.3: Snapshot access remains ownership-checked, and `latest_context_occupancy` neither changes each kind’s direct-run gate nor makes a scheduled, child, Parallel, team-member, or debug session continuable through the public chat path.
  - verify: `TestResumableSessionStatusMetrics_Scenario2_KindAuthorityUnchanged`
- AC2.4: The server does not expose snapshot usage, context occupancy, or model metadata to a caller that fails the existing ownership checks.
  - verify: `TestResumableSessionStatusMetrics_Scenario2_OwnershipUnchanged`

### Scenario 3 — Mecatui restores status-line metrics before the next prompt

Mecatui’s status source distinguishes cumulative `usage` from current `contextTokens` ([ADR 0356](../adr/0356-durable-context-occupancy.md), [`model.go`](../../cmd/mecatui/ui/model.go)); startup resume and `/sessions` continuation must adopt the server snapshot, not wait for a new `turn.end` event.

**Acceptance:**
- AC3.1: `mecatui --resume` and `--resume-latest` render the resolved context-window denominator, latest known context numerator/percentage (including its estimated hint), and cumulative main-session input/output/cache statistics before the operator submits a prompt.
  - verify: `TestResumableSessionStatusMetrics_Scenario3_StartupResumeRestoresStatus`
- AC3.2: Continuing a chat through `/sessions` restores the same status values during authoritative session adoption; a refetch for another session or an older response for the same session generation cannot overwrite newly selected or newer live metrics.
  - verify: `TestResumableSessionStatusMetrics_Scenario3_SessionSwitchRestoresStatus`
- AC3.3: A legacy or pre-turn session with no latest context occupancy displays an unknown numerator rather than deriving one from cumulative input; a resolved-model refresh still heals a missing or provisional context-window denominator without applying stale snapshot metrics.
  - verify: `TestResumableSessionStatusMetrics_Scenario3_UnknownAndProvisionalStatus`
- AC3.4: Inspecting a child, scheduled, Parallel, team-member, or debug session may read its occupancy but does not rebind the active main chat’s prompt target or status metrics.
  - verify: `TestResumableSessionStatusMetrics_Scenario3_InspectionIsNonDestructive`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Replaying or indexing historical per-turn usage | separate observability/accounting proposal | This plan stores one latest context-meter display value, not a history. |
| Provider pricing, cost totals, raw provider-only latest-turn reporting, or new usage kinds | future accounting work | ADR 0307 records canonical token accounting, not pricing or a per-turn history. |
| Changing compaction admission or its resolved-window precedence | existing context-window architecture | This plan projects the already resolved window and does not change model admission. |
| New user documentation before implementation | implementation PR | Living guides describe shipped behavior, not a proposed contract. |

## Definition of done

1. Applicable `task lint`, `task test:race`, `task docs`, and `task api:check` gates pass on the final candidate.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green for runtime changes.
4. The implementation PR links the Plan / Interface PR and approved commit and reports interface conformance.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- `latest_context_occupancy` deliberately models the existing sticky status meter for every session kind: it retains the last non-zero numerator and whether it is an estimate. It is not provider accounting or a history.
- The implementation must update this datum only after a successfully recorded assistant message and serialize it only at existing coherent snapshot boundaries; no per-turn persistence cadence or EventLog recovery is introduced.
- Snapshot metrics are adopted only as part of a guarded session-generation handoff. Ordinary resolved-model healing must not regress newer live metrics for the same session.
