# Session affinity and handoff — acceptance plan

**Phase:** capability — exact session affinity, lease-owned mutation hardening, and crash handoff
**Status:** landed, 2026-09-02. Synthesised from the settled multi-replica session-owner routing contract.
**ADR:** [ADR-0290](../adr/0290-session-correlation-and-affinity.md) — the exact end-to-end header contract, compatibility floor, lease-loss behavior, and infrastructure boundary.
**Accumulator branch:** `acc/session-affinity-and-handoff` (off `main`).

The smallest set of work that lets a cooperating gateway route every session-bound
request to the current mecak8s owner while the same exact session identity reaches the
provider, without mistaking routing for ownership or safety. It proves modeled
lease-expiry handoff and crash-state repair; Kubernetes endpoint removal and real
Envoy/Gateway routing are verified only by the separate infrastructure PR/live rollout.

The doc is organized scenario-first because acceptance is about what the running
harness can demonstrate, not which packages exist on disk. All settled work lands in
one future accumulator PR; infrastructure policy and rollout do not.

## Why these scope cuts

- [ADR-0290](../adr/0290-session-correlation-and-affinity.md) supersedes, rather
  than edits, frozen [ADR-0216](../adr/0216-provider-session-correlation-header.md).
  It preserves authoritative run-context provider projection while broadening the
  field into an ingress-to-egress affinity contract.
- Affinity is advisory; the session-scoped lease and durable store remain the
  correctness boundary. This preserves the storage-agnostic loop and composition-owned
  lease rule in [`AGENTS.md`](../../AGENTS.md) and the cloud-native split in
  [ADR-0027](../adr/0027-cloud-native.md).
- No protobuf change is needed: gRPC metadata and HTTP headers already carry the
  field, and additive client helpers preserve source compatibility.
- The Helm chart stays infrastructure-neutral under
  [ADR-0278](../adr/0278-mecak8s-edge-terminated-tls.md). A concrete Gateway API
  affinity policy belongs to its infrastructure repository, where deployment topology
  and rollout are known.

## In scope — 8 scenarios / 45 ACs, in implementation order

Scenarios are listed in implementation order. Each is independently demonstrable;
later scenarios assume earlier ones but do not weaken their acceptance criteria.

---

### Scenario 1 — One exact header contract governs every provider attempt

A root transport consumer has one canonical header name and byte-level legal-value decision
in [`contracts/sessionaffinity`](../../contracts/sessionaffinity). The real
provider adapters continue to read the actual run's context, never transport ingress,
preserving [ADR-0216](../adr/0216-provider-session-correlation-header.md) and the
provider-neutral request invariant in [`AGENTS.md`](../../AGENTS.md). Their separately
versioned `GOWORK=off` engine v0.12.0 dependency cannot consume a newly exported port
symbol in this PR, and ADR 0093 forbids a local replacement; provider production code
therefore retains its private ADR-0216 header constants and validators.

**Work:**
- root transport contract: add the canonical `X-Mecatl-Session-ID` constant and legal-value
  predicate to the stdlib-only `contracts/sessionaffinity` package, with a 256-byte maximum
  and one repository-owned exact legal/illegal vector fixture;
- provider modules: retain their existing private production constants and validators,
  and consume that exact vector fixture in parity tests while proving per-request projection,
  retries, fallbacks, and concurrent isolation;
- engine compatibility: the transport-only contract does not alter the engine API snapshots
  or `engine/CHANGELOG.md`.

**Acceptance:**
- AC1.1: The canonical transport contract accepts a non-empty printable-ASCII HTTP field
  value (`0x20`–`0x7e`, without boundary spaces) byte-for-byte and rejects empty,
  control-bearing, newline-bearing, non-ASCII, or otherwise illegal values; it never
  trims, encodes, truncates, or normalizes a session ID. The shared vector fixture also
  records the released providers' deliberately broader outbound-only decisions.
  - verify: `TestADR_0290_SessionHeaderLegalValue` and provider parity vector tests
