# Persistent read-before-write ledgers — acceptance plan

**Phase:** storage-independent file-mutation safety
**Status:** landed, 2026-08-26. Panel-repair wave completed: Workspace is content-only and Environment independently owns the selected ReadLedger.
**Issue:** [stacklok/mecatl#888](https://github.com/stacklok/mecatl/issues/888).
**ADR:** [ADR-0298](../adr/0298-persistent-read-before-write-ledgers.md) — separates session-scoped ledger storage from file-content storage while preserving fail-closed mutation and final filesystem CAS.
**Accumulator branch:** `acc/persistent-read-ledgers` (off `main`).

The smallest set of work that lets a deployment retain read-before-write evidence independently of file contents, across runs and replicas, without weakening Edit or Write. It includes a Redis-backed contract proof because Redis is the first durable consumer and is already an established root-module dependency; wiring a remote filesystem remains separate work.

The doc is organized scenario-first because acceptance is about what the running harness can demonstrate, not which packages exist on disk.

## Why these scope cuts

- [ADR-0208](../adr/0208-execution-environment.md) remains authoritative for opaque versions, I/O-free lexical keying, create-only writes, and conditional replacement; [ADR-0298](../adr/0298-persistent-read-before-write-ledgers.md) supersedes only ADR-0208's in-memory/live-Workspace ledger-lifetime decision.
- [ADR-0036](../adr/0036-engine-module.md) keeps the ledger contract and in-memory reference adapter in the importable engine module while the existing Redis dependency remains in the root adapter layer.
- [ADR-0048](../adr/0048-mecak8s.md) makes Redis the relevant durable proof, but mecak8s filesystem mode and operator-facing configuration belong to follow-up issue #889.

## In scope — 3 scenarios, in implementation order

Scenarios are listed in implementation order. Each is independently demoable; later scenarios assume earlier ones but do not change their acceptance criteria. Within each scenario, ACs progress from the contract and default path through durable storage to fail-closed tool behaviour.

### Scenario 1 — One Environment composes independent file-content and evidence capabilities

A host constructs Environments that may share the exact same Workspace/content backend while carrying distinct session-scoped ledgers. Agent-facing Read records the exact opaque version returned by `ReadVersion`; Edit and existing-file Write retrieve that evidence from `Environment.ReadLedger`, not from Workspace. Ledger operations are context-aware and error-bearing, so unavailable storage cannot be mistaken for absence or success. Workspace, ReadLedger, and Environment remain in `engine/tool`, preserving the cycle break and version protocol described by [`architecture.md` § ports and adapter boundaries](../architecture.md) and [`AGENTS.md` — `FileSystem`/`Workspace`/`Environment` and ADR-0208 invariants](../../AGENTS.md).

**Work:**
- engine domain (`engine/tool`): define the session-bound read-ledger capability, keep Workspace content-only, and make Environment carry both mandatory capabilities; keep `FileVersion` opaque with a narrow persistence codec and keep `LedgerKey` I/O-free.
- reference adapters (`engine/adapter/*`): provide the in-memory default and a reusable ledger conformance suite; file-content Workspaces do not import or own ledgers.
- composition and root adapters (`internal/app`, `internal/adapter/*`): pair each Environment with a ledger while preserving the exact osfs, ACP, no-fs, remote, or fork content backend; production selection of a durable ledger is deferred to #889.
- compatibility: update the engine API baselines and changelog for the intentional Workspace and Environment surface changes.

**Acceptance:**
- AC1.1: Recording a read stores the exact opaque `FileVersion` supplied by the corresponding version-bearing read, and lookup returns that same valid token without reading file contents, preserving [ADR-0208](../adr/0208-execution-environment.md)'s no-I/O evidence contract.
  - verify: `TestInvariant_persistent_read_ledger_exact_version`
- AC1.2: Relative and ordinary in-root absolute spellings converge through the existing I/O-free lexical key rule; physical aliases may conservatively miss without filesystem inspection.
  - verify: `TestInvariant_persistent_read_ledger_lexical_key`
- AC1.3: Two Environments can carry the exact same Workspace/content backend with different ledger instances, and a record in one session is absent from the other.
  - verify: `TestPersistentReadLedgers_EnvironmentSeparatesWorkspaceAndLedger`, `TestPersistentReadLedgers_Scenario1_IndependentSessionLedgers`
- AC1.4: With no durable ledger selected, standard Environment composition supplies a fresh in-memory ledger; rebuilding the Environment starts with no evidence.
  - verify: `TestPersistentReadLedgers_Scenario1_DefaultMemoryLifecycle`
- AC1.5: The ledger, Workspace, and Environment contracts remain in `engine/tool`; neither imports a root adapter or Redis dependency.
  - verify: `TestNoCoreImportsAdapter`, `TestCoreImportDirection`, `TestNoCyclesAmongCore`
- AC1.6: Every delegation child receives a fresh child ledger and never inherits or writes parent evidence. Isolated children pair it with the fork Workspace; direct-write and other base-sharing children retain the exact parent content backend and runner through any stricter child-authority Workspace view, without reconstructing storage from `Root()`.
  - verify: `TestPersistentReadLedgers_Scenario1_ForkLedgerIsolation`, `TestPersistentReadLedgers_ForkPreservesContentBackendAndFreshLedger`, `TestPersistentReadLedgers_DirectWritePreservesWorkspaceAndFreshLedger`, `TestChildWorkspaceViewPreservesContentBackend`, `TestPathEscapePosture_Scenario5_SharedWorkspaceChildNotRelaxed`, `TestPathEscapePosture_Scenario5_BaseSharingMemberNotRelaxed`

---

### Scenario 2 — A durable ledger reopens across replicas without owning file contents

Two independently constructed Redis ledger handles bind to the same session scope and observe the same recorded versions, while a different session scope sees none of them. The Redis adapter stores only ledger identity, normalized path keys, and opaque version tokens; it neither reads nor writes file contents. This is the durable contract proof, not mecak8s filesystem wiring. It follows the root-adapter dependency direction in [ADR-0036](../adr/0036-engine-module.md) and uses the secure shared Redis connection posture established by [ADR-0233](../adr/0233-secure-external-redis.md).

**Work:**
- root adapter (`internal/adapter/redisstore`): add a session-bound read-ledger implementation using the existing Redis client ownership and key-prefix conventions, with a versioned/validated stored representation.
- conformance: exercise separate handles against one offline miniredis backend, including session isolation, reopen, malformed state, and transport failure.
- lifecycle docs: inventory the durable ledger in ADR-0027's resource/fidelity ledgers; the adapter does not own or close the shared Redis client independently.

**Acceptance:**
- AC2.1: A version recorded through one Redis ledger handle is returned after reopening another handle for the same session, including when the two handles represent separate process/replica instances.
  - verify: `TestPersistentReadLedgers_Scenario2_RedisReopen`
- AC2.2: Two sessions sharing one Redis server and one file-content backend retain independent path/version entries; neither session can use the other's prior read.
  - verify: `TestPersistentReadLedgers_Scenario2_RedisSessionIsolation`
- AC2.3: Redis ledger storage carries an injectively encoded session scope plus normalized path identity and opaque version data only; adversarial delimiters cannot make two `(session, path)` pairs address the same entry, and the ledger API has no file-content operation.
  - verify: `TestInvariant_persistent_read_ledger_storage_independence`
- AC2.4: Missing state returns “not recorded,” while timeout, unavailable transport, missing/null/wrongly typed fields, undecodable data, or unknown formats return an error distinguishable from absence. Invalid zero versions are rejected before storage; valid empty opaque tokens round-trip.
  - verify: `TestPersistentReadLedgers_Scenario2_RedisFailureClassification`, `TestPersistentReadLedgers_RedisCorruptStateFailsClosed`, `TestPersistentReadLedgers_InvalidVersionRejected`, `TestFileVersionPersistenceRoundTrip`
- AC2.5: The in-memory and Redis implementations pass one shared read-ledger conformance contract entirely offline.
  - verify: `TestReadLedgerConformance`
- AC2.6: Concurrent record and lookup operations on one session ledger are race-free, and independently opened Redis handles converge on complete opaque tokens rather than torn or partially decoded state.
  - verify: `TestPersistentReadLedgers_Scenario2_ConcurrentAccess`
- AC2.7: Canonical and conditional Redis session deletion atomically remove the ledger with the session sidecars; explicit ledger-only cleanup is idempotent. Reopening or reusing the same session ID starts with no evidence.
  - verify: `TestPersistentReadLedgers_Scenario2_DeleteAndReuseStartsEmpty`, `TestPersistentReadLedgers_RedisSessionDeletionRemovesLedger`, `TestPersistentReadLedgers_RedisConditionalDeletionRemovesLedger`
- AC2.8: Distinct session/path pairs containing separators or common prefix material remain distinct Redis addresses and cannot observe one another's evidence.
  - verify: `TestPersistentReadLedgers_Scenario2_InjectiveRedisIdentity`

---

### Scenario 3 — File tools fail closed without weakening final CAS

The built-in Read/Edit/Write tools use the selected ledger's error-bearing operations. A failed lookup or a corrupt record never authorizes mutation; a changed file still fails before replacement; and a change racing after the tool's current read is still stopped by `ReplaceFile`. New-file Write remains create-only and needs no fabricated prior-read evidence. These are the load-bearing mutation rules in [ADR-0208](../adr/0208-execution-environment.md) and [the Workspace contract](../architecture/ports.md).

A post-mutation ledger-write failure is reported honestly: the already-successful create/replace is not rolled back or described as untouched, and no new evidence is assumed. Existing evidence is not fabricated or invalidated: a later existing-file mutation may proceed only when that evidence still equals the current version and final `ReplaceFile` CAS succeeds.

**Work:**
- file tools (`engine/adapter/fstools`): thread context and ledger errors through Read, Edit, and Write; distinguish absent evidence from unavailable/corrupt ledger state.
- conformance/regressions: retain the existing Workspace and model-facing file-tool suites while adding injected ledger failures before and after mutation.
- living docs: revise architecture, implementation notes, AGENTS.md, and ADR-0027 ledger lifetime/fidelity entries; regenerate public API and documentation artifacts.

**Acceptance:**
- AC3.1: If recording a successful Read fails, the tool reports that no new evidence was retained. It does not fabricate or invalidate prior evidence; a later mutation remains authorized only if an existing recorded version equals the current content and final CAS succeeds.
  - verify: `TestPersistentReadLedgers_Scenario3_ReadRecordFailureFailsClosed`
- AC3.2: An unavailable or corrupt ledger lookup refuses Edit and existing-file Write before `ReplaceFile` is called; it is never treated as an unrecorded-but-otherwise-authorized read.
  - verify: `TestPersistentReadLedgers_Scenario3_LookupFailurePreventsMutation`
- AC3.3: An absent ledger entry preserves the existing read-before-edit/read-before-overwrite refusal, while a stale recorded version preserves the changed-since-read refusal.
  - verify: `TestInvariant_read_before_edit`
- AC3.4: A mutation between the tool's current `ReadVersion` and final `ReplaceFile` remains model-visible and never overwrites the concurrent bytes, regardless of ledger backend.
  - verify: `TestEditConditionalReplaceRejectsConcurrentChange`, `TestWriteConditionalReplaceRejectsConcurrentChange`
- AC3.5: New-file Write remains create-only and does not require a ledger entry; concurrent creators still produce exactly one winner.
  - verify: `TestPersistentReadLedgers_Scenario3_CreateOnlyUnchanged`
- AC3.6: If persisting the new version after a successful create or replace fails, the tool reports both facts without rollback and establishes no new evidence. Existing evidence retains only its ordinary exact-version meaning and remains subject to final CAS.
  - verify: `TestPersistentReadLedgers_Scenario3_PostMutationRecordFailure`
- AC3.7: Existing osfs, memfs, ACP, no-fs, remoteenv, and file-tool conformance/regression suites remain green under the revised ledger seam.
  - verify: inspection — each adapter's existing conformance entry point runs under `task test`; the aggregate gate, rather than a fabricated cross-module test name, proves this matrix
- AC3.8: An Environment-selected ledger remains independently synchronized from ACP's file RPC path, so a parked file RPC does not block a local in-memory ledger operation.
  - verify: `TestFSWorkspaceLedgerNotBlockedByParkedRPC`

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| Redis-backed file contents and mecak8s operator/configuration wiring | [stacklok/mecatl#889](https://github.com/stacklok/mecatl/issues/889) | [ADR-0048](../adr/0048-mecak8s.md); this plan proves only the independent ledger half |
| Composition-level selection of durable ledgers for production sessions | #889 | This plan migrates signatures and proves adapters; it does not add a flag, profile, or default durable wiring |
| Principal-scoped file-content namespaces and anonymous fallback | #889 | File scoping is deliberately unchanged here |
| Shell, command execution, sandboxing, fork/merge, or executable semantics | future remote-filesystem work | [ADR-0211](../adr/0211-execution-environment-runtime-seam.md) remains unchanged |
| A generic remote filesystem or ledger RPC protocol | future driver design | [ADR-0214](../adr/0214-environment-persistence.md) defers transport selection |
| TTL or time-based pruning for durable ledger keys | follow-up with the concrete mecak8s lifecycle | Delete-with-session is required here; #888 does not choose an age-based retention policy |

## Cross-cutting deliverables

- Add [ADR-0298](../adr/0298-persistent-read-before-write-ledgers.md), which declares ADR-0208 decision 6 superseded; do not edit the frozen ADR-0208 text.
- Update `docs/architecture.md`, `docs/architecture/ports.md`, `docs/design/IMPLEMENTATION-NOTES.md`, and the `AGENTS.md` invariant so Workspace is content-only and Environment independently carries the selected ledger.
- Replace ADR-0027's “reset-by-design” read-ledger fidelity row with the split default-memory/durable-selected lifecycle and add any outlives-a-call Redis ledger resource to List 1.
- Update `engine/CHANGELOG.md` and `engine/api/*.txt` under the engine compatibility policy.

## Sequencing recommendation

First define the error-bearing ledger contract, in-memory adapter, and conformance suite. Then separate Workspace content operations from Environment-owned ledger selection and migrate file tools and child construction without replacing content backends. Add and harden the Redis implementation only after the shared contract is green, integrate its key into atomic session deletion, then finish living/API documentation. This keeps every backend judged by one contract and prevents Redis details from shaping the engine interface.

## Named tests landing in this plan

- `TestInvariant_persistent_read_ledger_exact_version`
- `TestInvariant_persistent_read_ledger_lexical_key`
- `TestPersistentReadLedgers_Scenario1_IndependentSessionLedgers`
- `TestPersistentReadLedgers_Scenario1_DefaultMemoryLifecycle`
- `TestPersistentReadLedgers_Scenario1_ForkLedgerIsolation`
- `TestPersistentReadLedgers_EnvironmentSeparatesWorkspaceAndLedger`
- `TestPersistentReadLedgers_ForkPreservesContentBackendAndFreshLedger`
- `TestPersistentReadLedgers_DirectWritePreservesWorkspaceAndFreshLedger`
- `TestFileVersionPersistenceRoundTrip`
- `TestPersistentReadLedgers_RedisCorruptStateFailsClosed`
- `TestPersistentReadLedgers_RedisSessionDeletionRemovesLedger`
- `TestPersistentReadLedgers_RedisConditionalDeletionRemovesLedger`
- `TestPersistentReadLedgers_Scenario2_RedisReopen`
- `TestPersistentReadLedgers_Scenario2_RedisSessionIsolation`
- `TestInvariant_persistent_read_ledger_storage_independence`
- `TestPersistentReadLedgers_Scenario2_RedisFailureClassification`
- `TestPersistentReadLedgers_Scenario2_ConcurrentAccess`
- `TestPersistentReadLedgers_Scenario2_DeleteAndReuseStartsEmpty`
- `TestPersistentReadLedgers_Scenario2_InjectiveRedisIdentity`
- `TestReadLedgerConformance`
- `TestPersistentReadLedgers_Scenario3_ReadRecordFailureFailsClosed`
- `TestPersistentReadLedgers_Scenario3_LookupFailurePreventsMutation`
- `TestPersistentReadLedgers_Scenario3_CreateOnlyUnchanged`
- `TestPersistentReadLedgers_Scenario3_PostMutationRecordFailure`

## Definition of done

1. `task lint` and `task test` pass (both modules, `-race`).
2. `task docs` — `llms.txt` regenerated and the matlatl strict link gate is green.
3. `task api:check` passes after `task api:update`, with the intentional engine API change classified in `engine/CHANGELOG.md` under `engine/COMPATIBILITY.md`.
4. `task ac-trace` resolves every draft AC proof before the status flip; after `/plan-orchestrate` marks this plan `landed`, `task ac-trace-strict` passes.
5. The named tests are green and grep-locatable by their identifiers.
6. `go run ./cmd/mecademo` still prints a full offline session.
7. The Redis proof runs against miniredis only; no test requires a network service or live credentials.
8. `task vuln` reports no new reachable vulnerability introduced by the Redis ledger path.

## Deferred decisions and known risks

- **Durable-key retention and capacity.** Deleting a session's ledger scope is required so a reused ID cannot inherit evidence; age-based pruning/TTL remains deferred to #889. Until production lifecycle wiring lands, operators must treat the proof adapter's non-expiring key growth as an availability/capacity risk.
- **Post-mutation ledger-write failure cannot be atomic with a separately selected content backend.** The mutation remains truthful and durable, the failed operation establishes no new evidence, and any existing evidence remains governed by exact-version equality plus final CAS; cross-store distributed transactions are not introduced.
- **Redis key schema becomes durable adapter state.** Its representation must be versioned and corruption must fail closed so #889 can reuse it without an in-place ambiguity.
- **Intentional core API change.** Workspace is content-only, Environment requires a separate ReadLedger, and ledger operations are error-bearing so absence is distinct from outage; compatibility artifacts and changelog make the break explicit.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this plan is satisfied.
