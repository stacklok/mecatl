---
id: 04-command-environment-overlay
title: Bound command environment overlay API
blocked_by: []
status: done
branch: "plan-managed-temporary-command-leases/04-command-environment-overlay"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/managed-temporary-command-leases
---

# Bound command environment overlay API

Add the minimum exported `engine/tool` per-invocation overlay capability and migrate all bound runner implementations/fakes. Preserve runner namespace affinity and the common secret-scrub boundary. This task must follow engine API compatibility protocol.

## Acceptance criteria

- AC3.1b: Across foreground/background and managed/system scope combinations, representative provider, GitHub, cloud, and generic API-key/token variables remain absent from the command environment while only the expected temporary-storage variables differ by scope.
  - verify: `TestADR_0281_TempOverlayPreservesSecretScrub`
