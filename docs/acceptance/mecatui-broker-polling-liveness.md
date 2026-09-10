# Mecatui broker polling liveness — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — two client-local broker control loops can lose progress without changing public protocols, durable state, authority, or subsystem ownership.
**Decision record:** None — this restores the existing automatic-observation contract using the current client contexts and generation guards; it introduces no durable architecture decision.
**Phase:** mecatui broker authorization reliability
**Status:** landed, 2026-09-10. Candidate transition after all seven acceptance proofs, aggregate gates, documentation checks, API compatibility, and the offline demo passed; authoritative only when the Combined PR merges.
**Delivery:** Combined. This is one compact mecatui reliability change with no public or durable interface changes.
**Expected tasks:** 1
**Combined rationale:** Both failures are private Bubble Tea command-handoff/liveness defects in the same broker-consent UI boundary. One worker can pin both state-machine regressions, make the minimal client-local fix, update the user wording, and run the same gates; a separate plan PR would not expose an additional interface or useful review boundary.
**Issue:** [stacklok/mecatl#1322](https://github.com/stacklok/mecatl/issues/1322).
**Combined implementation PR:** Not opened; implementation requires explicit `/plan-orchestrate` invocation.
**Approved baseline:** Not applicable until the Combined candidate is human-approved at final review.

Restore the behavior already promised by mecatui: after browser presentation, both a lazy
per-tool MCP authorization and pre-prompt workspace-service enrollment continue observing
without requiring an unrelated keypress. A pending control-stream event must hand off exactly
one current-generation authorization poll without reviving the parked Converse reader, while
each unary workspace-enrollment attempt must have a fixed client-side deadline so `busy`
cannot remain set forever.

## Human decisions

- [x] **Workspace deadline:** use a fixed deadline for connect, observe, retry, and cancel. — Decision: 30 seconds, allowing broker discovery more time than the unrelated ten-second MCP first-event watchdog.
- [x] **Ambiguous timeout recovery:** fail closed rather than automatically repeating the overloaded `ConnectWorkspaceServices` RPC. — Decision: stop automatic polling, leave local correlation state visible, and instruct the user to run `/clear` followed by `/tools-connect` in the replacement session.

## Interface contract

- **gRPC / protobuf:** None — existing MCP-authorization streaming controls and unary workspace-enrollment RPCs, messages, fields, and status mappings remain unchanged.
- **Exported Go APIs / interfaces:** None — the `cmd/mecatui` implementation changes only private state-machine helpers and test seams; `client.MCPAuthorizationController` and `client.WorkspaceEnrollmentController` signatures remain unchanged.
- **Tool schemas:** None — no model-facing or TUI command schema changes.
- **CLI / config:** None — no flag or configuration key changes. One fixed private 30-second client-side workspace-enrollment deadline applies to connect, observe, retry, and cancel; timeout recovery does not automatically repeat the overloaded `ConnectWorkspaceServices` RPC.
- **User-facing errors:** Keep the broker-derived `failed`, `declined`, `expired`, and disconnected wording; add bounded timeout wording that says the outcome may be uncertain and directs the user to `/clear` and then `/tools-connect` in the replacement session. Parent cancellation/stale completion remains silent rather than being mislabeled as timeout.
- **Events / persistence:** None — no event, snapshot, log, or persisted enrollment field changes. Recovery remains client-local and generation-correlated.
- **Security / authority:** None — session ownership, broker attachment authority, opaque authorization/enrollment identifiers, URL handling, and cancellation boundaries are unchanged. Timeout or stale completion never authorizes, publishes tools, or mutates a replacement session/operation.
- **Compatibility / migration:** This is a backward-compatible client behavior fix. Existing servers require no migration; older clients retain the old stall behavior.

## In scope — 1 scenario, in implementation order

### Scenario 1 — broker consent observation remains live without violating ownership

The lazy protected-tool path remains owned by the authorization control stream described in
[the architecture guide](../architecture.md). A correctly correlated `status=pending`
`MCPAuthorizationMsg` proves the current recheck is alive, resets authorization state to a new
generation, and schedules exactly one future poll for that new generation. The handler does
not propagate or recreate the parked run's ordinary Converse-reader command; stale ticks,
stream closes, and errors from the superseded generation remain inert. The existing
readiness/card interaction remains unchanged. The wording that consent is checked automatically
in `user-docs/mecatui/using-the-tui.md` remains the user-facing contract.

Workspace enrollment remains the separate opaque bundle-level workflow defined by
[ADR 0311](../adr/0311-per-upstream-mcp-broker-oauth-grants.md) and the broker implementation
notes in [`docs/design/IMPLEMENTATION-NOTES.md`](../design/IMPLEMENTATION-NOTES.md). Each
connect, periodic check, retry, and cancel command derives the approved fixed deadline from the
current operation context. A deadline is reduced through the existing correlated result path:
the current attempt clears `busy` and its cancellation handle, reports a bounded sanitized
failure that explicitly says the server outcome may be uncertain, and stops automatic polling.
It does not infer whether the server committed the operation and does not automatically repeat
the overloaded connect/observe RPC. The message directs the user through the existing `/clear`
then `/tools-connect` recovery path. Replacement generations reject any late completion.

**Acceptance:**
- AC1.1: A current control-stream pending status received while automatic polling is active returns a non-nil command that delivers one poll tick correlated to the updated authorization generation.
  - verify: `TestMecatuiBrokerPollingLiveness_Scenario1_PendingControlEventSchedulesCurrentGenerationPoll`
- AC1.2: Executing that returned command starts the next recheck without a user keypress, while the previously scheduled tick and the superseded stream's close/error cannot start, stop, or overwrite the replacement control.
  - verify: `TestMecatuiBrokerPollingLiveness_Scenario1_PendingControlEventContinuesAutomatically`
- AC1.3: The pending handoff never re-arms the original Converse reader and never creates two simultaneous authorization controls; granted/terminal continuation ownership remains on the existing control stream.
  - verify: `TestMecatuiBrokerPollingLiveness_Scenario1_PendingHandoffDoesNotRearmConverseOrDuplicateControl`
- AC1.4: A fake workspace-enrollment controller that waits for context cancellation observes the approved deadline within the fixed test-controlled bound for connect, check, retry, and cancel; each command returns and no current model remains permanently `busy`.
  - verify: `TestMecatuiBrokerPollingLiveness_Scenario1_AllWorkspaceEnrollmentActionsAreDeadlineBounded`
- AC1.5: When a fake controller records a server-side transition and then withholds the response until the client deadline, the current model clears `busy`, stops automatic polling, preserves its correlation state, and reports that the outcome may be uncertain without fabricating `connected`, `failed`, `declined`, or `expired`.
  - verify: `TestMecatuiBrokerPollingLiveness_Scenario1_AmbiguousTimeoutFailsClosed`
- AC1.6: A timeout from connect, observe, retry, or cancel issues no automatic follow-up controller call and gives the same actionable recovery: run `/clear`, then `/tools-connect` in the replacement session.
  - verify: `TestMecatuiBrokerPollingLiveness_Scenario1_TimeoutRequiresFreshSessionRecovery`
- AC1.7: Parent cancellation is not rendered as a timeout, and session/enrollment replacement plus late results retain the existing generation guard: no expired attempt can mutate a replacement operation, arm its poller, open a presentation URL, or submit a retained prompt.
  - verify: `TestMecatuiBrokerPollingLiveness_Scenario1_CancellationAndLateResultStayInert`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Server-side ToolHive latency, retry policy, or per-upstream browser progression | ToolHive/broker reliability follow-up | This plan bounds the mecatui attempt and preserves ToolHive's ownership from ADR 0311. |
| New gRPC deadlines, status codes, fields, or server configuration | Separate API proposal | No protocol change is needed to stop the client state machines from stalling. |
| Entire-TUI rendering or input deadlocks unrelated to broker authorization/enrollment | Separate diagnosis | These fixes restore control-loop progress; they do not claim every perceived freeze has this cause. |
| `mecatui connect` diagnostics-log support or temporary trace instrumentation | Separate observability issue | Debug-log availability is independent of polling correctness. |
| Durable or multi-replica broker ownership | Existing cloud-native deferred work | Client liveness does not change broker placement or recovery authority. |

## Definition of done

1. `task lint`, `task test`, `task docs`, and `task api:check` pass.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green.
4. Offline tests execute returned Bubble Tea commands far enough to observe their messages and context cancellation; state-only helpers that discard commands are not sufficient proof.
5. The public mecatui guides describe bounded automatic browser-consent observation, the uncertain-outcome timeout message, and the `/clear` then `/tools-connect` recovery without promising server-side completion.
6. The sole Combined implementation PR carries this plan and reports interface conformance.
7. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- The approved fixed client deadline bounds a cooperative gRPC/controller call; it cannot force an arbitrary in-process test double or future controller that ignores context to terminate. Production gRPC honors cancellation, and tests must prove the shipped client path observes it.
- Timeout can race a server-side state transition. The client therefore treats the outcome as uncertain, performs no automatic same-session retry or inference, and requires replacement-session recovery; seamless same-session reconciliation would require a separate idempotent/query protocol proposal.