- AC1.2: OpenAI Responses, OpenAI Chat Completions, and Anthropic retain ADR-0216's
  private header constants and validators in this PR, yet send the exact run-bound
  session ID on the initial request and every retry or provider-specific fallback, using
  per-request options rather than mutating a shared client.
  - verify: `TestADR_0290_ProviderSessionHeaderExact`
- AC1.3: When the run context has no session ID or carries an illegal value, each
  provider omits the field and continues inference, preserving ADR-0216's availability
  behavior.
  - verify: `TestADR_0290_ProviderSessionHeaderOptional`
- AC1.4: A child, member, compaction, resumed, or recovered run projects the
  authoritative session ID bound by that run; an ingress value cannot replace it and
  `port.LLMRequest` gains no routing field.
  - verify: `TestADR_0290_ProviderUsesAuthoritativeRunContext`
- AC1.5: A race-enabled concurrent test shares one provider client between two distinct
  sessions and interleaves their initial requests, retries, and provider-specific
  fallbacks. Every captured outbound request carries only its originating run-bound
  session ID; no per-request state leaks across sessions.
  - verify: `TestADR_0290_ProviderSessionHeaderConcurrentIsolationRace` (run with `-race`)

---

### Scenario 2 — gRPC rejects ambiguous or mismatched affinity before work

A client may omit metadata and retain existing behavior. If it opts in, every
session-bound unary and server-stream call is checked against the request message, and
`Converse` is checked before streaming work and again against the first prompt or retry
frame. This is transport validation, separate from caller ownership under
[ADR-0212](../adr/0212-caller-ownership-enforcement.md).

**Acceptance:**
- AC2.1: Every session-bound unary and server-streaming RPC accepts one legal
  `X-Mecatl-Session-ID` value only when it equals the authoritative request session ID
  byte-for-byte; missing metadata remains compatible. A derived `CreateSession` binds
  exactly one `source_session_id` or `debug_target_session_id` and rejects both together.
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

---

### Scenario 3 — HTTP path and header agree before dispatch

Every HTTP route whose path names a session applies the same shared predicate to the
single header value and compares it with the decoded authoritative path ID. Existing
headerless callers remain valid. The route inventory is grounded in
[`internal/adapter/server/http.go`](../../internal/adapter/server/http.go), while error
projection continues to follow [ADR-0248](../adr/0248-sdk-compatibility-and-error-contract.md).

**Acceptance:**
- AC3.1: Every session-bound HTTP read, mutation, prompt, retry, approval, control,
  replay, and watch route accepts a missing field or one legal field exactly equal to
  its decoded path session ID.
  - verify: `TestSessionAffinityAndHandoff_Scenario3_HTTPRouteInventory`
- AC3.2: Duplicate values, illegal bytes, and byte-mismatched values are rejected before
  handler dispatch with the ordinary typed invalid-argument response, and no response
  body or diagnostic reflects either value.
  - verify: `TestADR_0290_HTTPHeaderFailureIsNonDisclosing`
- AC3.3: Escaped path IDs are compared after the server's normal path decoding; the
  field itself remains byte-exact and is never URL-decoded, trimmed, or normalized.
  - verify: `TestADR_0290_HTTPDecodedPathEquality`
- AC3.4: Header validation grants no access: authentication, caller ownership, and
  management-root checks still run independently and return their existing outcomes.
  - verify: `TestADR_0290_AffinityHeaderGrantsNoAuthority`

---

### Scenario 4 — Official clients propagate affinity without a protobuf break

mecatui and `@stacklok/mecatl-sdk` attach the field automatically whenever they already
hold the target session. Raw callers get additive opt-in helpers. Existing
`OpenConverse(ctx)` remains source-compatible, consistent with the additive API
contract in [ADR-0248](../adr/0248-sdk-compatibility-and-error-contract.md) and the SDK
architecture in [ADR-0279](../adr/0279-typescript-sdk-architecture.md).

