# TypeScript SDK durable attachment (M2) — acceptance plan

**Phase:** capability — `@stacklok/mecatl-sdk` M2: durable attachment and connection authority
**Status:** landed, 2026-09-03. Stack PRs: [#999](https://github.com/stacklok/mecatl/pull/999), [#1013](https://github.com/stacklok/mecatl/pull/1013), [#1017](https://github.com/stacklok/mecatl/pull/1017), [#1021](https://github.com/stacklok/mecatl/pull/1021), [#1031](https://github.com/stacklok/mecatl/pull/1031), [#1035](https://github.com/stacklok/mecatl/pull/1035), [#1038](https://github.com/stacklok/mecatl/pull/1038), [#1040](https://github.com/stacklok/mecatl/pull/1040), [#1042](https://github.com/stacklok/mecatl/pull/1042), and [#1044](https://github.com/stacklok/mecatl/pull/1044). Synthesised from [#821](https://github.com/stacklok/mecatl/issues/821)'s settled "Attachment and reconnection" contract plus the client-side decisions ADR 0279 deferred to this milestone.
**Issue:** [stacklok/mecatl#821](https://github.com/stacklok/mecatl/issues/821) (parent: [#761](https://github.com/stacklok/mecatl/issues/761)).
**ADR:** [ADR-0288](../adr/0288-typescript-sdk-durable-attachment.md) — the envelope union, `attach`/`activity` semantics, the serializable run-and-filter-scoped cursor, the reconnect authority, the HTTP-only attached `cancel` (approval deferred), and the status arbitration rule.
**Accumulator / stack:** `sdk/21-envelope` is the stack root off `main`; subsequent layers are `sdk/22-attach` … `sdk/30-e2e` (linear, one PR per scenario).

The smallest set of work that lets a TypeScript client rejoin a running
mecatl session across a reload, a network drop, or a daemon restart, losing no
durably-appended event and never silently skipping one. The server half already
shipped — [ADR-0250](../adr/0250-durable-cursors-and-watch.md) landed
`port.CursorEventLog` across four backends, the `WatchSessionEvents` RPC, the
`GET /v1/sessions/{id}/watch` SSE route, and the `watch_session_events` feature
identifier. **This plan is client-side only.**

The doc is organized scenario-first because acceptance is about what the
running harness (here: the SDK against the running harness) can demonstrate,
not which modules exist on disk.

## Why these scope cuts

- **Nothing edits `contracts/proto/` or production `internal/adapter/server/`.**
  M2's first two work items are already on `main`. The only Go code this plan
  adds is three parity *tests* in the root module beside the existing
  `internal/adapter/server/sdk_typescript_*_test.go` files, which read TS
  manifests and compare them to the server's own constants. Reading server
  sources is fine; the no-touch constraint is about edits.
- **The envelope union is decided once, in the ADR, not per worker.**
  `WatchSessionEventsResponse` is `{event, cursor, phase}` with `event` absent
  on two different kinds of frame and `phase` an open string
  ([ADR-0250](../adr/0250-durable-cursors-and-watch.md) Decision 5), so there is
  no wire discriminant to switch on. Two workers inventing two discriminants
  would be an API split we could not undo after `v0.1.0`;
  [ADR-0288](../adr/0288-typescript-sdk-durable-attachment.md) Decision 1 fixes
  the four arms, the `boundary` naming, the narrowed `phase`, the cursor-free
  `gap` arm, and the once-per-attachment boundary up front. Decision 9 fixes the
  two attachment type signatures for the same reason.
- **`cancel()` is the one attached control M2 ships.** Enumerating
  every `rpc` in `HarnessService` yields no prompt-free `Approve`/`Cancel`/
  `Steer`: they exist only as `ConverseRequest` frames, and the gRPC handler
  enforces "first frame must be a prompt or retry", so an attachment — which
  owns no run — has no stream to put one on. HTTP's `POST
  /v1/sessions/{id}/cancel` is prompt-free and acks `204`, so **`cancel()` is
  the one attached control M2 ships**. Attached `approve`/`resolveAsk` are
  deferred: `/approve`'s cross-process rehydrate path relays SSE, and that body
  has no sound client handling — closing it cancels the run, leaving it unread
  stalls the relay, draining it is unbounded. Attached `steer` waits on
  [#873](https://github.com/stacklok/mecatl/issues/873). Each is a typed
  unsupported-feature error rather than an AC that passes against a fake and
  fails on the wire
  ([ADR-0288](../adr/0288-typescript-sdk-durable-attachment.md) Decision 6).
- **`attach()` derives its run from the log with ONE watch.** The proto's
  `Session` carries `state` but no `run_id`, and there is no `ListRuns`. Since
  runs are normally serial per session, "prefer the active run, otherwise the
  latest" collapses to "the newest run in the log" — and `attach()` with no
  `runId` opens one unfiltered watch with a client-side run filter rather than a
  discovery watch plus a second filtered one, which would be two full log reads
  and a watch to leak
  ([ADR-0288](../adr/0288-typescript-sdk-durable-attachment.md) Decision 2).
- **Cursor/filter scoping is enforced client-side because it cannot be enforced
  anywhere else.** The cursor envelope in
  [`engine/port/cursoreventlog.go`](../../engine/port/cursoreventlog.go) is
  `{version, session, generation, position}` — `run_id` is absent, so a cursor
  from a server-filtered watch replayed under a different filter decodes cleanly
  to a real, wrong position. The SDK cursor is a versioned **serializable
  string** (not a TypeScript brand, which would evaporate through
  `localStorage` — the reload case is the whole point) carrying the effective
  server filter *and*, separately, the run the attachment bound to — an implicit
  `attach()` filters run-side, so those two are not the same value, and storing
  only the filter would leave its cursor unresumable.
- **The default event filter is derived from the server, not written down.**
  `relayLiveEvent` is not simply "the three log-only kinds" — it *relays* a
  scheduled-fire `user_prompt` — and `isPublicEvent` strips `network.attempt`
  and `request.manifest` from every client surface **except** the watch route.
  A prose list would be wrong on day one, so the set is derived and gated by a
  third parity test extending the mechanism M1's
  `TestSDKTypescriptCore_Scenario6_LogOnlyKindsAudited` already uses.
- **The status monitor gains an input and an arbitration rule, not a
  vocabulary.** M1 shipped the closed six values and the subscriber-gated
  heartbeat; M2 adds a second long-lived writer, so the combining rule has to be
  stated or the scenario is vacuous — and it is a full precedence ranking, not
  just "any attachment reconnecting wins", since a retrying attachment can also
  be `unauthorized`.
- **Verify names follow M1's convention.** Go proofs are
  `TestSDKTypescriptAttach_ScenarioN_*`; TypeScript proofs use the strict
  `vitest:<path>#<base64url-title>` resolver form throughout, so a renamed or
  deleted test title fails `task ac-trace-strict` instead of degrading to a
  file-level proof.
- **The Redis follow pool is not ours.** N attachments are N blocked `XREAD`s
  competing with the write path; that is
  [#876](https://github.com/stacklok/mecatl/issues/876), named as a dependency
  rather than absorbed here.

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| A prompt-free gRPC control RPC (the fix for the attached-control asymmetry) | a later server plan | [ADR-0288](../adr/0288-typescript-sdk-durable-attachment.md) Decision 6 |
| An `active_run_id` (or `ListRuns`) server field that would remove `attach()`'s replay scan | a later server plan | [ADR-0288](../adr/0288-typescript-sdk-durable-attachment.md) Decision 2 |
| Applying `isPublicEvent` on the server's watch path | a later server plan | [ADR-0288](../adr/0288-typescript-sdk-durable-attachment.md) Decision 4 — M2 filters client-side and gates the set instead |
| Attached `steer` succeeding on either transport | gRPC: a control RPC; HTTP: [#873](https://github.com/stacklok/mecatl/issues/873) | [ADR-0252](../adr/0252-http-steer-endpoint.md) |
| **Concurrent** cross-daemon watch (two live daemons over one shared store) and any **Redis-backed** e2e | a later plan | [ADR-0250](../adr/0250-durable-cursors-and-watch.md) Decisions 2 and 4 — the e2e here is JSONL and sequential; the Redis follower's per-cycle basis re-verification is pinned by the shipped Go conformance suite, not by this client plan |
| `spawn()`, `query()`, callback `tool()`, the local MCP host | M3 plan | [#821](https://github.com/stacklok/mecatl/issues/821) M3 |
| Full HarnessService/ScheduleService RPC coverage; descriptor-to-transport parity gate | M4 plan | [#821](https://github.com/stacklok/mecatl/issues/821) M4 item 1 |
| Browser/Bun CI matrices, Playwright, TS 5.7 declaration matrix | M4 plan | [ADR-0279](../adr/0279-typescript-sdk-architecture.md) Decision 4 |
| Pausing an **attachment** on browser page-hidden | M4 plan | [ADR-0288](../adr/0288-typescript-sdk-durable-attachment.md) Decision 8 — a hidden tab that stops reading is the slow consumer `watch_lagging` terminates; the trade needs the real-browser matrix to measure |
| npm publish workflow, `sdk/typescript/vX.Y.Z` tags, SDK `user-docs/` pages | M4 plan | [#821](https://github.com/stacklok/mecatl/issues/821) M4 item 4 |
| Isolated Redis follow pool, fail-fast watcher admission, store-owned follower cancellation | [#876](https://github.com/stacklok/mecatl/issues/876) | [ADR-0250](../adr/0250-durable-cursors-and-watch.md) Consequences |
| `session.resolvePlan()` / `PlanResolution` | M4 plan | [#821](https://github.com/stacklok/mecatl/issues/821) — plan approval is distinct from permission approval |
| Downstream-app transport-agnostic e2e mocker | [#872](https://github.com/stacklok/mecatl/issues/872) | [ADR-0279](../adr/0279-typescript-sdk-architecture.md) Decision 3 |

## In scope — 10 scenarios, in implementation order

Scenarios are listed in implementation order. Each is independently demoable;
later scenarios assume earlier ones but do not change their acceptance
criteria. Within each, ACs progress happy path → richer happy path → edges →
cross-cutting. TypeScript proofs are cited with the strict `vitest:` resolver
token followed by the human-readable `<file> :: "<title>"`; titles are the
contract.

---

### Scenario 1 — The watch envelope union and the three parity gates

The wire envelope becomes a narrowable TypeScript union with a client-derived
`kind`: `event`, `boundary` (the single replay→live marker), `gap`, and
`unknown` for any phase string this SDK does not know
([ADR-0288](../adr/0288-typescript-sdk-durable-attachment.md) Decision 1). An
unknown phase is never thrown, never dropped, and never coerced to `live`:
`phase` is deliberately an open string
([ADR-0250](../adr/0250-durable-cursors-and-watch.md) Decision 5, mirroring the
[`AGENTS.md`](../../AGENTS.md) discipline that event `type`/`stop` are string
passthroughs), so a new phase must be a minor SDK release rather than a dead
branch — and the `unknown` arm is the tripwire that keeps a future
delivery-significant phase from folding silently into `event`.

Events inside an envelope are decoded by M1's existing `decodeEvent`, so the
kind-parity gate keeps covering the watch path with no second decoder. This
scenario also lands the three Go parity gates the milestone's quality strategy
rests on — phases, the feature id, and the derived filter set — all extending
the manifest-parsing mechanism M1's
`TestSDKTypescriptCore_Scenario6_LogOnlyKindsAudited` already proved.

**Work:**
- `sdk/typescript/src/`: the envelope union and decoder; BEGIN/END-delimited
  `MECATL_WATCH_PHASES` and `MECATL_ATTACH_FILTERED_KINDS` manifests beside the
  existing `MECATL_EVENT_KINDS`/`MECATL_ERROR_CODES`; the `./gen` descriptor
  wiring for `WatchSessionEvents`; the HTTP transport's `/watch` route mapping
  and its terminal `event: error` frame handling; a features accessor at the
  `RawClient`/`Client` seam (not private to the HTTP transport, or the gRPC
  watch would be ungated).
- root module: three parity tests beside
  `internal/adapter/server/sdk_typescript_eventkinds_test.go`, reusing its
  `parseTypescriptStringManifest` and source-parsing helpers.

**Acceptance:**
- AC1.1: A replay frame carrying an event, the replay→live marker, and a gap
  frame each decode to a distinct arm that narrows by literal `kind`; on the
  known arms `phase` is narrowed to its literal type, so a consumer can tell
  transcript from live without a string compare.
  - verify: vitest:sdk/typescript/test/watch-envelope.test.ts#ZXZlbnQsIGJvdW5kYXJ5LCBhbmQgZ2FwIGZyYW1lcyBuYXJyb3cgYnkga2luZCB3aXRoIGEgbmFycm93ZWQgcGhhc2U — `sdk/typescript/test/watch-envelope.test.ts :: "event, boundary, and gap frames narrow by kind with a narrowed phase"`
- AC1.2: An envelope whose `phase` this SDK does not know decodes to
  `{ kind: "unknown" }` preserving the raw phase string and its event if one
  was present; iteration continues, nothing throws, and the value is never
  relabelled `live`.
  - verify: vitest:sdk/typescript/test/watch-envelope.test.ts#YW4gdW5rbm93biBwaGFzZSBpcyBwcmVzZXJ2ZWQsIG5ldmVyIGNvZXJjZWQgYW5kIG5ldmVyIHRocm93bg — `sdk/typescript/test/watch-envelope.test.ts :: "an unknown phase is preserved, never coerced and never thrown"`
- AC1.3: An event inside an envelope is the same M1 `Event` union value the
  owned-run stream yields — a known kind narrows identically and an unknown
  event kind still becomes `{ kind: "unknown", wireKind, rawData }` with
  transport-native raw data. Replayed `message.delta` granularity may differ
  from the live stream's, because the server coalesces deltas before append.
  - verify: vitest:sdk/typescript/test/watch-envelope.test.ts#ZW52ZWxvcGUgZXZlbnRzIHJldXNlIHRoZSBNMSBldmVudCB1bmlvbiBhbmQgaXRzIHVua25vd24ta2luZCBjb252ZW50aW9u — `sdk/typescript/test/watch-envelope.test.ts :: "envelope events reuse the M1 event union and its unknown-kind convention"`
- AC1.4: One scripted frame sequence decodes to the same envelope values over
  the gRPC transport and the HTTP/SSE transport, including each frame's cursor —
  envelope parity at the raw seam, proven with twin in-process fakes.
  - verify: vitest:sdk/typescript/test/watch-envelope.test.ts#dGhlIHNhbWUgZnJhbWUgc2VxdWVuY2UgZGVjb2RlcyBpZGVudGljYWxseSBvdmVyIGdSUEMgYW5kIEhUVFA — `sdk/typescript/test/watch-envelope.test.ts :: "the same frame sequence decodes identically over gRPC and HTTP"`
- AC1.5: The `gap` arm exposes no resumable SDK cursor, even though the server's
  gap frame carries one — a consumer cannot accidentally resume past
  known-missing records, and the raw seam still surfaces the server's token.
  - verify: vitest:sdk/typescript/test/watch-envelope.test.ts#dGhlIGdhcCBhcm0gZXhwb3NlcyBubyByZXN1bWFibGUgY3Vyc29y — `sdk/typescript/test/watch-envelope.test.ts :: "the gap arm exposes no resumable cursor"`
- AC1.6: A Go parity guard fails when the SDK's known-phase manifest and the
  server's `WatchPhase*` constants disagree in either direction.
  - verify: `TestSDKTypescriptAttach_Scenario1_WatchPhaseParity`
- AC1.7: A Go parity guard fails when the feature identifier the SDK gates the
  watch on and the server's `FeatureWatchSessionEvents` constant disagree — the
  string is the contract a deployed client gates on, so a rename must not be a
  silent refactor.
  - verify: `TestSDKTypescriptAttach_Scenario1_WatchFeatureIdParity`
- AC1.8: A Go parity guard derives the server's non-public set (`isPublicEvent`)
  and its live-relay log-only skips (`relayLiveEvent`) from Go source and fails
  when the SDK's default-filtered manifest omits one or claims one the server
  does not skip; the one deliberate divergence (all `user_prompt`, versus the
  server's delivery-note exception) is an explicit, reasoned exclusion rather
  than a silent difference.
  - verify: `TestSDKTypescriptAttach_Scenario1_FilteredKindParity`

---

### Scenario 2 — `session.attach()`: run selection, liveness, and the four refusals

`session.attach(runId?)` returns one `AttachedRun`
([ADR-0288](../adr/0288-typescript-sdk-durable-attachment.md) Decision 9's
signature). With no `runId` it opens **one** unfiltered watch and filters
run-side in the client, taking the newest `run_id` it observes: because runs are
normally serial per session (the run registry gate in
[`AGENTS.md`](../../AGENTS.md)'s run-entry funnel), the active run *is* the
newest, so #821's "prefer the active run and otherwise the latest" cannot have
two answers. `AttachedRun.live` is a getter that flips false once the terminal
`result` is observed.

The scenario's edges are four refusals that must not be conflated, because they
demand different caller responses: no runs (create one), unknown-or-foreign
session (fix the id or the credentials), a build or deployment without the watch
(upgrade or reconfigure), and a delegation child id (that stream is deliberately
not caller-addressable —
[`internal/adapter/server/watch.go`](../../internal/adapter/server/watch.go)'s
gauntlet-#7 guard).

**Acceptance:**
- AC2.1: `attach()` with no run id on a session whose log holds two runs
  attaches to the newer one, exposes its non-empty run id, and opens exactly
  **one** watch — unfiltered, with the run filter applied client-side.
  - verify: vitest:sdk/typescript/test/attach.test.ts#YXR0YWNoIHdpdGggbm8gcnVuIGlkIHNlbGVjdHMgdGhlIG5ld2VzdCBydW4gb3ZlciBvbmUgdW5maWx0ZXJlZCB3YXRjaA — `sdk/typescript/test/attach.test.ts :: "attach with no run id selects the newest run over one unfiltered watch"`
- AC2.2: `AttachedRun.live` reads true while the run is in flight and false once
  its terminal `result` has been observed on the same attachment — it is a
  getter, not a value frozen at attach time.
  - verify: vitest:sdk/typescript/test/attach.test.ts#bGl2ZSBmbGlwcyBmYWxzZSB3aGVuIHRoZSB0ZXJtaW5hbCBpcyBvYnNlcnZlZA — `sdk/typescript/test/attach.test.ts :: "live flips false when the terminal is observed"`
- AC2.3: `attach()` on an existing, readable session whose log is empty or holds
  only run-less records rejects with `NoRunsError` and opens no follow.
  - verify: vitest:sdk/typescript/test/attach.test.ts#YSByZWFkYWJsZSBzZXNzaW9uIHdpdGggbm8gcnVuLWJlYXJpbmcgZXZlbnRzIGlzIE5vUnVuc0Vycm9y — `sdk/typescript/test/attach.test.ts :: "a readable session with no run-bearing events is NoRunsError"`
- AC2.4: `attach()` racing a just-started run — the id minted and stamped on
  the aggregate before the engine goroutine emits its first event — reports
  `NoRunsError` rather than hanging. The window is proven with a scripted
  transport whose session is running while its log is still empty, because **the
  initiating caller cannot deterministically orchestrate it**: `session.run()`
  resolves only after the first run-ID-bearing event, and the relay appends every
  event to the durable log before sending it, so a caller holding a run id
  necessarily holds it after the record that closes the window. A concurrent
  third party calling `attach()` blind can still land in the window — it is
  reachable through the public API, just not schedulable by the caller who
  started the run, which is why the proof is scripted rather than raced.
  - verify: vitest:sdk/typescript/test/attach.test.ts#YSBydW5uaW5nIHNlc3Npb24gd2l0aCBhbiBlbXB0eSBsb2cgcmVwb3J0cyBOb1J1bnNFcnJvcg — `sdk/typescript/test/attach.test.ts :: "a running session with an empty log reports NoRunsError"`
- AC2.5: `attach()` on an unknown or foreign session id under an
  ownership-enforcing deployment surfaces the server's typed
  `session_not_found` — **never** `NoRunsError`, because "create a run" and "fix
  your credentials" are opposite instructions.
  - verify: vitest:sdk/typescript/test/attach.test.ts#YW4gdW5rbm93biBvciBmb3JlaWduIHNlc3Npb24gaWQgaXMgc2Vzc2lvbl9ub3RfZm91bmQsIG5ldmVyIE5vUnVuc0Vycm9y — `sdk/typescript/test/attach.test.ts :: "an unknown or foreign session id is session_not_found, never NoRunsError"`
- AC2.6: `attach(runId)` with an explicit run id issues its watch with the
  server's `run_id` filter set, and observes no unfiltered replay.
  - verify: vitest:sdk/typescript/test/attach.test.ts#YW4gZXhwbGljaXQgcnVuIGlkIHVzZXMgdGhlIHNlcnZlciBydW4gZmlsdGVy — `sdk/typescript/test/attach.test.ts :: "an explicit run id uses the server run filter"`
- AC2.7: A server whose `GetCompatibilityInfo` omits `watch_session_events`
  fails `attach()` and `activity()` with the typed unsupported-feature error
  naming that feature before any watch request is sent, on **both** transports
  (the gate reads a shared features accessor, not one private to HTTP); a
  deployment whose build has the RPC but lacks a cursor-capable log surfaces the
  server's own `watch_unsupported`, and one with no event log at all surfaces
  `no_event_log`.
  - verify: vitest:sdk/typescript/test/attach.test.ts#dGhlIHdhdGNoIGZlYXR1cmUgZ2F0ZSBmaXJlcyBvbiBib3RoIHRyYW5zcG9ydHM — `sdk/typescript/test/attach.test.ts :: "the watch feature gate fires on both transports"`
- AC2.8: `attach()` or `activity()` on a delegation child session id
  (`subagent-*`, `parallel-*`, `team-*`) surfaces the server's typed
  invalid-argument refusal rather than an empty stream — a parent legitimately
  holds those ids from its own `agentId:` / `Team id:` result trailers — while a
  `sched--` fire session id is watchable, because the server's guard excludes it
  deliberately.
  - verify: vitest:sdk/typescript/test/attach.test.ts#YSBkZWxlZ2F0aW9uIGNoaWxkIGlkIGlzIHJlZnVzZWQgYW5kIGEgc2NoZWQtLSBpZCBpcyBub3Q — `sdk/typescript/test/attach.test.ts :: "a delegation child id is refused and a sched-- id is not"`

---

### Scenario 3 — Replay, the live boundary, and following to the terminal

An attachment is one operation with no window to lose events in: it replays
what was durable when it attached, crosses the single boundary frame, then
follows appends until the run's terminal `result` — the capability
[ADR-0250](../adr/0250-durable-cursors-and-watch.md) built the server half for.
An attachment on an already-finished run replays to that terminal and completes
without waiting.

`from: "now"` is a **client-side discard, not a wire capability**:
`WatchSessionEventsRequest` carries only `session_id`, `cursor`, and `run_id`,
so the replay still crosses the network and is dropped locally. It requires an
explicit `runId` because M2 declines to pay for a scan whose results it is
throwing away — not because it cannot see the run
([ADR-0288](../adr/0288-typescript-sdk-durable-attachment.md) Decision 4).

Both transports are proven from one shared scripted fixture, the
injected-`Transport` seam
[ADR-0279](../adr/0279-typescript-sdk-architecture.md) Decision 3 made the SDK's
only mock surface; the live-wire proof is Scenario 10.

**Acceptance:**
- AC3.1: An attachment on a live run yields its replayed envelopes in append
  order, then the boundary frame, then each live envelope as it is appended, and
  completes on the run's terminal `result` event.
  - verify: vitest:sdk/typescript/test/attach-lifecycle.test.ts#YW4gYXR0YWNobWVudCByZXBsYXlzLCBjcm9zc2VzIHRoZSBsaXZlIGJvdW5kYXJ5LCBhbmQgZm9sbG93cyB0byB0aGUgdGVybWluYWw — `sdk/typescript/test/attach-lifecycle.test.ts :: "an attachment replays, crosses the live boundary, and follows to the terminal"`
- AC3.2: An attachment on a run that already terminated replays to that terminal
  and completes; it does not park waiting for appends that will never come.
  - verify: vitest:sdk/typescript/test/attach-lifecycle.test.ts#YW4gYXR0YWNobWVudCBvbiBhIGZpbmlzaGVkIHJ1biByZXBsYXlzIHRvIGl0cyB0ZXJtaW5hbCBhbmQgY29tcGxldGVz — `sdk/typescript/test/attach-lifecycle.test.ts :: "an attachment on a finished run replays to its terminal and completes"`
- AC3.3: `attach(runId, { from: "now" })` yields no replay envelope and begins at
  the live boundary, having discarded the replay locally rather than asked the
  server to skip it; `from: "now"` without a run id fails locally with a typed
  error rather than attaching to nothing.
  - verify: vitest:sdk/typescript/test/attach-lifecycle.test.ts#ZnJvbSBub3cgZGlzY2FyZHMgdGhlIHJlcGxheSBsb2NhbGx5IGFuZCByZXF1aXJlcyBhIHJ1biBpZA — `sdk/typescript/test/attach-lifecycle.test.ts :: "from now discards the replay locally and requires a run id"`
- AC3.4: The full replay → boundary → live → terminal envelope sequence is
  identical over the gRPC transport and the HTTP/SSE transport for the same
  scripted server state.
  - verify: vitest:sdk/typescript/test/attach-lifecycle.test.ts#dGhlIGF0dGFjaG1lbnQgbGlmZWN5Y2xlIGFncmVlcyBhY3Jvc3MgZ1JQQyBhbmQgSFRUUA — `sdk/typescript/test/attach-lifecycle.test.ts :: "the attachment lifecycle agrees across gRPC and HTTP"`

---

### Scenario 4 — Checkpointing, at-least-once, and the serializable branded cursor

Filtering is a yield-time decision; checkpointing is a consumption-time one. The
checkpoint advances **when the consumer requests the next envelope** — #821's
"natural at-least-once", so a consumer that dies after processing an envelope
but before asking for another re-receives it. It also advances over records the
ergonomic filter dropped, because the watch route is the read-back of the
durable log and carries kinds the live relay skips
([`internal/adapter/server/http.go`](../../internal/adapter/server/http.go): "do
NOT copy the live-relay filter here").

The same consume-but-do-not-yield split has a second consumer: `approval` is
filtered by default while `permission.ask` is not, so an attachment that only
yielded on the filter would report long-resolved asks as pending.

The exposed cursor is a versioned, **serializable** opaque string branded with
its effective server filter
([ADR-0288](../adr/0288-typescript-sdk-durable-attachment.md) Decision 3) — a
string rather than a TypeScript brand precisely because the headline use case is
a page reload, where a brand would evaporate through `localStorage` and defeat
the check for the one case it exists for. A cross-filter resume is refused
locally with `CursorScopeError`, because the server structurally cannot catch it
(`run_id` is absent from its cursor envelope).

**Acceptance:**
- AC4.1: An attachment's exposed cursor equals the cursor of the last envelope
  the consumer *requested past*, not the last one yielded — pulling envelope N
  leaves the checkpoint at N−1 until N+1 is requested.
  - verify: vitest:sdk/typescript/test/attach-cursor.test.ts#dGhlIGNoZWNrcG9pbnQgYWR2YW5jZXMgd2hlbiB0aGUgY29uc3VtZXIgcmVxdWVzdHMgdGhlIG5leHQgZW52ZWxvcGU — `sdk/typescript/test/attach-cursor.test.ts :: "the checkpoint advances when the consumer requests the next envelope"`
- AC4.2: A default attachment omits the derived filtered kinds from what it
  yields while still advancing its checkpoint past them, so a resume from that
  cursor re-delivers none of them.
  - verify: vitest:sdk/typescript/test/attach-cursor.test.ts#dGhlIGNoZWNrcG9pbnQgYWR2YW5jZXMgb3ZlciBmaWx0ZXJlZCByZWNvcmRz — `sdk/typescript/test/attach-cursor.test.ts :: "the checkpoint advances over filtered records"`
- AC4.3: A filtered `approval` record is still **consumed** for ask bookkeeping,
  so an attachment replaying a log with a resolved ask does not report that ask
  as pending — the filter suppresses the yield, never the state.
  - verify: vitest:sdk/typescript/test/attach-cursor.test.ts#YSBmaWx0ZXJlZCBhcHByb3ZhbCBzdGlsbCByZXRpcmVzIGl0cyBhc2s — `sdk/typescript/test/attach-cursor.test.ts :: "a filtered approval still retires its ask"`
- AC4.4: Re-attaching with a previously exposed cursor resumes at the next
  record and delivers each subsequent envelope at least once; an envelope
  processed but not requested past is re-delivered exactly once rather than lost.
  - verify: vitest:sdk/typescript/test/attach-cursor.test.ts#cmVzdW1pbmcgZnJvbSBhbiBleHBvc2VkIGN1cnNvciByZS1kZWxpdmVycyBhdCBsZWFzdCBvbmNl — `sdk/typescript/test/attach-cursor.test.ts :: "resuming from an exposed cursor re-delivers at least once"`
- AC4.5: A cursor survives `JSON.stringify`/`parse` into application storage and
  resumes correctly when handed to a **freshly constructed** `Client` — the
  reload case — retaining its filter brand across the round trip.
  - verify: vitest:sdk/typescript/test/attach-cursor.test.ts#YSBzZXJpYWxpemVkIGN1cnNvciByZXN1bWVzIHRocm91Z2ggYSBmcmVzaCBjbGllbnQ — `sdk/typescript/test/attach-cursor.test.ts :: "a serialized cursor resumes through a fresh client"`
- AC4.6: Cursor scope is **delivered-set containment over both the run binding
  and the server filter** — a cursor may be handed only to a view that delivers a
  subset of what its own view delivered. A cursor bound to run `R` (from either
  `attach()` form) resumes only a view bound to `R`; handing it to `activity()`
  or to `attach(otherRun)` fails with `CursorScopeError` before any request,
  because the bound view advanced its position past other runs' records **without
  yielding them** and a wider view resuming there would skip them silently. An
  unbound `activity()` cursor resumes anything, since an attachment yields a
  subset of what activity already delivered. A cursor issued under a server
  `run_id` filter is additionally never widened past it.
  - verify: vitest:sdk/typescript/test/attach-cursor.test.ts#Y3Vyc29yIHNjb3BlIGlzIGRlbGl2ZXJlZC1zZXQgY29udGFpbm1lbnQgb3ZlciBydW4gYW5kIGZpbHRlcg — `sdk/typescript/test/attach-cursor.test.ts :: "cursor scope is delivered-set containment over run and filter"`
- AC4.7: Cursor acceptance is **structural**, not provenance-based: a value
  that is not a well-formed `sdkcur/1` envelope — wrong version, undecodable,
  missing fields, or a raw server token from the raw seam — is refused locally
  with `CursorMalformedError` before any request, and the public cursor type
  exposes no member a caller can meaningfully author. A well-formed,
  correctly-filtered envelope is accepted whoever built it — the encoding is
  stateless and unsigned, so a fresh `Client` cannot distinguish one it issued
  from a byte-identical hand-built one, and its inner token then faces the
  server's own generation and session validation.
  - verify: vitest:sdk/typescript/test/attach-cursor.test.ts#Y3Vyc29yIGFjY2VwdGFuY2UgaXMgc3RydWN0dXJhbCByYXRoZXIgdGhhbiBwcm92ZW5hbmNlLWJhc2Vk — `sdk/typescript/test/attach-cursor.test.ts :: "cursor acceptance is structural rather than provenance-based"`
- AC4.8: A complete attach → consume → reconnect → dispose cycle touches no
  `localStorage`, no `sessionStorage`, and no filesystem path; the SDK exposes
  cursors for the application to persist and persists nothing itself.
  - verify: vitest:sdk/typescript/test/attach-cursor.test.ts#YW4gYXR0YWNobWVudCB3cml0ZXMgbm8gbG9jYWwgc3RvcmFnZSBhbmQgbm8gZmlsZXM — `sdk/typescript/test/attach-cursor.test.ts :: "an attachment writes no local storage and no files"`
- AC4.9: An attachment is single-consumption: a second concurrent consumer of
  the same attachment fails with a typed invalid-state error rather than
  silently splitting the envelope stream across two readers of one checkpoint.
  - verify: vitest:sdk/typescript/test/attach-cursor.test.ts#YW4gYXR0YWNobWVudCBoYXMgZXhhY3RseSBvbmUgY29uc3VtZXI — `sdk/typescript/test/attach-cursor.test.ts :: "an attachment has exactly one consumer"`
- AC4.10: An implicit `attach()`'s cursor carries the run it bound to, distinct
  from its (empty) server filter, so a resume through a fresh `Client` reattaches
  to **that** run — not to whichever run is newest by then, and not
  `NoRunsError` — even when later runs have since been appended. The run
  identity is restored from the cursor rather than re-derived, and the same
  binding is what AC4.6 refuses to widen.
  - verify: vitest:sdk/typescript/test/attach-cursor.test.ts#YW4gaW1wbGljaXQgYXR0YWNoIGN1cnNvciByZXN0b3JlcyBpdHMgcnVuIHJhdGhlciB0aGFuIHJlLWRlcml2aW5nIG9uZQ — `sdk/typescript/test/attach-cursor.test.ts :: "an implicit attach cursor restores its run rather than re-deriving one"`

---

### Scenario 5 — Gaps and cursor faults are explicit, never silent

The conditions that end an attachment instead of reconnecting it.
`ActivityGapError` means delivery is known-incomplete: it is reachable both as a
`gap` **envelope arm** a raw consumer observes and as the terminal error the
ergonomic attachment raises, and neither path ever advances the checkpoint past
the gap. `CursorExpiredError` means the log generation moved; per
[ADR-0250](../adr/0250-durable-cursors-and-watch.md) Decision 3 it "requires an
explicit restart-from-beginning or transcript reload and never degrades
silently", so the SDK must not quietly restart on the caller's behalf.
`CursorMalformedError` is distinct because the server distinguishes them for a
reason: expiry means something moved underneath the caller, malformed means the
cursor was never valid here.

The transports surface a cursor fault differently, and the plan must say so:
over gRPC it is a status before any Send, but over SSE the cursor is decoded
inside `log.ReadAfter` — **after** the 200 has been committed — so it is always
a 200 plus a stream-terminal `event: error` frame, which
[`internal/adapter/server/errorcodes.go`](../../internal/adapter/server/errorcodes.go)
records as a deliberate transport note on exactly these two rows.

**Acceptance:**
- AC5.1: A `gap` envelope mid-stream ends an ergonomic attachment with
  `ActivityGapError` whose code is `activity_gap` and whose transport is
  `local`, and the attachment's exposed cursor still points at the last envelope
  before the gap — never past it.
  - verify: vitest:sdk/typescript/test/attach-errors.test.ts#YSBnYXAgYmVjb21lcyBhIGxvY2FsIEFjdGl2aXR5R2FwRXJyb3IgYW5kIG5ldmVyIGFkdmFuY2VzIHRoZSBjaGVja3BvaW50 — `sdk/typescript/test/attach-errors.test.ts :: "a gap becomes a local ActivityGapError and never advances the checkpoint"`
- AC5.2: A `cursor_expired` failure ends the attachment with
  `CursorExpiredError` and triggers no reconnect and no implicit
  restart-from-beginning; a caller that wants either asks for it explicitly.
  - verify: vitest:sdk/typescript/test/attach-errors.test.ts#YW4gZXhwaXJlZCBjdXJzb3IgZW5kcyB0aGUgYXR0YWNobWVudCBpbnN0ZWFkIG9mIHJlc3RhcnRpbmcgaXQ — `sdk/typescript/test/attach-errors.test.ts :: "an expired cursor ends the attachment instead of restarting it"`
- AC5.3: A cursor fault yields the same typed SDK error on both transports from
  the two shapes the server actually emits: a gRPC status carrying the code, and
  over HTTP/SSE a **200 followed by a terminal `event: error` frame** carrying
  it — there is no cursor-fault problem body on the watch route, because the
  cursor is decoded after the status is committed. `cursor_malformed` stays a
  distinct type from `cursor_expired`, and both preserve code, request id, and
  transport metadata.
  - verify: vitest:sdk/typescript/test/attach-errors.test.ts#YSBjdXJzb3IgZmF1bHQgbWFwcyBpZGVudGljYWxseSBmcm9tIGEgZ1JQQyBzdGF0dXMgYW5kIGFuIFNTRSBlcnJvciBmcmFtZQ — `sdk/typescript/test/attach-errors.test.ts :: "a cursor fault maps identically from a gRPC status and an SSE error frame"`
- AC5.4: The raw watch surfaces a gap as a `{ kind: "gap" }` envelope the caller
  can observe and step past deliberately, rather than throwing — the raw seam
  reports, the ergonomic layer decides.
  - verify: vitest:sdk/typescript/test/attach-errors.test.ts#dGhlIHJhdyB3YXRjaCBzdXJmYWNlcyB0aGUgZ2FwIGFybSByYXRoZXIgdGhhbiB0aHJvd2luZw — `sdk/typescript/test/attach-errors.test.ts :: "the raw watch surfaces the gap arm rather than throwing"`
- AC5.5: The SDK's published attachment documentation states the
  at-least-once-for-durably-appended-events guarantee and its residual
  undetectable-gap case, without claiming an absolute
  [ADR-0250](../adr/0250-durable-cursors-and-watch.md) explicitly declined to
  ship.
  - verify: inspection — a doc claim cannot be unit-tested; review checks the
    published wording against ADR-0250 Decision 6, and AC5.1/AC5.4 pin the
    behaviour the wording describes.

---

### Scenario 6 — Reconnect authority: three arms, keyed on a closed code set

An attachment reconnects indefinitely until its `AbortSignal` fires or it is
disposed, with bounded exponential backoff plus jitter, resuming from its own
checkpoint under its own filter — #821's contract. The classification is
**three-way** ([ADR-0288](../adr/0288-typescript-sdk-durable-attachment.md)
Decision 5): resume a transport-shaped failure or `watch_lagging`; **terminate**
on a closed, one-place set of durably-answered refusal codes; and never retry
any non-watch operation at all — a property of the *named* operation, not an
error-class allowlist a future RPC could join by resembling one.

`watch_lagging` is explicitly resumable:
[`internal/adapter/server/watch.go`](../../internal/adapter/server/watch.go)
documents that its error carries no cursor precisely because the client's own
last-received envelope is the correct resume point.

**Work:** an **internal** scheduler seam (`delayFor(attempt)` / `sleep(ms,
signal)`) threaded through an unexported options bag, because jitter is
`Math.random` and a cleared timer is otherwise unobservable; it stays out of the
public surface, as the bounds are not caller configuration.

**Acceptance:**
- AC6.1: A watch that drops mid-follow reconnects and resumes from the
  attachment's checkpoint with the same effective filter it attached under; the
  consumer observes a continuous envelope sequence with no missing record.
  - verify: vitest:sdk/typescript/test/attach-reconnect.test.ts#YSBkcm9wcGVkIHdhdGNoIHJlY29ubmVjdHMgZnJvbSBpdHMgY2hlY2twb2ludCB1bmRlciB0aGUgc2FtZSBmaWx0ZXI — `sdk/typescript/test/attach-reconnect.test.ts :: "a dropped watch reconnects from its checkpoint under the same filter"`
- AC6.2: Reconnect delays grow monotonically to a fixed cap and stay there, and
  are jittered — asserted over the injected scheduler seam across at least five
  attempts, so the bound is falsifiable and the assertion is not flake-prone.
  - verify: vitest:sdk/typescript/test/attach-reconnect.test.ts#YmFja29mZiBncm93cyBtb25vdG9uaWNhbGx5IHRvIGEgY2FwIGFuZCBpcyBqaXR0ZXJlZA — `sdk/typescript/test/attach-reconnect.test.ts :: "backoff grows monotonically to a cap and is jittered"`
- AC6.3: A `watch_lagging` termination reconnects from the checkpoint and is not
  surfaced to the consumer as an error; no envelope between the checkpoint and
  the termination is skipped.
  - verify: vitest:sdk/typescript/test/attach-reconnect.test.ts#YSBsYWdnaW5nIHdhdGNoIGlzIHJlc3VtYWJsZSBhbmQgcmVjb25uZWN0cyByYXRoZXIgdGhhbiBmYWlsaW5n — `sdk/typescript/test/attach-reconnect.test.ts :: "a lagging watch is resumable and reconnects rather than failing"`
- AC6.4: A transport failure on a session mutation, a prompt, a permission
  verdict, or an owned `Run`'s stream is never retried automatically — each
  surfaces its typed error on the first failure, and the server observes exactly
  one attempt.
  - verify: vitest:sdk/typescript/test/attach-reconnect.test.ts#bXV0YXRpb25zLCBwcm9tcHRzLCBhcHByb3ZhbHMsIGFuZCBvd25lZCBydW5zIGFyZSBuZXZlciByZXRyaWVk — `sdk/typescript/test/attach-reconnect.test.ts :: "mutations, prompts, approvals, and owned runs are never retried"`
- AC6.5: Each code in the closed terminal set — `cursor_expired`,
  `cursor_malformed`, `activity_gap`, `session_not_found`, `invalid_argument`,
  `management_unauthorized`, `incompatible_server`, `watch_unsupported`,
  `no_event_log` — ends the attachment with its typed error and issues **no**
  further reconnect attempt; the set is read from one place, so a code cannot be
  classified two ways.
  - verify: vitest:sdk/typescript/test/attach-reconnect.test.ts#YSBwZXJtYW5lbnQgcHJlY29uZGl0aW9uIGNvZGUgZW5kcyB0aGUgYXR0YWNobWVudCByYXRoZXIgdGhhbiByZXRyeWluZw — `sdk/typescript/test/attach-reconnect.test.ts :: "a permanent precondition code ends the attachment rather than retrying"`
- AC6.6: A watch stream that ends **cleanly** — no error, no terminal `result`
  — is treated as resumable and reconnects from the checkpoint, not as a
  completed attachment. This is the shape a daemon shutdown produces
  (`Service.closeWatches` ends every watch without an error and documents that
  the client reconnects with its cursor), and over HTTP/SSE it arrives as a body
  that simply finishes; an attachment that completed there would end silently
  mid-run.
  - verify: vitest:sdk/typescript/test/attach-reconnect.test.ts#YSBjbGVhbiBub24tdGVybWluYWwgc3RyZWFtIGVuZCByZXN1bWVzIHJhdGhlciB0aGFuIGNvbXBsZXRpbmc — `sdk/typescript/test/attach-reconnect.test.ts :: "a clean non-terminal stream end resumes rather than completing"`
- AC6.7: A `SessionActivity` reconnects on **every** clean EOF, including one
  arriving after it has already observed one or more `result` events — an
  activity stream has no terminal, so a run ending is not the timeline ending.
  An `AttachedRun`, by contrast, completes on its own run's `result` and
  resumes only on a clean EOF that precedes it.
  - verify: vitest:sdk/typescript/test/attach-reconnect.test.ts#YWN0aXZpdHkgcmVzdW1lcyBhZnRlciBhIGNsZWFuIEVPRiB0aGF0IGZvbGxvd3MgYSBydW4gcmVzdWx0 — `sdk/typescript/test/attach-reconnect.test.ts :: "activity resumes after a clean EOF that follows a run result"`
- AC6.8: An ordinary `authentication` failure on a reconnect attempt resumes,
  re-invoking the credential provider so a refreshed token is presented on the
  next attempt, while `management_unauthorized` terminates — the 401/403 split
  the server's own registry draws, since no refresh changes an authorization
  decision.
  - verify: vitest:sdk/typescript/test/attach-reconnect.test.ts#YXV0aGVudGljYXRpb24gcmVzdW1lcyB3aXRoIGEgcmVmcmVzaGVkIGNyZWRlbnRpYWwgd2hpbGUgbWFuYWdlbWVudF91bmF1dGhvcml6ZWQgdGVybWluYXRlcw — `sdk/typescript/test/attach-reconnect.test.ts :: "authentication resumes with a refreshed credential while management_unauthorized terminates"`
- AC6.9: The cached compatibility info is invalidated **before the first
  reconnect attempt**, the moment the attachment leaves `online`, and re-probed
  as part of that attempt — not after a reconnect succeeds, which would already
  have spent one attempt on a stale floor. A daemon restarted without
  `watch_session_events` is observed as unsupported on the very first attempt
  rather than dialled once and re-checked afterwards.
  - verify: vitest:sdk/typescript/test/attach-reconnect.test.ts#YSByZWNvbm5lY3QgYWZ0ZXIgYSBnYXAgaW52YWxpZGF0ZXMgdGhlIGNvbXBhdGliaWxpdHkgY2FjaGU — `sdk/typescript/test/attach-reconnect.test.ts :: "a reconnect after a gap invalidates the compatibility cache"`
- AC6.10: An attachment that reconnects announces the replay→live boundary
  **once**, on its first crossing — the resumed watch's own boundary frame is
  not re-yielded, so a consumer that switches from transcript to live view on
  the boundary does not flap on every network blip.
  - verify: vitest:sdk/typescript/test/attach-reconnect.test.ts#YSByZWNvbm5lY3QgZG9lcyBub3QgcmUtYW5ub3VuY2UgdGhlIHJlcGxheS10by1saXZlIGJvdW5kYXJ5 — `sdk/typescript/test/attach-reconnect.test.ts :: "a reconnect does not re-announce the replay-to-live boundary"`
- AC6.11: Aborting the signal, `Symbol.asyncDispose`, `break`ing out of
  `for await`, and closing the owning `Client` mid-backoff each stop the loop,
  clear the pending timer, release the watch, and issue no further request.
  - verify: vitest:sdk/typescript/test/attach-reconnect.test.ts#ZXZlcnkgcmVsZWFzZSBwYXRoIHN0b3BzIHRoZSBsb29wIGFuZCBjbGVhcnMgdGhlIHRpbWVy — `sdk/typescript/test/attach-reconnect.test.ts :: "every release path stops the loop and clears the timer"`

---

### Scenario 7 — `session.activity()`: the cross-run timeline

`activity()` is the unfiltered watch: every run's events in one ordered stream,
the session timeline no existing endpoint offered. It shares the ergonomic
layer's derived filtering and its opt-in, and issues cursors branded with the
empty filter.

It is also the other half of an asymmetry
[ADR-0249](../adr/0249-durable-run-identity.md) explicitly predicted "will look
like an inconsistency to anyone who has not read this ADR": a session whose only
log content is run-less (`schedule.*`) events yields `NoRunsError` from
`attach()` while `activity()` succeeds. Pinning both halves is what makes the
asymmetry a decision rather than a bug report.

Filtering never suppresses a `gap`: a gap is a delivery fact rather than an
event, so it has no `kind` to filter on
([ADR-0250](../adr/0250-durable-cursors-and-watch.md) Decision 5 — a gap is "a
fact about DELIVERY, not something that happened in the run").

**Acceptance:**
- AC7.1: `activity()` on a session with two completed runs and one live run
  yields every run's envelopes in append order, each carrying its own `run_id`,
  and follows the live run's appends.
  - verify: vitest:sdk/typescript/test/activity.test.ts#YWN0aXZpdHkgc3BhbnMgZXZlcnkgcnVuIGluIHRoZSBzZXNzaW9u — `sdk/typescript/test/activity.test.ts :: "activity spans every run in the session"`
- AC7.2: `activity()` omits the derived filtered kinds by default;
  `{ includeLogOnly: true }` yields them, and the envelope sequence is otherwise
  identical — the opt-in adds records, it does not reorder or re-cursor them.
  - verify: vitest:sdk/typescript/test/activity.test.ts#ZmlsdGVyZWQga2luZHMgYXJlIG9taXR0ZWQgYnkgZGVmYXVsdCBhbmQgb3B0LWluIHJlc3RvcmVzIHRoZW0 — `sdk/typescript/test/activity.test.ts :: "filtered kinds are omitted by default and opt-in restores them"`
- AC7.3: A `gap` envelope reaches the consumer with filtering active — the
  filter operates on event kinds and can never drop a delivery-phase frame.
  - verify: vitest:sdk/typescript/test/activity.test.ts#Z2FwIGZyYW1lcyBzdXJ2aXZlIGZpbHRlcmluZw — `sdk/typescript/test/activity.test.ts :: "gap frames survive filtering"`
- AC7.4: On a session whose log holds only run-less events, `activity()` yields
  them and follows while `attach()` rejects with `NoRunsError` — the two-tier
  model ADR-0249 records, pinned on both sides.
  - verify: vitest:sdk/typescript/test/activity.test.ts#YWN0aXZpdHkgZm9sbG93cyBhIHJ1bi1sZXNzIHNlc3Npb24gd2hlcmUgYXR0YWNoIHJlZnVzZXM — `sdk/typescript/test/activity.test.ts :: "activity follows a run-less session where attach refuses"`

---

### Scenario 8 — Attached cancel: HTTP-only, stale-guarded, and detach-safe

`AttachedRun` exposes `cancel()`, carrying `expected_run_id` for the run it
attached to — the
[ADR-0249](../adr/0249-durable-run-identity.md) stale-control contract. It does
**not** ride M1's `RunOperations.send`, which is a synchronous `void` push of a
`ConverseRequest` frame onto a stream an attachment does not have and cannot
await; it goes through a dedicated asynchronous out-of-band control seam
(AC8.1). What is shared with M1 is the `expected_run_id` contract and its error
mapping — not the request path, and not the verdict vocabulary, which `cancel()`
has no use for and which returns to scope only with the deferred approval
methods. Over HTTP it rides the prompt-free `POST /v1/sessions/{id}/cancel`
route, a `204` ack with no body; **over gRPC it raises a typed unsupported-feature error
naming the missing channel**, because there is no prompt-free control RPC and
`Converse`'s first frame must be a prompt.

`approve()` and `resolveAsk()` are **deferred** (AC8.4). The approve route acks
`204` on the same-process path, but on the cross-process rehydrate path it
relays the resumed run's events as SSE — a body that cannot be closed early
(that cancels the run), left unread (that stalls the relay), or drained to EOF
(unbounded: the resumed run can park on another ask, so a drain surviving
`Client.close()` holds a socket and keeps the event loop alive). The server
already acks-and-drains itself when the writer is not a `Flusher`; making that
selectable is the fix, and it is a server change out of scope here
([ADR-0288](../adr/0288-typescript-sdk-durable-attachment.md) Decision 6).

**Detach never cancels:** releasing an attachment by any path releases the watch
and nothing else, and the run continues — the property that makes an observer
safe to attach.

**Acceptance:**
- AC8.1: Every `cancel` issued through an `AttachedRun` over HTTP carries that
  run's id as `expected_run_id` and rides the prompt-free
  `POST /v1/sessions/{id}/cancel` route, which is a `204` ack with no response
  body. It goes through a dedicated **asynchronous out-of-band control seam**
  (`cancelRun(sessionId, runId): Promise<void>`), not M1's `RunOperations.send`,
  which is a synchronous `void` push of a `ConverseRequest` frame onto a stream
  an attachment does not have. The ADR-0249 `expected_run_id` contract and its
  error mapping are shared between the two seams; the delivery mechanism is not.
  - verify: vitest:sdk/typescript/test/attached-controls.test.ts#YXR0YWNoZWQgY2FuY2VsIGdvZXMgdGhyb3VnaCB0aGUgYXN5bmMgb3V0LW9mLWJhbmQgY29udHJvbCBzZWFt — `sdk/typescript/test/attached-controls.test.ts :: "attached cancel goes through the async out-of-band control seam"`
- AC8.2: `cancel()` itself **resolves from the response**: the returned promise
  settles only after the server accepts (`204`), and a caller that awaits it and
  then reads the attachment observes the cancelled terminal. A transport failure
  on the cancel request rejects that same promise rather than being swallowed by
  a fire-and-forget send.
  - verify: vitest:sdk/typescript/test/attached-controls.test.ts#Y2FuY2VsIHJlc29sdmVzIGZyb20gdGhlIHJlc3BvbnNlIGFuZCByZWplY3RzIG9uIHRyYW5zcG9ydCBmYWlsdXJl — `sdk/typescript/test/attached-controls.test.ts :: "cancel resolves from the response and rejects on transport failure"`
- AC8.3: A `cancel` issued through an attachment whose run has since terminated
  surfaces the server's typed stale-control failure, and a newer run on the same
  session is observably untouched by it.
  - verify: vitest:sdk/typescript/test/attached-controls.test.ts#YSBzdGFsZSBhdHRhY2hlZCBjb250cm9sIGZhaWxzIHR5cGVkIGFuZCBsZWF2ZXMgYSBuZXdlciBydW4gdW50b3VjaGVk — `sdk/typescript/test/attached-controls.test.ts :: "a stale attached control fails typed and leaves a newer run untouched"`
- AC8.4: `cancel` over the gRPC transport fails with a typed
  unsupported-feature error naming the absent prompt-free control channel — not
  a generic transport error, and never by opening a `Converse` stream with a
  prompt.
  - verify: vitest:sdk/typescript/test/attached-controls.test.ts#YXR0YWNoZWQgY2FuY2VsIG92ZXIgZ1JQQyBpcyBhIHR5cGVkIHVuc3VwcG9ydGVkLWZlYXR1cmUgZXJyb3I — `sdk/typescript/test/attached-controls.test.ts :: "attached cancel over gRPC is a typed unsupported-feature error"`
- AC8.5: `AttachedRun.approve()` and `resolveAsk()` retain their
  `Promise<never>` signatures and fail locally with a typed unsupported-feature
  error on both transports. Both now name the shared `attached_run_controls`
  compatibility deferral and direct applications to
  `session.controls(attached.runId)`; neither opens `Converse` nor posts to the
  legacy SSE-relaying approve route. The original transport-specific
  `approve_ack_only` (HTTP) and `prompt_free_controls` (gRPC) reasons were
  accurate when this plan landed, but [ADR-0346](../adr/0346-run-id-addressed-prompt-free-controls.md)
  Decision 8 supersedes that deferral model now that prompt-free controls ship
  as a separate run-ID-addressed resource.
  - verify: vitest:sdk/typescript/test/attached-controls.test.ts#YXR0YWNoZWQgYXBwcm92YWwga2VlcHMgdGhlIGxvY2FsIGNvbXBhdGliaWxpdHkgZGVmZXJyYWwgb24gYm90aCB0cmFuc3BvcnRz — `sdk/typescript/test/attached-controls.test.ts :: "attached approval keeps the local compatibility deferral on both transports"`
- AC8.6: `AttachedRun.steer()` follows the same ADR-0346 compatibility rule:
  it fails locally with `attached_run_controls` on both transports, directs the
  caller to the run-ID-addressed resource, and never promotes into a fresh run,
  which would mint a run id the attachment's filter can never match and turn a
  refusal into silence.
  - verify: vitest:sdk/typescript/test/attached-controls.test.ts#YXR0YWNoZWQgc3RlZXIga2VlcHMgdGhlIGxvY2FsIGNvbXBhdGliaWxpdHkgZGVmZXJyYWwgYW5kIG5ldmVyIHByb21vdGVz — `sdk/typescript/test/attached-controls.test.ts :: "attached steer keeps the local compatibility deferral and never promotes"`
- AC8.7: Aborting the signal, disposing via `Symbol.asyncDispose`, and `break`ing
  out of iteration each release the watch without sending a cancel; the run
  continues to its own terminal and a fresh attachment observes that terminal.
  - verify: vitest:sdk/typescript/test/attached-controls.test.ts#ZXZlcnkgZGV0YWNoIHBhdGggbGVhdmVzIHRoZSBydW4gcnVubmluZw — `sdk/typescript/test/attached-controls.test.ts :: "every detach path leaves the run running"`


---

### Scenario 9 — The status monitor: a second writer and an arbitration rule

The six-value vocabulary stays closed — M1's contract, unchanged. What M2 adds
is a *second* long-lived writer, so the rule for combining them has to be stated
or every assertion is satisfied by last-writer-wins
([ADR-0288](../adr/0288-typescript-sdk-durable-attachment.md) Decision 8): the
client reports `reconnecting` while **any** attachment is reconnecting — unless
`unauthorized` or `incompatible` outranks it (AC9.3) — and `online` only when
none is, and a reconnecting attachment never publishes `offline` even though
M1's error mapping would.

An attachment is deliberately **not** a status subscriber: it must not keep a
heartbeat alive, because a long attachment is exactly the case where the
heartbeat is redundant — the stream itself is the liveness signal. Attachments
are also **not** paused on page-hidden, a trade deferred to M4's real-browser
matrix rather than guessed here.

**Acceptance:**
- AC9.1: A watch that drops moves the client's status to `reconnecting`, and a
  successful resume moves it back to `online`; `getSnapshot()` agrees with the
  latest `subscribe()` emission throughout.
  - verify: vitest:sdk/typescript/test/attach-status.test.ts#YSByZWNvbm5lY3RpbmcgYXR0YWNobWVudCBkcml2ZXMgdGhlIGNvbm5lY3Rpb24gc3RhdHVzIG1vbml0b3I — `sdk/typescript/test/attach-status.test.ts :: "a reconnecting attachment drives the connection status monitor"`
- AC9.2: With two attachments where one is reconnecting and the other is
  healthy, the client reports `reconnecting` — absent a higher-precedence
  `unauthorized` or `incompatible` (AC9.3); it reports `online` only once no
  attachment is reconnecting, and a successful unary request while an attachment
  is down does not report `online`.
  - verify: vitest:sdk/typescript/test/attach-status.test.ts#cmVjb25uZWN0aW5nIHdpbnMgd2hpbGUgYW55IGF0dGFjaG1lbnQgaXMgcmVjb25uZWN0aW5n — `sdk/typescript/test/attach-status.test.ts :: "reconnecting wins while any attachment is reconnecting"`
- AC9.3: Status resolves by a fixed precedence — `incompatible` >
  `unauthorized` > `reconnecting` > `connecting` > `offline` > `online` — not by
  last writer. An attachment retrying an `authentication` failure is both
  reconnecting and unauthorized and reports `unauthorized`, because the
  credential is the actionable fact; a reconnecting attachment therefore never
  publishes `offline`, which sits below `reconnecting` in the ranking.
  - verify: vitest:sdk/typescript/test/attach-status.test.ts#c3RhdHVzIHJlc29sdmVzIGJ5IGZpeGVkIHByZWNlZGVuY2UgcmF0aGVyIHRoYW4gbGFzdCB3cml0ZXI — `sdk/typescript/test/attach-status.test.ts :: "status resolves by fixed precedence rather than last writer"`
- AC9.4: An open attachment with no status subscriber starts no heartbeat, and
  an attachment does not keep a heartbeat alive after the last status subscriber
  unsubscribes.
  - verify: vitest:sdk/typescript/test/attach-status.test.ts#YW4gYXR0YWNobWVudCBpcyBub3QgYSBoZWFydGJlYXQgc3Vic2NyaWJlcg — `sdk/typescript/test/attach-status.test.ts :: "an attachment is not a heartbeat subscriber"`
- AC9.5: In a browser-like environment a hidden page pauses the heartbeat (M1
  behaviour, preserved) and does **not** pause or detach an open attachment; the
  attachment keeps consuming while hidden.
  - verify: vitest:sdk/typescript/test/attach-status.test.ts#cGFnZSB2aXNpYmlsaXR5IGRvZXMgbm90IHBhdXNlIGFuIGF0dGFjaG1lbnQ — `sdk/typescript/test/attach-status.test.ts :: "page visibility does not pause an attachment"`

---

### Scenario 10 — Offline e2e against a same-checkout `mecated --mock-script`

The whole M2 surface proven end to end, offline, against a spawned daemon — the
repo's standing test discipline ([`AGENTS.md`](../../AGENTS.md): tests are
offline, mockllm-backed, never a live model). It extends M1's
`sdk/typescript/e2e/` harness, which already spawns `mecated` over TCP and a
Unix domain socket and reads its `--ready-file`.

Two cases carry most of the value, and they are deliberately the only two
shapes that can cross a process boundary. The **`activity()` restart** is the
only proof that the cursor is durable rather than process-local — the whole
point of [ADR-0250](../adr/0250-durable-cursors-and-watch.md) and the reason the
Redis LIST became a Stream; a same-process reconnect cannot distinguish the two.
It uses `activity()` because a run does not survive its daemon, so an
`AttachedRun`'s `run_id` filter would exclude everything appended afterwards.
The **awaiting-approval restart** is the one case where a *filtered* attachment
does survive — with the ask resolved by an actor outside the attachment, since
attached approval is deferred (AC8.4) — because
[ADR-0249](../adr/0249-durable-run-identity.md) has `resumeFromAwaiting` reuse
the persisted run id rather than mint one, and if that ever regressed the
attachment would go permanently *quiet* while the run completed normally —
silence, which is the one failure class this plan exists to abolish.

**Work:** a restart helper on the existing harness that stops the daemon and
brings a second one up on the same durable store directory.

**Acceptance:**
- AC10.1: A Node gRPC e2e over TCP starts a run on a same-checkout
  `mecated --mock-script`, attaches to it, observes replay then the live
  boundary then the terminal result, and detaches without cancelling — no
  network beyond loopback.
  - verify: vitest:sdk/typescript/e2e/attach.e2e.test.ts#YXR0YWNoIHJlcGxheXMgYW5kIGZvbGxvd3MgYSBsaXZlIHJ1biBvdmVyIGdSUEM — `sdk/typescript/e2e/attach.e2e.test.ts :: "attach replays and follows a live run over gRPC"`
- AC10.2: The same flow over a Unix domain socket daemon
  (`--grpc-unix-socket`) yields the same envelope sequence and terminal outcome.
  - verify: vitest:sdk/typescript/e2e/attach.e2e.test.ts#YXR0YWNoIHJlYWNoZXMgdGhlIGRhZW1vbiBvdmVyIGEgdW5peCBkb21haW4gc29ja2V0 — `sdk/typescript/e2e/attach.e2e.test.ts :: "attach reaches the daemon over a unix domain socket"`
- AC10.3: The same flow over the HTTP/SSE transport against
  `GET /v1/sessions/{id}/watch` yields the same normalized envelopes and
  terminal outcome.
  - verify: vitest:sdk/typescript/e2e/attach.e2e.test.ts#YXR0YWNoIHJlcGxheXMgYW5kIGZvbGxvd3MgYSBsaXZlIHJ1biBvdmVyIEhUVFAvU1NF — `sdk/typescript/e2e/attach.e2e.test.ts :: "attach replays and follows a live run over HTTP/SSE"`
- AC10.4: With an **`activity()`** attachment open, stopping the daemon and
  starting a fresh one over the same durable store lets the attachment resume
  from its cursor and deliver every envelope appended before the restart plus
  every envelope of a **new run started after it**, with no missing record — the
  cursor is durable across processes, not process-local. It is `activity()`
  rather than `attach()` because a run does not survive its daemon: whatever
  runs next carries a new `run_id` that an `AttachedRun`'s filter excludes by
  construction, so the same test over `attach()` would assert an
  empty-but-correct stream and prove nothing.
  - verify: vitest:sdk/typescript/e2e/activity.e2e.test.ts#YSBkYWVtb24gcmVzdGFydCBtaWQtd2F0Y2ggcmVzdW1lcyBmcm9tIHRoZSBjdXJzb3IgYWNyb3NzIGEgbmV3IHJ1bg — `sdk/typescript/e2e/activity.e2e.test.ts :: "a daemon restart mid-watch resumes from the cursor across a new run"`
- AC10.5: With an attachment open on a session parked `awaiting`, restarting the
  daemon over the same store and then resolving the ask **from outside the SDK
  entirely** — the e2e harness posting to `/v1/sessions/{id}/approve` with a
  direct `fetch` and draining that response itself — delivers the resumed run's
  envelopes on the **same** `run_id` the attachment holds, and the attachment
  never has to re-`attach()`. It must be a harness `fetch` rather than the raw
  seam: `RawClient` exposes only descriptor-backed `unary`/`stream`, there is no
  standalone approval RPC to name, and the HTTP transport's control path is
  private. A bounded drain in harness code is fine; it is only unsound as an SDK
  contract (AC8.4). This is the one shape in which a *filtered* attachment
  survives a restart, because `resumeFromAwaiting` reuses the persisted run id.
  - verify: vitest:sdk/typescript/e2e/attach.e2e.test.ts#YW4gYXdhaXRpbmcgYXBwcm92YWwgcmVzb2x2ZWQgb3V0c2lkZSB0aGUgU0RLIHN1cnZpdmVzIGEgcmVzdGFydCBvbiB0aGUgc2FtZSBydW4gaWQ — `sdk/typescript/e2e/attach.e2e.test.ts :: "an awaiting approval resolved outside the SDK survives a restart on the same run id"`
- AC10.6: An attached `cancel` over HTTP stops a real in-flight run on the wire
  carrying `expected_run_id`, the attachment observes the cancelled terminal,
  and a `cancel` naming a run that already finished fails typed without touching
  the session's next run.
  - verify: vitest:sdk/typescript/e2e/attach.e2e.test.ts#YW4gYXR0YWNoZWQgY2FuY2VsIHN0b3BzIGEgcmVhbCBydW4gYW5kIGEgc3RhbGUgb25lIGZhaWxzIHR5cGVk — `sdk/typescript/e2e/attach.e2e.test.ts :: "an attached cancel stops a real run and a stale one fails typed"`
- AC10.7: A watch opened with a malformed cursor over HTTP/SSE receives a 200
  followed by a terminal `event: error` frame carrying `cursor_malformed`, which
  the SDK surfaces as the typed error — the real-wire proof of the SSE
  error-frame parser AC5.3 specifies.
  - verify: vitest:sdk/typescript/e2e/attach.e2e.test.ts#YSBtYWxmb3JtZWQgY3Vyc29yIG1pZC13YXRjaCBhcnJpdmVzIGFzIGEgdGVybWluYWwgU1NFIGVycm9yIGZyYW1l — `sdk/typescript/e2e/attach.e2e.test.ts :: "a malformed cursor mid-watch arrives as a terminal SSE error frame"`
- AC10.8: A real-wire attachment over a run that produced a log-only record does
  not yield it by default, yields it under `{ includeLogOnly: true }`, and a
  resume from the default attachment's cursor re-delivers none of them — the
  filter is proven against the records the server actually writes, not only
  against a fake.
  - verify: vitest:sdk/typescript/e2e/attach.e2e.test.ts#YSBsb2ctb25seSByZWNvcmQgaXMgZmlsdGVyZWQgYnkgZGVmYXVsdCBhbmQgcmVzdG9yZWQgYnkgb3B0LWlu — `sdk/typescript/e2e/attach.e2e.test.ts :: "a log-only record is filtered by default and restored by opt-in"`
- AC10.9: `activity()` on the real wire spans two sequential runs of one session
  in append order, each envelope carrying its own `run_id`.
  - verify: vitest:sdk/typescript/e2e/activity.e2e.test.ts#YWN0aXZpdHkgc3BhbnMgdHdvIHJ1bnMgb24gdGhlIHJlYWwgd2lyZQ — `sdk/typescript/e2e/activity.e2e.test.ts :: "activity spans two runs on the real wire"`
- AC10.10: The `sdk` CI job runs the new unit suites and these e2e suites
  alongside M1's on every PR — a failure in any of them fails the PR, and no new
  job is added.
  - verify: inspection — the `sdk` job in `.github/workflows/ci.yml` runs
    `task sdk:*` targets that discover the new suites without enumerating them.

## Cross-cutting deliverables

- [ADR-0288](../adr/0288-typescript-sdk-durable-attachment.md) — authored with
  this plan, on the accumulator, not a worker task.
- API Extractor reports for `.` and `./node` regenerated intentionally as each
  scenario adds public surface (`SessionActivity`, `AttachedRun`,
  `WatchEnvelope` and its arms, `SdkCursor`, `MECATL_WATCH_PHASES`,
  `MECATL_ATTACH_FILTERED_KINDS`, the two new `SDKErrorCode` members, and the
  five error types); `./gen` stays codegen-governed per
  [ADR-0279](../adr/0279-typescript-sdk-architecture.md) Decision 4.
- `docs/architecture.md`: extend the SDK section with attachment;
  `docs/design/IMPLEMENTATION-NOTES.md`: the dense notes for the attachment
  subsystem — the checkpoint's consumption-time advancement, the
  consume-but-do-not-yield split, the cursor envelope, the derived filter set,
  and the three-arm reconnect classification.
- `task docs` configuration-reference regeneration with every Markdown change.
- No `engine/` API change is expected — the cursor port already landed. If one
  appears, `task api:check` / `task api:update` plus the `engine/CHANGELOG.md`
  note per the standing rule.

## Sequencing recommendation

Scenario 1 is strictly first: every later scenario consumes the envelope union,
and the HTTP `/watch` route mapping plus the shared features accessor it adds are
what make the transport-parity halves of Scenarios 3–8 possible at all.
Scenario 2 follows (it introduces the two attachment types everything else hangs
off). Scenarios 3–9 then build on 1–2 and parallelize, with one caution matching
M1's: they share `sdk/typescript/src/`'s exports barrel and error hierarchy, so
treat those two files as merge-conflict hotspots rather than serializing the
work. Scenarios 4 and 6 are the tightest coupling — the checkpoint is the
reconnect's resume point — so a single worker taking both is reasonable.
Scenario 10 lands last, consuming everything.

## Named tests landing in this plan

- `TestSDKTypescriptAttach_Scenario1_WatchPhaseParity`
- `TestSDKTypescriptAttach_Scenario1_WatchFeatureIdParity`
- `TestSDKTypescriptAttach_Scenario1_FilteredKindParity`

All three live in the root module beside the existing
`internal/adapter/server/sdk_typescript_*_test.go` parity guards — they read
TypeScript sources from `sdk/typescript/`, so they can never live under
`engine/` ([`AGENTS.md`](../../AGENTS.md): the engine tree is self-contained and
its module boundary rejects a cross-tree read). All other proofs are vitest
suites under `sdk/typescript/`, cited per AC.

## Definition of done

1. `task lint` and `task test` pass (both Go modules, `-race`), plus
   `task sdk:lint`, `task sdk:typecheck`, `task sdk:test`, `task sdk:e2e`, and
   `task sdk:api:check`.
2. `task docs` — configuration reference regenerated and the matlatl strict link gate green.
3. `task generate` reproduces both generated trees byte-identically —
   `WatchSessionEvents` descriptors come from the existing committed output, so
   this should be a no-op.
4. `task ac-trace-strict` — every AC's `verify:` proof resolves (this plan is
   `landed`), including every `vitest:` resolver token.
5. The three named Go parity tests are green and grep-locatable by their
   identifiers.
6. `go run ./cmd/mecademo` still prints a full offline session.
7. `contracts/proto/` and production `internal/adapter/server/` are
   byte-unchanged in this plan's diff; the only Go additions are the three
   parity `_test.go` files.

## Deferred decisions and known risks

- **`attach()`'s replay scan is the price of no `ListRuns`.** With no run id the
  SDK reads the replay phase to discover the newest run, which on a long-lived
  session is the whole log crossing the network. Mitigated only by passing a
  known `runId` — `from: "now"` does not help, since it discards the replay
  locally rather than asking the server to skip it. Removed properly by a
  server-side active-run field, which is out of scope here.
- **The single-writer premise is an assumption with a named falsifier.**
  Same-process it is the run registry; cross-process it is `port.SessionLease`,
  and the default path wires none. A lease-less multi-replica `mecated` over a
  shared Redis log can interleave two runs on one session, so "the newest run"
  may not be the run the caller meant.
  [ADR-0288](../adr/0288-typescript-sdk-durable-attachment.md) Decision 2 records
  this as a residual rather than an impossibility.
- **The attached-control surface is one method wide.** Only `cancel()` ships,
  and only over HTTP; `approve`, `resolveAsk`, and `steer` all raise typed
  unsupported errors, and gRPC has no attached control at all. This will be the
  first thing a user files. Three independent server changes unblock the rest —
  an ack-only approve response, a prompt-free control RPC, and
  [#873](https://github.com/stacklok/mecatl/issues/873)'s HTTP steer route —
  and each needs an SDK release on top, since none of the three has a latent
  client path to switch on.
- **The at-least-once contract makes duplicates a documented feature.**
  Consumers with side effects need their own idempotency. If implementation
  finds the duplicate window wider than one envelope, the AC4.4 wording is what
  to tighten — never the guarantee.
- **One filter divergence from the live wire is deliberate.** The SDK drops all
  `user_prompt`; the server relays the scheduled-fire delivery note. Matching it
  would mean re-implementing a fenced-provenance prefix sniff in the client.
  Gated by AC1.8 and reachable via `{ includeLogOnly: true }`.
- **Redis follow contention is unfixed upstream.** N attachments are N blocked
  `XREAD`s competing with `Save`/`Append`
  ([#876](https://github.com/stacklok/mecatl/issues/876)). No client-side
  admission limit is added — that policy belongs at the server.
- **Unknown-phase tolerance can only be proven against a fake.** The server
  emits exactly three phases, so AC1.2 uses a scripted transport emitting a
  fourth. A forward-compatibility property has no other proof before the future
  exists.
- **`NoRunsError` has a documented false positive.** The run id is stamped on
  the aggregate before the engine goroutine emits, so an `attach()` landing in
  that window reports no runs for a running session (AC2.4). Every alternative —
  waiting, consulting `Session.state`, blind retry — either hangs a truly empty
  session or reintroduces the second round-trip the design removed. Closed
  properly by the deferred server-side active-run field.
- **Cursor validation is structural, not provenance-based.** The envelope is
  stateless and unsigned, so a well-formed, correctly-filtered cursor is
  accepted whoever built it (AC4.7). Signing would need a client-side secret the
  SDK cannot hold, and a caller lying to itself about its own cursor is not a
  threat model.
- **Attached `approve`/`resolveAsk` do not ship in M2.** No client-side
  strategy for the rehydrate path's SSE body is sound — close cancels the run,
  unread stalls the relay, and drain-to-EOF is unbounded because the resumed run
  can park on another ask. Deferred to an ack-only approve route, which the
  server already implements for non-`Flusher` writers but does not expose.
  `cancel` is unaffected and ships.
- **An `AttachedRun` cannot survive a daemon restart** — its filter names a run
  that does not continue. Only `activity()` (AC10.4) and the awaiting-approval
  resume (AC10.5) cross a process boundary; there is deliberately no general
  "attachment survives restart" claim.
- **The two restart e2e cases (AC10.4, AC10.5) are the plan's most fragile
  tests.** Both depend on a second daemon adopting the first's durable store
  directory. If they prove flaky rather than wrong, the fix is a deterministic
  wait on the second daemon's ready-file — not weakening the assertions, which
  are the only cross-process durability proofs in the plan.
- **Attachment page-visibility behaviour is stated, not measured.** M2 asserts
  attachments are not paused when hidden (AC9.5); whether that is right for a
  real browser tab against a real `watch_lagging` bound is M4's matrix to
  answer.
- **The bundled authoring check warns "no named tests".** Its regex recognises
  only `TestADR_NNNN_*` and `TestInvariant_*`; this plan's Go proofs use the
  `Test<Plan>_Scenario<N>_*` form for the reason
  `docs/acceptance/sdk-typescript-core.md` records — an ADR renumber silently
  repointed the enabler plan's `TestADR_*` pins, once at a missing ADR and once
  at an unrelated one. The warning is accepted deliberately, not overlooked.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this plan
is satisfied.
