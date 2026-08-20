# Review 3 — reconciliation regression audit

**Reviewed:** `acc/authority-evaluator-port` at `f9a794a1`, with the reconciliation
commit `da05727fd7e40b6c7eb3dfc8ec6edef9a1e5b076` compared to its parent
`8cb976bcbd6f13b31be06ead465fb91c4ae2e151` and `origin/main` at
`a79feaea7eadf92a49e0cd7c0086f35302deae26`.
**Date:** 2026-08-19.
**Verdict:** **do not merge.** The authority repairs are sound, but the
single-parent reconciliation commit clobbered upstream storage-maintenance wiring,
compatibility history, and documentation structure. `origin/main` was already an
ancestor before this commit, so these are resolution regressions, not unapplied
upstream work.

## Authority repairs remain correct

- **Ownerless evaluation (`7bd15592`).** The optional
  `port.AuthorityOwnerRequirement` lets Cedar require an owner while local authority
  remains usable for unauthenticated deployments.
- **Filesystem resources (`0c2b4318`).** `tool.AuthorityResourceResolver` derives
  the physically resolved workspace target, failing closed where resolution is not
  available.

The reconciliation's principal/authority persistence adaptations are also sound:
event-source restoration, session snapshots, memstore metadata estimates, exact
principal scope hashing, and adoption carrying the source bound authority all
preserve their intended behavior.

## Ship blockers — production composition was clobbered

The `server.Config` literal in `internal/app/build.go` gained `RootAuthority` but
dropped six independent upstream bindings. `RootAuthority` is additive; it does not
replace any of them.

```go
StorageManagementAuthorized:         storageManagementAuthorizer(cfg),
LocalStorageMaintenanceSingleWriter: localStorageMaintenanceSingleWriter(store),
SessionLiveness:                     cfg.sessionLiveness,
RetentionPolicy:                     server.RetentionPolicy{...},
StorageMaintenanceStatus:            cfg.storageMaintenance.snapshot,
StorageMaintenanceUpdate:            cfg.storageMaintenance.update,
```

### Consequences

1. **Retention is disabled.** Missing `RetentionPolicy` gives the service zero
   retention bounds/cadence. This directly causes:

   ```text
   TestBuildEnabledChildGCNarratesAndStops
   Build narrated 'session GC ENABLED' 0 times, want exactly 1
   ```

2. **Live delegated sessions can be deleted by retention.** Missing
   `SessionLiveness` disconnects the service-side maintenance exclusion from the
   liveness registry that still protects active children in engine wiring. This
   directly causes:

   ```text
   TestBuildChildLeaseBlocksRemoteRetention
   remote retention delete = <nil>, want ErrSessionLeasedElsewhere
   ```

3. **Storage-management authorization is removed.**
   `StorageManagementAuthorized` was the service authorization callback. Its absence
   is an unrelated security regression.

4. **The local maintenance single-writer guard is removed.**
   `LocalStorageMaintenanceSingleWriter` was the concurrency guard for local
   maintenance operations.

5. **Maintenance health/progress is disconnected.**
   `StorageMaintenanceStatus` and `StorageMaintenanceUpdate` leave the
   Build-owned maintenance state unused.

### Follow-on deletion hid the symptom

`f9a794a1` then deleted `storageMaintenanceState.update` because the dropped
`StorageMaintenanceUpdate: cfg.storageMaintenance.update` method-value binding left
it callerless. The current file jumps from active-job calculation to `snapshot`.
That cleanup is not authority work and must be reversed together with the wiring.

## Additional reconciliation losses

### Engine compatibility history is incomplete

`engine/CHANGELOG.md` replaces upstream Unreleased storage-continuity entries with
authority-only entries while the corresponding exported APIs remain in the tree.
Restore the removed Added and Changed records covering session liveness, migration
exclusion, retention estimates/conditional cleanup, storage health, adoption
metadata, metadata pagination, and shared metadata ordering. The API snapshot gate
can stay green while this required compatibility record is false.

### Documentation structure was overwritten

- `docs/architecture.md` lost the upstream `## 2. The big picture` heading; the
  overview material is now unsectioned immediately before `## Authority evaluation`.
  Restore the big-picture heading and keep authority as an additive section.
- `docs/design/IMPLEMENTATION-NOTES.md` lost `## Domain — engine/prompt/` when the
  delegated-authority section was inserted. Restore the prompt-domain heading before
  its existing prompt content.

### Authority documentation needs correction

- `user-docs/what-you-get/permissions.md` names **ADR 0231** while linking
  ADR-0233.
- The acceptance plan properly remains `in-progress`, but it incorrectly says
  ADR-0233 is still “to be written” and repeats that work item even though the ADR
  exists and is accepted.
- AC3.8 duplicates the same `verify:` line.

## Why tooling did not stop this

Go permits the now-disconnected helpers to remain package-level and unused. The
`server.Config` literal was the sole composition site, so losing method-value and
callback assignments compiled cleanly. Existing tests caught only the retention and
liveness symptoms; they do not directly prove the authorization callback or
single-writer guard are wired.

## Required repair before merge

1. Restore all six `server.Config` fields alongside `RootAuthority`.
2. Restore `storageMaintenanceState.update`.
3. Add composition-level coverage for both `StorageManagementAuthorized` and
   `LocalStorageMaintenanceSingleWriter` wiring, in addition to the two existing
   regression tests.
4. Restore the upstream compatibility changelog entries.
5. Restore the two overwritten documentation section headings and correct the three
   authority-documentation errors.
6. Re-run:

   ```sh
   go test ./internal/app -count=1
   task lint
   task test
   task test:engine-standalone
   task api:check
   task docs
   ```

No PR, `ac-trace`, or final panel approval should proceed until this repair is green.
