# Caller separation — acceptance plan

**Phase:** capability — application caller isolation
**Status:** in-progress, 2026-08-11. Synthesized from issue #368 and the caller-identity spike.
**Issue:** [stacklok/mecatl#368](https://github.com/stacklok/mecatl/issues/368).
**ADR:** [ADR-0102](../adr/0102-caller-ownership-enforcement.md) — the shared application ownership decision and the trusted-driver boundary.
**Accumulator branch:** `acc/caller-separation` (stacked from
`368-caller-separation`, currently #367; final PR base is
`368-caller-separation`, not `main`).

The smallest set of work that lets an OIDC-authenticated caller reach only that
caller's sessions, schedules, teams, memory, event streams, and live runs—regardless
of whether the identifier comes from an API client or a model-facing tool. A refusal is
indistinguishable from absence. The raw remote-driver boundary remains explicitly
trusted infrastructure; [ADR-0103](../adr/0103-driver-caller-ownership.md) and issue
[#452](https://github.com/stacklok/mecatl/issues/452) are the separate B-lite follow-up.

The document is scenario-first: each scenario describes something a running harness
can demonstrate rather than a package-level implementation task.

## Why these scope cuts

- [ADR-0100](../adr/0100-caller-identity-threading.md) already owns verified
  principal ingestion, durable session/schedule owners, and explicit system principals;
  this plan adds enforcement rather than changing attribution.
- [ADR-0102](../adr/0102-caller-ownership-enforcement.md) makes application access
  exhaustive and per-request, but leaves direct remote driver enforcement to B-lite.
- [`AGENTS.md` — the layering rule](../../AGENTS.md) keeps ownership policy in the
  core/application seams and keeps concrete server/proto/adapter types out of
  `engine/agent`.

## In scope — 6 scenarios, in implementation order

### Scenario 1 — OIDC activates owner isolation without fabricating a caller

A service with the #367 OIDC verifier creates Alice's and Bob's owned sessions,
schedules, teams, and memory entries. The owner is the durable `(Issuer, Subject)` pair
from ADR-0100. OIDC mode rejects every pre-identity ownerless resource rather than
assigning it to the first reader; a deployment without a verifier preserves the existing
ownerless compatibility path. Creation binds ownership atomically with visibility, and
fork/carryover first authorize the source before copying history. This builds on
[ADR-0100](../adr/0100-caller-identity-threading.md) and the no-fabricated-principal
invariant in [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**
- AC1.1: An OIDC-authenticated caller can access a session, schedule, team, or memory
  record whose durable `(issuer, subject)` owner equals the caller.
  - verify: `TestCallerSeparation_Scenario1_OwnerCanAccessOwnedResources`
- AC1.2: In OIDC mode, every ownerless pre-identity resource—including a session,
  schedule, team, user-model memory entry, project-memory entry, and its derived
  handle/event—is unavailable to every caller and is never adopted or rewritten on
  access.
  - verify: `TestCallerSeparation_Scenario1_OwnerlessResourcesAreNotAdopted`
- AC1.3: With no verifier wired, no principal is fabricated and the pre-existing
  ownerless deployment behavior remains available.
  - verify: `TestInvariant_no_fabricated_principal`
- AC1.4: Ownership compares the exact issuer and subject emitted by the verifier;
  equal subjects from different issuers, alternate issuer spellings, request headers,
  display names, and grant types cannot collide or select an owner.
  - verify: `TestADR_0102_VerifiedIssuerSubjectPairIsOwnerIdentity`
- AC1.5: Create and retry paths bind the verified owner atomically with visibility.
  A same-owner retry of the same immutable request is idempotent; a cross-owner ID
  collision returns absence and cannot overwrite, adopt, or expose the resource.
  - verify: `TestCallerSeparation_Scenario1_AtomicCreationBindsVerifiedOwner`
- AC1.6: Bob cannot fork Alice's session or create a session with Alice's
  `source_session_id`; both source references return absence, create no destination,
  and leave Alice's owner and history unchanged. Alice's same-owner fork/carryover
  remains available.
  - verify: `TestCallerSeparation_Scenario1_ForkAndCarryoverAuthorizeSource`

---

### Scenario 2 — persisted objects and lists are caller-filtered

Alice and Bob create sessions, schedules, teams, and entries in the two distinct
memory kinds: user-model memory (`RememberUser`/`RecallUser`/`SearchUserModel`) and
project memory (`Remember`/`Recall`/`SearchMemory`/`Forget`). Equal logical keys do not
collapse the stores. Every persisted access consults the one per-kind ownership decision.
Lists return the caller's own rows, not an empty placeholder, and derived objects resolve
their owner through the parent. This is the application decision and kind-table contract
in [ADR-0102](../adr/0102-caller-ownership-enforcement.md).

**Acceptance:**
- AC2.1: Alice and Bob can each store and retrieve a user-model-memory entry using the
  same logical key without observing the other's value; the local user-model store is
  caller-partitioned and is not silently treated as a remote driver store.
  - verify: `TestCallerSeparation_Scenario2_UserModelMemoryIsCallerPartitioned`
- AC2.2: Project memory is partitioned by verified owner and workspace, so equal logical
  keys in the same project are caller-isolated while a caller retains its own project
  memory across sessions. Its backing store, search/index, cache, consolidation, and
  delete operations stay within that owner/workspace namespace.
  - verify: `TestCallerSeparation_Scenario2_ProjectMemoryIsCallerPartitioned`
- AC2.3: A list of sessions, schedules, teams, or schedule fires contains all and only
  resources owned by the requesting caller; its count, cursor, page boundary, and
  empty-page behaviour are computed from that caller-owned set, not a globally paginated
  set.
  - verify: `TestCallerSeparation_Scenario2_ListMetadataIsOwnerScoped`
- AC2.4: A direct load, update, or delete of another caller's persisted resource,
  including a schedule fire, is indistinguishable from loading a missing resource.
  - verify: `TestCallerSeparation_Scenario2_OwnerMismatchIsNotFound`
- AC2.5: Event-log access resolves through the session owner; Bob cannot retrieve
  Alice's session or schedule-fire event stream by any handle, while Alice can retrieve
  her own.
  - verify: `TestCallerSeparation_Scenario2_EventStreamsResolveParentOwner`

---

### Scenario 3 — live and model-facing paths cannot bypass ownership

A live run is not necessarily represented by a fresh storage access. Approval,
cancellation, mode changes, plan approval, persistence, and model-facing child/memory
tools therefore enforce the same decision on every request. This is the non-store
coverage required by issue #368 and [ADR-0102](../adr/0102-caller-ownership-enforcement.md).

**Acceptance:**
- AC3.1: Bob cannot prompt Alice's session twice; each attempt is refused as absent and
  neither creates a live run nor changes Alice's history.
  - verify: `TestCallerSeparation_Scenario3_RepeatedForeignPromptIsNotFound`
- AC3.2: Bob cannot approve, deny, cancel, persist, change mode, or approve a plan on
  Alice's live run; Alice can perform the corresponding operations on her own run. A
  foreign verb performs no run lookup with side effects, cancellation/approval send,
  run-entry lock acquisition, durable append, or target-correlated event/diagnostic;
  its response and unchanged target state match a missing handle.
  - verify: `TestCallerSeparation_Scenario3_LiveRunVerbsAreOwnerChecked`
- AC3.3: A model given another caller's subagent or team handle cannot inspect its
  transcript or act on it through an agent-facing tool. Found by an adversarial pass
  to be implemented at half strength: `InspectSubagent`/`InspectMember` correctly
  authorized inspection, but the Subagent tool's `resume: <agentId>` argument — the
  "act on it" half — loaded a persisted child session by id with no ownership check
  at all.
  - verify: `TestCallerSeparation_Scenario3_ModelFacingHandlesAreOwnerChecked`
  - verify: `TestCallerSeparation_Scenario3_SubagentResumeIsOwnerChecked`
- AC3.4: The real Remember, Recall, search, and forget memory-tool paths keep Alice's
  and Bob's same-key user/project memory isolated; a foreign read is absent and a
  foreign write or delete cannot alter the owner's value.
  - verify: `TestCallerSeparation_Scenario3_ModelFacingMemoryToolsAreOwnerChecked`
- AC3.5: The enforcement decision is evaluated for every request rather than cached at
  session creation or lease acquisition; repeated foreign live-run verbs before and
  after the owner's run completes make no mutation and remain indistinguishable from
  absent handles.
  - verify: `TestCallerSeparation_Scenario3_ForeignLiveRunReplayIsNotFound`
- AC3.6: Bob cannot open Alice's live event subscription (the gRPC `StreamSessionLive`
  feed); the refusal is absence-shaped and registers no subscriber, so no event
  Alice's run produces is ever fanned to him. Alice can open her own. Found by an
  adversarial pass over this plan: `Service.Subscribe` took no context and made no
  ownership decision at all — its only caller is the untrusted wire handler, not a
  trusted in-process embed as its stale doc comment claimed.
  - verify: `TestCallerSeparation_Scenario3_LiveSubscriptionIsOwnerChecked`

---

### Scenario 4 — system work is explicit, narrow, and auditable

The scheduler, child GC, and memory consolidators have no human caller, but ADR-0100
already gives them explicit system principals. This scenario limits them to the shared
infrastructure operations named in the ownership table; system identity is not a
universal bypass. The posture ladder remains unrelated to ownership
([ADR-0102](../adr/0102-caller-ownership-enforcement.md)).

**Acceptance:**
- AC4.1: A due schedule owned by Alice runs from the scheduler's explicit system
  context, while only the fire session's run-entry calls use the schedule's captured
  owner context. Claim/record bookkeeping, diagnostics, and durable lifecycle/run-event
  attribution remain system-owned; the created work retains Alice's durable schedule
  owner without exposing either attribution on the client wire.
  - verify: `TestMakeFireFuncUsesScheduleOwnerForRunEntry`
- AC4.2: Child GC and each memory/dream consolidator complete their explicitly
  classified shared-infrastructure operation under their registered system principal.
  - verify: `TestCallerSeparation_Scenario4_InternalWorkersUseOnlyClassifiedAccess`
- AC4.3: A system worker is denied when it attempts a caller-owned operation not
  explicitly classified as shared infrastructure.
  - verify: `TestCallerSeparation_Scenario4_SystemPrincipalIsNotUniversalBypass`
- AC4.4: Changing `strict`, `trusted`, `auto`, or `yolo` posture never disables caller
  ownership enforcement.
  - verify: `TestCallerSeparation_Scenario4_PostureCannotDisableOwnership`
- AC4.5: An OIDC deployment selects one concrete raw-driver boundary—NetworkPolicy,
  mTLS-pinned workload peer, or Unix socket—and proves the mecatl workload can use it
  while a tenant peer cannot connect or authenticate to a raw driver.
  - verify: `TestCallerSeparation_Scenario4_RawDriverIsTenantInaccessible`
- AC4.6: Before OIDC isolation is enabled, an operator can inventory the ownerless
  records that will become inaccessible. After enablement, background workers neither
  adopt nor repeatedly mutate/retry those stranded records; disabling the verifier
  restores only the pre-existing ownerless compatibility path.
  - verify: `TestCallerSeparation_Scenario4_OwnerlessCutoverIsObservableAndSafe`

---

### Scenario 5 — new access paths cannot silently escape the decision

The per-kind access table is mechanically guarded by a classified inventory of the
designated application-facade, in-memory-registry, event-relay, cache/index, and
model-tool boundaries. Each resolves to exactly one caller-owned, derived,
shared-infrastructure, or explicit-exempt classification. A new owned boundary cannot
ship unclassified, while an exemption remains visible for review. The guard enforces
ADR-0102 rather than relying on a future implementer to remember every store, service,
and model-facing path.

**Acceptance:**
- AC5.1: The classification guard resolves every current designated object-touching
  application boundary to exactly one valid table entry and reports an unclassified call
  site or stale table entry.
  - verify: `TestInvariant_owned_access_is_classified`
- AC5.2: A fixture that adds an unclassified owned access path causes the classification
  guard to fail.
  - verify: `TestCallerSeparation_Scenario5_UnclassifiedAccessFailsGuard`
- AC5.3: Explicit shared-infrastructure/exempt entries state their rationale and do not
  become caller-owned bypasses.
  - verify: `TestCallerSeparation_Scenario5_ExemptionsAreExplicitAndNarrow`

---

### Scenario 6 — schedule creation is collision-free across owners

A live-cluster probe (real OIDC callers against a real kind deployment) found that
`CreateSchedule`'s collision guard checks the schedule store's globally-keyed `Name`
before the create-time owner is resolved, so a name already used by a *different*
owner is rejected with a distinguishing "already exists" error instead of the
absence-style behavior every other create/collision path in this plan already
guarantees. Schedule names are a caller-chosen, human-readable key exactly like a
memory key (`docs/acceptance/caller-separation.md` Scenario 2's memory partitioning),
not a system-generated identifier like a session ID or team ID — the fix is to give
schedules the same owner-namespaced-key treatment memory already has, not to add a
second, differently-shaped refusal. `port.ScheduleStore` and both backends
(`redisstore`, `jsonlstore`) keep taking an opaque `name string`; the owner-derived
physical key is computed once, in composition (`internal/adapter/server/schedule_manager.go`),
mirroring `memory.CallerStore`'s owner-digest scheme. This is a breaking, no-migration
change consistent with this plan's existing ownerless-historical-data posture.

**Acceptance:**
- AC6.1: A create using a schedule name already used by a *different* owner succeeds
  and creates the caller's own independent schedule; it does not overwrite, adopt, or
  block on the other owner's use of that name, and the two schedules are independently
  loadable, updatable, and deletable by their respective owners.
  - verify: `TestCallerSeparation_Scenario6_SameNameDifferentOwnersDoNotCollide`
- AC6.2: A create using a schedule name already used by the *same* owner is still
  rejected as a same-owner collision, unchanged from today's behavior.
  - verify: `TestCallerSeparation_Scenario6_SameOwnerCollisionStillRejected`
- AC6.3: No create response, error, or timing distinguishes "this name is already used
  by a different owner" from "this name is available" — a caller cannot learn that
  another caller already has a schedule with a given name.
  - verify: `TestCallerSeparation_Scenario6_CollisionProbeDoesNotLeakOtherOwner`
- AC6.4: With no verifier wired (ownerless/non-OIDC deployment), schedule creation and
  same-name collision detection remain byte-identical to today: a single flat
  namespace, unaffected by the owner-prefixed key change.
  - verify: `TestCallerSeparation_Scenario6_OwnerlessNamespaceUnchanged`

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| Remote driver-side claim propagation, authenticated peer checks, and remote-store enforcement | issue [#452](https://github.com/stacklok/mecatl/issues/452) | [ADR-0103](../adr/0103-driver-caller-ownership.md) |
| Driver ownership registry, workspace-root digest, and enforced-driver cutover | issue [#452](https://github.com/stacklok/mecatl/issues/452) | [ADR-0103](../adr/0103-driver-caller-ownership.md) |
| Sharing, delegated authority, labels, and signing | later agent-identity tracks | [ADR-0100](../adr/0100-caller-identity-threading.md) |
| Migration or first-reader adoption of ownerless historical records | deliberately excluded | [ADR-0102](../adr/0102-caller-ownership-enforcement.md) |

## Sequencing recommendation

First establish the core decision/table and its exhaustive guard. Then cover persisted
and derived resources before live-run and model-facing paths, so every non-store
exception is tested against the same semantics. Finally wire the explicitly scoped
system operations and run the aggregate negative tests. `/plan-orchestrate` owns the
concrete task split.

## Named tests landing in this plan

- `TestADR_0102_IssuerSubjectPairIsOwnerIdentity`
- `TestInvariant_owned_access_is_classified`
- `TestCallerSeparation_Scenario1_OwnerCanAccessOwnedResources`
- `TestCallerSeparation_Scenario1_CreationBindsVerifiedOwner`
- `TestCallerSeparation_Scenario2_ListMetadataIsOwnerScoped`
- `TestCallerSeparation_Scenario3_LiveRunVerbsAreOwnerChecked`
- `TestCallerSeparation_Scenario3_ModelFacingMemoryToolsAreOwnerChecked`
- `TestCallerSeparation_Scenario3_LiveSubscriptionIsOwnerChecked`
- `TestCallerSeparation_Scenario4_InternalWorkersUseOnlyClassifiedAccess`
- `TestCallerSeparation_Scenario5_UnclassifiedAccessFailsGuard`
- `TestCallerSeparation_Scenario6_SameNameDifferentOwnersDoNotCollide`
- `TestCallerSeparation_Scenario6_CollisionProbeDoesNotLeakOtherOwner`

## Definition of done

1. `task lint` and `task test` pass.
2. `task docs` regenerates `llms.txt` and passes the strict documentation link gate.
3. `task api:check` passes; if an engine exported surface changes, `task api:update`
   and the required `engine/CHANGELOG.md` compatibility note are included.
4. `task ac-trace-strict` passes when this plan is marked `landed`.
5. Every acceptance proof above is present, green, and covers both a caller's own
   successful operation and another caller's denied operation where applicable.
6. `go run ./cmd/mecademo` still completes a full offline session.
7. Deployment documentation and the relevant deployment configuration/test prove that
   raw driver listeners are tenant-inaccessible trusted infrastructure until issue #452
   lands (NetworkPolicy, mTLS pinning, or a Unix socket).

## Deferred decisions and known risks

- **Raw remote drivers remain a trust boundary.** This plan does not claim they are
  caller-enforced; deployment isolation is mandatory until ADR-0103 is delivered.
- **Ownerless historical data is unavailable in OIDC mode.** There is no adoption or
  migration path in this plan.
- **Driver B-lite has no migration.** Enforced driver mode will treat records without
  registry ownership as absent, per ADR-0103.
- **The preserved-fork LRU (`engine/agent/forkreaper.go`) is one unpartitioned,
  process-wide cache (cap 8), found by an adversarial pass over this plan.** A
  high-volume caller's Parallel winners can evict a low-volume caller's still-
  referenced preserved fork before it reads it. Availability/DoS only — no
  confidentiality break, since a caller can only ever reference a fork path it
  legitimately obtained. Tracked as a separate follow-up, not blocking this plan.
- **The scheduler's owner-context bridge is intentionally narrow.** The tick loop keeps
  its `mecatl:internal/scheduler` context for claims, records, diagnostics, and event
  attribution. Once a fire session is created, composition derives the captured schedule
  owner only for authorization-sensitive run-entry reads, starts, and watchdog cancel;
  an ownerless schedule therefore remains fail-closed when ownership is enforced. The
  physical owner namespace is similarly retained only by `ScheduleStore`: generated fire
  IDs, delivery notes, lifecycle events, and metric labels use the literal schedule name.

## Exit criteria

When every Definition-of-done item holds on the accumulator, this plan satisfies issue
#368 and can move from `draft` to `landed` through `/plan-orchestrate`.
