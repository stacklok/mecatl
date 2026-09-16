---
id: 16-platform-e2e
title: Linux and macOS live certification and isolation journey
blocked_by: [12-multi-session, 14-child-merge-lifecycle, 15-operations-docs, 17-concrete-runtime]
status: done
branch: "plan-microvm-execution-environments/16-platform-e2e"
worktree: ".scratch/task-microvm-16"
issue: "535"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Task brief

Add the explicit real-hypervisor E2E matrix and adversarial journey for Linux amd64/arm64 and supported Darwin arm64. Ordinary tests remain offline. The live journey uses verified pinned artifacts and covers every assembled capability; do not paper over platform differences.

## Acceptance criteria

- AC8.1: Linux amd64, Linux arm64, and Apple Silicon macOS at the documented minimum
  version each pass the same live boot, worktree, filesystem, exec/cancel, egress,
  detach/reattach, concurrent-session, and delete journey; any platform exception is
  explicit and tested.
  - verify: demonstration — `task e2e:microvm` runs on the supported-platform CI matrix because ordinary unit tests cannot emulate KVM/HVF
- AC8.2: Through the configured capability surface, a guest cannot read host provider,
  MCP, identity, or registry credentials; the daemon control socket; unrelated host
  roots; a sibling worktree; or a sibling guest endpoint. Positive controls prove the
  intended worktree and allowed network destinations remain usable.
  - verify: `TestMicroVMEnvironments_Scenario8_HostSecretAndSiblingIsolation`
