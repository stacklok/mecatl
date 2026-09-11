# Managed temporary command leases — acceptance plan

**Phase:** capability — deterministic lifecycle for command and background-job temporary storage.
**Status:** landed, 2026-09-06. macOS managed temporary-storage lifecycle support is complete.
**ADR:** [ADR-0281](../adr/0281-managed-temporary-command-leases.md) — managed and system scopes, validated private leases, and deterministic reaping.
**Accumulator branch:** `acc/managed-temporary-command-leases` (off `main`).

The smallest set of work that makes managed temporary storage the default for local
Linux and macOS Shell commands and background jobs: each gets a private, attributable lease;
the harness cleans it promptly where safe and reclaims only validated crash residue
on a bounded schedule. Workspace keys derive transiently from canonical physical
paths and are never persisted as raw backend identity. The owner-only workspace manifest
records the canonical current workspace path for debugging and refreshes it when the same
key is opened from a new path. It retains a human-approved system
temporary-directory escape without treating it as a filesystem sandbox. It
deliberately excludes the workspace scratchpad and delegation-fork lifecycle proposed
by ADR 0282 and ADR 0283.

The doc is organized scenario-first because acceptance is about what the running
harness can demonstrate, not which packages exist on disk.

## Why these scope cuts

- [ADR-0281](../adr/0281-managed-temporary-command-leases.md) supports Linux and macOS in v1,
  uses the inherited system temporary directory by default rather than an XDG
  cache/state/runtime directory, and makes `mode: system` the explicit rollback.
- [ADR-0201](../adr/0201-background-bash.md) keeps foreground and background work
  under the existing `Shell` permission and process-group lifecycle; the new storage
  feature must not create a second shell-execution tool or weaken cancellation.
- [ADR-0211](../adr/0211-execution-environment-runtime-seam.md) keeps the bound
  `CommandRunner` and `Workspace` together in `tool.Environment`; an invocation
  environment overlay extends that runner seam rather than changing shell text.
- The scratchpad and preserved filesystem/environment forks remain deferred to
  [ADR-0282](../adr/0282-managed-workspace-scratch-cache.md) and
  [ADR-0283](../adr/0283-managed-delegation-fork-lifecycle.md). This plan reserves
  their directory names but does not create their allocation protocols.

## In scope — 4 scenarios, in implementation order

Scenarios are listed in implementation order. Each is independently demoable; later
scenarios assume earlier ones but do not change their acceptance criteria.

### Scenario 1 — an operator admits one safe managed namespace

A Linux or macOS operator selects managed temporary storage from user-global configuration.
The harness resolves the default relative root below the inherited system temporary
directory, creates or adopts only a private owner-controlled root, and derives a
non-model-visible workspace key from stable workspace identity. The configuration
and allocation protocol must fail closed rather than turn a malformed or foreign
path into deletion authority ([ADR-0281](../adr/0281-managed-temporary-command-leases.md)
§1, §3, §6; [`AGENTS.md` — filesystem mutation invariants](../../AGENTS.md)).

**Work:**
- adapters (`internal/adapter/permconfig`, `internal/adapter/osfs`): strict
  operator-only `temporary_storage` schema and a Linux managed-storage adapter with
  handle-rooted no-link traversal, private creation/adoption, workspace identity,
  digest key, manifests, and locks.
- engine/tool: the smallest exported per-invocation command-environment overlay
  capability needed by a bound runner; no workspace/path capability is widened.
- composition (`internal/app`): resolve operator configuration, construct the
  managed backend once, and preserve the existing system-temp runner when
  `mode: system` is selected.

**Acceptance:**
- AC1.1: With no `temporary_storage` block, a Linux or macOS local runner selects managed
  mode and resolves the private `mecatl` root below the inherited system temporary
  directory; it does not select an XDG cache, state, or runtime directory.
  - verify: `TestADR_0281_DefaultManagedRootUsesSystemTemp`
