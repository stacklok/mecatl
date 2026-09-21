# Session storage continuity — acceptance plan

**Phase:** Large historical stores, migration, cleanup, and writable legacy continuity.
**Status:** superseded historical record, 2026-09-21. The directing user's approved alpha compatibility cleanup retired legacy storage migration, promotion, adoption, and maintenance APIs plus their proofs; current-only namespace behavior is authoritative and this record has no current traceability claims.
**Issue:** [stacklok/mecatl#583](https://github.com/stacklok/mecatl/issues/583), with sub-issues [#586](https://github.com/stacklok/mecatl/issues/586)–[#596](https://github.com/stacklok/mecatl/issues/596).
**ADR:** [ADR-0226](../adr/0226-session-storage-maintenance.md) — bounded current snapshots, indexed metadata, distinct maintenance jobs, and explicit legacy adoption.
**Accumulator branch:** `acc/session-storage-continuity` (off `main`).

The smallest set of work that lets a large historical session store stay responsive, stop
quadratic growth, reclaim old physical history safely, apply understandable retention, and turn
an eligible legacy chat into a new writable continuation from mecatui.

The doc is organized scenario-first because acceptance is about what the running harness and
client demonstrate, not which packages exist on disk.

## Why these scope cuts

- [ADR-0217](../adr/0217-session-discovery-continuation.md) keeps durable kind and the authoritative transcript server-owned; missing metadata never grants chat continuation.
- [ADR-0027](../adr/0027-cloud-native.md) keeps the loop storage-agnostic and makes snapshots, event logs, leases, and restart fidelity distinct resources.
- [ADR-0104](../adr/0104-session-family-physical-naming.md) keeps session IDs opaque and deletion/migration family-safe.
- [ADR-0226](../adr/0226-session-storage-maintenance.md) separates physical optimization from destructive cleanup and semantic adoption.
- Existing v1 stores remain readable throughout; there is no startup rewrite of an entire historical store.

## In scope — 9 scenarios, in implementation order

Scenarios are listed in implementation order. Each is independently demoable; later scenarios
assume earlier ones but do not weaken their acceptance criteria.

### Scenario 1 — Current snapshots remain bounded and crash-safe

An operator drives the same JSONL-backed session through many saves. The store keeps one
versioned current snapshot, preserves append-only tool/event sidecars, and can still load v1
records through the bounded reader. Atomic replacement and snapshot fidelity follow
[ADR-0226](../adr/0226-session-storage-maintenance.md), [ADR-0027](../adr/0027-cloud-native.md),
and the SessionStore boundary in [`architecture.md`](../architecture.md).

**Work:**
- `port.SessionStore` remains unchanged and format-neutral; any inventory, maintenance, and health additions are separate optional `engine/port` capabilities carrying domain metadata only—never paths, JSONL headers, filenames, locks, or job-file representations.
- jsonlstore keeps format detection, physical locking, catalog persistence, atomic replacement, and temp naming adapter-private; migration/cleanup orchestration and authorization remain in Service/composition, and `engine/agent` imports none of these capabilities.
- conformance/docs: crash/disk-full fixtures, compatibility, ADR/architecture/resource-inventory updates.

**Acceptance:**
- AC1.1: Saving a same-sized session 1,000 times leaves steady-state snapshot storage bounded to one current snapshot plus bounded metadata and one in-progress temporary replacement.
  - verify: `TestSessionStorageContinuity_Scenario1_BoundedRepeatedSaves`
- AC1.2: On filesystems supporting same-directory atomic rename plus file and directory sync, every injected process/OS-crash point reopens either the prior committed snapshot or the new committed snapshot—never absence, a torn aggregate, or a silently older v1 record; unsupported durability is detected and reported as a weaker explicit capability rather than overclaimed. Once verified v2 is committed it is authoritative over coexisting v1.
  - verify: `TestSessionStorageContinuity_Scenario1_AtomicCrashRecovery`, `TestSessionStorageContinuity_Scenario1_DurabilityCapabilityTruth`
- AC1.3: V1 snapshots remain loadable and a successful lazy promotion preserves conversation, usage, counters, state, profile, selector, environment reference, owner, kind, relationship, title/provenance, logical modification time, and sidecars; `unknown` remains `unknown`.
  - verify: `TestSessionStorageContinuity_Scenario1_V1PromotionFidelity`
- AC1.4: Disk-full or replacement failure leaves the prior committed family readable and produces a loud storage error.
  - verify: `TestSessionStorageContinuity_Scenario1_DiskFullPreservesCommittedSnapshot`
- AC1.5: Startup and the next successful save identify and safely remove abandoned same-family temporary replacements only while holding the cross-process family mutation lock and proving the temp naming/ownership generation is inactive; repeated crashes cannot grow orphan temps without bound, and one process never removes another process's in-progress replacement or a committed snapshot.
  - verify: `TestSessionStorageContinuity_Scenario1_OrphanTemporaryRecovery`, `TestSessionStorageContinuity_Scenario1_ActiveTemporaryNotReaped`

---

### Scenario 2 — Inventory pages are truly work-bounded

A client asks for page one and page two from a store containing hundreds of large transcripts.
The store consults only derivative inventory metadata, not conversations; cursors bind one
catalog generation and filter set. Catalog rebuild and lock scope follow [ADR-0226](../adr/0226-session-storage-maintenance.md), while public ordering/ownership keep
[ADR-0217](../adr/0217-session-discovery-continuation.md)'s contract and the inward dependency
rule in [`AGENTS.md`](../../AGENTS.md).

**Work:**
- ports/adapters: metadata generation/cursor contract and conformance over memstore, jsonlstore, redisstore, and grpcdriver.
- jsonlstore: rebuildable metadata catalog over v2 headers and bounded v1 tails; narrow catalog and per-family locking.
- composition: stale-session reconciliation and retention consume the cheap metadata path.

**Acceptance:**
- AC2.1: Page latency, bytes read, and allocations are independent of transcript/history size and proportional to page size after catalog readiness; fetching page two does not rediscover every snapshot.
  - verify: `TestSessionStorageContinuity_Scenario2_PageWorkBounded`
- AC2.2: Ordering is `(modified_at DESC, session_id ASC)`; a cursor is filter- and generation-bound, and a stale cursor returns an explicit restart signal rather than mixing generations.
  - verify: `TestSessionStorageContinuity_Scenario2_GenerationBoundCursor`
- AC2.3: A missing/corrupt/stale catalog rebuilds from v2 headers or bounded v1 tails without becoming transcript authority, and shared-directory changes are detected rather than hidden by a process-local cache.
  - verify: `TestSessionStorageContinuity_Scenario2_CatalogRebuildAndExternalChange`
- AC2.4: A blocked inventory or catalog rebuild does not delay Save, Load, EventLog.Append, or ToolCall for a different session; Save, Delete, migration promotion/removal, EventLog.Append, and ToolCall for the same family coordinate under one cross-process mutation identity so snapshot-last deletion or migration cannot race a sidecar append.
  - verify: `TestSessionStorageContinuity_Scenario2_UnrelatedMutationNotBlocked`, `TestSessionStorageContinuity_Scenario2_SameFamilyMutationSerialized`
- AC2.5: In the 807-row/12-GiB-equivalent fixture, a 100-row page visits at most 101 ordered catalog rows, performs zero snapshot/transcript reads, and page two repeats neither catalog rebuild nor prior-page traversal; the benchmark records latency and allocations as a trend signal.
  - verify: `BenchmarkSessionStorageContinuity_LargeInventory`, `TestSessionStorageContinuity_Scenario2_LargeInventoryWorkCounters`

---

### Scenario 3 — Sessions becomes usable after the first page

`mecatui sessions` and `/sessions` render the first page immediately, continue loading without
selection drift, and preserve useful rows on a later failure. This is one shared UI path under
[ADR-0217](../adr/0217-session-discovery-continuation.md) and the TUI client layering in
[`architecture.md`](../architecture.md).

**Acceptance:**
- AC3.1: Startup and interactive inventory display the first usable page before requesting the next page.
  - verify: `TestSessionStorageContinuity_Scenario3_FirstPageRendersImmediately`
- AC3.2: Appending pages preserves active tab, search query, selection by exact session ID, scroll position, and deduplicated deterministic rows.
  - verify: `TestSessionStorageContinuity_Scenario3_IncrementalStateStable`
- AC3.3: Initial loading, loading-more, complete, cancelled, stale-cursor restart, and retryable later-page failure are distinct; a later failure retains already-rendered rows and retry never duplicates them.
  - verify: `TestSessionStorageContinuity_Scenario3_PartialFailureAndRetry`
- AC3.4: Cancelling or closing the panel stops further page requests without creating or rebinding a session.
  - verify: `TestSessionStorageContinuity_Scenario3_CancelStopsPagination`

---

### Scenario 4 — Historical v1 storage is optimized incrementally

An authenticated operator plans and applies semantics-preserving v1-to-v2 compaction. The job
runs server-side in bounded batches, survives disconnect/restart, and never changes durable kind
or sidecars. It is the physical migration from [ADR-0226](../adr/0226-session-storage-maintenance.md), not the semantic adoption from [ADR-0217](../adr/0217-session-discovery-continuation.md).

**Acceptance:**
- AC4.1: Dry-run reports v1/v2/invalid/skipped family counts, current bytes, estimated reclaimable bytes, and temporary-space requirements without writing.
  - verify: `TestSessionStorageContinuity_Scenario4_MigrationDryRunIsReadOnly`
- AC4.2: Apply processes bounded batches with durable progress, cancellation, resumability, per-item errors, and idempotent convergence; cancellation stops future items and does not roll back committed items. A stable job-scoped cross-process exclusion makes cancellation monotonic and rejects a stale overlapping resume without regressing counters.
  - verify: `TestSessionStorageContinuity_Scenario4_ResumableMigrationJob`, `TestSessionStorageContinuity_MigrationConcurrentResumesRejectStaleCheckpoint`, `TestSessionStorageContinuity_MigrationCancelWinsAfterConcurrentResume`, `TestSessionStorageContinuity_MigrationRejectsStalePlanGeneration`
- AC4.3: Each family is verified readable as v2 before v1 removal; a crash or insufficient space leaves v1 or verified v2 authoritative, with peak extra space bounded to one family.
  - verify: `TestSessionStorageContinuity_Scenario4_PerFamilyCrashSafety`
- AC4.4: Migration preserves logical modification order, full snapshot semantics, tool/event sidecars, owner, and durable kind, including `unknown`; corrupt/torn records are reported or quarantined, never silently discarded.
  - verify: `TestSessionStorageContinuity_Scenario4_MigrationPreservesSemantics`
- AC4.5: Before each family mutation, migration acquires the cross-process family lock and maintenance/run-entry lease in the documented lock order, revalidates owner scope, durable kind, state, liveness, and lease, and holds both exclusions through complete promotion/removal; a changed or active family is skipped without discarding its newest write.
  - verify: `TestSessionStorageContinuity_Scenario4_MigrationRevalidatesUnderLease`
- AC4.6: Migration plan/apply/cancel/resume/job inspection require the advertised management authority; plans and resumable job handles bind the authenticated management principal, and unauthorized/cross-caller requests reveal neither family existence, IDs, paths, owner data, nor aggregate scope.
  - verify: `TestSessionStorageContinuity_Scenario4_MigrationAuthorizationAndNoOracle`
- AC4.7: Per-item failures and job status expose bounded stable reason codes plus sanitized messages; APIs, diagnostics, durable job state, and TUI projections never carry raw backend errors, transcript/tool content, secret-shaped data, or unneeded filesystem paths.
  - verify: `TestSessionStorageContinuity_Scenario4_MaintenanceErrorsAreSanitized`

---

### Scenario 5 — Cleanup is planned before anything is deleted

An authenticated operator requests a retention plan, reviews its scope, and applies the exact
generation-bound plan. Automatic retention and manual cleanup use one deterministic planner;
physical deletion retains ADR-0104 family ordering and mutation-time lease protection from
[ADR-0027](../adr/0027-cloud-native.md).

**Acceptance:**
- AC5.1: Dry-run writes nothing and reports eligible/protected counts by durable kind/state, age/cap reason, modified time, and estimated bytes without transcript, tool-argument, secret, or foreign-owner content.
  - verify: `TestSessionStorageContinuity_Scenario5_CleanupDryRunIsReadOnly`
- AC5.2: Unknown, invalid, corrupt, running, awaiting, live, and leased sessions are protected by default and do not consume eligible-main cap slots.
  - verify: `TestInvariant_retention_requires_durable_taxonomy`
- AC5.3: Apply requires an opaque confirmation token bound to the authenticated management principal, catalog generation, exact scope, and effective retention-policy version; it acquires the family lock and maintenance/run-entry lease in documented order, rechecks ownership, kind, state, liveness, and lease, and holds both exclusions through sidecar-first/snapshot-last deletion. Cross-caller replay is existence-indistinguishable; changed candidates or policy return an explicit stale-plan result and are skipped.
  - verify: `TestSessionStorageContinuity_Scenario5_ApplyRevalidatesPlan`, `TestSessionStorageContinuity_Scenario5_CrossCallerPlanReplayDenied`
- AC5.4: Partial failure is accurately reported with bounded stable reason codes and sanitized messages, and is retryable; unsupported backends report unsupported rather than zero impact, and no raw backend error/path/content/secret reaches API, diagnostics, durable job state, or TUI.
  - verify: `TestSessionStorageContinuity_Scenario5_PartialFailureAndUnsupported`, `TestSessionStorageContinuity_Scenario5_CleanupErrorsAreSanitized`
- AC5.5: Automatic sweeps and manual plans select the same candidates for the same policy and metadata generation.
  - verify: `TestSessionStorageContinuity_Scenario5_AutomaticManualPlannerParity`
- AC5.6: Age/cap selection is deterministic across adapters and catalog rebuilds, ordered oldest-first by `(modified_at ASC, session_id ASC)` for equal timestamps.
  - verify: `TestSessionStorageContinuity_Scenario5_DeterministicCleanupOrdering`
- AC5.7: Plan, apply, cancel, and store-wide health/job inspection require the advertised management authority; unauthorized and foreign-owner requests reveal neither session existence, IDs, paths, owner data, nor aggregate maintenance scope.
  - verify: `TestSessionStorageContinuity_Scenario5_ManagementAuthorizationAndNoOracle`

---

### Scenario 6 — Retention and storage health are visible and configurable

A local embedded user and a daemon operator can inspect the effective retention policy and
storage health without scanning transcripts. Configuration remains operator-tier and follows the
existing precedence/documentation discipline in [`architecture.md`](../architecture.md) and
[ADR-0226](../adr/0226-session-storage-maintenance.md).

**Acceptance:**
- AC6.1: Versioned operator configuration exposes main/child/scheduled age and count limits plus sweep cadence; `0` consistently disables, invalid/negative/unknown values fail, and explicit CLI values outrank configuration.
  - verify: `TestSessionStorageContinuity_Scenario6_RetentionConfigPrecedence`
- AC6.2: Embedded mecatui exposes its local effective policy instead of relying on hidden hard-coded destructive behavior; connected mecatui cannot claim to configure a remote server without an advertised management capability.
  - verify: `TestSessionStorageContinuity_Scenario6_EmbeddedAndRemotePolicyTruth`
- AC6.3: Health reports current/reclaimable bytes, session/file and v1/v2/main/child/scheduled/unknown/corrupt counts, last/next sweep, effective policy, active maintenance job, and last failure from bounded indexed data; unavailable is distinct from zero.
  - verify: `TestSessionStorageContinuity_Scenario6_HealthIsBoundedAndHonest`
- AC6.4: Enabling or tightening destructive main retention presents/logs a plan summary and requires explicit acknowledgement; unknown remains protected.
  - verify: `TestSessionStorageContinuity_Scenario6_DestructivePolicyAcknowledgement`

---

### Scenario 7 — An eligible legacy session becomes a new writable chat

An operator inspecting an owned `unknown` session asks the server to adopt it. The server
preflights and revalidates, creates a new explicit-main aggregate from the authoritative
transcript, and leaves the legacy source untouched. This is an explicit authority boundary under
[ADR-0217](../adr/0217-session-discovery-continuation.md), [ADR-0226](../adr/0226-session-storage-maintenance.md), caller ownership, and the run-entry lease invariants in
[`AGENTS.md`](../../AGENTS.md).

**Acceptance:**
- AC7.1: Preflight returns a capability and stable reason code; only an `unknown` source owned by the authenticated caller identity from trusted context—not request-supplied owner metadata—that is complete, valid, terminal/idle, tool-paired, and free of child/team/parallel/scheduled provenance is eligible.
  - verify: `TestSessionStorageContinuity_Scenario7_AdoptionEligibilityMatrix`, `TestSessionStorageContinuity_Scenario7_OwnershipComesFromCallerContext`
- AC7.2: Workspace/environment and provider/model bindings are displayed and explicit; unresolved bindings block adoption rather than silently using the current default, and cross-provider provider-private replay state is stripped through the existing carryover rule.
  - verify: `TestSessionStorageContinuity_Scenario7_ExplicitBindingsAndProviderNeutrality`
- AC7.3: Adoption revalidates every precondition while holding the mutation/run-entry lease and uses an idempotency key bound to the authenticated caller and source so retry/lost-response returns the same complete target rather than creating duplicates; cross-caller replay is existence-indistinguishable. Success atomically publishes one new opaque main ID with equivalent authoritative transcript and source audit relationship, while cancellation/failure leaves the source unchanged and no partial target visible.
  - verify: `TestSessionStorageContinuity_Scenario7_NewMainCopyPreservesSource`, `TestSessionStorageContinuity_Scenario7_AdoptionIdempotency`, `TestSessionStorageContinuity_Scenario7_CrossCallerReplayDenied`
- AC7.4: Adoption never occurs automatically or in bulk, reserved provenance remains permanently inspect-only, and ownership failures do not become an ID-existence oracle.
  - verify: `TestSessionStorageContinuity_Scenario7_NoEscalationOrOracle`

---

### Scenario 8 — Sessions exposes distinct adoption, optimization, and cleanup flows

The Sessions panel labels legacy rows, offers **Adopt as chat** only when the server advertises
it, and keeps semantics-preserving **Optimize storage** separate from destructive **Clean up
sessions**. The capability-driven client and explicit vocabulary follow [ADR-0217](../adr/0217-session-discovery-continuation.md), [ADR-0226](../adr/0226-session-storage-maintenance.md), and
[`docs/tui.md`](../tui.md).

**Acceptance:**
- AC8.1: An unknown row reads `Legacy session — inspect only`; `a: adopt as chat` appears only for server-advertised eligibility, while disabled rows show the authoritative reason.
  - verify: `TestSessionStorageContinuity_Scenario8_AdoptAffordanceTruth`
- AC8.2: Adoption review shows source, new-session semantics, target workspace/environment, provider/model, and future tool-write implications; cancel/error is stable, and success opens the authoritative new chat writable while the source remains inspect-only.
  - verify: `TestSessionStorageContinuity_Scenario8_AdoptionTUIFlow`
- AC8.3: Optimize storage shows reclaim estimate, invalid/skipped counts, temporary-space requirement, resumable progress, and states that sessions are preserved.
  - verify: `TestSessionStorageContinuity_Scenario8_OptimizeStorageFlow`
- AC8.4: Cleanup begins with a dry-run partitioned by main/child/scheduled/unknown/protected/live/awaiting, protects unknown by default, requires high-friction bulk confirmation, reports apply-time skips/partial completion, and never reuses single-row delete consent.
  - verify: `TestSessionStorageContinuity_Scenario8_CleanupFlow`
- AC8.5: Closing/reopening the panel reattaches to maintenance progress; cancellation is worded as stopping future items, never rollback.
  - verify: `TestSessionStorageContinuity_Scenario8_MaintenanceProgressReattach`

---

### Scenario 9 — Operators run and back up maintenance safely

A daemon operator follows tested systemd or macOS launchd examples, inspects policy and dry-run
impact, and performs a quiesced backup/migration/restore. The daemon remains the sole automatic
cleanup owner under [ADR-0226](../adr/0226-session-storage-maintenance.md), with storage privacy
and lifecycle documented in [`user-docs/reference/configuration.md`](https://mecatl.dev/docs/reference/configuration).

**Acceptance:**
- AC9.1: Tested systemd user-service and launchd examples parse, resolve the intended executable/config/state paths, preserve each argument exactly, and invoke the daemon-owned retention configuration rather than an external deletion command.
  - verify: `TestSessionStorageContinuity_Scenario9_ServiceExamplesExecuteConfiguredArgs`
- AC9.2: Documentation explicitly rejects cron/find/glob deletion, explains embedded-local versus connected-remote management, and shows effective-policy inspection plus dry-run before apply.
  - verify: none — deletion-safety guidance is reviewed by humans; `task docs` checks links and structure
- AC9.3: The runbook covers plaintext sensitivity/permissions, space forecasting, unsupported backends, and stop → backup → migrate/apply → verify → start with restore-to-new-directory validation.
  - verify: none — backup and migration runbook completeness is reviewed by humans; `task docs` checks links and structure

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| Reclassifying legacy rows in place | Not planned; adoption creates a new main session | [ADR-0226](../adr/0226-session-storage-maintenance.md) |
| Automatic or bulk semantic adoption | Not planned; explicit per-row authority only | [ADR-0217](../adr/0217-session-discovery-continuation.md) |
| External cron/systemd-timer/launchd deletion scripts | Not supported; daemon-owned sweeper | [ADR-0226](../adr/0226-session-storage-maintenance.md) |
| Replacing the SessionStore port with SQL/CQRS | Future backend choice, not required here | [ADR-0027](../adr/0027-cloud-native.md) |
| Event/tool log compaction semantics | Separate audit-retention decision | [ADR-0027](../adr/0027-cloud-native.md) |
| Rollback of already-completed maintenance items | Not promised; cancellation stops future work | [ADR-0226](../adr/0226-session-storage-maintenance.md) |

## Cross-cutting deliverables

- Update `docs/architecture.md`, `docs/design/IMPLEMENTATION-NOTES.md`, `docs/design/PRODUCTION-READINESS.md`, `docs/tui.md`, and relevant `user-docs/` pages with the shipped behavior.
- Inventory every catalog, maintenance-job registry, cache, goroutine, semaphore, and durable file in ADR 0027 Lists 1/2 as required by `AGENTS.md`.
- Extend store/driver conformance and engine compatibility artifacts for any exported optional port surface.
- Keep every test offline; no live model, Redis service, or network dependency.

## Sequencing recommendation

1. Land ADR-0226 and any optional port/domain contracts before adapter work.
2. Build the atomic v2 snapshot and indexed catalog foundations; both depend on the bounded v1 reader already shipped, and catalog readers consume v2 headers once available.
3. Add progressive inventory after indexed pagination.
4. Build migration, cleanup planner, health, and adoption as parallel server-side capabilities over the indexed foundation.
5. Add retention configuration after planner parity; add TUI adoption after adoption+progressive inventory; add TUI maintenance after migration+cleanup+health.
6. Finish with operator docs and the aggregate performance/crash/UX gate.

## Named tests landing in this plan

The scenario test names are the `verify:` identifiers above. `TestInvariant_retention_requires_durable_taxonomy` remains the cross-cutting deletion-safety pin. `BenchmarkSessionStorageContinuity_LargeInventory` is the representative large-store performance proof.

## Definition of done

1. `task lint` and `task test` pass across root, engine, authn, and provider modules with race detection.
2. `task docs` regenerates the configuration reference; matlatl strict and the user-docs Docusaurus build are green.
3. `task api:check` passes, or intentional engine additions update `engine/api/*.txt` and `engine/CHANGELOG.md` per `engine/COMPATIBILITY.md`.
4. `task ac-trace-strict` resolves every `verify:` proof after this plan is `landed`.
5. Every scenario test and `TestInvariant_retention_requires_durable_taxonomy` is green and grep-locatable.
6. `go run ./cmd/mecademo` still prints a complete offline session.
7. The large-inventory benchmark demonstrates transcript-size-independent page work; crash-injection tests prove v1/v2/migration durability.
8. `/sessions` renders a first page progressively, and an eligible legacy row can be adopted into a new writable chat through the real offline server/client/TUI path.
9. The final inline `/panel-review` reports zero ship blockers after any repair waves.

## Deferred decisions and known risks

- **Catalog implementation detail.** ADR-0226 pins behavior, rebuildability, and generation semantics, not a specific database/library; the implementation should prefer stdlib and existing dependencies unless measured evidence requires otherwise.
- **Shared-directory coordination.** File locks/generation checks must cover external writers; a process-local unchecked cache is explicitly insufficient.
- **Maintenance authorization.** Management capability and caller separation must be explicit for remote deployments; unsupported is safer than broad access.
- **Disk pressure.** Atomic replacement and migration need temporary space; plan/apply must estimate and fail without destroying the committed source.
- **Partial completion.** Migration and cleanup are per-family transactions, not global transactions; UI and APIs must report that honestly.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this plan is satisfied.