**Acceptance:**
- AC4.1: mecatui sends the exact session ID as gRPC metadata for every session-bound
  unary and server-stream operation it performs.
  - verify: `TestSessionAffinityAndHandoff_Scenario4_MecatuiUnaryAndStreamPropagation`
- AC4.2: mecatui opens a session-bound `Converse` before prompt or retry and all later
  controls use that stream binding; the existing `OpenConverse(ctx)` API still compiles
  and behaves as before for external/raw callers.
  - verify: `TestADR_0290_MecatuiOpenConverseCompatibility`
- AC4.3: The TypeScript SDK high-level `Session`, owned `Run`, and attached-run APIs
  automatically propagate the exact session ID on every session-bound unary,
  server-stream, HTTP/SSE, prompt, retry, approval, cancel, steer, watch, and replay
  operation supported by that API.
  - verify: `TestSessionAffinityAndHandoff_Scenario4_TypeScriptHighLevelPropagation`
- AC4.4: Additive TypeScript raw helpers can bind an explicit legal session ID on either
  transport without changing generated protobuf code; an illegal explicit ID is rejected
  synchronously and actionably without altering caller headers. Calls without the helper,
  including high-level use of an unrepresentable server-issued ID where no explicit bind
  was requested, retain their current behavior and omit affinity.
  - verify: `TestADR_0290_TypeScriptRawHelperCompatibility`
- AC4.5: Public mecatui/engine and TypeScript API reports change only by the intended
  additive helpers and shared symbols; generated protobuf output is unchanged.
  - verify: inspection — compare engine and TypeScript API reports and `git diff -- contracts/proto contracts/gen sdk/typescript/src/gen`

---

### Scenario 5 — Every out-of-band mutation is lease-owned and lease loss stops local mutation

Affinity can miss, so correctness remains in the optional session-scoped lease. The
mutation audit covers snapshots, deletes, durable event/tool sidecars, metadata indexes,
`SetMode`, and every other out-of-band session-family mutation rather than only prompt
entry. The loop remains unaware of `port.SessionLease`, as required by
[`AGENTS.md`](../../AGENTS.md), and storage durability remains adapter-owned under
[ADR-0027](../adr/0027-cloud-native.md). This work does **not** add storage-level
fencing: an already-started storage call may still complete after lease loss. A local
awaiting run that loses its lease is stopped and its local ask delivery is retracted, but
its already-durable awaiting snapshot and `PendingAsk` are preserved for the successor.

**Acceptance:**
- AC5.1: `SetMode` and every out-of-band session-family mutation acquires or proves the
  same session-scoped lease before changing a snapshot, deleting a family, appending an
  event/tool sidecar, or changing derivative metadata.
  - verify: `TestSessionAffinityAndHandoff_Scenario5_MutationLeaseInventory`
- AC5.2: The inventory test fails when a new session-bound mutator is added without an
  explicit lease-ownership classification; read-only operations and composition-time
  setters are explicitly distinguished.
  - verify: `TestADR_0290_AllSessionMutatorsClassified`
- AC5.3: `CloseSession` returns `FailedPrecondition` while a local run is active or
  awaiting, and does not release the lease or tear down its engine, policy, or
  environment; the caller must cancel or settle that local run first.
  - verify: `TestADR_0290_CloseGRPCAndHTTPRejectLiveOrAwaitingRun`
- AC5.4: A persisted awaiting session with no live local run retains its durable
  `PendingAsk`; `CloseSession` may release local resources and its lease without
  destroying that resume point.
  - verify: `TestADR_0290_CloseGRPCAndHTTPPreservePersistedAwaitingResumePoint`
- AC5.5: Lease-renewal loss cancels the owning run and locally invalidates its mutation
  capability before new application mutations begin. Later local saves, deletes,
  event appends, tool-call records, and metadata/sidecar mutations are prevented; an
  already-started storage call is explicitly allowed to complete until backend fencing
  exists.
  - verify: `TestSessionAffinityAndHandoff_Scenario5_LeaseLossCancelsAndPreventsNewMutations`
