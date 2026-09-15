---
id: 29-streaming-cancellation-repair
title: Preserve ordered streaming and cancellation across microvmd UDS
blocked_by: []
status: done
branch: "plan-microvm-execution-environments/29-streaming-cancellation-repair"
worktree: ".scratch/task-microvm-29"
issue: "530"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Second panel repair

Cross-confirmed Spec/Security blocker: microvmd buffers stdout/stderr into separate strings, root concatenates stdout before stderr, and client cancellation/disconnect does not cancel the daemon-wide guest execution context.

Implement framed streaming over the management UDS preserving global frame order, partial output, explicit exit, and backpressure. Bind each request to a per-connection/request context; client cancellation closes/cancels the request and peer disconnect cancels guest execution/process group. Keep transport, cancellation, nonzero exit distinct.

Protects AC4.1–AC4.3.

Verification: production UDS E2E interleaves stdout/stderr in order; cancellation without deadline and abrupt disconnect kill guest process group and preserve only prior output; no slot leak; existing guestexec tests plus lint/test pass.
