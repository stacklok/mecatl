---
id: 18-guest-multiplex
title: Multiplexed cross-VM guest protocol and transferable capabilities
blocked_by: [04-control-protocol, 07-workspace-rpc, 08-exec-rpc]
status: done
branch: "plan-microvm-execution-environments/18-guest-multiplex"
worktree: ".scratch/task-microvm-18"
issue: "530"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Task brief

Repair the concrete-runtime gap: replace the independent Workspace/exec handshakes and shared in-memory credential registry with one authenticated, generation-bound, multiplexed guest-agent protocol over the single vsock binding. One connection/handshake negotiates services; every request carries a service/method/request ID and a transferable single-session capability that the guest can verify without shared host memory. Preserve replay rejection, bounds, stream backpressure, cancellation, and no-host-fallback behavior.

This enabling task owns no new numbered plan AC. It strengthens the existing AC2.4/AC2.5/AC3.4/AC4.1–AC4.3 proofs so they hold across a real process/VM boundary.

## Verification obligations

- `TestGuestProtocol_MultiplexesWorkspaceAndExecAfterOneHandshake`
- `TestGuestProtocol_TransferableCapabilityRejectsReplayAndWrongGeneration`
- Existing task-04, task-07, and task-08 named tests remain green with no shared registry shortcut.
- `task test:microvm-standalone`, `task lint`, and `task test` pass.
