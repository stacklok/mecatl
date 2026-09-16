---
id: 53-daemon-identity-managed-restart
title: Bind bootstrap reuse to daemon binary and policy identity
blocked_by: []
status: done
branch: "96ccd68b"
worktree: ".scratch/task-microvm-53"
issue: "526"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Usability/security repair

Add authenticated daemon-info/readiness RPC returning protocol version, release/binary identity, loaded config digest/policy revision, profiles, and socket binding. Init may reuse only an exact compatible match; otherwise safely restart the exact managed process or fail with service-manager instructions before enabling alias. Persist/verify PID plus process-start identity and expected args/socket; pidfd on Linux where practical. Doctor must query serving daemon identity, not validate only new files.

Verification: stale binary/policy daemon never enables alias; exact daemon reuses; managed restart identity-safe; PID reuse same binary rejected; owner isolation/full gates.
