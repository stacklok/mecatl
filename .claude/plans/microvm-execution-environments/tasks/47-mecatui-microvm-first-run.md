---
id: 47-mecatui-microvm-first-run
title: Add explicit mecatui microVM first-run and daily-use UX
blocked_by: [46-user-bootstrap-daemon-manager]
status: done
branch: "plan-microvm-execution-environments/47-mecatui-microvm-first-run"
worktree: ".scratch/task-microvm-47"
issue: "526"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Usability round

Add `mecatui microvm init|doctor|status|recover|delete` and an explicit daily `mecatui --microvm` shortcut selecting `microvm-local`. Bare mecatui remains host-local byte-compatible; `connect` mode never starts/probes a local daemon. Init displays trust/egress/resource/path decisions and requires interactive confirmation (or explicit noninteractive acknowledgement). Embedded app.Build receives the generated operator profile only after doctor success. Surface source/worktree/guest paths, enforced egress, detach-vs-delete, and retained dirty worktree location.

Verification: CLI/help/config discoverability, explicit activation, connect isolation, first-run confirmation, default unchanged, profile selection through real session adapter, delete confirmation/recovery UX.