- AC1.2: User-global `temporary_storage` settings accept a validated relative or
  owner-controlled absolute managed root; a validated configured or inherited
  `system_temp_dir`; a one-minute-to-thirty-day command TTL; a
  one-minute-to-twenty-four-hour sweep interval; a one-second-to-one-hour regular
  `reap_timeout` (five minutes by default); and a one-second-to-five-minute
  `shutdown_reap_timeout` (one minute by default). Invalid roots, relative escapes,
  zero/out-of-range durations, and invalid modes fail loudly.
  - verify: `TestADR_0281_TemporaryStorageConfigValidation`
- AC1.3: A project-tier `temporary_storage` block cannot redirect the root, disable
  cleanup, or alter retention; it is ignored with an operator warning while the
  trusted operator-tier resolution remains effective.
  - verify: `TestADR_0281_ProjectTemporaryStorageIgnored`
- AC1.4: Linux and macOS admit `mode: managed`; Windows and other unsupported
  platforms fail configuration validation before serving, while `mode: system`
  remains available and preserves existing behavior.
  - verify: `TestADR_0281_ManagedModeUnixAdmission`
- AC1.5: Every managed root, workspace, lease, lock, manifest, completion record,
  and temporary-write file is created handle-relatively with private permissions
  before it is published. A permissive umask never creates a group- or world-readable
  observation window, and an existing foreign or permissive object is rejected rather
  than repaired.
  - verify: `TestADR_0281_ManagedObjectsCreatedAtomicallyPrivate`
- AC1.6: A managed root/workspace allocation rejects symlink, replacement,
  ownership, mode, or manifest-key/current-path failures without deleting anything. Its
  96-bit domain-separated key is encoded as canonical unpadded URL-safe Base64 in
  exactly 16 characters; non-canonical or differently sized values fail closed. Its
  owner-only manifest contains the canonical current workspace path and refreshes that
  field when the same key opens from a new path, but persists no raw backend identity or global
  workspace index. Handle-rooted cleanup protects against malformed, foreign-owned,
  symlinked, and non-cooperating replacement paths; processes running as the same UID
  as the harness remain cooperating local principals and are outside this cleanup
  boundary.
  - verify: `TestADR_0281_ManagedRootAndWorkspaceFailClosed`
- AC1.7: A missing, malformed, or newer-than-supported version in any allocation
  manifest or sweep-completion record is reported and retained without rewrite or
  deletion.
  - verify: `TestADR_0281_UnknownManifestVersionRetained`

---

### Scenario 2 — Shell explicitly chooses lifecycle scope

The existing `Shell` tool accepts `temp_scope: managed|system`, defaulting to managed
when the operator has management enabled. It uses a second, tool-wide synthetic
`ShellSystemTemp` policy decision only for the system environment overlay; this is
not a path authorization, does not grant Shell, and deliberately introduces no
compound Shell-option permission grammar in v1 ([ADR-0281](../adr/0281-managed-temporary-command-leases.md)
§1; [ADR-0201](../adr/0201-background-bash.md) D1/D5).

**Work:**
- engine/tool and engine/app (`engine/agent`): validate the optional scope field,
  preserve the existing Shell command authorization, and evaluate the second
  synthetic capability only for a system-scope request.
- composition (`internal/app`): install the floor-scoped `ShellSystemTemp` Ask and
  model-visible Shell guidance through the real engine factory.
- adapters / docs: render an approval/audit annotation that identifies the requested
  unmanaged scope but never records a raw temporary path; document operator config
  and migration behavior in `user-docs/`.

**Acceptance:**
- AC2.1: A Shell call without `temp_scope`, or with `temp_scope: managed`, receives
  the managed lease overlay when managed mode is enabled; an unknown scope is
  rejected before command execution.
  - verify: `TestADR_0281_ShellManagedScopeDefaultAndValidation`
- AC2.2: A `temp_scope: system` request requires both the ordinary Shell decision
  and `ShellSystemTemp`; allowing `Shell(go test:*)` alone never permits the system
  overlay, while a deny on `ShellSystemTemp` dominates every allow.
  - verify: `TestADR_0281_SystemScopeRequiresIndependentCapability`
