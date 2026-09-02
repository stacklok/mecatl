---
id: 05-foreground-managed-leases
title: Foreground command leases and test-home containment
blocked_by: [02-managed-namespace-safety, 03-lease-protocol, 04-command-environment-overlay]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/managed-temporary-command-leases
---

# Foreground command leases and test-home containment

Wire validated foreground managed leases through the local runner and test-home helper. Preserve the current process-group cancellation/result semantics and verify worktree/copy child environment affinity.

## Acceptance criteria

- AC3.1: A managed foreground Bash call receives a distinct owner-only `cmd-<128-bit-random-id>/tmp` directory in both `TMPDIR` and `GOTMPDIR`; shell text is byte-for-byte free of injected temporary paths, and manifest metadata contains no command, output, environment, credential, or transcript content. The fixed internal overlay is applied after the common secret scrub and cannot restore, override, audit, or log a scrubbed credential-shaped variable.
  - verify: `TestADR_0281_ForegroundLeaseOverlayAndMetadataPrivacy`
- AC3.3: Successful completion, cancellation, and timeout retain their existing exit/cancellation/deadline outcomes while removing the exact lease after the managed process group has terminated.
  - verify: `TestADR_0281_LeaseCleanupPreservesCommandOutcome`
- AC3.4: If the managed process group remains live, terminal handling records the lifecycle transition and defers deletion; an escaped descendant after the managed group exits does not prevent the documented immediate lease deletion.
  - verify: `TestADR_0281_GroupLivenessControlsImmediateCleanup`
- AC3.5: The test-home helper accepts only a validated runner-owned marker and keeps its temporary HOME/XDG_CONFIG_HOME tree inside that command's lease; an absent or forged marker retains its existing safe behavior.
  - verify: `TestADR_0281_TestHomeUsesValidatedLeaseMarker`
- AC3.6: Bash executing through an isolated worktree or force-copy child `tool.Environment` receives a lease keyed to that child workspace instance—not its parent—and its overlay is applied by the runner bound to the same child namespace.
  - verify: `TestADR_0281_ChildEnvironmentGetsDistinctAffinedLease`