- AC5.6: Lease-renewal loss while a local run is awaiting approval stops and invalidates
  that local run and retracts its local permission-ask delivery. It neither resolves,
  cancels, overwrites, nor destroys the already-durable awaiting snapshot or `PendingAsk`.
  If local cancellation cannot settle the run, it does not explicitly release merely on
  that cancellation; after ownership loss the stale process cannot approve, deny, or
  otherwise resolve the ask.
  - verify: `TestADR_0290_AwaitingLeaseLossRetractsLocalAskPreservesSnapshot`
- AC5.7: After lease expiry and successor takeover, the successor reloads and resumes the
  exact durable `PendingAsk`; no stale local approval can change that ask or start its
  tool call.
  - verify: `TestSessionAffinityAndHandoff_Scenario5_AwaitingLeaseLossSuccessorResumesExactAsk`
- AC5.8: With no `SessionLease` configured, admission, mutation, close, drain, and
  awaiting-approval behavior remain byte-identical to the current non-leased path: no
  lease acquisition or lease-loss invalidation is introduced. `ErrLeaseUnsupported`
  retains its existing sticky-disable diagnostic and fallback semantics.
  - verify: `TestADR_0290_OptionalLeaseCompatibilityAndUnsupportedFallback`
- AC5.9: Lease identity remains scoped to the durable session across runs, retries,
  awaiting resume, compaction, and controls; no run ID, header value, or gateway route
  becomes a lease or fencing token.
  - verify: `TestADR_0290_LeaseRemainsSessionScoped`

---

### Scenario 6 — Graceful drain cancels, joins, and releases only settled ownership

mecak8s first stops admission, then cancels and joins executing runs before explicitly
releasing their leases. A durable awaiting session is preserved rather than cancelled:
when no local run owns it, its `PendingAsk` remains the resume point and local resources
and the lease may be released. This strengthens the original drain sequence in
[ADR-0048](../adr/0048-mecak8s.md) without moving lease logic into the engine.

**Acceptance:**
- AC6.1: Once drain begins, new prompt, retry, resume, and out-of-band mutation entries
  are refused before lease acquisition while already-owned runs follow the bounded
  shutdown path.
  - verify: `TestADR_0290_DrainStopsAdmissionBeforeOwnershipChange`
- AC6.2: Drain preserves a persisted awaiting session's durable `PendingAsk`; when no
  local run is live, it may close local resources and release the lease without
  destroying the durable resume point.
  - verify: `TestADR_0290_DrainPreservesAwaitingResumePoint`
- AC6.3: Drain cancels and joins an executing run before explicitly releasing its lease.
  A terminal or cancelled recoverable snapshot is persisted when storage is available;
  a persistence failure is diagnosed and the prior durable state remains authoritative.
  - verify: `TestSessionAffinityAndHandoff_Scenario6_DrainCancelsJoinsAndDiagnosesPersistFailure`
- AC6.4: If an executing run cannot join before the shutdown bound, mecak8s does not
  explicitly release its lease; process death and lease TTL govern later takeover.
  - verify: `TestADR_0290_DrainTimeoutRetainsLeaseForTTLTakeover`
- AC6.5: Mecak8s exposes separate positive bounds for Service drain, gRPC graceful stop,
  HTTP shutdown, and resource close. Including the 3-second preStop delay and telemetry
  flush, the default 43-second sequential budget is strictly below the Helm chart's
  operator-configurable 60-second `terminationGracePeriodSeconds` default.
  - verify: `TestADR_0290_TerminationBudgetFitsPodGracePeriod` and `TestADR_0290_TerminationGracePeriodIsConfigurableAndFitsDefaults`

---

### Scenario 7 — Modeled crash handoff occurs only after TTL and state repair