- AC2.3: `ShellSystemTemp` is a deliberately tool-wide v1 capability: its allow
  permits the system overlay only for Shell commands independently allowed by normal
  policy, never Shell execution or arbitrary filesystem paths.
  - verify: `TestADR_0281_SystemTempCapabilityIsGlobalButNotShellAllow`
- AC2.4: A system-scope approval and audit record disclose the scope and safe command
  summary but contain neither raw temporary path nor environment values.
  - verify: `TestADR_0281_SystemTempApprovalDoesNotLeakPath`
- AC2.5: The real built engine's system prompt explains that managed storage is
  disposable, `temp_scope: system` is the declared escape for host-shared or
  longer-lived temporary state, and the field is not a filesystem sandbox.
  - verify: `TestADR_0281_EngineSystemPromptContainsTempScopeContract`
- AC2.6: When the operator selects `mode: system`, every Shell call retains the
  configured/inherited system temporary-directory behavior, no managed allocation
  is created, and per-call `temp_scope` cannot re-enable management.
  - verify: `TestADR_0281_SystemModeIsRollbackSwitch`

---

### Scenario 3 — every managed command and job has one disposable lease

A foreground command and a background Shell job each receive their own random private
lease. The runner supplies the lease `tmp/` child through an invocation environment
overlay—not through shell interpolation—and ordinary completion, cancellation, and
timeout preserve the existing process-group result while attempting exact-target
cleanup only after the managed group is gone ([ADR-0281](../adr/0281-managed-temporary-command-leases.md)
§2, §4; [ADR-0201](../adr/0201-background-bash.md) D7/D9).

**Work:**
- adapters (`internal/adapter/osfs`, `internal/adapter/procgroup`): allocate, lock,
  publish lifecycle metadata, inject `TMPDIR`/`GOTMPDIR` and the private test-home
  marker per process invocation, and perform normal cleanup without changing command
  result semantics.
- engine/tool and engine/app: thread the overlay through foreground and streaming
  command runner paths without changing the runner's namespace affinity.
- test support (`internal/testutil/testhome`): recognize only a validated
  runner-owned marker and create test HOME/XDG roots below that lease.

**Acceptance:**
- AC3.1: A managed foreground Shell call receives a distinct owner-only
  `cmd-<16-char-base64-random-id>/tmp` directory in both `TMPDIR` and `GOTMPDIR`; shell
  text is byte-for-byte free of injected temporary paths, and manifest metadata
  contains no command, output, environment, credential, or transcript content.
  The fixed internal overlay is applied after the common secret scrub and cannot
  restore, override, audit, or log a scrubbed credential-shaped variable.
  - verify: `TestADR_0281_ForegroundLeaseOverlayAndMetadataPrivacy`
- AC3.1b: Across foreground/background and managed/system scope combinations,
  representative provider, GitHub, cloud, and generic API-key/token variables remain
  absent from the command environment while only the expected temporary-storage
  variables differ by scope.
  - verify: `TestADR_0281_TempOverlayPreservesSecretScrub`
- AC3.2: A managed `background: true` Shell call receives one distinct
  `job-<16-char-base64-random-id>/tmp` lease that remains held for its complete job lifetime
  and is cleaned under the same terminal rules as a foreground command.
  - verify: `TestADR_0281_BackgroundJobLeaseLifecycle`
- AC3.3: Successful completion, cancellation, and timeout retain their existing
  exit/cancellation/deadline outcomes while removing the exact lease after the
  managed process group has terminated.
  - verify: `TestADR_0281_LeaseCleanupPreservesCommandOutcome`
- AC3.4: If the managed process group remains live, terminal handling records the
  lifecycle transition and defers deletion; an escaped descendant after the managed
  group exits does not prevent the documented immediate lease deletion.
  - verify: `TestADR_0281_GroupLivenessControlsImmediateCleanup`
