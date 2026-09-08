---
id: 01-temporary-storage-config
title: Operator-only temporary-storage configuration
blocked_by: []
status: done
branch: "plan-managed-temporary-command-leases/01-temporary-storage-config"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/managed-temporary-command-leases
---

# Operator-only temporary-storage configuration

Add the strict operator-global `temporary_storage` schema, resolution, validation, and composition admission required by ADR 0281. Do not implement allocation, runner overlays, or the reaper. Project-tier values must be warn-ignored; do not introduce CLI settings overrides.

## Acceptance criteria

- AC1.1: With no `temporary_storage` block, a Linux local runner selects managed mode and resolves the private `mecatl` root below the inherited system temporary directory; it does not select an XDG cache, state, or runtime directory.
  - verify: `TestADR_0281_DefaultManagedRootUsesSystemTemp`
- AC1.2: User-global `temporary_storage` settings accept a validated relative or owner-controlled absolute managed root; a validated configured or inherited `system_temp_dir`; a one-minute-to-thirty-day command TTL; a one-minute-to-twenty-four-hour sweep interval; a one-second-to-one-hour regular `reap_timeout` (five minutes by default); and a one-second-to-five-minute `shutdown_reap_timeout` (one minute by default). Invalid roots, relative escapes, zero/out-of-range durations, and invalid modes fail loudly.
  - verify: `TestADR_0281_TemporaryStorageConfigValidation`
- AC1.3: A project-tier `temporary_storage` block cannot redirect the root, disable cleanup, or alter retention; it is ignored with an operator warning while the trusted operator-tier resolution remains effective.
  - verify: `TestADR_0281_ProjectTemporaryStorageIgnored`
- AC1.4: On a non-Linux platform, `mode: managed` fails configuration validation before serving; `mode: system` remains available and preserves existing behavior.
  - verify: `TestADR_0281_ManagedModeLinuxAdmission`