A two-Service, fake-clock mecak8s fixture kills the current owner without graceful
release. Before TTL, survivor requests cannot acquire ownership or start a provider
call. After TTL, exactly one survivor acquires the lease, reloads Redis, repairs the
crash-orphaned `running` state, and continues. The same modeled fixture also proves
awaiting-approval lease loss: local ask retraction preserves the durable `PendingAsk` for
exact successor resumption. This PR does not claim that ordinary offline tests prove
Kubernetes EndpointSlice removal or actual Envoy/Gateway routing;
those are verified by the separate infrastructure PR and live rollout. The repair
reuses the run-entry `Abandon` seam documented in
[ADR-0027](../adr/0027-cloud-native.md), and the storage topology remains the
Redis-plus-session-lease design of [ADR-0048](../adr/0048-mecak8s.md).

**Acceptance:**
- AC7.1: The modeled killed owner drops its active client stream without fabricating a
  success or terminal event.
  - verify: `TestSessionAffinityAndHandoff_Scenario7_KilledOwnerDropsStream`
- AC7.2: Before lease TTL expiry, every survivor request for that session cannot acquire
  ownership, mutate durable state through newly admitted application work, or start a
  provider call.
  - verify: `TestADR_0290_PreTTLRequestsCannotAcquireOrRun`
- AC7.3: After TTL expiry, exactly one modeled survivor acquires the Kubernetes lease;
  concurrent survivors cannot both start ownership work or rehydration.
  - verify: `TestADR_0290_PostTTLSingleSurvivorAcquires`
- AC7.4: The new owner rehydrates the latest Redis snapshot and sidecar state, detects
  the crash-orphaned `running` state, repairs unanswered tool-call pairing through
  `Session.Abandon`, persists the repair, and then continues the same durable session.
  - verify: `TestSessionAffinityAndHandoff_Scenario7_RehydrateRepairAndContinue`
- AC7.5: The first provider request after continuation carries the exact same durable
  session ID as client ingress, while its run ID may correctly be new. Storage-level
  fencing of a delayed old owner's already-started call is not claimed.
  - verify: `TestADR_0290_HandoffEndToEndCorrelation`
- AC7.6: A modeled transport fixture receives one legal header from an official client,
  forwards it unchanged to mecak8s, and captures the identical bytes at the fake
  provider; omitting the field still completes through compatibility, while duplicate,
  illegal, and mismatched variants stop at mecak8s and never reach the provider.
  - verify: `TestSessionAffinityAndHandoff_Scenario7_ClientTransportProviderBytes`
- AC7.7: A modeled owner that loses renewal while awaiting approval retracts only its
  local ask delivery and cannot resolve the ask. After TTL/takeover, exactly one
  successor reloads the unchanged durable awaiting snapshot and resumes that exact
  `PendingAsk`, rather than a cancellation or replacement.
  - verify: `TestSessionAffinityAndHandoff_Scenario7_AwaitingLeaseLossHandoff`

---

### Scenario 8 — The chart stays neutral and the operator contract is documented