- AC3.5: The test-home helper accepts only a validated runner-owned marker and keeps
  its temporary HOME/XDG_CONFIG_HOME tree inside that command's lease; an absent or
  forged marker retains its existing safe behavior.
  - verify: `TestADR_0281_TestHomeUsesValidatedLeaseMarker`
- AC3.6: Shell executing through an isolated worktree or force-copy child
  `tool.Environment` receives a lease keyed to that child workspace instance—not its
  parent—and its overlay is applied by the runner bound to the same child namespace.
  - verify: `TestADR_0281_ChildEnvironmentGetsDistinctAffinedLease`
- AC3.7: Concurrent managed runners receive distinct leases, and a reaper cannot
  remove either live allocation while its per-lease lock is held, including for a
  command with no timeout.
  - verify: `TestADR_0281_ActiveLeaseLockDefeatsReaper`

---

### Scenario 4 — crash residue is reaped by a bounded Build-owned worker

A managed lease that normal cleanup could not remove is reclaimed only after TTL and
only after all owner/type/containment/schema/no-link/lock checks pass. `app.Build`
owns the interval-gated startup and periodic recovery worker, and `Built.Close`
cancels and joins it before tearing down dependencies. The resource inventory records
this process-lifetime worker and its durable sweep coordination state
([ADR-0281](../adr/0281-managed-temporary-command-leases.md) §4–§6;
[ADR-0027](../adr/0027-cloud-native.md) List 1).

**Work:**
- adapters (`internal/adapter/osfs` or a narrow managed-storage adapter): nonblocking
  root and lease locks, atomic completion record, validated TTL candidate scan, and
  idempotent exact-target removal.
- composition (`internal/app`): Build-owned startup/cadence worker with individual
  sweep bounds, cancellation, join, no-op system/unsupported path, and injected
  diagnostics only where no event owns the fact.
- docs: add the worker, locks, durable completion record, and restart disposition to
  ADR 0027's resource inventory; update architecture, operator usage, and user docs.

**Acceptance:**
- AC4.1: A startup or periodic sweep first obtains the nonblocking root lock, rereads
  durable completion state, skips a scan completed within the interval, and records
  completion only after a full successful scan; it never delays allocation or command
  execution.
  - verify: `TestADR_0281_IntervalGatedSweepCoordination`
- AC4.2: An unlocked, validated command/job lease becomes reclaimable after its
  applicable TTL from terminal state or last recorded lifecycle transition; malformed,
  foreign, symlinked, replaced, unrecognized, or lock-contended candidates are
  retained without deletion.
  - verify: `TestADR_0281_ReaperDeletesOnlyValidatedEligibleLease`
- AC4.3: A crash-like abandoned lease is reclaimed by a later sweep, while concurrent
  runners and reapers cannot delete the wrong allocation or publish a successful
  sweep state after an interrupted scan.
  - verify: `TestADR_0281_CrashRecoveryAndConcurrentReaping`
- AC4.4: `app.Build` performs one best-effort interval-gated startup attempt and
  later sweeps on the configured cadence. A startup/periodic sweep runs under the
  configured `reap_timeout` (five minutes by default); a blocked scan or metadata
  operation is cancelled, records no successful completion, and cannot hang shutdown.
  `Built.Close` cancels the worker and applies the independently configured
  `shutdown_reap_timeout` (one minute by default), reporting a deadline expiry rather
  than claiming the worker joined. Disabled, system, and unsupported backends add no
  worker or mutation.
  - verify: `TestADR_0281_BuildOwnsManagedTempWorkerLifecycle`
- AC4.5: The ADR 0027 resource inventory records the managed-temp worker, root/lease
  locks, and completion record with owner, scope, cleanup, and restart disposition;
  architecture and public operator documentation describe the Linux and macOS managed
  lifecycle and system-mode rollback.
  - verify: inspection — documentation and inventory are reviewed with the lifecycle implementation; `task docs` enforces links and generated `llms.txt`.
