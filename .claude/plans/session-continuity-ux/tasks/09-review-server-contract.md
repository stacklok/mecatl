---
id: 09-review-server-contract
title: Repair server contract findings from panel review
blocked_by: [08-exit-handoff]
status: done
branch: "plan-session-continuity-ux/09-review-server-contract"
worktree: ".scratch/worktrees/session-continuity-ux-repair-09"
issue: "471"
retries: 0
last_error: ""
accumulator: acc/session-continuity-ux
---

# Repair brief

Fix panel ship-blockers without weakening ADR-0217: move the entire load/authorize/purpose/reopen sequence under the existing per-session run-entry lock so two same-process prompt starts cannot both mutate/reopen before registration; sanitize transcript storage failures to a generic caller error while retaining operator diagnostics; add workspace plus independent authoritative-transcript and optional-activity-replay capability fields end-to-end in inventory; make exact `--resume <id>` use exact GetSession+GetTranscript without requiring optional inventory paging; keep latest dependent on paging. Preserve ownership non-enumeration. Reconcile the engine API finding by avoiding a source-breaking expansion of the pre-existing exported `port.SessionMeta` where practical (introduce an additive discovery metadata type for the pager), or explicitly classify any unavoidable v0 break as Changed with a pinning test.

## Protected acceptance criteria

- AC2.5 ownership oracle remains closed.
- AC3.4 activity replay stays independent from authoritative transcript.
- AC3.7 HTTP/gRPC metadata parity includes workspace and independent capability fields.
- AC4.4 workspace search is backed by wire data.
- AC6.2 exact startup resume performs no create and does not require listing.
- AC6.6 first prompt revalidates atomically under the run-entry lock.

Add regression tests for concurrent same-id starts, raw backend-error redaction, exact resume with pager unavailable, and workspace/capability transport mapping.
