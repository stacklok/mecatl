---
id: 59-manager-ensure-ready-surface
title: Replace the separate microVM mode with ordinary idempotent readiness
blocked_by: []
status: done
branch: "plan-microvm-execution-environments/59-manager-ensure-ready-surface"
worktree: ""
issue: "526"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Manager and activation redesign

Replace the dedicated activation/bootstrap state machine with built-in `microvm-local`
selection through ordinary configuration. Add inter-process-locked idempotent
`EnsureReady`, preserve desired config on failure, remove `--microvm`, init, and recover,
and retain status/doctor/delete entry points. Keep non-microVM behavior unchanged.

## Acceptance criteria

- AC1.1: Selecting `microvm-local` through ordinary configuration runs idempotent `EnsureReady` before the first microVM session, while selecting another profile does not start or probe microvmd.
  - verify: `TestMicroVMRedesign_Scenario1_ProfileSelectionEnsuresReady`
- AC1.2: Concurrent startup or first-use calls are serialized by the inter-process manager lock and converge on one compatible daemon and configuration.
  - verify: `TestMicroVMRedesign_Scenario1_EnsureReadyConvergesUnderManagerLock`
- AC1.3: A failed readiness attempt returns an actionable error without enabling, disabling, or rewriting the operator's desired `environment_profile`; repeating ordinary use retries readiness.
  - verify: `TestMicroVMRedesign_Scenario1_ReadinessNeverRewritesDesiredConfig`
- AC1.4: The dedicated `--microvm` flag and required `microvm init` and `microvm recover` commands are absent; `status`, `doctor`, and `delete` remain available and nonduplicative.
  - verify: `TestMicroVMRedesign_Scenario1_ObsoleteActivationAndRecoverySurfaceIsRemoved`
- AC1.5: With no microVM profile selected, existing default and no-fs snapshots, catalogs, prompts, startup, and tool behavior remain byte-compatible and no microVM dependency enters the engine module.
  - verify: `TestInvariant_microvm_disabled_is_byte_compatible`