- AC4.6: In an offline end-to-end run, a managed Shell command allocates a private
  lease, normal terminal handling removes it, and a separately deferred eligible
  residue is removed only by a deterministic later reaper sweep—without a live model
  or network.
  - verify: `TestManagedTemporaryCommandLeases_Scenario4_EndToEnd`

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| Cross-command workspace scratchpad and its retention protocol | later implementation plan | [ADR-0282](../adr/0282-managed-workspace-scratch-cache.md) |
| Filesystem/environment fork allocation, retained-winner cap, and fork quarantine | later implementation plan | [ADR-0283](../adr/0283-managed-delegation-fork-lifecycle.md) |
| A compound or parameterized permission grammar such as Shell-option selectors | separate decision and ADR | [ADR-0281](../adr/0281-managed-temporary-command-leases.md) §1 |
| Windows managed-mode ownership/locking/no-link contract | later platform design | [ADR-0281](../adr/0281-managed-temporary-command-leases.md) §1, §6 |
| Global system-temp cleanup, arbitrary agent-selected cleanup paths, container/VM sandboxing, and session-lifetime temp directories | not planned by this capability | [ADR-0281](../adr/0281-managed-temporary-command-leases.md) §7 |
| Policy for migrating a supported older manifest version | version-increment proposal | [ADR-0281](../adr/0281-managed-temporary-command-leases.md) §3 |

## Sequencing recommendation

First establish config admission and the validated managed namespace. Then widen the
engine command-runner API once and update every runner/fake through the API-compat
protocol before adding `temp_scope` policy. Add foreground and background leases over
that common overlay. Last, add the reaper and Build worker once normal lifecycle
metadata exists. Keep all deletion authority in the validated managed-storage adapter;
the agent loop remains adapter-agnostic.

## Named tests landing in this plan

- `TestADR_0281_TemporaryStorageConfigValidation`
- `TestADR_0281_ManagedObjectsCreatedAtomicallyPrivate`
- `TestADR_0281_ManagedRootAndWorkspaceFailClosed`
- `TestADR_0281_SystemScopeRequiresIndependentCapability`
- `TestADR_0281_TempOverlayPreservesSecretScrub`
- `TestADR_0281_ForegroundLeaseOverlayAndMetadataPrivacy`
- `TestADR_0281_BackgroundJobLeaseLifecycle`
- `TestADR_0281_ChildEnvironmentGetsDistinctAffinedLease`
- `TestADR_0281_ActiveLeaseLockDefeatsReaper`
- `TestADR_0281_ReaperDeletesOnlyValidatedEligibleLease`
- `TestADR_0281_BuildOwnsManagedTempWorkerLifecycle`
- `TestManagedTemporaryCommandLeases_Scenario4_EndToEnd`

## Definition of done

1. `task lint` and `task test` pass, including both Go modules and race tests.
2. `task docs` regenerates `llms.txt` and passes the strict matlatl link gate.
3. `task api:check` passes; because the runner overlay is an exported engine API,
   `task api:update` is run and `engine/CHANGELOG.md` contains its compatibility
   classification if the public surface changes.
4. `task ac-trace-strict` passes after `/plan-orchestrate` marks this plan `landed`.
5. Every named `TestADR_0281_*` and scenario test is green offline; no test uses a
   live model or network.
6. `go run ./cmd/mecademo` still prints a full offline session.
7. The implementation records the Build-owned worker and durable coordination data in
   ADR 0027's List 1 resource inventory before landing.

## Deferred decisions and known risks

- **Manifest migrations.** Future format versions fail closed until a version-specific
  migration policy is proposed; this plan implements no migration path.
- **System-scope granularity.** `ShellSystemTemp` is intentionally global in v1. A
  command-pattern/options mini-language needs a separate design decision.
- **Escaped processes.** Process-group escape remains observable but not reliably
  discoverable; ADR 0281 intentionally permits deletion after group exit or crash TTL.
- **Managed root durability.** The inherited system temp directory can be cleared by
  the host; this is compatible with temporary storage and leaves no claim of durable
  artifact preservation.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this plan is
satisfied.