The repository documents the affinity and handoff contract but does not ship a
Gateway API implementation. The existing external-boundary guard in
[`deploy/helm/mecak8s/chart_test.go`](../../deploy/helm/mecak8s/chart_test.go) grows to
cover `BackendTrafficPolicy`, preserving [ADR-0278](../adr/0278-mecak8s-edge-terminated-tls.md).
User-facing guidance explains what an infrastructure repository must provide without
pretending the chart enforces it. The separate rollout must treat
[CWE-400](https://cwe.mitre.org/data/definitions/400.html) and
[OWASP API4:2023](https://owasp.org/API-Security/editions/2023/en/0xa4-unrestricted-resource-consumption/)
as applicable resource-consumption risks: a legal, attacker-chosen session ID can
otherwise concentrate traffic on one selected replica.

**Acceptance:**
- AC8.1: Every chart fixture renders no `Gateway`, `HTTPRoute`, `GRPCRoute`,
  `TLSRoute`, `Route`, `Certificate`, or `BackendTrafficPolicy`; the existing narrow
  chart-owned NetworkPolicy exception is unchanged.
  - verify: `TestMecak8sHelmChart_EdgeFixtureRendersNoExternalBoundaryResources`
- AC8.2: Helm tests and schema/lint gates pass without adding a gateway or affinity
  values subtree to this chart.
  - verify: `TestADR_0290_HelmHasNoAffinityPolicySurface`
- AC8.3: architecture, usage, implementation notes, and public user docs describe the
  exact field behavior, missing-header compatibility, non-disclosing failures,
  authoritative provider context, lease-loss limits, close/drain/handoff sequence, and
  the fact that routing grants no authority; they distinguish modeled PR tests from
  infrastructure rollout verification.
  - verify: `TestADR_0290_DocumentationContract`
- AC8.4: Generated `llms.txt` contains the new ADR and acceptance-plan contract and is
  fresh after `task docs`.
  - verify: demonstration — `task docs` regenerates and checks the documentation corpus
- AC8.5: Before enabling affinity in the separate infrastructure rollout, its Gateway,
  mesh, or equivalent ingress policy is live-validated to apply authenticated admission,
  request and header-size bounds, and client/IP/principal rate limits before or
  independently of affinity routing. The validation demonstrates that legal,
  attacker-chosen session IDs cannot create an unbounded targeted-replica sink; this is
  an infrastructure prerequisite, not a mecak8s chart guarantee.
  - verify: live infrastructure acceptance — the separate infrastructure PR records the
    deployed policy and an authenticated, bounded load test covering client, IP, and
    principal limits before rollout/cutover

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| Owner-to-owner forwarding of a live session or control | separate future ADR and acceptance plan | [ADR-0290](../adr/0290-session-correlation-and-affinity.md) deliberately chooses client retry plus lease handoff; forwarding needs an authenticated internal protocol |
| Storage-level fencing for Redis state, including a lease-token epoch or token-bearing SessionStore/EventLog/ToolCallRecorder protocol | separate future ADR and acceptance plan | This plan only cancels on lease loss and prevents new local application mutations; it does not claim atomic Kubernetes→Redis publication or reject an already-started storage call |
| Gateway/Envoy/mesh affinity implementation, EndpointSlice behavior, infrastructure repository changes, and production rollout/cutover | separate infrastructure repo and PR/live rollout | [ADR-0278](../adr/0278-mecak8s-edge-terminated-tls.md) keeps external boundary resources operator-owned |
| Exactly-once provider calls, tool execution, or other external side effects across owner death | external idempotency design, if required | [ADR-0290](../adr/0290-session-correlation-and-affinity.md) promises neither transactional external systems nor storage-level fencing |
| Using the affinity field for authentication, authorization, caller ownership, tracing, idempotency, cache identity, or a fencing token | permanently excluded from this contract | [ADR-0290](../adr/0290-session-correlation-and-affinity.md) |

## Cross-cutting deliverables

- Living architecture and implementation notes reflect the new run-entry, mutation,
  lease-loss, and handoff invariants; user docs cover client and mecak8s operator use.
- `docs/adr/0027-cloud-native.md` resource and rehydration inventories are re-audited
  for any new owner-transition state, goroutine, registry, or durable field.
- `docs/usage.md`, relevant `user-docs/` pages, and generated `llms.txt` land with the
  behavior; frozen ADR 0216 remains byte-unchanged.
- No production code is implemented by this design step; `/plan-orchestrate` owns
  decomposition and one accumulator PR.

## Sequencing recommendation

Land the shared byte contract before transport validation, then official-client
propagation, then lease mutation auditing and drain ordering. The killed-owner proof depends
on all four. Documentation and Helm neutrality close the same accumulator after the
behavioral gates are green. This is sequencing only, not worker decomposition.

## Named tests landing in this plan

The `TestADR_0290_*` tests pin durable decisions. The
`TestSessionAffinityAndHandoff_ScenarioN_*` tests prove cross-layer scenarios. Existing
provider and Helm tests are extended where they are the stronger regression oracle;
TypeScript unit/e2e tests may sit behind the named Go parity/e2e gates but remain part
of the SDK gate below.

## Definition of done

1. All 44 ACs above are implemented in the single `acc/session-affinity-and-handoff`
   accumulator and the plan status is `landed`.
2. `task lint` and `task test` pass, including both Go modules and race-enabled suites.
3. `task api:check` passes; if the shared engine symbols change the exported surface,
   `task api:update` has refreshed `engine/api/*.txt` and `engine/CHANGELOG.md` records
   the compatibility classification.
4. `task sdk:lint`, `task sdk:typecheck`, `task sdk:test`, `task sdk:e2e`, and
   `task sdk:api:check` pass; an intentional additive SDK API change has its committed
   API report refreshed through `task sdk:api:update`.
5. Helm unit tests plus `task deploy:check` pass, including the
   `BackendTrafficPolicy` no-resource guard.
6. `task docs` passes, regenerating `llms.txt` and satisfying the strict matlatl link
   gate; frozen ADR 0216 has no diff.
7. `task site:build` passes for the updated public `user-docs/` content.
8. `task ac-trace-strict` passes after this plan becomes `landed`; every named proof is
   grep-locatable and resolves.
9. `go run ./cmd/mecademo` prints the full offline turn → tool call → permission ask →
   approval → result session.
10. The modeled two-Service/fake-clock killed-owner scenarios run offline: stream drop,
    pre-TTL refusal, post-TTL single-owner acquisition, Redis rehydration, `running`
    repair and continuation, plus awaiting-ask lease-loss retraction and exact
    `PendingAsk` takeover are observed. EndpointSlice/Gateway behavior is verified by
    the separate infrastructure PR/live rollout.
11. The separate infrastructure rollout is blocked until it has live-validated
    authenticated admission, request/header-size bounds, and client/IP/principal rate
    limits before or independently of affinity routing, demonstrating that legal
    attacker-chosen session IDs cannot form an unbounded targeted-replica sink.
12. The optional-lease regression proves the no-`SessionLease` path is byte-identical and
    `ErrLeaseUnsupported` still sticky-disables with its existing diagnostic/fallback.
13. `/panel-review` reports no unresolved critical/high correctness or security gap,
    and accepted corrections are folded with all gates rerun.
14. `/plan-orchestrate session-affinity-and-handoff` opens exactly one accumulator PR
    containing the plan, ADR, implementation, tests, docs, user docs, and generated
    artifacts; owner forwarding, storage-level fencing, and infrastructure rollout are
    separate future work.

## Deferred decisions and known risks

- **Owner forwarding is intentionally undecided.** A future ADR/plan must choose its
  authenticated protocol, backpressure, authorization, lifecycle, and failure model.
- **Infrastructure policy is repository-specific.** Header-preserving hash/affinity,
  endpoint update timing, TTL tuning, and rollout validation remain the separate
  infrastructure PR's responsibility.
- **Storage-level fencing is intentionally deferred.** A future ADR/plan must decide
  whether and how a durable store fences stale owners. It may not silently redefine
  `port.Lease.Token`, widen store/event/tool-recorder APIs with tokens, or claim an
  atomic Kubernetes-to-Redis ownership publication. Until then, lease loss cancels the
  run and prevents new local application mutations, while an already-started storage
  call may complete.
- **External effects can repeat.** This plan does not provide storage-level fencing and
  cannot retract a provider request or tool side effect completed immediately before
  owner death; exactly-once requires an external idempotency/transaction contract.
- **Hard handoff is interruptive.** The stream drops and callers retry after endpoint
  and TTL convergence; this plan does not disguise that interval as transparent
  forwarding.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this plan is
satisfied and ready for its single human merge gate.
