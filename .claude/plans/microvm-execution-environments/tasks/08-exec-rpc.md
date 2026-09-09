---
id: 08-exec-rpc
title: Structured guest exec streaming and cancellation
blocked_by: [04-control-protocol, 06-virtiofs-assets]
status: done
branch: "plan-microvm-execution-environments/08-exec-rpc"
worktree: ".scratch/task-microvm-08"
issue: "530"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Task brief

Implement bound CommandRunner/CommandStreamer behavior over the authenticated guest protocol: ordered stdout/stderr, explicit exits, bounds/backpressure, cancellation, and process-group termination. SSH remains bootstrap/debug only and no stale path can run on the host.

## Acceptance criteria

- AC4.1: Exec streams ordered stdout and stderr plus an explicit exit status; a nonzero
  guest exit is a completed command result, not a transport failure.
  - verify: `TestMicroVMEnvironments_Scenario4_ExecStreamingPreservesChannelsAndExit`
- AC4.2: Cancellation or deadline terminates the entire guest process group within a
  bound, returns partial output already produced, and is distinguishable from a
  transport fault.
  - verify: `TestMicroVMEnvironments_Scenario4_CancelKillsGuestProcessGroup`
- AC4.3: Wrong owner/session/ref/generation, replayed endpoint credentials,
  malformed/oversized frames, output overflow, and excessive concurrent execs fail
  closed without reaching another environment or the daemon control plane.
  - verify: `TestMicroVMEnvironments_Scenario4_GuestProtocolBoundaryIsBounded`
